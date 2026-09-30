//go:build !noonebot && !noirc

package gateway

import (
	"testing"

	"github.com/matterbridge-org/matterbridge/bridge"
	"github.com/matterbridge-org/matterbridge/bridge/config"
)

func TestReplyMappingRoundTripAndScope(t *testing.T) {
	r := maketestRouter([]byte(`[irc.local]
Server="localhost:6667"
[onebot.qq]
Server="ws://localhost:3001"
[[gateway]]
name="test"
enable=true
[[gateway.inout]]
account="irc.local"
channel="#a"
[[gateway.inout]]
account="irc.local"
channel="#b"
[[gateway.inout]]
account="onebot.qq"
channel="123"
[[gateway.inout]]
account="onebot.qq"
channel="456"
`))
	gw := r.Gateways["test"]
	irc, qq := gw.Bridges["irc.local"], gw.Bridges["onebot.qq"]
	ircA, ircB := gw.Channels["#airc.local"], gw.Channels["#birc.local"]
	qqA, qqB := gw.Channels["123onebot.qq"], gw.Channels["456onebot.qq"]
	original := config.Message{Account: irc.Account, Channel: ircA.Name, ID: "wire-1", Username: "Alice", Text: "first"}
	gw.rememberReply(&original, []*BrMsgID{{br: qq, ID: "onebot 42", ChannelID: qqA.ID}})
	// An identical QQ ID in another group is a different message.
	other := config.Message{Account: qq.Account, Channel: qqB.Name, ID: "42", Username: "Bob", Text: "other group"}
	gw.rememberReply(&other, []*BrMsgID{{br: irc, ID: "irc local-2", ChannelID: ircB.ID}})
	for _, tc := range []struct {
		name         string
		source       config.Message
		dest         *bridge.Bridge
		channel      *config.ChannelInfo
		parent, text string
	}{
		{"reply to bridge copy goes back to original", config.Message{Account: qq.Account, Channel: qqA.Name, ParentID: "42"}, irc, ircA, "wire-1", "first"},
		{"IRC reply finds QQ copy", config.Message{Account: irc.Account, Channel: ircA.Name, ParentID: "wire-1"}, qq, qqA, "42", "first"},
		{"same ID other group", config.Message{Account: qq.Account, Channel: qqB.Name, ParentID: "42"}, irc, ircB, "local-2", "other group"},
		{"no cross-group fallback", config.Message{Account: irc.Account, Channel: ircA.Name, ParentID: "wire-1"}, qq, qqB, config.ParentIDNotFound, "first"},
		{"no cross-account lookup", config.Message{Account: "onebot.other", Channel: qqA.Name, ParentID: "42"}, irc, ircA, config.ParentIDNotFound, ""},
		{"unknown", config.Message{Account: qq.Account, Channel: qqA.Name, ParentID: "unknown"}, irc, ircA, config.ParentIDNotFound, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent, quote := gw.replyForDestination(&tc.source, tc.dest, tc.channel)
			if parent != tc.parent || quote == nil || quote.Text != tc.text {
				t.Fatalf("got parent=%q quote=%+v", parent, quote)
			}
		})
	}
	irc.SetBool("PreserveThreading", false)
	parent, quote := gw.replyForDestination(&config.Message{Account: qq.Account, Channel: qqA.Name, ParentID: "42"}, irc, ircA)
	if parent != "" || quote.Text != "first" {
		t.Fatal("disabled threading did not preserve textual context")
	}
	gw.replies.Purge()
	parent, quote = gw.replyForDestination(&config.Message{Account: irc.Account, Channel: ircA.Name, ParentID: "wire-1"}, qq, qqA)
	if parent != config.ParentIDNotFound || quote.Text != "" {
		t.Fatal("evicted mapping reused a foreign ID")
	}
}
