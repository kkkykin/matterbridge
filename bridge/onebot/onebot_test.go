package onebot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/matterbridge-org/matterbridge/bridge"
	"github.com/matterbridge-org/matterbridge/bridge/config"
	ob "github.com/matterbridge-org/matterbridge/bridge/onebot/internal/client"
	"github.com/sirupsen/logrus"
)

func testBridge(t *testing.T) *Bridge {
	t.Helper()
	log := logrus.New()
	log.Out = io.Discard
	cfg := config.NewConfigFromString(log, []byte(`[onebot.qq]
Server="ws://127.0.0.1:1/"
Token="test"
`))
	base := bridge.New(&config.Bridge{Account: "onebot.qq"})
	base.Config = cfg
	base.Log = logrus.NewEntry(log)
	return New(&bridge.Config{Bridge: base, Remote: make(chan config.Message)}).(*Bridge)
}

func TestExampleConfigurations(t *testing.T) {
	for _, path := range []string{"../../docs/protocols/onebot/matterbridge.example.toml", "../../docs/protocols/onebot/qq-only.example.toml"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		log := logrus.New()
		log.Out = io.Discard
		cfg := config.NewConfigFromString(log, data)
		server, _ := cfg.GetString("onebot.qq.Server")
		token, _ := cfg.GetString("onebot.qq.Token")
		if err := validateServer(server, token); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		count := 0
		for _, gw := range cfg.BridgeValues().Gateway {
			for _, ch := range append(append(gw.In, gw.Out...), gw.InOut...) {
				if ch.Account == "onebot.qq" {
					count++
					if _, err := groupID(ch.Channel); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
		if count != 2 {
			t.Fatalf("%s: expected two native group channels, got %d", path, count)
		}
	}
}

func TestNativeSocketSendReceiveAndCancellation(t *testing.T) {
	connections := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test" {
			http.Error(w, "unauthorized", 401)
			return
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		connections <- conn
		_ = conn.WriteJSON(map[string]any{"post_type": "message", "message_type": "group", "self_id": 999, "user_id": 888, "group_id": 123, "message_id": 42, "message": "hello"})
		for {
			var action struct {
				Action string `json:"action"`
				Echo   string `json:"echo"`
			}
			if conn.ReadJSON(&action) != nil {
				return
			}
			_ = conn.WriteJSON(map[string]any{"status": "ok", "retcode": 0, "echo": action.Echo, "data": map[string]any{"message_id": 456}})
		}
	}))
	defer server.Close()
	b := testBridge(t)
	b.Config.Config.SetVal("onebot.qq.Server", "ws"+strings.TrimPrefix(server.URL, "http"))
	if err := b.JoinChannel(config.ChannelInfo{Name: "123"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Connect(); err != nil {
		t.Fatal(err)
	}
	defer b.Disconnect()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	select {
	case <-connections:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// The receive worker is blocked delivering to Remote; replies must still flow.
	if id, err := b.Send(config.Message{Channel: "123", Username: "[irc] alice", Text: "literal [CQ:at,qq=all]"}); err != nil || id != "456" {
		t.Fatalf("send: %q %v", id, err)
	}
	select {
	case m := <-b.Remote:
		if m.Channel != "123" || m.Text != "hello" {
			t.Fatalf("receive: %+v", m)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := b.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Send(config.Message{Channel: "123", Text: "after shutdown"}); err == nil {
		t.Fatal("send after shutdown succeeded")
	}
}

func TestNativeIncoming(t *testing.T) {
	b := testBridge(t)
	for _, channel := range []string{"123", "456"} {
		if err := b.JoinChannel(config.ChannelInfo{Name: channel}); err != nil {
			t.Fatal(err)
		}
	}
	e := ob.Event{PostType: "message", MessageType: "group", SelfID: 999, UserID: 888, GroupID: 123, MessageID: -42, Message: json.RawMessage(`"你好"`)}
	e.Sender.Card = "群名片"
	m, ok := b.incoming(e)
	if !ok || m.Account != "onebot.qq" || m.Protocol != "onebot" || m.Channel != "123" || m.Gateway != "" || m.ID != "-42" || m.Username != "群名片" {
		t.Fatalf("bad native message: %+v", m)
	}
	if _, ok := b.incoming(e); ok {
		t.Fatal("duplicate accepted")
	}
	e.GroupID = 456
	if _, ok := b.incoming(e); !ok {
		t.Fatal("dedupe crossed channel boundary")
	}
	e.UserID = 999
	if _, ok := b.incoming(e); ok {
		t.Fatal("self message accepted")
	}
	e.UserID = 888
	e.GroupID = 777
	if _, ok := b.incoming(e); ok {
		t.Fatal("unsubscribed group accepted")
	}
	e.GroupID = 123
	e.MessageType = "private"
	if _, ok := b.incoming(e); ok {
		t.Fatal("private message accepted")
	}
}

func TestOptionsAndLifecycle(t *testing.T) {
	b := testBridge(t)
	for _, channel := range []string{"", "0", "-1", "0123", "+123", "#qq"} {
		if b.JoinChannel(config.ChannelInfo{Name: channel}) == nil {
			t.Errorf("accepted channel %q", channel)
		}
	}
	for _, server := range []string{"https://localhost", "ws://localhost/api", "ws://localhost/event", "ws://localhost/?token=secret"} {
		if validateServer(server, "") == nil {
			t.Errorf("accepted server %s", server)
		}
	}
	if b.GetString("Token") != "test" {
		t.Fatal("native account config unavailable")
	}
	if err := b.Connect(); err != nil {
		t.Fatal(err)
	}
	if b.Connect() == nil {
		t.Fatal("duplicate connect accepted")
	}
	done := make(chan struct{})
	go func() { _ = b.Disconnect(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown blocked during reconnect")
	}
	if err := b.Connect(); err != nil {
		t.Fatal(err)
	}
	_ = b.Disconnect()
}

func TestNativeFilesAndEvents(t *testing.T) {
	m := config.Message{Extra: map[string][]any{"file": {config.FileInfo{Name: "photo.png", URL: "https://example.org/photo.png"}, config.FileInfo{Name: "local", URL: "file:///etc/passwd"}}}}
	if text := plainText(m); text != "[文件 photo.png] https://example.org/photo.png\n[文件 local]" {
		t.Fatalf("bad file rendering %q", text)
	}
	b := testBridge(t)
	for _, event := range []string{config.EventMsgDelete, config.EventJoin, config.EventUserTyping} {
		if id, err := b.Send(config.Message{Event: event}); err != nil || id != "" {
			t.Fatalf("unsupported event %s: %s %v", event, id, err)
		}
	}
	if _, err := b.Send(config.Message{Channel: "123", Text: "not joined"}); err == nil {
		t.Fatal("sent to unconfigured group")
	}
	b.seen["expired"] = time.Now().Add(-11 * time.Minute)
	b.remember("fresh", time.Now())
	if _, ok := b.seen["expired"]; ok {
		t.Fatal("expired dedupe entry retained")
	}
}
