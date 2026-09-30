//go:build integration && !noonebot && !noirc

package e2e

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestErgoAutoreplay(t *testing.T) {
	for _, threading := range []bool{false, true} {
		t.Run(fmt.Sprintf("threading=%t", threading), func(t *testing.T) {
			address := startErgo(t, "ascii", func(cfg map[string]any) {
				cfg["history"].(map[string]any)["autoreplay-on-join"] = 100
			})
			irc := connectIRC(t, address)
			irc.send(t, "NICK probe\r\nUSER probe 0 * :history test\r\nJOIN #history\r\nMODE #history +m\r\nMODE #history -m\r\nPRIVMSG #history :old message\r\nNICK renamed\r\n")
			irc.wait(t, func(line string) bool { return strings.Contains(line, " NICK renamed") })

			// Verify that ordinary clients still receive both chat and HistServ replay.
			reader := connectIRC(t, address)
			reader.send(t, "NICK reader\r\nUSER reader 0 * :history reader\r\nJOIN #history\r\n")
			reader.wait(t, func(line string) bool {
				return strings.Contains(line, ":HistServ!") && strings.Contains(line, "set channel modes: +m")
			})
			reader.wait(t, func(line string) bool { return strings.Contains(line, " PRIVMSG #history :old message") })
			reader.wait(t, func(line string) bool {
				return strings.Contains(line, ":HistServ!") && strings.Contains(line, "changed nick to renamed")
			})

			ob := newOneBot(t)
			dir := t.TempDir()
			path := filepath.Join(dir, "history.toml")
			writeFile(t, path, []byte(fmt.Sprintf(`[onebot.qq]
Server=%q
Token="ob-test-token"
RemoteNickFormat="[{PROTOCOL}] <{NICK}> "
[irc.local]
Server=%q
Nick="qq-bridge"
Charset="utf-8"
MessageDelay=10
PreserveThreading=%t
RemoteNickFormat="{NICK}/{PROTOCOL}"
[[gateway]]
name="history"
enable=true
[[gateway.inout]]
account="irc.local"
channel="#history"
[[gateway.inout]]
account="onebot.qq"
channel="123"
`, ob.url, address, threading)))
			for run := 0; run < 2; run++ {
				p := start(t, dir, fmt.Sprintf("history-bridge-%d", run), requiredEnv(t, "E2E_RELAY"), "-conf", path)
				peer := receive(t, ob.connections)
				irc.wait(t, func(line string) bool {
					return strings.Contains(line, ":qq-bridge!") && strings.Contains(line, " JOIN #history")
				})
				waitLog(t, p.logPath, "Now relaying messages", 1)
				assertQuiet(t, irc, ob.actions)
				body := fmt.Sprintf("live message %d", run)
				irc.send(t, "PRIVMSG #history :"+body+"\r\n")
				assertActions(t, ob.actions, []int64{123}, "[irc] <renamed> "+body)
				peer.send(t, event(123, int64(901+run), 888, "live QQ message"))
				irc.wait(t, func(line string) bool {
					return strings.Contains(line, " PRIVMSG #history :") && strings.Contains(line, "live QQ message")
				})
				assertQuiet(t, irc, ob.actions)
				p.stop()
				irc.wait(t, func(line string) bool {
					return strings.Contains(line, ":qq-bridge!") && strings.Contains(line, " QUIT ")
				})
			}
		})
	}
}
