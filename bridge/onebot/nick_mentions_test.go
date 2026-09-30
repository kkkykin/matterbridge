package onebot

import (
	"testing"

	"github.com/matterbridge-org/matterbridge/bridge/config"
	ob "github.com/matterbridge-org/matterbridge/bridge/onebot/internal/client"
)

//nolint:gosmopolitan // Unicode nicknames must resolve to the correct QQ user.
func TestRelayedNickMentions(t *testing.T) {
	for _, tc := range []struct {
		name, account, channel string
		enabled, rewrite       bool
		want                   []ob.Segment
	}{
		{"native", "onebot.qq", "123", true, false, []ob.Segment{atSegment("888"), textSegment(": hi")}},
		{"different account", "onebot.other", "123", true, false, []ob.Segment{textSegment("张-三/qq: hi")}},
		{"different group", "onebot.qq", "456", true, false, []ob.Segment{textSegment("张-三/qq: hi")}},
		{"disabled", "onebot.qq", "123", false, false, []ob.Segment{textSegment("张-三/qq: hi")}},
		{"rewritten", "onebot.qq", "123", true, true, []ob.Segment{textSegment("rewritten")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, requests := mentionBridge(t, func(int64) any { return []ob.GroupMember{} })
			if !tc.enabled {
				b.Config.Config.SetVal("onebot.qq.AllowMention", []string{})
			}
			m := config.Message{Protocol: "irc", Text: "张-三/qq: hi", MentionParts: []config.MentionPart{
				{Text: "张-三/qq", Kind: config.MentionUser, UserID: "888", Account: tc.account, Channel: tc.channel},
				{Text: ": hi", Kind: config.MentionText},
			}}
			if tc.rewrite {
				m.Text = "rewritten"
			}
			sendMentionMessage(t, b, m)
			assertMentionSegments(t, nextMentionRequest(t, requests, "send_group_msg", 123), tc.want...)
		})
	}
}
