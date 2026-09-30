package birc

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/matterbridge-org/matterbridge/bridge/config"
)

var ircMentionTokens = regexp.MustCompile(`\[CQ:[^\]]*\]|(?:[a-zA-Z][a-zA-Z0-9+.-]*://|www\.)\S+|@`)

// prepareMentions only renders the original OneBot body. Quotes and attachment
// descriptions are literal, and rewriting the body invalidates its hints.
func (b *Birc) prepareMentions(msg *config.Message) {
	if msg.Protocol != "onebot" || !b.GetBool("ReverseMention") || len(msg.MentionParts) == 0 {
		return
	}
	var original strings.Builder
	for _, part := range msg.MentionParts {
		original.WriteString(part.Text)
	}
	if original.String() != msg.Text {
		return
	}
	target := b.GetString("BotMentionTarget")
	var out strings.Builder
	position := 0
	for _, part := range msg.MentionParts {
		end := position + len(part.Text)
		switch {
		case part.Kind == config.MentionText:
			out.WriteString(ircTextMentions(part.Text, msg.Text[:position], msg.Text[end:]))
		case part.Kind == config.MentionBot && validMentionNick(target):
			// Native QQ mentions need not have spaces around them. Keep the
			// replacement separate so IRC clients can recognize the whole nick.
			if position > 0 {
				r, _ := utf8.DecodeLastRuneInString(msg.Text[:position])
				if !unicode.IsSpace(r) {
					out.WriteByte(' ')
				}
			}
			out.WriteString(target)
			if end < len(msg.Text) {
				r, _ := utf8.DecodeRuneInString(msg.Text[end:])
				if !unicode.IsSpace(r) {
					out.WriteByte(' ')
				}
			}
		default:
			out.WriteString(part.Text)
		}
		position = end
	}
	msg.Text = out.String()
}

// IRC has no native mention segment; a whole nick in the body is its highlight
// convention. Do not resolve names against QQ or require a channel member list.
func ircTextMentions(text, before, after string) string {
	var out strings.Builder
	last := 0
	for _, loc := range ircMentionTokens.FindAllStringIndex(text, -1) {
		start, end := loc[0], loc[1]
		if text[start:end] != "@" || !ircMentionStart(before+text[:start]) {
			continue
		}
		for _, r := range text[end:] {
			if !mentionNickRune(r, end == loc[1]) {
				break
			}
			end += utf8.RuneLen(r)
		}
		nick := text[loc[1]:end]
		if nick == "" || nick == "all" || nick == "全体成员" || !ircMentionEnd(text[end:]+after) {
			continue
		}
		out.WriteString(text[last:start])
		out.WriteString(nick)
		last = end
	}
	out.WriteString(text[last:])
	return out.String()
}

func validMentionNick(nick string) bool {
	if nick == "" {
		return false
	}
	for i, r := range nick {
		if !mentionNickRune(r, i == 0) {
			return false
		}
	}
	return true
}

func mentionNickRune(r rune, first bool) bool {
	return unicode.IsLetter(r) || strings.ContainsRune("[]\\`_^{|}", r) ||
		(!first && (unicode.IsDigit(r) || unicode.IsMark(r) || r == '-'))
}

func ircMentionStart(text string) bool {
	if text == "" {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(text)
	return unicode.IsSpace(r) || strings.ContainsRune("([{<（【「『“‘，。！？；：、,!?:;", r)
}

func ircMentionEnd(text string) bool {
	text = strings.TrimLeft(text, ".")
	if text == "" {
		return true
	}
	r, _ := utf8.DecodeRuneInString(text)
	return unicode.IsSpace(r) || strings.ContainsRune(")]}>）】」』”’，。！？；：、,!?:;", r)
}
