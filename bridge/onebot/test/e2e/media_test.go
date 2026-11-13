//go:build integration && !noonebot && !noirc

package e2e

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestIRCFileLinkFallback(t *testing.T) {
	address := startErgo(t, "ascii")
	irc := connectIRC(t, address)
	irc.send(t, "NICK probe\r\nUSER probe 0 * :media test\r\nJOIN #qq\r\nJOIN #qq2\r\n")
	irc.wait(t, func(line string) bool { return strings.Contains(line, " 366 probe #qq2 ") })
	var requests atomic.Int64
	media := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer media.Close()
	ob := newOneBot(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "media.toml")
	writeFile(t, path, []byte(fmt.Sprintf(`[onebot.qq]
Server=%q
Token="ob-test-token"
[irc.local]
Server=%q
Nick="qq-bridge"
Charset="utf-8"
MessageDelay=10
MediaLinkToken="test-token"
MediaLinkHosts=["127.0.0.1"]
[[gateway]]
name="media"
enable=true
[[gateway.in]]
account="irc.local"
channel="#qq"
[[gateway.out]]
account="irc.local"
channel="#qq2"
[[gateway.out]]
account="onebot.qq"
channel="123"
[[gateway.out]]
account="onebot.qq"
channel="456"
`, ob.url, address)))
	p := start(t, dir, "media-bridge", requiredEnv(t, "E2E_RELAY"), "-conf", path)
	receive(t, ob.connections)
	waitJoins(t, irc)
	waitLog(t, p.logPath, "Now relaying messages", 1)
	for _, tc := range []struct{ token, url string }{
		{"test-token", media.URL + "/private.png"},
		{"test-token:原始文件.png", "https://unlisted.example/photo.png"},
		{"wrong-token", media.URL + "/wrong.png"},
		{"test-token", "http://127.0.0.1/photo.png"},
	} {
		irc.send(t, "PRIVMSG #qq :["+tc.token+"]("+tc.url+")\r\n")
		want := "[irc] <probe> " + tc.url
		assertActions(t, ob.actions, []int64{123, 456}, want)
		irc.expectMessage(t, "#qq2", want)
	}
	// Inline and adjacent links must all lose their token on every destination.
	first := media.URL + "/first.png"
	second := media.URL + "/second.png"
	for _, tc := range []struct{ text, want string }{
		{"开头 [test-token:一.png](" + first + ") 中间 [test-token:二.png](" + second + ") 结尾", "开头 " + first + " 中间 " + second + " 结尾"},
		{"[test-token](" + first + ")[wrong-token](" + second + ")", first + " " + second},
		{"图文 ![test-token](" + first + ")", "图文 " + first},
	} {
		irc.send(t, "PRIVMSG #qq :"+tc.text+"\r\n")
		want := "[irc] <probe> " + tc.want
		assertActions(t, ob.actions, []int64{123, 456}, want)
		irc.expectMessage(t, "#qq2", want)
	}

	if requests.Load() != 0 {
		t.Fatal("downloader reached private endpoint")
	}
	irc.send(t, "PRIVMSG #qq :ordinary https://unlisted.example/photo.png\r\n")
	want := "[irc] <probe> ordinary https://unlisted.example/photo.png"
	assertActions(t, ob.actions, []int64{123, 456}, want)
	irc.expectMessage(t, "#qq2", want)
	assertQuiet(t, irc, ob.actions)
}
