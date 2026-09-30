package onebot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
	"github.com/matterbridge-org/matterbridge/bridge/config"
	ob "github.com/matterbridge-org/matterbridge/bridge/onebot/internal/client"
)

func TestForwardLookupBounds(t *testing.T) {
	for _, kind := range []string{"cycle", "nodes", "bytes", "failure", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			var calls atomic.Int32
			var nodes []any
			content := "body"
			switch kind {
			case "cycle":
				content = strings.Repeat("[CQ:forward,id=root]", 1000)
			case "bytes":
				content = strings.Repeat("你好🌉", 20000)
			}
			n := 1
			if kind == "nodes" {
				n = 250
			}
			for i := 0; i < n; i++ {
				nodes = append(nodes, map[string]any{"type": "node", "data": map[string]any{"user_id": "123", "nickname": "Alice\x01\r\n", "content": content}})
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				for {
					var req struct {
						Action, Echo string
						Params       map[string]string
					}
					if conn.ReadJSON(&req) != nil {
						return
					}
					calls.Add(1)
					if req.Action != "get_forward_msg" || req.Params["id"] != "root" {
						t.Errorf("unexpected request: %+v", req)
					}
					status, code := "ok", 0
					if kind == "failure" {
						status, code = "failed", 1404
					}
					_ = conn.WriteJSON(map[string]any{"status": status, "retcode": code, "echo": req.Echo, "data": map[string]any{"message": nodes}})
				}
			}))
			defer server.Close()
			b := testBridge(t)
			client := ob.New("ws"+strings.TrimPrefix(server.URL, "http"), "", b.Log)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			done := make(chan struct{})
			go func() { defer close(done); client.Run(ctx) }()
			defer func() { cancel(); <-done }()
			if kind == "canceled" {
				cancel()
			}
			m := config.Message{Text: "[合并转发]"}
			b.enrichForwards(ctx, client, ob.Event{Message: json.RawMessage(`"[CQ:forward,id=root]"`)}, &m)
			forward := m.Extra[config.ExtraForward][0].(*config.MessageForward)
			if m.Text != "[合并转发]" || len(forward.Nodes) == 0 || len(forward.Nodes) > 201 {
				t.Fatalf("unbounded or lost forward: %d nodes", len(forward.Nodes))
			}
			if kind != "canceled" && calls.Load() != 1 {
				t.Fatalf("lookup repeated %d times", calls.Load())
			}
			for _, node := range forward.Nodes {
				if !utf8.ValidString(node.Text) || strings.ContainsAny(node.Username, "\r\n\x01") {
					t.Fatal("unsafe or broken text")
				}
			}
			last := forward.Nodes[len(forward.Nodes)-1].Text
			if (kind == "nodes" || kind == "bytes") && !strings.Contains(last, "截断") {
				t.Fatal("truncation not indicated")
			}
			if (kind == "failure" || kind == "canceled") && !strings.Contains(last, "读取失败") {
				t.Fatal("lookup failure not indicated")
			}
		})
	}
}
