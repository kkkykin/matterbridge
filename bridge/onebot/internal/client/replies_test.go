package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestMessageTextReplies(t *testing.T) {
	for _, tc := range []struct{ name, raw, text, parent string }{
		{"array string", `[{"type":"reply","data":{"id":"-42"}},{"type":"text","data":{"text":"hello"}}]`, "hello", "-42"},
		{"array number", `[{"type":"reply","data":{"id":9007199254740993}},{"type":"at","data":{"qq":123}},{"type":"text","data":{"text":" hello"}}]`, "@123 hello", "9007199254740993"},
		{"CQ", `"[CQ:reply,id=42]hello &amp; goodbye"`, "hello & goodbye", "42"},
		{"escaped CQ stays text", `"&#91;CQ:reply,id=42&#93; hello"`, "[CQ:reply,id=42] hello", ""},
		{"invalid ID", `"[CQ:reply,id=0]body"`, "[回复 #0] body", ""},
		{"foreign ID", `"[CQ:reply,id=irc 42]body"`, "[回复 #irc 42] body", ""},
		{"only one parent", `"[CQ:reply,id=1][CQ:reply,id=2]body"`, "[回复 #2] body", "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, parent, err := MessageText(json.RawMessage(tc.raw))
			if err != nil || text != tc.text || parent != tc.parent {
				t.Fatalf("got %q parent=%q err=%v", text, parent, err)
			}
		})
	}
	for _, id := range []string{"", "0", "+1", "01", "-0", "1\n", "9223372036854775808"} {
		if ValidMessageID(id) {
			t.Errorf("accepted invalid ID %q", id)
		}
	}
}

func TestGroupMessageValidatesScope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			var request struct {
				Action, Echo string
				Params       struct {
					MessageID int64 `json:"message_id"`
				}
			}
			if conn.ReadJSON(&request) != nil {
				return
			}
			if request.Action != "get_msg" {
				t.Errorf("unexpected action %s", request.Action)
				return
			}
			group, id, kind := 123, request.Params.MessageID, "group"
			switch id {
			case 2:
				group = 456
			case 3:
				id = 99
			case 4:
				kind = "private"
			}
			_ = conn.WriteJSON(map[string]any{"status": "ok", "retcode": 0, "echo": request.Echo, "data": map[string]any{
				"message_type": kind, "group_id": group, "message_id": id, "user_id": 888, "message": "quoted text",
			}})
		}
	}))
	defer server.Close()
	c := New("ws"+strings.TrimPrefix(server.URL, "http"), "", testLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	done := make(chan struct{})
	go func() { defer close(done); c.Run(ctx) }()
	defer func() { cancel(); <-done }()
	if message, err := c.GroupMessage(ctx, 123, "1"); err != nil || message.MessageID != 1 {
		t.Fatalf("valid quote: %+v %v", message, err)
	}
	for _, id := range []string{"2", "3", "4", "invalid"} {
		if _, err := c.GroupMessage(ctx, 123, id); err == nil {
			t.Errorf("accepted wrong quote for %s", id)
		}
	}
}
