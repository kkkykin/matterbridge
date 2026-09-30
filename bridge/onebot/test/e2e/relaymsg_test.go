//go:build integration && !noonebot && !noirc

package e2e

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lrstanley/girc"
)

func TestRelaymsgBridge(t *testing.T) {
	for _, casemapping := range []string{"ascii", "precis"} {
		for _, split := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/split=%t", casemapping, split), func(t *testing.T) { testRelaymsgBridge(t, casemapping, split) })
		}
	}
}

func testRelaymsgBridge(t *testing.T, casemapping string, split bool) {
	address := startErgo(t, casemapping)
	irc := connectIRC(t, address)
	irc.send(t, "CAP LS 302\r\nCAP REQ :message-tags echo-message\r\nNICK probe\r\nUSER probe 0 * :relaymsg test\r\nCAP END\r\n")
	irc.wait(t, func(line string) bool { return strings.Contains(line, " 001 probe ") })
	irc.send(t, "JOIN #relaymsg\r\n")
	irc.wait(t, func(line string) bool { return strings.Contains(line, " 366 probe #relaymsg ") })
	ob := newOneBot(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "relaymsg.toml")
	writeFile(t, path, []byte(fmt.Sprintf(`[onebot.qq]
Server=%q
Token="ob-test-token"
RemoteNickFormat="[{PROTOCOL}] <{NICK}> "
[irc.local]
Server=%q
Nick="qq-bridge"
Charset="utf-8"
UseRelayMsg=true
ReverseMention=true
BotMentionTarget="probe"
MessageDelay=10
MessageSplit=%t
RemoteNickFormat="{NICK}-{USERID}/{PROTOCOL}"
[[gateway]]
name="relaymsg"
enable=true
[[gateway.inout]]
account="irc.local"
channel="#relaymsg"
[[gateway.inout]]
account="onebot.qq"
channel="123"
[[gateway.in]]
account="irc.local"
channel="#actions"
`, ob.url, address, split)))
	p := start(t, dir, "relaymsg-matterbridge", requiredEnv(t, "E2E_RELAY"), "-conf", path)
	peer := receive(t, ob.connections)
	irc.wait(t, func(line string) bool {
		return strings.Contains(line, ":qq-bridge!") && strings.Contains(line, " JOIN #relaymsg")
	})
	waitLog(t, p.logPath, "Now relaying messages", 1)
	irc.send(t, "MODE #relaymsg +o qq-bridge\r\n")
	irc.wait(t, func(line string) bool { return strings.Contains(line, " MODE #relaymsg +o qq-bridge") })
	peer.send(t, event(123, 901, 888, []segment{
		{Type: "at", Data: map[string]string{"qq": "999"}},
		{Type: "text", Data: map[string]string{"text": " hi @Bob"}},
	}))
	got := replyIRCMessage(t, irc, "probe hi Bob")
	nick := "群名片-888/onebot"
	if casemapping == "ascii" {
		nick = "888/onebot"
	}
	relayer, _ := got.Tags.Get("draft/relaymsg")
	if got.Source.Name != nick || relayer != "qq-bridge" {
		t.Fatalf("RELAYMSG sender: %s", got.String())
	}
	parentID, _ := got.Tags.Get("msgid")
	for _, body := range []string{"@" + got.Source.Name + " hello", got.Source.Name + ": hello"} {
		irc.send(t, "PRIVMSG #relaymsg :"+body+"\r\n")
		_ = replyIRCMessage(t, irc, body)
		suffix := " hello"
		if !strings.HasPrefix(body, "@") {
			suffix = ": hello"
		}
		assertSegments(t, ob.actions, []int64{123}, []segment{
			{Type: "text", Data: map[string]string{"text": "[irc] <probe> "}},
			{Type: "at", Data: map[string]string{"qq": "888"}},
			{Type: "text", Data: map[string]string{"text": suffix}},
		})
	}
	peer.send(t, event(123, 902, 888, []segment{
		{Type: "reply", Data: map[string]string{"id": "901"}},
		{Type: "text", Data: map[string]string{"text": "reply through relaymsg"}},
	}))
	got = replyIRCMessage(t, irc, "reply through relaymsg")
	assertIRCReply(t, got, parentID)
	long := strings.Repeat("你好🌉", 150)
	peer.send(t, event(123, 903, 888, long))
	reassembled := ""
	irc.wait(t, func(line string) bool {
		e := girc.ParseEvent(line)
		if e == nil || e.Command != girc.PRIVMSG || e.Source == nil || e.Source.Name != nick {
			return false
		}
		if !utf8.ValidString(e.Last()) {
			t.Fatalf("split corrupted UTF-8: %q", e.Last())
		}
		reassembled += strings.TrimSuffix(e.Last(), " <clipped message>")
		return len(reassembled) >= len(long)
	})
	if reassembled != long {
		t.Fatalf("RELAYMSG splitting changed message content: got %d bytes %q, want %d bytes", len(reassembled), reassembled, len(long))
	}
	irc.send(t, "JOIN #actions\r\n")
	irc.wait(t, func(line string) bool { return strings.Contains(line, " 366 probe #actions ") })
	irc.send(t, "PRIVMSG #actions :\x01ACTION waves\x01\r\n")
	_ = replyIRCMessage(t, irc, "waves\x01")
	got = replyIRCMessage(t, irc, "waves\x01")
	relayer, _ = got.Tags.Get("draft/relaymsg")
	if !got.IsAction() || relayer != "qq-bridge" {
		t.Fatalf("RELAYMSG action: %s", got.String())
	}
	assertActions(t, ob.actions, []int64{123}, "* [irc] <probe> waves")
	assertQuiet(t, irc, ob.actions)
	t.Log("PASS: RELAYMSG messages/actions, bidirectional OneBot mentions, native QQ @bot, UTF-8 splitting, native replies, and echo filtering")
}
