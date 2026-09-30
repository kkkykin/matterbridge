//go:build integration && !noonebot && !noirc

package e2e

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lrstanley/girc"
)

// Exercise the real executable with IRCv3 IDs, client-only reply tags and
// echo-message against Ergo. TestNativeBridge covers disabled threading.
func TestNativeReplies(t *testing.T) {
	for _, relay := range []bool{false, true} {
		for _, split := range []bool{false, true} {
			t.Run(fmt.Sprintf("relay=%t/split=%t", relay, split), func(t *testing.T) {
				testNativeReplies(t, relay, split)
			})
		}
	}
}

func testNativeReplies(t *testing.T, relay, split bool) {
	address := startErgo(t, "ascii")
	nickFormat := "[{PROTOCOL}] <{NICK}> "
	if relay {
		nickFormat = "{USERID}/{PROTOCOL}"
	}
	irc := connectIRC(t, address)
	irc.send(t, "CAP LS 302\r\nCAP REQ :message-tags echo-message\r\nNICK probe\r\nUSER probe 0 * :reply test\r\nCAP END\r\n")
	irc.wait(t, func(line string) bool { return strings.Contains(line, " 001 probe ") })
	irc.send(t, "JOIN #reply\r\n")
	irc.wait(t, func(line string) bool { return strings.Contains(line, " 366 probe #reply ") })
	ob := newOneBot(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "replies.toml")
	writeFile(t, path, []byte(fmt.Sprintf(`[onebot.qq]
Server=%q
Token="ob-test-token"
RemoteNickFormat="[{PROTOCOL}] <{NICK}> "
[irc.local]
Server=%q
Nick="qq-bridge"
Charset="utf-8"
MessageDelay=10
MessageSplit=%t
UseRelayMsg=%t
RemoteNickFormat=%q
[[gateway]]
name="replies"
enable=true
[[gateway.inout]]
account="irc.local"
channel="#reply"
[[gateway.inout]]
account="onebot.qq"
channel="123"
[[gateway.inout]]
account="onebot.qq"
channel="456"
`, ob.url, address, split, relay, nickFormat)))
	p := start(t, dir, "reply-matterbridge", requiredEnv(t, "E2E_RELAY"), "-conf", path)
	peer := receive(t, ob.connections)
	irc.wait(t, func(line string) bool {
		return strings.Contains(line, ":qq-bridge!") && strings.Contains(line, " JOIN #reply")
	})
	waitLog(t, p.logPath, "Now relaying messages", 1)
	if relay {
		irc.send(t, "MODE #reply +o qq-bridge\r\n")
		irc.wait(t, func(line string) bool { return strings.Contains(line, " MODE #reply +o qq-bridge") })
	}

	irc.send(t, "PRIVMSG #reply :IRC parent\r\n")
	original := replyIRCMessage(t, irc, "IRC parent")
	originalID, _ := original.Tags.Get("msgid")
	if originalID == "" {
		t.Fatal("IRC parent has no server ID")
	}
	copies := replyActions(t, ob, map[int64]string{123: "", 456: ""}, "[irc] <probe> IRC parent")
	irc.send(t, "@+draft/reply="+originalID+" PRIVMSG #reply :IRC child\r\n")
	_ = replyIRCMessage(t, irc, "IRC child")
	replyActions(t, ob, copies, "[irc] <probe> IRC child")

	peer.send(t, event(123, 901, 888, []segment{{Type: "reply", Data: map[string]string{"id": copies[123]}}, {Type: "text", Data: map[string]string{"text": "QQ replies to IRC original"}}}))
	replyActions(t, ob, map[int64]string{456: copies[456]}, "[onebot] <群名片> QQ replies to IRC original")
	assertIRCReply(t, replyIRCMessage(t, irc, "QQ replies to IRC original"), originalID)

	peer.send(t, event(123, 902, 888, "QQ parent"))
	qqCopies := replyActions(t, ob, map[int64]string{456: ""}, "[onebot] <群名片> QQ parent")
	qqOnIRC := replyIRCMessage(t, irc, "QQ parent")
	if relay && qqOnIRC.Source.Name != "888/onebot" {
		t.Fatalf("RELAYMSG sender: %s", qqOnIRC.String())
	}
	qqOnIRCID, _ := qqOnIRC.Tags.Get("msgid")
	if token, ok := qqOnIRC.Tags.Get("+matterbridge/id"); !ok || token == "" {
		t.Fatal("bridge send is missing echo correlation")
	}
	// The server delivers its own echo before the next probe command.
	irc.send(t, "@+draft/reply="+qqOnIRCID+" PRIVMSG #reply :IRC replies to QQ copy\r\n")
	_ = replyIRCMessage(t, irc, "IRC replies to QQ copy")
	replyActions(t, ob, map[int64]string{123: "902", 456: qqCopies[456]}, "[irc] <probe> IRC replies to QQ copy")

	peer.send(t, event(123, 903, 888, []segment{{Type: "reply", Data: map[string]string{"id": "902"}}, {Type: "text", Data: map[string]string{"text": "QQ child"}}}))
	replyActions(t, ob, map[int64]string{456: qqCopies[456]}, "[onebot] <群名片> QQ child")
	assertIRCReply(t, replyIRCMessage(t, irc, "QQ child"), qqOnIRCID)

	if split {
		peer.send(t, event(123, 904, 888, "fragment one\nfragment two\nfragment three"))
		fragments := replyActions(t, ob, map[int64]string{456: ""}, "[onebot] <群名片> fragment one\nfragment two\nfragment three")
		first := replyIRCMessage(t, irc, "fragment one")
		second := replyIRCMessage(t, irc, "fragment two")
		third := replyIRCMessage(t, irc, "fragment three")
		firstID, _ := first.Tags.Get("msgid")
		secondID, _ := second.Tags.Get("msgid")
		correlation, _ := first.Tags.Get("+matterbridge/id")
		for _, fragment := range []*girc.Event{second, third} {
			if token, _ := fragment.Tags.Get("+matterbridge/id"); token == "" || token != correlation {
				t.Fatalf("fragment lost its logical message identity: %s", fragment.String())
			}
		}
		irc.send(t, "@+draft/reply="+secondID+" PRIVMSG #reply :reply to second fragment\r\n")
		_ = replyIRCMessage(t, irc, "reply to second fragment")
		replyActions(t, ob, map[int64]string{123: "904", 456: fragments[456]}, "[irc] <probe> reply to second fragment")
		peer.send(t, event(123, 905, 888, []segment{{Type: "reply", Data: map[string]string{"id": "904"}}, {Type: "text", Data: map[string]string{"text": "reply targets first fragment"}}}))
		replyActions(t, ob, map[int64]string{456: fragments[456]}, "[onebot] <群名片> reply targets first fragment")
		assertIRCReply(t, replyIRCMessage(t, irc, "reply targets first fragment"), firstID)
	}

	irc.send(t, "@+draft/reply=missing-parent PRIVMSG #reply :unknown parent\r\n")
	_ = replyIRCMessage(t, irc, "unknown parent")
	replyActions(t, ob, map[int64]string{123: "", 456: ""}, "[irc] <probe> [回复 #missing-parent] unknown parent")
	assertQuiet(t, irc, ob.actions)
	t.Log("PASS: native IRC/QQ replies, replies to bridge copies, channel-scoped parents, unknown-parent fallback and echo filtering")
}

func replyActions(t *testing.T, ob *oneBot, parents map[int64]string, text string) map[int64]string {
	t.Helper()
	ids := map[int64]string{}
	for range parents {
		a := receive(t, ob.actions)
		parent, ok := parents[a.Params.GroupID]
		if !ok || ids[a.Params.GroupID] != "" {
			t.Fatalf("unexpected reply target: %+v", a)
		}
		var want []segment
		if parent != "" {
			want = append(want, segment{Type: "reply", Data: map[string]string{"id": parent}})
		}
		want = append(want, segment{Type: "text", Data: map[string]string{"text": text}})
		assertActionSegments(t, a, a.Params.GroupID, want)
		ids[a.Params.GroupID] = strconv.FormatInt(a.MessageID, 10)
	}
	return ids
}

func replyIRCMessage(t *testing.T, irc *ircClient, suffix string) *girc.Event {
	t.Helper()
	var found *girc.Event
	irc.wait(t, func(line string) bool {
		e := girc.ParseEvent(line)
		if e != nil && e.Command == girc.PRIVMSG && strings.HasSuffix(e.Last(), suffix) {
			found = e
			return true
		}
		return false
	})
	return found
}

func assertIRCReply(t *testing.T, event *girc.Event, parent string) {
	t.Helper()
	if got, _ := event.Tags.Get("+draft/reply"); got != parent {
		t.Fatalf("IRC parent=%q want=%q: %s", got, parent, event.String())
	}
	if strings.Contains(event.Last(), "[回复") {
		t.Fatalf("native reply duplicated a textual quote: %s", event.String())
	}
}
