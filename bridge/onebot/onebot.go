// Package onebot implements matterbridge's native bridge interface for NapCat.
// QQ groups are ordinary channels; all routing belongs to matterbridge.
package onebot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/matterbridge-org/matterbridge/bridge"
	"github.com/matterbridge-org/matterbridge/bridge/config"
	"github.com/matterbridge-org/matterbridge/bridge/helper"
	ob "github.com/matterbridge-org/matterbridge/bridge/onebot/internal/client"
)

// Bridge relays QQ group messages through a OneBot 11 WebSocket server.
type Bridge struct {
	*bridge.Config
	mu        sync.Mutex
	lifecycle sync.Mutex
	client    *ob.Client
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	channels  map[string]bool
	seen      map[string]time.Time
	members   map[int64]memberCache
	quotes    *lru.Cache[quoteKey, *config.MessageQuote]
}

// New creates a OneBot bridge using the shared matterbridge configuration.
func New(cfg *bridge.Config) bridge.Bridger {
	quotes, _ := lru.New[quoteKey, *config.MessageQuote](4096)
	b := &Bridge{Config: cfg, channels: map[string]bool{}, seen: map[string]time.Time{}, quotes: quotes}
	if !b.IsKeySet("PreserveThreading") {
		b.SetBool("PreserveThreading", true)
	}
	return b
}

func (b *Bridge) Connect() error {
	b.lifecycle.Lock()
	defer b.lifecycle.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cancel != nil {
		return errors.New("OneBot bridge already connected")
	}
	server, token := b.GetString("Server"), b.GetString("Token")
	if err := validateServer(server, token); err != nil {
		return err
	}
	b.ctx, b.cancel = context.WithCancel(context.Background())
	b.client = ob.New(server, token, b.Log.WithField("account", b.Account))
	b.members = make(map[int64]memberCache)
	b.quotes.Purge()
	ctx, client := b.ctx, b.client
	b.wg.Add(2)
	go func() { defer b.wg.Done(); client.Run(ctx) }()
	go func() { defer b.wg.Done(); b.receive(ctx, client) }()
	return nil
}

func (b *Bridge) Disconnect() error {
	b.lifecycle.Lock()
	defer b.lifecycle.Unlock()
	b.mu.Lock()
	cancel := b.cancel
	b.cancel = nil
	b.mu.Unlock()
	if cancel != nil {
		cancel()
		b.wg.Wait()
	}
	return nil
}

func (b *Bridge) JoinChannel(channel config.ChannelInfo) error {
	if _, err := groupID(channel.Name); err != nil {
		return err
	}
	// Membership is managed in QQ. Joining here subscribes an existing group.
	b.mu.Lock()
	b.channels[channel.Name] = true
	b.mu.Unlock()
	return nil
}

func (b *Bridge) Send(m config.Message) (string, error) {
	if m.Event != "" && m.Event != config.EventUserAction {
		return "", nil
	}
	id, err := groupID(m.Channel)
	if err != nil {
		return "", err
	}
	b.mu.Lock()
	joined, ctx, client := b.channels[m.Channel], b.ctx, b.client
	b.mu.Unlock()
	if !joined {
		return "", fmt.Errorf("OneBot group %d has not been joined", id)
	}
	if ctx == nil || client == nil {
		return "", errors.New("OneBot bridge is not connected")
	}
	text := plainText(m)
	if strings.TrimSpace(text) == "" {
		return "", nil
	}
	prefix := ""
	name := ob.CleanName(m.Username) // already formatted by matterbridge
	if name != "" {
		prefix = name + " "
	}
	if m.Event == config.EventUserAction {
		prefix = "* " + prefix
	}
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// Only the original body participates in mention matching. The formatted
	// sender prefix and the attachment descriptions appended by plainText stay literal.
	body := ob.CleanText(m.Text)
	segments := b.outgoingMessageMentions(callCtx, client, id, m, body)
	var message []ob.Segment
	if b.GetBool("PreserveThreading") && ob.ValidMessageID(m.ParentID) {
		message = append(message, ob.Segment{Type: "reply", Data: map[string]string{"id": m.ParentID}})
	} else {
		prefix += helper.QuotePrefix(m.Quote)
	}
	appendText(&message, prefix)
	for _, segment := range segments {
		if segment.Type == "text" {
			appendText(&message, segment.Data["text"])
		} else {
			message = append(message, segment)
		}
	}
	appendText(&message, strings.TrimPrefix(text, body))
	messageID, err := client.SendGroup(callCtx, id, message)
	if err == nil {
		b.quotes.Add(quoteKey{id, messageID}, helper.QuotePreview(messageID, m.Username, text))
	}
	return messageID, err
}
