package onebot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/matterbridge-org/matterbridge/bridge/config"
	"github.com/matterbridge-org/matterbridge/bridge/helper"
	ob "github.com/matterbridge-org/matterbridge/bridge/onebot/internal/client"
)

func (b *Bridge) receive(ctx context.Context, client *ob.Client) {
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-client.Events():
			m, ok := b.incoming(e)
			if !ok {
				continue
			}
			b.enrichReply(ctx, client, e.GroupID, &m)
			b.enrichForwards(ctx, client, e, &m)
			select {
			case b.Remote <- m:
			case <-ctx.Done():
				return
			}
		}
	}
}

func (b *Bridge) incoming(e ob.Event) (config.Message, bool) {
	channel := strconv.FormatInt(int64(e.GroupID), 10)
	b.mu.Lock()
	joined := b.channels[channel]
	b.mu.Unlock()
	if !joined || e.PostType != "message" || e.MessageType != "group" || e.UserID <= 0 || e.SelfID <= 0 || e.UserID == e.SelfID {
		return config.Message{}, false
	}
	key := fmt.Sprintf("%d:%d:%d", e.SelfID, e.GroupID, e.MessageID)
	if t, ok := b.seen[key]; e.MessageID != 0 && ok && time.Since(t) < 10*time.Minute {
		return config.Message{}, false
	}
	text, parentID, mentionParts, err := ob.MessageTextMentions(e.Message, strconv.FormatInt(int64(e.SelfID), 10))
	if strings.TrimSpace(text) == "" && parentID != "" {
		text = "[回复]"
	}
	if err != nil || strings.TrimSpace(text) == "" {
		return config.Message{}, false
	}
	name := ob.CleanName(e.Sender.Card)
	if name == "" {
		name = ob.CleanName(e.Sender.Nickname)
	}
	if name == "" {
		name = strconv.FormatInt(int64(e.UserID), 10)
	}
	if e.MessageID != 0 {
		b.remember(key, time.Now())
	}
	m := config.Message{Text: text, Username: name, UserID: strconv.FormatInt(int64(e.UserID), 10), Channel: channel, Account: b.Account, Protocol: "onebot", ID: strconv.FormatInt(int64(e.MessageID), 10), ParentID: parentID}
	m.MentionParts = mentionParts
	if parentID != "" {
		m.Quote = helper.QuotePreview(parentID, "", "")
		if quote, ok := b.quotes.Get(quoteKey{int64(e.GroupID), parentID}); ok {
			m.Quote = quote
		}
	}
	if e.MessageID != 0 {
		b.quotes.Add(quoteKey{int64(e.GroupID), m.ID}, helper.QuotePreview(m.ID, name, text))
	}
	return m, true
}

func (b *Bridge) remember(key string, now time.Time) {
	oldestKey := ""
	oldest := now
	for k, t := range b.seen {
		if now.Sub(t) >= 10*time.Minute {
			delete(b.seen, k)
			continue
		}
		if oldestKey == "" || t.Before(oldest) {
			oldestKey, oldest = k, t
		}
	}
	if len(b.seen) >= 4096 {
		delete(b.seen, oldestKey)
	}
	b.seen[key] = now
}
