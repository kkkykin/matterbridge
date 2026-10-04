package birc

import (
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/matterbridge-org/matterbridge/bridge"
	"github.com/matterbridge-org/matterbridge/bridge/config"
	"github.com/sirupsen/logrus"
)

func TestForwardChannelLinks(t *testing.T) {
	log := logrus.New()
	log.Out = io.Discard
	cfg := config.NewConfigFromString(log, []byte(`
[irc.plain]
Server = "127.0.0.1:6667"
ForwardChannelURL = "irc://irc.example.com"
[irc.tls]
Server = "127.0.0.1:6667"
ForwardChannelURL = "ircs://other.example.com:6697/"
[irc.ipv6]
Server = "127.0.0.1:6667"
ForwardChannelURL = "ircs://[2001:db8::1]:6697"
[irc.legacy]
Server = "127.0.0.1:6667"
`))
	for _, tc := range []struct {
		account, prefix, separator string
	}{
		{"irc.plain", "irc://irc.example.com/", "?"},
		{"irc.tls", "ircs://other.example.com:6697/", "?"},
		{"irc.ipv6", "ircs://[2001:db8::1]:6697/", "?"},
		{"irc.legacy", "/join ", " "},
	} {
		t.Run(tc.account, func(t *testing.T) {
			base := bridge.New(&config.Bridge{Account: tc.account})
			base.Config, base.Log = cfg, logrus.NewEntry(log)
			b := New(&bridge.Config{Bridge: base}).(*Birc)
			client, err := b.getClient()
			if err != nil {
				t.Fatal(err)
			}
			b.forwards = &ircForwards{rooms: map[string]*forwardRoom{}, client: client}
			pattern := regexp.MustCompile(`^summary \[查看合并转发：` + regexp.QuoteMeta(tc.prefix) +
				`(#mb-forward-[a-z2-7]+)` + regexp.QuoteMeta(tc.separator) + `([A-Z2-7]+)\]$`)
			seenKeys := map[string]bool{}
			for range 2 {
				msg := config.Message{Text: "summary", Extra: map[string][]any{
					config.ExtraForward: {&config.MessageForward{Nodes: []config.ForwardNode{{Username: "Alice", Text: "hello"}}}},
				}}
				b.prepareForwards(&msg)
				match := pattern.FindStringSubmatch(msg.Text)
				if match == nil {
					t.Fatalf("unexpected forward entry: %s", msg.Text)
				}
				if b.forwards.rooms[match[1]] == nil || len(match[2]) < 26 || seenKeys[match[2]] {
					t.Fatalf("missing room or non-independent key: %s", msg.Text)
				}
				seenKeys[match[2]] = true
			}
			if len(b.forwards.rooms) != 2 {
				t.Fatal("forwards did not create independent rooms")
			}
		})
	}
}

func TestForwardChannelURLValidation(t *testing.T) {
	for _, raw := range []string{"", "irc://irc.example.com", "ircs://irc.example.com:6697/", "irc://[2001:db8::1]:6667"} {
		if err := validateForwardChannelURL(raw); err != nil {
			t.Errorf("valid URL %q rejected: %v", raw, err)
		}
	}
	for _, raw := range []string{"irc.example.com", "https://irc.example.com", "irc://", "irc://host:bad", "irc://user:password@host", "irc://host/channel", "irc://host/?key", "irc://host/#channel", "irc://host/#", "irc://host\n"} {
		b := replyTestBridge(t)
		b.SetString("ForwardChannelURL", raw)
		if err := b.Connect(); err == nil || !strings.Contains(err.Error(), "ForwardChannelURL") {
			t.Fatalf("invalid URL %q not rejected at connect: %v", raw, err)
		}
	}
}
