package birc

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/lrstanley/girc"
	"github.com/matterbridge-org/matterbridge/bridge/config"
	"golang.org/x/text/unicode/norm"
)

type roleplayKey struct{ channel, nick string }
type roleplayUser struct{ account, channel, id string }
type ircRoleplay struct {
	mu    sync.Mutex
	nicks *lru.Cache[roleplayKey, roleplayUser]
}

func newIRCRoleplay() *ircRoleplay {
	nicks, _ := lru.New[roleplayKey, roleplayUser](4096)
	return &ircRoleplay{nicks: nicks}
}

// NPC takes a single IRC parameter. Keep Unicode names, but remove control
// characters, whitespace, formatting and reserved nick/mask punctuation.
func roleplayNick(name string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) || strings.ContainsRune("[]\\`_^{|}/-", r) {
			return r
		}
		return '-'
	}, norm.NFC.String(strings.TrimSpace(name)))
	name = strings.Trim(name, "-")
	if name == "" {
		return "unknown"
	}
	return name
}

func (b *Birc) roleplayNick(name string) string {
	// Ergo's ASCII/RFC1459 modes reject Unicode even in NPC names. PRECIS
	// servers advertise UTF8MAPPING alongside CASEMAPPING=ascii.
	casemap, _ := b.i.GetServerOption("CASEMAPPING")
	utf8map, _ := b.i.GetServerOption("UTF8MAPPING")
	if utf8map == "" && (casemap == CM_ASCII || casemap == CM_RFC1459 || casemap == CM_RFC1459STRICT) {
		name = strings.Map(sanitizeASCII, name)
	}
	return roleplayNick(name)
}

func roleplayLine(msg config.Message) string {
	command := "NPC"
	if msg.Event == config.EventUserAction {
		command = "NPCA"
	}
	return fmt.Sprintf("%s %s %s :%s", command, msg.Channel, roleplayNick(msg.Username), msg.Text)
}

func ownRoleplayEcho(event girc.Event, nick string) bool {
	// Ergo always echoes NPC, even without echo-message. Trust the server's
	// reserved host and ident, never the user-controlled trailing " (nick)".
	return event.Source != nil && event.Source.Host == "npc.fakeuser.invalid" &&
		strings.EqualFold(event.Source.Ident, nick)
}

func (b *Birc) handleRoleplayError(_ *girc.Client, event girc.Event) {
	if !b.GetBool("UseRoleplay") {
		return
	}
	if event.Command == girc.ERR_UNKNOWNCOMMAND && (len(event.Params) < 2 || (event.Params[1] != "NPC" && event.Params[1] != "NPCA")) {
		return
	}
	b.Log.Warnf("IRC roleplay failed: %s; enable roleplay in Ergo and channel mode +E, and check roleplay permissions", event.Last())
}

func (r *ircRoleplay) remember(msg config.Message) {
	user := roleplayUser{}
	if msg.Protocol == "onebot" && msg.UserID != "" && msg.SourceChannel != "" {
		user = roleplayUser{msg.Account, msg.SourceChannel, msg.UserID}
	}
	key := roleplayKey{strings.ToLower(msg.Channel), strings.ToLower(norm.NFC.String(msg.Username))}
	r.mu.Lock()
	defer r.mu.Unlock()
	if previous, ok := r.nicks.Get(key); ok && previous != user {
		// Nick collisions (including sanitized names) must not ping the wrong
		// person. An ambiguous alias stays literal until evicted or restarted.
		user = roleplayUser{}
	}
	r.nicks.Add(key, user)
}

// Recognize explicit @NPC mentions and the usual IRC "*NPC*: text" address.
// Consume URLs and CQ codes first so their contents remain literal.
var roleplayMentionTokens = regexp.MustCompile(`\[CQ:[^\]]*\]|(?:[a-zA-Z][a-zA-Z0-9+.-]*://|www\.)\S+|@\*?[^\s@*]+\*?|^\*[^\s*]+\*[:,：，]`)

func (r *ircRoleplay) mentions(msg *config.Message) {
	var parts []config.MentionPart
	last := 0
	for _, loc := range roleplayMentionTokens.FindAllStringIndex(msg.Text, -1) {
		start, end := loc[0], loc[1]
		token := msg.Text[start:end]
		if token[0] != '@' && !(start == 0 && token[0] == '*') || !ircMentionStart(msg.Text[:start]) {
			continue
		}
		// Leave address punctuation in the text following the mention.
		token = strings.TrimRight(token, ",:;.!?，：；。！？")
		end = start + len(token)
		if !ircMentionEnd(msg.Text[end:]) {
			continue
		}
		nick := strings.TrimPrefix(token, "@")
		nick = strings.TrimPrefix(strings.TrimSuffix(nick, "*"), "*")
		r.mu.Lock()
		user, ok := r.nicks.Get(roleplayKey{strings.ToLower(msg.Channel), strings.ToLower(norm.NFC.String(nick))})
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
