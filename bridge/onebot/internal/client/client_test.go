package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

func testLogger() *logrus.Entry {
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	return logrus.NewEntry(logger)
}

func TestClientResponseEventsAndReconnect(t *testing.T) {
	requests := make(chan map[string]json.RawMessage, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			http.Error(w, "unauthorized", 401)
			return
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteJSON(map[string]any{"post_type": "message", "message_type": "group", "group_id": "123", "message_id": -42, "user_id": 456, "self_id": 789, "message": []any{}})
		for {
			var request map[string]json.RawMessage
			if conn.ReadJSON(&request) != nil {
				return
			}
			requests <- request
			var action string
			_ = json.Unmarshal(request["action"], &action)
			switch action {
			case "disconnect":
				return
			case "timeout":
				continue
			case "fail":
				_ = conn.WriteJSON(map[string]any{"status": "failed", "retcode": 1200, "echo": request["echo"]})
			default:
				_ = conn.WriteJSON(map[string]any{"status": "ok", "retcode": 0, "echo": request["echo"], "data": map[string]any{"message_id": 123}})
			}
		}
	}))
	defer server.Close()
	c := New("ws"+strings.TrimPrefix(server.URL, "http"), "secret", testLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); c.Run(ctx) }()
	defer func() { cancel(); <-done }()
	select {
	case e := <-c.Events():
		if e.GroupID != 123 || e.MessageID != -42 {
			t.Fatalf("bad IDs: %+v", e)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := c.SendGroup(ctx, 123, []Segment{{Type: "text", Data: map[string]string{"text": "hello [CQ:at,qq=all]"}}}); err != nil {
		t.Fatal(err)
	}
	r := <-requests
	var params struct {
		Message []Segment `json:"message"`
	}
	_ = json.Unmarshal(r["params"], &params)
	if len(params.Message) != 1 || params.Message[0].Type != "text" || params.Message[0].Data["text"] != "hello [CQ:at,qq=all]" {
		t.Fatalf("unsafe outbound message: %s", r["params"])
	}
	if _, err := c.Call(ctx, "fail", nil); err == nil {
		t.Fatal("ignored failed action")
	}
	short, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	if _, err := c.Call(short, "timeout", nil); err == nil {
		t.Fatal("ignored action timeout")
	}
	stop()
	if _, err := c.Call(ctx, "disconnect", nil); err == nil {
		t.Fatal("ignored disconnect")
	}
	if _, err := c.SendGroup(ctx, 123, []Segment{{Type: "text", Data: map[string]string{"text": "after reconnect"}}}); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	pending := len(c.pending)
	c.mu.Unlock()
	if pending != 0 {
		t.Fatalf("leaked pending calls: %d", pending)
	}
}

func TestConcurrentCallsMatchEchoWithFullEventQueue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		// Saturate the event channel. API replies must still be read promptly.
		for i := 0; i < 300; i++ {
			if conn.WriteJSON(map[string]any{"post_type": "message", "message_type": "group", "group_id": 123, "self_id": 999, "user_id": 888, "message_id": i, "message": "queued"}) != nil {
				return
			}
		}
		var requests [2]struct {
			Echo   string `json:"echo"`
			Params string `json:"params"`
		}
		for i := range requests {
			if conn.ReadJSON(&requests[i]) != nil {
				return
			}
		}
		// OneBot permits concurrent responses to arrive in a different order.
		for i := len(requests) - 1; i >= 0; i-- {
			if conn.WriteJSON(map[string]any{"status": "ok", "retcode": 0, "echo": requests[i].Echo, "data": requests[i].Params}) != nil {
				return
			}
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	c := New("ws"+strings.TrimPrefix(server.URL, "http"), "", testLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	done := make(chan struct{})
	go func() { defer close(done); c.Run(ctx) }()
	defer func() { cancel(); <-done }()
	results := make(chan error, 2)
	for _, value := range []string{"first", "second"} {
		go func(value string) {
			data, err := c.Call(ctx, "echo", value)
			if err == nil {
				var got string
				err = json.Unmarshal(data, &got)
				if err == nil && got != value {
					err = fmt.Errorf("echo mismatch: got %q want %q", got, value)
				}
			}
			results <- err
		}(value)
	}
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if len(c.events) != cap(c.events) {
		t.Fatal("event queue was not saturated")
	}
}
