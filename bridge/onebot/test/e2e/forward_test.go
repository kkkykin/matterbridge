//go:build integration && !noonebot && !noirc

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/lrstanley/girc"
	"golang.org/x/crypto/bcrypt"
)

func TestForwardChannels(t *testing.T) {
	t.Run("join", func(t *testing.T) { testForwardChannels(t, "") })
	t.Run("public_url", func(t *testing.T) { testForwardChannels(t, "irc://irc.example.com:16667/") })
}

func testForwardChannels(t *testing.T, publicURL string) {
	address := startErgo(t, "precis", func(cfg map[string]any) {
		hash, err := bcrypt.GenerateFromPassword([]byte("forward-test"), bcrypt.MinCost)
		if err != nil {
			t.Fatal(err)
		}
		cfg["opers"] = map[string]any{"forward-test": map[string]any{"class": "server-admin", "password": string(hash)}}
	})
	reader := forwardReader(t, address, "reader")
	reader.send(t, "JOIN #qq\r\n")
	reader.wait(t, func(s string) bool { return strings.Contains(s, " 366 reader #qq ") })
	fixtures := map[string]json.RawMessage{
		"outer":  json.RawMessage(`{"message":[{"type":"node","data":{"user_id":"123","nickname":"Alice","content":[{"type":"text","data":{"text":"hello 转发"}},{"type":"forward","data":{"id":"nested"}}]}}]}`),
		"nested": json.RawMessage(`{"messages":[{"user_id":456,"sender":{"nickname":"Bob"},"message":[{"type":"image","data":{"url":"https://example.com/image.png"}},{"type":"text","data":{"text":"nested body"}},{"type":"forward","data":{"id":"not-an-api-resource","content":[{"user_id":789,"sender":{"nickname":"Carol"},"message":[{"type":"node","data":{"user_id":"321","nickname":"Dave","content":"deep body"}}]}]}}]}]}`),
	}
	if path := os.Getenv("E2E_LIVE_FORWARD_FILE"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		fixtures["live"] = data
	}
	ob := newOneBot(t, fixtures)
	dir := t.TempDir()
	path := filepath.Join(dir, "forward.toml")
	writeFile(t, path, []byte(fmt.Sprintf(`[onebot.qq]
Server=%q
Token="ob-test-token"
[irc.local]
Server=%q
Nick="qq-bridge"
Charset="utf-8"
MessageDelay=10
MessageQueue=10
ForwardChannelTimeout=2
ForwardChannelURL=%q
RemoteNickFormat="<{NICK}> "
[[gateway]]
name="forward"
enable=true
[[gateway.inout]]
account="irc.local"
channel="#qq"
[[gateway.inout]]
account="onebot.qq"
channel="123"
`, ob.url, address, publicURL)))
	p := start(t, dir, "forward-matterbridge", requiredEnv(t, "E2E_RELAY"), "-conf", path)
	peer := receive(t, ob.connections)
	reader.wait(t, func(s string) bool { return strings.Contains(s, ":qq-bridge!") && strings.Contains(s, " JOIN #qq") })
	waitLog(t, p.logPath, "Now relaying messages", 1)
	seq := int64(8000)
	keys := map[string]string{}
	entryPrefix := "/join "
	separator := " "
	if publicURL != "" {
		entryPrefix = strings.TrimSuffix(publicURL, "/") + "/"
		separator = "?"
	}
	entry := regexp.MustCompile(regexp.QuoteMeta(entryPrefix) + `(#mb-forward-[a-z2-7]+)` + regexp.QuoteMeta(separator) + `([A-Z2-7]+)`)
	open := func(id string) string {
		seq++
		peer.send(t, event(123, seq, 888, []segment{{Type: "forward", Data: map[string]string{"id": id}}}))
		line := reader.wait(t, func(s string) bool {
			return strings.Contains(s, " PRIVMSG #qq ") && strings.Contains(s, "查看合并转发：")
		})
		match := entry.FindStringSubmatch(line)
		if match == nil {
			t.Fatalf("unexpected forward entry: %s", line)
		}
		keys[match[1]] = match[2]
		return match[1]
	}
	join := func(c *ircClient, channel string) {
		c.send(t, "JOIN "+channel+" "+keys[channel]+"\r\n")
	}
	channel := open("outer")
	// The advertised key must already be active when the entry reaches readers.
	reader.send(t, "JOIN "+channel+"\r\n")
	reader.wait(t, func(s string) bool { return strings.Contains(s, " 475 reader "+channel+" ") })
	reader.send(t, "JOIN "+channel+" wrong-key\r\n")
	reader.wait(t, func(s string) bool { return strings.Contains(s, " 475 reader "+channel+" ") })
	join(reader, channel)
	transcript := forwardTranscript(t, reader, channel)
	for _, want := range []string{"Alice", "hello 转发", "↳ Bob", "https://example.com/image.png", "nested body", "↳ ↳ Carol", "↳ ↳ ↳ Dave", "deep body"} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("missing %q in %s", want, transcript)
		}
	}
	// Moderated, secret viewing channels cannot feed user messages back to QQ.
	reader.send(t, "PRIVMSG "+channel+" :must not route to QQ\r\n")
	reader.wait(t, func(s string) bool { return strings.Contains(s, " 404 ") })
	second := forwardReader(t, address, "second")
	second.send(t, "JOIN "+channel+" wrong-key\r\n")
	second.wait(t, func(s string) bool { return strings.Contains(s, " 475 second "+channel+" ") })
	join(second, channel)
	if got := forwardTranscript(t, second, channel); got != transcript {
		t.Fatal("late reader did not get the whole transcript")
	}
	reader.send(t, "NICK renamed\r\nPART "+channel+"\r\n")
	second.wait(t, func(s string) bool { return strings.Contains(s, ":renamed!") && strings.Contains(s, " PART "+channel) })
	forwardNames(t, second, "second", channel, true)
	// Last-reader QUIT must remove the bot, even without a PART.
	second.send(t, "QUIT :done\r\n")
	awaitForwardGone(t, reader, "renamed", channel)
	// A kicked viewer bot must discard the room rather than rejoin it through
	// the normal gateway KICK handler.
	channel = open("outer")
	join(reader, channel)
	forwardTranscript(t, reader, channel)
	reader.send(t, "OPER forward-test forward-test\r\n")
	reader.wait(t, func(s string) bool { return strings.Contains(s, " 381 renamed ") })
	reader.send(t, "SAMODE "+channel+" +o renamed\r\n")
	reader.wait(t, func(s string) bool { return strings.Contains(s, " MODE "+channel+" +o renamed") })
	reader.send(t, "KICK "+channel+" qq-bridge :test\r\n")
	reader.wait(t, func(s string) bool { return strings.Contains(s, " KICK "+channel+" qq-bridge ") })
	reader.send(t, "PART "+channel+"\r\n")
	awaitForwardGone(t, reader, "renamed", channel)
	channel = open("outer")
	join(reader, channel)
	forwardTranscript(t, reader, channel)
	reader.send(t, "PART "+channel+"\r\n")
	awaitForwardGone(t, reader, "renamed", channel)
	// An unvisited channel must expire, without needing any subsequent event.
	channel = open("outer")
	time.Sleep(2200 * time.Millisecond)
	forwardNames(t, reader, "renamed", channel, false)
	channel = open("missing")
	join(reader, channel)
	if got := forwardTranscript(t, reader, channel); !strings.Contains(got, "读取失败") {
		t.Fatal(got)
	}
	reader.send(t, "PART "+channel+"\r\n")
	awaitForwardGone(t, reader, "renamed", channel)
	if fixtures["live"] != nil {
		channel = open("live")
		join(reader, channel)
		got := forwardTranscript(t, reader, channel)
		if strings.Contains(got, "读取失败") || strings.Contains(got, "无法解析") {
			t.Fatal("live transcript failed")
		}
		var raw any
		if err := json.Unmarshal(fixtures["live"], &raw); err != nil {
			t.Fatal(err)
		}
		var checkText func(any)
		checkText = func(value any) {
			switch v := value.(type) {
			case []any:
				for _, item := range v {
					checkText(item)
				}
			case map[string]any:
				if v["type"] == "text" {
					data, _ := v["data"].(map[string]any)
					text, _ := data["text"].(string)
					if !strings.Contains(got, text) {
						t.Fatal("live nested text missing from IRC transcript")
					}
				}
				for key, item := range v {
					if key == "message" || key == "messages" || key == "data" || key == "content" {
						checkText(item)
					}
				}
			}
		}
		checkText(raw)
		reader.send(t, "PART "+channel+"\r\n")
		awaitForwardGone(t, reader, "renamed", channel)
		t.Log("real OneBot get_forward_msg response rendered successfully on Ergo")
	}
	assertQuiet(t, reader, ob.actions)
}

func forwardReader(t *testing.T, address, nick string) *ircClient {
	c := connectIRC(t, address)
	c.send(t, "NICK "+nick+"\r\nUSER "+nick+" 0 * :forward reader\r\n")
	c.wait(t, func(s string) bool { return strings.Contains(s, " 001 "+nick+" ") })
	return c
}

func forwardTranscript(t *testing.T, c *ircClient, channel string) string {
	var lines []string
	c.wait(t, func(s string) bool {
		e := girc.ParseEvent(s)
		if e == nil || e.Command != girc.PRIVMSG || e.Params[0] != channel {
			return false
		}
		if e.Source.Name != "qq-bridge" {
			t.Fatalf("unexpected viewer sender: %s", e.Source.Name)
		}
		lines = append(lines, e.Last())
		return e.Last() == "[合并转发结束]"
	})
	return strings.Join(lines, "\n")
}

// Rejoining after cleanup creates an empty, new channel and gives us op.
func awaitForwardGone(t *testing.T, c *ircClient, nick, channel string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		c.send(t, "JOIN "+channel+"\r\n")
		found := false
		c.wait(t, func(s string) bool {
			e := girc.ParseEvent(s)
			if e == nil {
				return false
			}
			if e.Command == girc.ERR_BADCHANNELKEY && len(e.Params) >= 2 && e.Params[1] == channel {
				found = true // The keyed room has not been cleaned up yet.
				return true
			}
			if e.Command == girc.RPL_NAMREPLY && strings.Contains(e.Last(), "qq-bridge") {
				found = true
			}
			return strings.Contains(s, " 366 "+nick+" "+channel+" ")
		})
		c.send(t, "PART "+channel+"\r\n")
		if !found {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("bot did not leave empty forward channel")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func forwardNames(t *testing.T, c *ircClient, nick, channel string, wantBot bool) {
	t.Helper()
	// Secret channels hide NAMES from outsiders; JOIN is needed to verify expiry.
	if !wantBot {
		awaitForwardGone(t, c, nick, channel)
		return
	}
	c.send(t, "NAMES "+channel+"\r\n")
	found := false
	c.wait(t, func(s string) bool {
		e := girc.ParseEvent(s)
		if e != nil && e.Command == girc.RPL_NAMREPLY && strings.Contains(e.Last(), "qq-bridge") {
			found = true
		}
		return strings.Contains(s, " 366 "+nick+" "+channel+" ")
	})
	if !found {
		t.Fatal("bot left while another reader was present")
	}
}
