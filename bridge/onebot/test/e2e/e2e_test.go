//go:build integration && !noonebot && !noirc

// This test starts the matterbridge executable and Ergo.
// Only the QQ-facing OneBot 11 server is simulated; no QQ account is required.
package e2e

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/matterbridge-org/matterbridge/bridge/config"
)

func TestNativeBridge(t *testing.T) {
	dir := t.TempDir()
	ircAddress, apiAddress := startErgo(t, "ascii"), freeAddress(t)
	irc := connectIRC(t, ircAddress)
	irc.send(t, "NICK probe\r\nUSER probe 0 * :integration test\r\n")
	irc.wait(t, func(line string) bool { return strings.Contains(line, " 001 probe ") })
	irc.send(t, "JOIN #qq\r\nJOIN #qq2\r\n")
	irc.wait(t, func(line string) bool { return strings.Contains(line, " 366 probe #qq2 ") })
	ob := newOneBot(t)
	// This is the only routing configuration. QQ groups are native channels.
	cfg := fmt.Sprintf(`[onebot.qq]
Server = %q
Token = "ob-test-token"
RemoteNickFormat = "[{PROTOCOL}] <{NICK}> "
[irc.local]
Server = %q
Nick = "qq-bridge"
RemoteNickFormat = "[QQ] <{NICK}> "
Charset = "utf-8"
MessageDelay = 10
MessageQueue = 100
PreserveThreading = false
ReverseMention = true
BotMentionTarget = "probe"
[api.testing]
BindAddress = %q
Token = "test-token"
RemoteNickFormat = "{NICK}"
[[gateway]]
name = "shared"
enable = true
[[gateway.inout]]
account = "irc.local"
channel = "#qq"
[[gateway.inout]]
account = "onebot.qq"
channel = "123"
[[gateway.inout]]
account = "onebot.qq"
channel = "456"
[[gateway.inout]]
account = "api.testing"
channel = "api"
[[gateway]]
name = "isolated"
enable = true
[[gateway.inout]]
account = "irc.local"
channel = "#qq2"
[[gateway.inout]]
account = "onebot.qq"
channel = "789"
[[gateway]]
name = "qq-only"
enable = true
[[gateway.in]]
account = "onebot.qq"
channel = "500"
[[gateway.out]]
account = "onebot.qq"
channel = "600"
[[gateway.inout]]
account = "onebot.qq"
channel = "700"
[[gateway]]
name = "additional-subscription"
enable = true
[[gateway.in]]
account = "onebot.qq"
channel = "123"
[[gateway.out]]
account = "onebot.qq"
channel = "800"
`, ob.url, ircAddress, apiAddress)
	path := filepath.Join(dir, "matterbridge.toml")
	writeFile(t, path, []byte(cfg))
	binary := requiredEnv(t, "E2E_RELAY")
	process := start(t, dir, "native-matterbridge", binary, "-conf", path)
	waitJoins(t, irc)
	peer := receive(t, ob.connections)
	waitLog(t, process.logPath, "Now relaying messages", 1)
	conn, _, err := websocket.DefaultDialer.Dial("ws://"+apiAddress+"/api/websocket", http.Header{"Authorization": []string{"Bearer test-token"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	irc.send(t, "PRIVMSG #qq :hello [CQ:at,qq=all]\r\n")
	assertActions(t, ob.actions, []int64{123, 456}, "[irc] <probe> hello [CQ:at,qq=all]")
	irc.send(t, "PRIVMSG #qq :@Alice 看一下 @123456 @同名 @all\r\n")
	mentionTargets := map[int64]string{123: "111111", 456: "222222"}
	for range 2 {
		a := receive(t, ob.actions)
		qq, ok := mentionTargets[a.Params.GroupID]
		if !ok {
			t.Fatalf("unexpected mention target: %+v", a)
		}
		delete(mentionTargets, a.Params.GroupID)
		assertActionSegments(t, a, a.Params.GroupID, []segment{
			{Type: "text", Data: map[string]string{"text": "[irc] <probe> "}},
			{Type: "at", Data: map[string]string{"qq": qq}},
			{Type: "text", Data: map[string]string{"text": " 看一下 "}},
			{Type: "at", Data: map[string]string{"qq": "123456"}},
			{Type: "text", Data: map[string]string{"text": " @同名 @all"}},
		})
	}
	irc.send(t, "PRIVMSG #qq2 :isolated\r\n")
	assertActions(t, ob.actions, []int64{789}, "[irc] <probe> isolated")
	irc.send(t, "PRIVMSG #qq :\x01ACTION waves\x01\r\n")
	assertActions(t, ob.actions, []int64{123, 456}, "* [irc] <probe> waves")
	peer.send(t, event(123, 1, 888, "你好 from QQ"))
	assertActions(t, ob.actions, []int64{456, 800}, "[onebot] <群名片> 你好 from QQ")
	irc.expectMessage(t, "#qq", "[QQ] <群名片> 你好 from QQ")
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		var m config.Message
		if err := conn.ReadJSON(&m); err != nil {
			t.Fatal(err)
		}
		if m.Protocol == "onebot" {
			if m.Account != "onebot.qq" || m.Channel != "123" || m.Gateway != "shared" {
				t.Fatalf("lost native source: %+v", m)
			}
			break
		}
	}
	if err := conn.WriteJSON(config.Message{Text: "external API", Username: "api-user", Gateway: "shared"}); err != nil {
		t.Fatal(err)
	}
	assertActions(t, ob.actions, []int64{123, 456}, "[api] <api-user> external API")
	irc.expectMessage(t, "#qq", "[QQ] <api-user> external API")
	// The incoming QQ representation stays textual; QQ destinations can now
	// translate it back into a native mention without changing the IRC output.
	peer.send(t, event(123, 10, 888, []segment{
		{Type: "at", Data: map[string]string{"qq": "123456"}},
		{Type: "text", Data: map[string]string{"text": " from QQ"}},
	}))
	assertSegments(t, ob.actions, []int64{456, 800}, []segment{
		{Type: "text", Data: map[string]string{"text": "[onebot] <群名片> "}},
		{Type: "at", Data: map[string]string{"qq": "123456"}},
		{Type: "text", Data: map[string]string{"text": " from QQ"}},
	})
	irc.expectMessage(t, "#qq", "[QQ] <群名片> @123456 from QQ")
	// QQ @bot targets a configured IRC nick; ordinary @nick can address any
	// IRC user. The same source still has its original mentions on QQ outputs.
	peer.send(t, event(123, 20, 888, []segment{
		{Type: "at", Data: map[string]string{"qq": "999"}},
		{Type: "text", Data: map[string]string{"text": " check with @Bob"}},
	}))
	assertSegments(t, ob.actions, []int64{456, 800}, []segment{
		{Type: "text", Data: map[string]string{"text": "[onebot] <群名片> "}},
		{Type: "at", Data: map[string]string{"qq": "999"}},
		{Type: "text", Data: map[string]string{"text": " check with @Bob"}},
	})
	irc.expectMessage(t, "#qq", "[QQ] <群名片> probe check with Bob")
	peer.send(t, event(123, 21, 888, "@probe arbitrary user"))
	assertActions(t, ob.actions, []int64{456, 800}, "[onebot] <群名片> @probe arbitrary user")
	irc.expectMessage(t, "#qq", "[QQ] <群名片> probe arbitrary user")
	peer.send(t, event(123, 22, 888, "literal &#91;CQ:at,qq=999&#93; https://example.org/@Bob"))
	assertActions(t, ob.actions, []int64{456, 800}, "[onebot] <群名片> literal [CQ:at,qq=999] https://example.org/@Bob")
	irc.expectMessage(t, "#qq", "[QQ] <群名片> literal [CQ:at,qq=999] https://example.org/@Bob")
	// Dedup, bot echo, private messages and unconfigured groups must not route.
	peer.send(t, event(123, 1, 888, "duplicate"))
	peer.send(t, event(456, 2, 999, "self echo"))
	peer.send(t, event(9999, 3, 888, "unconfigured"))
	private := event(123, 4, 888, "private")
	private["message_type"] = "private"
	peer.send(t, private)
	assertQuiet(t, irc, ob.actions)
	// Directionality comes entirely from upstream in/out/inout routing.
	peer.send(t, event(500, 5, 888, "input only"))
	assertActions(t, ob.actions, []int64{600, 700}, "[onebot] <群名片> input only")
	peer.send(t, event(700, 6, 888, "bidirectional source"))
	assertActions(t, ob.actions, []int64{600}, "[onebot] <群名片> bidirectional source")
	peer.send(t, event(600, 7, 888, "output only must not send"))
	assertQuiet(t, irc, ob.actions)
	peer.conn.Close()
	peer = receive(t, ob.connections)
	irc.send(t, "PRIVMSG #qq :after reconnect\r\n")
	assertActions(t, ob.actions, []int64{123, 456}, "[irc] <probe> after reconnect")
	peer.send(t, event(456, 8, 888, "reply after reconnect"))
	assertActions(t, ob.actions, []int64{123}, "[onebot] <群名片> reply after reconnect")
	irc.expectMessage(t, "#qq", "[QQ] <群名片> reply after reconnect")
	// Restart the actual matterbridge executable using the very same TOML file.
	process.stop()
	process = start(t, dir, "restarted", binary, "-conf", path)
	waitJoins(t, irc)
	peer = receive(t, ob.connections)
	waitLog(t, process.logPath, "Now relaying messages", 1)
	irc.send(t, "PRIVMSG #qq2 :after restart\r\n")
	assertActions(t, ob.actions, []int64{789}, "[irc] <probe> after restart")
	peer.send(t, event(789, 9, 888, "QQ after restart"))
	irc.expectMessage(t, "#qq2", "[QQ] <群名片> QQ after restart")
	assertQuiet(t, irc, ob.actions)
	peer.send(t, event(789, 12, 888, []segment{
		{Type: "reply", Data: map[string]string{"id": "9"}},
		{Type: "text", Data: map[string]string{"text": "classic IRC reply"}},
	}))
	irc.expectMessage(t, "#qq2", "[QQ] <群名片> [回复 群名片：QQ after restart] classic IRC reply")
	assertQuiet(t, irc, ob.actions)
	if err := process.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-process.done:
		// The upstream main uses the default signal handler.
		var exitErr *exec.ExitError
		if !errors.As(process.err, &exitErr) {
			t.Fatalf("expected SIGTERM exit, got %v", process.err)
		}
		status, ok := exitErr.Sys().(syscall.WaitStatus)
		if !ok || !status.Signaled() || status.Signal() != syscall.SIGTERM {
			t.Fatalf("unexpected exit status: %v", process.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown timed out")
	}
	t.Log("PASS: one TOML routes native OneBot channels, IRC/API, QQ peers, directionality, multi-gateway membership, reconnect and restart")
}

func assertActions(t *testing.T, ch <-chan action, groups []int64, text string) {
	t.Helper()
	assertSegments(t, ch, groups, []segment{{Type: "text", Data: map[string]string{"text": text}}})
}

func assertSegments(t *testing.T, ch <-chan action, groups []int64, message []segment) {
	t.Helper()
	remaining := map[int64]bool{}
	for _, g := range groups {
		remaining[g] = true
	}
	for range groups {
		a := receive(t, ch)
		if !remaining[a.Params.GroupID] {
			t.Fatalf("unexpected or duplicate target: %+v", a)
		}
		assertActionSegments(t, a, a.Params.GroupID, message)
		delete(remaining, a.Params.GroupID)
	}
}

func requiredEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("%s is required; run bridge/onebot/test-e2e.sh", name)
	}
	return value
}

func freeAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

type process struct {
	cmd     *exec.Cmd
	done    chan struct{}
	err     error
	logPath string
}

func start(t *testing.T, dir, name, executable string, args ...string) *process {
	t.Helper()
	p := &process{cmd: exec.Command(executable, args...), done: make(chan struct{}), logPath: filepath.Join(dir, name+".log")}
	p.cmd.Dir = dir
	// Test configuration is self-contained, unaffected by production token envs.
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "ONEBOT_TOKEN=") && !strings.HasPrefix(env, "MATTERBRIDGE_") {
			p.cmd.Env = append(p.cmd.Env, env)
		}
	}
	f, err := os.Create(p.logPath)
	if err != nil {
		t.Fatal(err)
	}
	p.cmd.Stdout, p.cmd.Stderr = f, f
	if err := p.cmd.Start(); err != nil {
		f.Close()
		t.Fatal(err)
	}
	go func() { p.err = p.cmd.Wait(); f.Close(); close(p.done) }()
	t.Cleanup(func() {
		p.stop()
		if t.Failed() {
			data, _ := os.ReadFile(p.logPath)
			t.Logf("%s:\n%s", name, data)
		}
	})
	return p
}

func (p *process) stop() {
	select {
	case <-p.done:
		return
	default:
	}
	_ = p.cmd.Process.Kill()
	<-p.done
}

func waitLog(t *testing.T, path, text string, count int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(path)
		if strings.Count(string(data), text) >= count {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("did not find %d occurrences of %q in %s", count, text, path)
}

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value, ok := <-ch:
		if !ok {
			t.Fatal("connection closed")
		}
		return value
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for message")
	}
	var zero T
	return zero
}

type ircClient struct {
	conn  net.Conn
	mu    sync.Mutex
	lines chan string
}

func connectIRC(t *testing.T, address string) *ircClient {
	t.Helper()
	var conn net.Conn
	var err error
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err = net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	c := &ircClient{conn: conn, lines: make(chan string, 512)}
	t.Cleanup(func() { conn.Close() })
	go func() {
		defer close(c.lines)
		scanner := bufio.NewScanner(conn)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "PING ") {
				c.mu.Lock()
				_, _ = fmt.Fprintf(conn, "PONG %s\r\n", strings.TrimPrefix(line, "PING "))
				c.mu.Unlock()
			} else {
				c.lines <- line
			}
		}
	}()
	return c
}

func (c *ircClient) send(t *testing.T, line string) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := fmt.Fprint(c.conn, line); err != nil {
		t.Fatal(err)
	}
}

func (c *ircClient) wait(t *testing.T, match func(string) bool) string {
	t.Helper()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case line, ok := <-c.lines:
			if !ok {
				t.Fatal("IRC disconnected")
			}
			if match(line) {
				return line
			}
		case <-deadline.C:
			t.Fatal("timed out waiting for IRC event")
		}
	}
}

func (c *ircClient) expectMessage(t *testing.T, channel, text string) {
	t.Helper()
	line := c.wait(t, func(s string) bool { return strings.Contains(s, " PRIVMSG ") })
	want := " PRIVMSG " + channel + " :" + text
	if !strings.HasSuffix(line, want) {
		t.Fatalf("IRC got %q; want suffix %q", line, want)
	}
}

func waitJoins(t *testing.T, irc *ircClient) {
	t.Helper()
	joined := map[string]bool{}
	irc.wait(t, func(line string) bool {
		if strings.HasPrefix(line, ":qq-bridge!") && strings.Contains(line, " JOIN ") {
			for _, channel := range []string{"#qq", "#qq2"} {
				if strings.HasSuffix(line, channel) {
					joined[channel] = true
				}
			}
		}
		return len(joined) == 2
	})
}

type action struct {
	Action    string `json:"action"`
	Echo      string `json:"echo"`
	MessageID int64  `json:"-"`
	Params    struct {
		ID      string    `json:"id"`
		GroupID int64     `json:"group_id"`
		Message []segment `json:"message"`
	} `json:"params"`
}

type segment struct {
	Type string            `json:"type"`
	Data map[string]string `json:"data"`
}

type peer struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (p *peer) send(t *testing.T, v any) {
	t.Helper()
	if err := p.write(v); err != nil {
		t.Fatal(err)
	}
}
func (p *peer) write(v any) error { p.mu.Lock(); defer p.mu.Unlock(); return p.conn.WriteJSON(v) }

type oneBot struct {
	url         string
	connections chan *peer
	actions     chan action
}

func newOneBot(t *testing.T, forwards ...map[string]json.RawMessage) *oneBot {
	t.Helper()
	ob := &oneBot{connections: make(chan *peer, 8), actions: make(chan action, 64)}
	var mu sync.Mutex
	var nextID int64 = 100
	var peers []*peer
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ob-test-token" {
			http.Error(w, "unauthorized", 401)
			return
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		p := &peer{conn: conn}
		mu.Lock()
		peers = append(peers, p)
		mu.Unlock()
		ob.connections <- p
		_ = p.write(map[string]any{"post_type": "meta_event", "meta_event_type": "lifecycle", "self_id": 999})
		for {
			var a action
			if conn.ReadJSON(&a) != nil {
				return
			}
			if a.Action == "get_group_member_list" {
				qq := 111111
				if a.Params.GroupID == 456 {
					qq = 222222
				}
				_ = p.write(map[string]any{"status": "ok", "retcode": 0, "echo": a.Echo, "data": []any{
					map[string]any{"user_id": qq, "nickname": "Alice", "card": "群名片"},
					map[string]any{"user_id": 333333, "nickname": "同名", "card": ""},
					map[string]any{"user_id": 444444, "nickname": "同名", "card": ""},
				}})
				continue
			}
			if a.Action == "get_msg" {
				_ = p.write(map[string]any{"status": "failed", "retcode": 1404, "echo": a.Echo})
				continue
			}
			if a.Action == "get_forward_msg" {
				var data json.RawMessage
				if len(forwards) > 0 {
					data = forwards[0][a.Params.ID]
				}
				status, code := "ok", 0
				if len(data) == 0 {
					status, code = "failed", 1404
				}
				_ = p.write(map[string]any{"status": status, "retcode": code, "echo": a.Echo, "data": data})
				continue
			}
			mu.Lock()
			nextID++
			a.MessageID = nextID
			mu.Unlock()
			if p.write(map[string]any{"status": "ok", "retcode": 0, "echo": a.Echo, "data": map[string]any{"message_id": a.MessageID}}) != nil {
				return
			}
			ob.actions <- a
		}
	}))
	ob.url = "ws" + strings.TrimPrefix(server.URL, "http") + "/"
	t.Cleanup(func() {
		server.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, p := range peers {
			p.conn.Close()
		}
	})
	return ob
}

func event(group, id, user int64, message any) map[string]any {
	return map[string]any{"post_type": "message", "message_type": "group", "self_id": 999, "group_id": group, "user_id": user, "message_id": id, "message": message, "sender": map[string]any{"nickname": "昵称", "card": "群名片"}}
}

func assertActionSegments(t *testing.T, a action, group int64, message []segment) {
	t.Helper()
	if a.Action != "send_group_msg" || a.Params.GroupID != group || !reflect.DeepEqual(a.Params.Message, message) {
		data, _ := json.Marshal(a)
		want, _ := json.Marshal(message)
		t.Fatalf("OneBot action got %s; want group=%d message=%s", data, group, want)
	}
}

func assertQuiet(t *testing.T, irc *ircClient, actions <-chan action) {
	t.Helper()
	timer := time.NewTimer(300 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case a := <-actions:
			t.Fatalf("unexpected QQ action (possible echo): %+v", a)
		case line, ok := <-irc.lines:
			if !ok {
				t.Fatal("IRC disconnected")
			}
			if strings.Contains(line, " PRIVMSG ") {
				t.Fatalf("unexpected IRC message (possible echo/cross-group leak): %s", line)
			}
		case <-timer.C:
			return
		}
	}
}
