package onebot

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/matterbridge-org/matterbridge/bridge/config"
	ob "github.com/matterbridge-org/matterbridge/bridge/onebot/internal/client"
)

// Skip literal CQ codes and URLs before looking for mention markers.
var mentionTokens = regexp.MustCompile(`\[CQ:[^\]]*\]|(?:[a-zA-Z][a-zA-Z0-9+.-]*://|www\.)\S+|@`)

type memberCache struct {
	names   map[string]string // An empty ID marks an ambiguous name.
	expires time.Time
}

func (b *Bridge) outgoingMentions(ctx context.Context, client *ob.Client, group int64, text string) []ob.Segment {
	users, everyone := b.allowedMentions()
	segments, needsNames := parseMentions(text, nil, users, everyone)
	if needsNames {
		names := b.groupMemberNames(ctx, client, group)
		segments, _ = parseMentions(text, names, users, everyone)
	}
	return segments
}

func (b *Bridge) allowedMentions() (users, everyone bool) {
	users = true
	if b.IsKeySet("AllowMention") {
		users = false
		for _, kind := range b.GetStringSlice("AllowMention") {
			switch kind {
			case "users":
				users = true
			case "everyone":
				everyone = true
			}
		}
	}
	return users, everyone
}

func (b *Bridge) outgoingMessageMentions(ctx context.Context, client *ob.Client, group int64, msg config.Message, body string) []ob.Segment {
	var original strings.Builder
	for _, part := range msg.MentionParts {
		original.WriteString(part.Text)
	}

	if len(msg.MentionParts) == 0 || original.String() != body {
		return b.outgoingMentions(ctx, client, group, body)
	}
	users, _ := b.allowedMentions()
	var segments []ob.Segment
	for _, part := range msg.MentionParts {
		if part.Kind == config.MentionText || part.Kind == config.MentionBot || part.Kind == config.MentionNative {
			segments = append(segments, b.outgoingMentions(ctx, client, group, part.Text)...)
			continue
		}
		id, err := strconv.ParseInt(part.UserID, 10, 64)
		if users && part.Kind == config.MentionUser && part.Account == b.Account && part.Channel == msg.Channel &&
			err == nil && id > 0 && strconv.FormatInt(id, 10) == part.UserID {
			segments = append(segments, ob.Segment{Type: "at", Data: map[string]string{"qq": part.UserID}})
		} else {
			appendText(&segments, part.Text)
		}
	}
	return segments
}

func (b *Bridge) groupMemberNames(ctx context.Context, client *ob.Client, group int64) map[string]string {
	b.mu.Lock()
	cached := b.members[group]
	b.mu.Unlock()
	if time.Now().Before(cached.expires) {
		return cached.names
	}
	// A failed lookup should still leave time to deliver the message as text.
	lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	members, err := client.GroupMembers(lookupCtx, group)
	names := make(map[string]string)
	ttl := 5 * time.Minute
	if err != nil {
		b.Log.WithError(err).Warnf("OneBot group %d member lookup failed; named mentions stay text", group)
		ttl = 30 * time.Second
	} else {
		for _, member := range members {
			if member.UserID <= 0 {
				continue
			}
			id := strconv.FormatInt(int64(member.UserID), 10)
			for _, name := range []string{member.Card, member.Nickname} {
				name = ob.CleanName(name)
				if name == "" {
					continue
				}
				if previous, exists := names[name]; exists && previous != id {
					names[name] = ""
				} else {
					names[name] = id
				}
			}
		}
	}
	b.mu.Lock()
	if client == b.client && ctx.Err() == nil {
		b.members[group] = memberCache{names: names, expires: time.Now().Add(ttl)}
	}
	b.mu.Unlock()
	return names
}

// parseMentions reports whether a group member lookup could resolve more names.
// Numeric QQ IDs and everyone mentions do not require the member list.
func parseMentions(text string, names map[string]string, users, everyone bool) ([]ob.Segment, bool) {
	var segments []ob.Segment
	last := 0
	needsNames := false
	if users || everyone {
		for _, loc := range mentionTokens.FindAllStringIndex(text, -1) {
			start := loc[0]
			if start < last || text[start:loc[1]] != "@" || !(start == last && last > 0 || mentionStart(text[:start])) {
				continue
			}
			tail := text[loc[1]:]
			length, id, named := matchMention(tail, names, users, everyone)
			needsNames = needsNames || named
			if id == "" {
				continue
			}
			appendText(&segments, text[last:start])
			segments = append(segments, ob.Segment{Type: "at", Data: map[string]string{"qq": id}})
			last = loc[1] + length
		}
	}
	appendText(&segments, text[last:])
	return segments, needsNames
}

func matchMention(text string, names map[string]string, users, everyone bool) (int, string, bool) {
	for _, name := range []string{"全体成员", "all"} {
		if strings.HasPrefix(text, name) && mentionEnd(text[len(name):]) {
			if everyone {
				return len(name), "all", false
			}
			return 0, "", false
		}
	}
	if !users || text == "" {
		return 0, "", false
	}
	n := 0
	for n < len(text) && text[n] >= '0' && text[n] <= '9' {
		n++
	}
	if n > 0 && mentionEnd(text[n:]) {
		id, err := strconv.ParseInt(text[:n], 10, 64)
		if err == nil && id > 0 && strconv.FormatInt(id, 10) == text[:n] {
			return n, text[:n], false
		}
		return 0, "", false
	}
	// Prefer the longest whole name, including ambiguous entries, so a duplicate
	// "Alice Smith" cannot accidentally fall back to a different member "Alice".
	length, id := 0, ""
	for name, memberID := range names {
		if len(name) > length && strings.HasPrefix(text, name) && mentionEnd(text[len(name):]) {
			length, id = len(name), memberID
		}
	}
	r, _ := utf8.DecodeRuneInString(text)
	return length, id, length == 0 && !unicode.IsSpace(r) && r != '@'
}

func mentionStart(before string) bool {
	if before == "" {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(before)
	return unicode.IsSpace(r) || strings.ContainsRune("([{<（【「『“‘，。！？；：、,!?:;", r)
}

func mentionEnd(after string) bool {
	// Sentence-ending dots are fine, but email/domain suffixes are not.
	after = strings.TrimLeft(after, ".")
	if after == "" {
		return true
	}
	r, _ := utf8.DecodeRuneInString(after)
	return unicode.IsSpace(r) || strings.ContainsRune(")]}>）】」』”’，。！？；：、,!?:;@", r)
}

func appendText(segments *[]ob.Segment, text string) {
	if text == "" {
		return
	}
	if n := len(*segments); n > 0 && (*segments)[n-1].Type == "text" {
		(*segments)[n-1].Data["text"] += text
		return
	}
	*segments = append(*segments, ob.Segment{Type: "text", Data: map[string]string{"text": text}})
}
