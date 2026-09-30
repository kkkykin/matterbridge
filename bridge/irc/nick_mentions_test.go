package birc

import (
	"testing"

	"github.com/matterbridge-org/matterbridge/bridge/config"
)

func TestNickMentionIdentity(t *testing.T) {
	r := newIRCNickMentions()
	r.remember(config.Message{Protocol: "onebot", Account: "onebot.qq", SourceChannel: "123", Channel: "#a", Username: "张-三/qq", UserID: "888"})
	for _, tc := range []struct {
		text, channel string
		count         int
	}{
		{"@张-三/qq hello", "#a", 1},
		{"张-三/qq: hello @张-三/qq", "#a", 2},
		{"张-三/qq：你好", "#a", 1},
		{"张-三/QQ, hello", "#A", 1},
		{"@张-三/qq hello", "#b", 0},
		{"https://host/@张-三/qq [CQ:text,text=@张-三/qq] email@张-三/qq @@张-三/qq", "#a", 0},
		{"https://host/@张-三/qq @张-三/qq", "#a", 1},
		{"ordinary 张-三/qq text", "#a", 0},
		{"@unknown/qq hello", "#a", 0},
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
}

func TestNickMentionCollisions(t *testing.T) {
	original := config.Message{Protocol: "onebot", Account: "onebot.qq", SourceChannel: "123", Channel: "#a", Username: "888/qq", UserID: "888"}
	for _, field := range []string{"user", "account", "group", "protocol"} {
		t.Run(field, func(t *testing.T) {
			r := newIRCNickMentions()
			r.remember(original)
			conflict := original
			switch field {
			case "user":
				conflict.UserID = "777"
			case "account":
				conflict.Account = "onebot.other"
			case "group":
				conflict.SourceChannel = "456"
			case "protocol":
				conflict.Protocol = "irc"
			}
			r.remember(conflict)
			r.remember(original)
			m := config.Message{Text: "@888/qq", Channel: "#a"}
			r.mentions(&m)
			if len(m.MentionParts) != 0 {
				t.Fatal("ambiguous alias resolved to a user")
			}
		})
	}
}
