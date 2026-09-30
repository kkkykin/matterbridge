package birc

import (
	"testing"

	"github.com/matterbridge-org/matterbridge/bridge/config"
)

//nolint:goconst,gosmopolitan // Keep Unicode inputs and expected renderings readable.
func TestOneBotIRCMentions(t *testing.T) {
	for _, tc := range []struct {
		name, target, want string
		enabled            bool
		parts              []config.MentionPart
	}{
		{"disabled", "Alice", "@999 hi @Bob", false, []config.MentionPart{{Text: "@999", Kind: config.MentionBot}, {Text: " hi @Bob", Kind: config.MentionText}}},
		{"fixed and arbitrary", "Alice", "Alice hi Bob", true, []config.MentionPart{{Text: "@999", Kind: config.MentionBot}, {Text: " hi @Bob", Kind: config.MentionText}}},
		{"arbitrary only", "", "@999 hi Bob", true, []config.MentionPart{{Text: "@999", Kind: config.MentionBot}, {Text: " hi @Bob", Kind: config.MentionText}}},
		{"native bot without spaces", "Alice", "你好 Alice 看一下", true, []config.MentionPart{{Text: "你好", Kind: config.MentionText}, {Text: "@999", Kind: config.MentionBot}, {Text: "看一下", Kind: config.MentionText}}},
		{"native mentions stay literal", "Alice", "@123 @全体成员 Bob", true, []config.MentionPart{{Text: "@123 @全体成员", Kind: config.MentionLiteral}, {Text: " @Bob", Kind: config.MentionText}}},
		{"media stays literal", "Alice", "[图片 @Bob] https://example.org/@Bob Bob", true, []config.MentionPart{{Text: "[图片 @Bob] https://example.org/@Bob", Kind: config.MentionLiteral}, {Text: " @Bob", Kind: config.MentionText}}},
		{"typed bot number stays literal", "Alice", "@999 bot", true, []config.MentionPart{{Text: "@999 @bot", Kind: config.MentionText}}},
		{"invalid target", "Alice Bob", "@999", true, []config.MentionPart{{Text: "@999", Kind: config.MentionBot}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := replyTestBridge(t)
			b.SetBool("ReverseMention", tc.enabled)
			b.SetString("BotMentionTarget", tc.target)
			msg := config.Message{Protocol: "onebot", MentionParts: tc.parts}
			for _, part := range tc.parts {
				msg.Text += part.Text
			}
			b.prepareMentions(&msg)
			if msg.Text != tc.want {
				t.Fatalf("got %q; want %q", msg.Text, tc.want)
			}
		})
	}
}

func TestIRCTextMentionBoundaries(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"@Alice hi @Bob!", "Alice hi Bob!"},
		{"(@Alice), @Bob.\n@张三 看一下", "(Alice), Bob.\n张三 看一下"},
		{"@nick-name @a_b @a`b @a[b]", "nick-name a_b a`b a[b]"},
		{"@123 @all @全体成员", "@123 @all @全体成员"},
		{"alice@Bob example@Alice.org @Alice.org", "alice@Bob example@Alice.org @Alice.org"},
		{"https://example.org/@Alice www.example.org/@Bob", "https://example.org/@Alice www.example.org/@Bob"},
		{"[CQ:text,text=@Alice] \\@Bob @@Alice", "[CQ:text,text=@Alice] \\@Bob @@Alice"},
		{"@AliceSuffix", "AliceSuffix"},
	} {
		if got := ircTextMentions(tc.text, "", ""); got != tc.want {
			t.Errorf("%q: got %q; want %q", tc.text, got, tc.want)
		}
	}
	if got := ircTextMentions("@Alice", "email", ".org"); got != "@Alice" {
		t.Fatalf("lost part boundaries: %q", got)
	}
}

func TestIRCMentionsPreserveRewritesAndOtherSources(t *testing.T) {
	b := replyTestBridge(t)
	b.SetBool("ReverseMention", true)
	b.SetString("BotMentionTarget", "Alice")
	for _, msg := range []config.Message{
		{Protocol: "onebot", Text: "rewritten @999", MentionParts: []config.MentionPart{{Text: "@999", Kind: config.MentionBot}}},
		{Protocol: "discord", Text: "@999", MentionParts: []config.MentionPart{{Text: "@999", Kind: config.MentionBot}}},
		{Protocol: "onebot", Text: "@999"},
	} {
		want := msg.Text
		b.prepareMentions(&msg)
		if msg.Text != want {
			t.Fatalf("modified unrelated text: %+v", msg)
		}
	}
}

func TestIRCSendMentionsBeforeQuoteAndSplit(t *testing.T) {
	b := replyTestBridge(t)
	b.SetBool("ReverseMention", true)
	b.SetString("BotMentionTarget", "Alice")
	b.SetBool("MessageSplit", true)
	b.prefixDone = true
	b.Local = make(chan config.Message, 10)
	msg := config.Message{
		Protocol: "onebot", Channel: "#test", Text: "@999 hello @Bob", Username: "@Sender",
		MentionParts: []config.MentionPart{{Text: "@999", Kind: config.MentionBot}, {Text: " hello @Bob", Kind: config.MentionText}},
		Quote:        &config.MessageQuote{Username: "@Author", Text: "@999 @Bob quoted"},
	}
	if _, err := b.Send(msg); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-b.Local:
		if got.Text != "[回复 @Author：@999 @Bob quoted] Alice hello Bob" || got.Username != "@Sender" {
			t.Fatalf("unexpected queued message: %+v", got)
		}
	default:
		t.Fatal("no message queued")
	}
	if msg.Text != "@999 hello @Bob" || msg.MentionParts[0].Text != "@999" {
		t.Fatal("mutated shared source message")
	}
}

func TestInvalidBotMentionTarget(t *testing.T) {
	b := replyTestBridge(t)
	b.SetBool("ReverseMention", true)
	for _, target := range []string{"@Alice", "Alice Bob", "Alice\r\nPRIVMSG", "#channel", "123"} {
		b.SetString("BotMentionTarget", target)
		if err := b.Connect(); err == nil {
			t.Fatalf("accepted invalid target %q", target)
		}
	}
}
