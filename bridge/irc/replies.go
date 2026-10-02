package birc

import (
	"crypto/rand"
	"strings"
	"sync"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/lrstanley/girc"
	"github.com/matterbridge-org/matterbridge/bridge/config"
	"github.com/matterbridge-org/matterbridge/bridge/helper"
)

const ircLocalIDPrefix = "mb-"
const ircCorrelationTag = "+matterbridge/id"

type ircReplyKey struct{ channel, id string }
type ircReplyMessage struct {
	localID, wireID string
	quote           *config.MessageQuote
}
type ircReplies struct {
	mu      sync.Mutex
	entries *lru.Cache[ircReplyKey, *ircReplyMessage]
}

func newIRCReplies() *ircReplies {
	entries, _ := lru.New[ircReplyKey, *ircReplyMessage](8192)
	return &ircReplies{entries: entries}
}

func validIRCMessageID(id string) bool {
	if id == "" || len(id) > 256 || id == config.ParentIDNotFound {
		return false
	}
	for _, c := range id {
		if c <= ' ' || c > '~' || c == ';' {
			return false
		}
	}
	return true
}

func ircMessageID(event girc.Event) string {
	for _, tag := range []string{"msgid", "draft/msgid"} {
		if id, ok := event.Tags.Get(tag); ok && validIRCMessageID(id) {
			return id
		}
	}
	return ""
}

func (r *ircReplies) remember(message config.Message, wireID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries.Add(ircReplyKey{message.Channel, message.ID}, &ircReplyMessage{
		localID: message.ID, wireID: wireID,
		quote: helper.QuotePreview(message.ID, message.Username, message.Text),
	})
}

func (r *ircReplies) parent(channel, id string) (string, *config.MessageQuote) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry, ok := r.entries.Get(ircReplyKey{channel, id}); ok {
		return entry.localID, entry.quote
	}
	return id, helper.QuotePreview(id, "", "")
}

func (r *ircReplies) wireID(channel, id string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry, ok := r.entries.Get(ircReplyKey{channel, id}); ok {
		return entry.wireID
	}
	// Unmapped local IDs must never appear in a native reply tag.
	if strings.HasPrefix(id, ircLocalIDPrefix) {
		return ""
	}
	if validIRCMessageID(id) {
		return id
	}
	return ""
}

// Each split fragment aliases the same logical message; replies target its
// first server-assigned msgid. Only authenticated self echoes bind these IDs.
func (r *ircReplies) echo(channel, localID, wireID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries.Get(ircReplyKey{channel, localID})
	if !ok || entry.localID != localID {
		return
	}
	if entry.wireID == "" {
		entry.wireID = wireID
	}
	r.entries.Add(ircReplyKey{channel, wireID}, entry)
}

func (b *Birc) handleReplyEcho(client *girc.Client, event girc.Event) {
	if b.replies == nil || event.Source == nil || len(event.Params) < 2 || (event.Command != girc.PRIVMSG && event.Command != girc.NOTICE) {
		return
	}
	own := event.Source.Name == client.GetNick()
	for _, tag := range []string{"draft/relaymsg", "relaymsg"} {
		if nick, ok := event.Tags.Get(tag); ok && nick == client.GetNick() {
			own = true
		}
	}
	if !own {
		return
	}
	localID, ok := event.Tags.Get(ircCorrelationTag)
	wireID := ircMessageID(event)
	if ok && wireID != "" {
		b.replies.echo(strings.ToLower(event.Params[0]), localID, wireID)
	}
}

func (b *Birc) incomingReply(event girc.Event, message *config.Message) {
	wireID := ircMessageID(event)
	message.ID = wireID
	if message.ID == "" {
		message.ID = ircLocalIDPrefix + rand.Text()
	}
	for _, tag := range []string{"+reply", "+draft/reply", "draft/reply"} {
		if parent, ok := event.Tags.Get(tag); ok && validIRCMessageID(parent) {
			message.ParentID, message.Quote = b.replies.parent(message.Channel, parent)
			break
		}
	}
	b.replies.remember(*message, wireID)
}

func (b *Birc) prepareReply(message *config.Message) {
	parent := ""
	if b.GetBool("PreserveThreading") && b.i.HasCapability("message-tags") && message.ParentValid() {
		parent = b.replies.wireID(message.Channel, message.ParentID)
	}
	if parent == "" {
		if message.Quote == nil && message.ParentValid() {
			message.Quote = helper.QuotePreview(message.ParentID, "", "")
		}
		message.Text = helper.QuotePrefix(message.Quote) + message.Text
	}
	message.ParentID = parent
}

func (b *Birc) outgoingReplyID(message config.Message) string {
	if !b.GetBool("PreserveThreading") {
		return ""
	}

	message.ID = ircLocalIDPrefix + rand.Text()
	b.replies.remember(message, "")

	return message.ID
}

func (b *Birc) sendIRCLine(line string, message config.Message) error {
	line = strings.TrimRight(line, "\r\n")
	if b.GetBool("PreserveThreading") && b.i.HasCapability("message-tags") {
		tags := girc.Tags{}
		if message.ID != "" {
			tags.Set(ircCorrelationTag, message.ID)
		}
		if validIRCMessageID(message.ParentID) {
			tags.Set("+draft/reply", message.ParentID)
		}
		if len(tags) > 0 {
			line = string(tags.Bytes()) + " " + line
		}
	}
	if b.GetBool("MessageSplit") || b.GetBool("UseRelayMsg") {
		return b.i.Cmd.SendRawNoSplit(line + "\r\n")
	}
	return b.i.Cmd.SendRaw(line)
}
