package birc

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lrstanley/girc"
	"github.com/matterbridge-org/matterbridge/bridge/config"
)

//nolint:goconst // Keep the source text beside its expected rendering.
func TestRoleplaySend(t *testing.T) {
	for _, split := range []bool{false, true} {
		b := replyTestBridge(t)
		b.SetBool("UseRoleplay", true)
		b.SetBool("MessageSplit", split)
		b.SetBool("Colornicks", true)
		b.SetBool("ReverseMention", true)
		b.SetString("BotMentionTarget", "Alice")
		b.prefixDone = true
		b.Local = make(chan config.Message, 40)
		m := config.Message{Channel: "#a", Username: "张 三\r\n!@*", Protocol: "onebot", Text: "@999 hello @Bob",
			MentionParts: []config.MentionPart{{Text: "@999", Kind: config.MentionBot}, {Text: " hello @Bob", Kind: config.MentionText}},
			Quote:        &config.MessageQuote{Username: "parent", Text: "@123"}}
		if _, err := b.Send(m); err != nil {
			t.Fatal(err)
		}
		got := <-b.Local
		if line := roleplayLine(got); line != "NPC #a 张-三 :[回复 parent：@123] Alice hello Bob" {
			t.Fatalf("roleplay command: %q", line)
		}
		got.Event = config.EventUserAction
		if !strings.HasPrefix(roleplayLine(got), "NPCA #a 张-三 :") {
			t.Fatal("actions must use NPCA")
		}
		m.Text, m.MentionParts, m.Quote = strings.Repeat("你好🌉", 300), nil, nil
		if _, err := b.Send(m); err != nil {
			t.Fatal(err)
		}
		if len(b.Local) < 2 {
			t.Fatal("NPC must split even when MessageSplit is false")
		}
		for len(b.Local) > 0 {
			part := <-b.Local
			delivered := fmt.Sprintf(":*%s*!%s@npc.fakeuser.invalid PRIVMSG %s :%s (%s)\r\n", part.Username, b.Nick, part.Channel, part.Text, b.Nick)
			if !utf8.ValidString(part.Text) || len(delivered) > b.MessageLength {
				t.Fatalf("invalid fragment: %q", part.Text)
			}
		}
	}
	b := replyTestBridge(t)
	b.SetBool("UseRoleplay", true)
	b.SetBool("UseRelayMsg", true)
	if err := b.Connect(); err == nil {
		t.Fatal("conflicting transports accepted")
	}
}

func TestRoleplayEchoFiltering(t *testing.T) {
	b := replyTestBridge(t)
	b.SetBool("UseRoleplay", true)
	for _, tc := range []struct {
		source, text string
		skip         bool
	}{
		{"*Alice*!bridge@npc.fakeuser.invalid", "hello (bridge)", true},
		{"*Alice*!bridge@npc.fakeuser.invalid", "hello", true},
		{"*Alice*!other@npc.fakeuser.invalid", "hello (bridge)", false},
		{"Alice!bridge@real.host", "hello (bridge)", false},
	} {
		event := girc.ParseEvent(":" + tc.source + " PRIVMSG #a :" + tc.text)
		if got := b.skipPrivMsg(*event); got != tc.skip {
			t.Errorf("%s: skip=%v", tc.source, got)
		}
	}
}

func TestRoleplayMentionIdentity(t *testing.T) {
	r := newIRCRoleplay()
	r.remember(config.Message{Protocol: "onebot", Account: "onebot.qq", SourceChannel: "123", Channel: "#a", Username: "张-三/qq", UserID: "888"})
	for _, tc := range []struct {
		text, channel string
		count         int
	}{
		{"@*张-三/qq* hello", "#a", 1},
		{"*张-三/qq*: hello @张-三/qq", "#a", 2},
		{"@*张-三/qq* hello", "#b", 0},
		{"https://host/@*张-三/qq* [CQ:text,text=@张-三/qq] email@张-三/qq @@张-三/qq", "#a", 0},
		{"ordinary *张-三/qq* text", "#a", 0},
	} {
		m := config.Message{Text: tc.text, Channel: tc.channel}
		r.mentions(&m)
		count, body := 0, ""
		for _, part := range m.MentionParts {
			body += part.Text
			if part.Kind == config.MentionUser {
				count++
				if part.UserID != "888" || part.Account != "onebot.qq" || part.Channel != "123" {
					t.Fatalf("identity lost: %+v", part)
				}
			}
		}
		if count != tc.count || count > 0 && body != tc.text {
			t.Errorf("%q: %+v", tc.text, m.MentionParts)
		}
	}
	r.remember(config.Message{Protocol: "onebot", Account: "onebot.qq", SourceChannel: "123", Channel: "#a", Username: "张-三/qq", UserID: "777"})
	m := config.Message{Text: "@*张-三/qq*", Channel: "#a"}
	r.mentions(&m)
	if len(m.MentionParts) != 0 {
		t.Fatal("ambiguous alias resolved to a user")
	}
}
