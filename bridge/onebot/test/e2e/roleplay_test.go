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

func TestRoleplayBridge(t *testing.T) {
	for _, casemapping := range []string{"ascii", "precis"} {
		t.Run(casemapping, func(t *testing.T) { testRoleplayBridge(t, casemapping) })
	}
}

func testRoleplayBridge(t *testing.T, casemapping string) {
	address := startErgo(t, casemapping)
	irc := connectIRC(t, address)
	irc.send(t, "CAP LS 302\r\nCAP REQ :message-tags echo-message\r\nNICK probe\r\nUSER probe 0 * :roleplay test\r\nCAP END\r\n")
	irc.wait(t, func(line string) bool { return strings.Contains(line, " 001 probe ") })
	irc.send(t, "JOIN #roleplay\r\n")
	irc.wait(t, func(line string) bool { return strings.Contains(line, " 366 probe #roleplay ") })
	irc.send(t, "MODE #roleplay +E\r\nPING :roleplay-ready\r\n")
	irc.wait(t, func(line string) bool {
		return strings.Contains(line, " PONG ") && strings.Contains(line, "roleplay-ready")
	})
	ob := newOneBot(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "roleplay.toml")
	writeFile(t, path, []byte(fmt.Sprintf(`[onebot.qq]
Server=%q
Token="ob-test-token"
RemoteNickFormat="[{PROTOCOL}] <{NICK}> "
[irc.local]
Server=%q
Nick="qq-bridge"
Charset="utf-8"
UseRoleplay=true
ReverseMention=true
BotMentionTarget="probe"
MessageDelay=10
MessageSplit=false
RemoteNickFormat="{NICK}-{USERID}"
[[gateway]]
name="roleplay"
enable=true
[[gateway.inout]]
account="irc.local"
channel="#roleplay"
[[gateway.inout]]
account="onebot.qq"
channel="123"
[[gateway.in]]
account="irc.local"
channel="#actions"
`, ob.url, address)))
	p := start(t, dir, "roleplay-matterbridge", requiredEnv(t, "E2E_RELAY"), "-conf", path)
	peer := receive(t, ob.connections)
	irc.wait(t, func(line string) bool {
		return strings.Contains(line, ":qq-bridge!") && strings.Contains(line, " JOIN #roleplay")
	})
	waitLog(t, p.logPath, "Now relaying messages", 1)
	peer.send(t, event(123, 901, 888, []segment{
		{Type: "at", Data: map[string]string{"qq": "999"}},
		{Type: "text", Data: map[string]string{"text": " hi @Bob"}},
	}))
	got := replyIRCMessage(t, irc, "probe hi Bob (qq-bridge)")
	nick := "*群名片-888*"
	if casemapping == "ascii" {
		nick = "*888*"
	}
	if got.Source.Name != nick || got.Source.Ident != "qq-bridge" {
		t.Fatalf("roleplay sender: %s", got.String())
	}
	for _, body := range []string{"@" + got.Source.Name + " hello", got.Source.Name + ": hello"} {
		irc.send(t, "PRIVMSG #roleplay :"+body+"\r\n")
		_ = replyIRCMessage(t, irc, body)
		suffix := " hello"
		if strings.HasPrefix(body, "*") {
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
		{Type: "text", Data: map[string]string{"text": "reply through roleplay"}},
	}))
	got = replyIRCMessage(t, irc, "reply through roleplay (qq-bridge)")
	if !strings.Contains(got.Last(), "[回复") || strings.Contains(got.Last(), "[onebot]") {
		t.Fatalf("roleplay quote fallback: %s", got.String())
	}
	long := strings.Repeat("你好🌉", 150)
	peer.send(t, event(123, 903, 888, long))
	reassembled := ""
	irc.wait(t, func(line string) bool {
		e := girc.ParseEvent(line)
		if e == nil || e.Command != girc.PRIVMSG || e.Source.Host != "npc.fakeuser.invalid" {
			return false
		}
		if !utf8.ValidString(e.Last()) {
			t.Fatalf("split corrupted UTF-8: %q", e.Last())
		}
		fragment := strings.TrimSuffix(e.Last(), " (qq-bridge)")
		reassembled += strings.TrimSuffix(fragment, " <clipped message>")
		return len(reassembled) >= len(long)
	})
	if reassembled != long {
		t.Fatalf("roleplay splitting changed message content: got %d bytes %q, want %d bytes", len(reassembled), reassembled, len(long))
	}
	irc.send(t, "JOIN #actions\r\n")
	irc.wait(t, func(line string) bool { return strings.Contains(line, " 366 probe #actions ") })
	irc.send(t, "PRIVMSG #actions :\x01ACTION waves\x01\r\n")
	_ = replyIRCMessage(t, irc, "waves\x01")
	got = replyIRCMessage(t, irc, "waves (qq-bridge)\x01")
	if !got.IsAction() || got.Source.Host != "npc.fakeuser.invalid" {
		t.Fatalf("NPCA action: %s", got.String())
	}
	assertActions(t, ob.actions, []int64{123}, "* [irc] <probe> waves")
	assertQuiet(t, irc, ob.actions)
	t.Log("PASS: NPC/NPCA, bidirectional OneBot mentions, native QQ @bot, UTF-8 splitting, quote fallback, and mandatory echo filtering")
}
