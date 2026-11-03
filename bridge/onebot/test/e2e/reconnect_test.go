//go:build integration && !noonebot && !noirc

package e2e

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestErgoIRCReconnect(t *testing.T) {
	for _, relaymsg := range []bool{false, true} {
		t.Run(fmt.Sprintf("relaymsg=%t", relaymsg), func(t *testing.T) {
			t.Parallel()
			address := startErgo(t, "ascii", func(cfg map[string]any) {
				hash, err := bcrypt.GenerateFromPassword([]byte("reconnect-test"), bcrypt.MinCost)
				if err != nil {
					t.Fatal(err)
				}
				cfg["opers"] = map[string]any{"reconnect-test": map[string]any{"class": "server-admin", "password": string(hash)}}
			})
			irc := connectIRC(t, address)
			irc.send(t, "NICK probe\r\nUSER probe 0 * :reconnect test\r\nJOIN #reconnect\r\nOPER reconnect-test reconnect-test\r\n")
			irc.wait(t, func(line string) bool { return strings.Contains(line, " 381 probe ") })
			ob := newOneBot(t)
			dir := t.TempDir()
			path := filepath.Join(dir, "reconnect.toml")
			cfg := fmt.Sprintf(`[onebot.qq]
Server=%q
Token="ob-test-token"
RemoteNickFormat="[{PROTOCOL}] <{NICK}> "
[irc.local]
Server=%q
Nick="qq-bridge"
Charset="utf-8"
MessageDelay=10
UseRelayMsg=%t
RemoteNickFormat="{NICK}/{PROTOCOL}"
`, ob.url, address, relaymsg)
			// All three gateways share the same IRC bridge and channel.
			for _, group := range []int64{123, 456, 789} {
				cfg += fmt.Sprintf(`[[gateway]]
name="reconnect-%d"
enable=true
[[gateway.inout]]
account="irc.local"
channel="#reconnect"
[[gateway.inout]]
account="onebot.qq"
channel="%d"
`, group, group)
			}
			writeFile(t, path, []byte(cfg))
			p := start(t, dir, "reconnect-bridge", requiredEnv(t, "E2E_RELAY"), "-conf", path)
			peer := receive(t, ob.connections)
			joined := func(line string) bool {
				return strings.HasPrefix(line, ":qq-bridge!") && strings.Contains(line, " JOIN #reconnect")
			}
			irc.wait(t, joined)
			waitLog(t, p.logPath, "Now relaying messages", 1)
			grantOp := func() {
				irc.send(t, "MODE #reconnect +o qq-bridge\r\n")
				irc.wait(t, func(line string) bool { return strings.Contains(line, " MODE #reconnect +o qq-bridge") })
			}
			verify := func(id int64, phase string) {
				irc.send(t, "PRIVMSG #reconnect :IRC "+phase+"\r\n")
				assertActions(t, ob.actions, []int64{123, 456, 789}, "[irc] <probe> IRC "+phase)
				peer.send(t, event(123, id, 888, "QQ "+phase))
				irc.wait(t, func(line string) bool {
					return strings.Contains(line, " PRIVMSG #reconnect :") && strings.Contains(line, "QQ "+phase)
				})
				assertQuiet(t, irc, ob.actions)
			}
			grantOp()
			verify(1, "before reconnect")
			// Disconnect only the bot; the Ergo server and OneBot socket stay up.
			irc.send(t, "KILL qq-bridge :reconnect regression test\r\n")
			waitLog(t, p.logPath, "irc.local reconnecting in 60 seconds...", 1)
			deadline := time.NewTimer(75 * time.Second)
			defer deadline.Stop()
		waitRejoin:
			for {
				select {
				case line, ok := <-irc.lines:
					if !ok {
						t.Fatal("IRC probe disconnected")
					}
					if joined(line) {
						break waitRejoin
					}
				case <-deadline.C:
					t.Fatal("IRC bridge did not rejoin after disconnect")
				}
			}
			waitLog(t, p.logPath, "irc.local: joining #reconnect", 4)
			grantOp()
			verify(2, "after reconnect")
		})
	}
}
