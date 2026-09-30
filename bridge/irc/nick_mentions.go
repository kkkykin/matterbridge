package birc

import (
	"regexp"
	"strings"
	"sync"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/matterbridge-org/matterbridge/bridge/config"
	"golang.org/x/text/unicode/norm"
)

type nickMentionKey struct{ channel, nick string }
type nickMentionUser struct{ account, channel, id string }
type ircNickMentions struct {
	mu    sync.Mutex
	nicks *lru.Cache[nickMentionKey, nickMentionUser]
}

func newIRCNickMentions() *ircNickMentions {
	nicks, _ := lru.New[nickMentionKey, nickMentionUser](4096)
	return &ircNickMentions{nicks: nicks}
}

// Record the final, sanitized RELAYMSG nickname and its source identity.
func (r *ircNickMentions) remember(msg config.Message) {
	user := nickMentionUser{}
	if msg.Protocol == "onebot" && msg.UserID != "" && msg.SourceChannel != "" {
		user = nickMentionUser{msg.Account, msg.SourceChannel, msg.UserID}
	}
	key := nickMentionKey{strings.ToLower(msg.Channel), strings.ToLower(norm.NFC.String(msg.Username))}
	r.mu.Lock()
	defer r.mu.Unlock()
	if previous, ok := r.nicks.Get(key); ok && previous != user {
		// Nick collisions (including sanitized names) must not ping the wrong
		// person. An ambiguous alias stays literal until evicted or restarted.
		user = nickMentionUser{}
	}
	r.nicks.Add(key, user)
}

// Only the capturing group denotes a mention: @nick or a leading "nick:".
// Consume URLs and CQ codes first so their contents remain literal.
var nickMentionTokens = regexp.MustCompile(`\[CQ:[^\]]*\]|(?:[a-zA-Z][a-zA-Z0-9+.-]*://|www\.)\S+|(@[^\s@]+|^[^\s@:：,，]+[:,：，])`)

func (r *ircNickMentions) mentions(msg *config.Message) {
	var parts []config.MentionPart
	last := 0
	for _, loc := range nickMentionTokens.FindAllStringSubmatchIndex(msg.Text, -1) {
		start, end := loc[2], loc[3]
		if start < 0 || !ircMentionStart(msg.Text[:start]) {
			continue
		}
		// Leave address punctuation in the text following the mention.
		token := strings.TrimRight(msg.Text[start:end], ",:;.!?，：；。！？")
		end = start + len(token)
		if !ircMentionEnd(msg.Text[end:]) {
			continue
		}
		nick := strings.TrimPrefix(token, "@")
		r.mu.Lock()
		user, ok := r.nicks.Get(nickMentionKey{strings.ToLower(msg.Channel), strings.ToLower(norm.NFC.String(nick))})
		r.mu.Unlock()
		if !ok || user.id == "" {
			continue
		}

		parts = append(parts, config.MentionPart{Text: msg.Text[last:start], Kind: config.MentionText}, config.MentionPart{
			Text: msg.Text[start:end], Kind: config.MentionUser, UserID: user.id, Account: user.account, Channel: user.channel,
		})
		last = end
	}
	if len(parts) > 0 {
		parts = append(parts, config.MentionPart{Text: msg.Text[last:], Kind: config.MentionText})
		msg.MentionParts = parts
	}
}
