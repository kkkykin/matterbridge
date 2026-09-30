package onebot

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/matterbridge-org/matterbridge/bridge/config"
	ob "github.com/matterbridge-org/matterbridge/bridge/onebot/internal/client"
)

func textSegment(text string) ob.Segment {
	return ob.Segment{Type: "text", Data: map[string]string{"text": text}}
}

func atSegment(qq string) ob.Segment {
	return ob.Segment{Type: "at", Data: map[string]string{"qq": qq}}
}

func TestIncomingMentionParts(t *testing.T) {
	b := testBridge(t)
	if err := b.JoinChannel(config.ChannelInfo{Name: "123"}); err != nil {
		t.Fatal(err)
	}
	m, ok := b.incoming(ob.Event{
		PostType: "message", MessageType: "group", SelfID: 999, UserID: 888, GroupID: 123,
		Message: json.RawMessage(`[{"type":"at","data":{"qq":"999"}},{"type":"text","data":{"text":" @Alice"}}]`),
	})
	if !ok || m.Text != "@999 @Alice" || !reflect.DeepEqual(m.MentionParts, []config.MentionPart{{Text: "@999", Kind: config.MentionBot}, {Text: " @Alice", Kind: config.MentionText}}) {
		t.Fatalf("lost OneBot mention identity: %+v", m)
	}
}

//nolint:gosmopolitan // Verify literal Chinese media labels in forwarded messages.
func TestForwardedOneBotMentionBoundaries(t *testing.T) {
	b, requests := mentionBridge(t, func(int64) any { return []ob.GroupMember{} })

	m, ok := b.incoming(ob.Event{
		PostType: "message", MessageType: "group", GroupID: 123, SelfID: 999, UserID: 888, MessageID: 42,
		Message: json.RawMessage(`[{"type":"text","data":{"text":"hello "}},{"type":"at","data":{"qq":"123456"}},{"type":"image","data":{"name":"@777","url":"https://example.org/@888"}}]`),
	})
	if !ok {
		t.Fatal("OneBot message rejected")
	}

	m.Username = ""
	sendMentionMessage(t, b, m)
	assertMentionSegments(t, nextMentionRequest(t, requests, "send_group_msg", 123),
		textSegment("hello "), atSegment("123456"), textSegment("[图片 @777] https://example.org/@888 "))
}

func TestMentionParsing(t *testing.T) {
	names := map[string]string{"Alice": "101", "Alice Smith": "102", "张三": "103", "🍒": "104", "同名": "", "Alice Jones": ""}
	for _, tc := range []struct {
		name string
		text string
		want []ob.Segment
	}{
		{"numeric", "@123456 hello", []ob.Segment{atSegment("123456"), textSegment(" hello")}},
		{"adjacent mentions", "@123456@Alice", []ob.Segment{atSegment("123456"), atSegment("101")}},
		{"names", "hi @Alice, @张三！ @🍒", []ob.Segment{textSegment("hi "), atSegment("101"), textSegment(", "), atSegment("103"), textSegment("！ "), atSegment("104")}},
		{"longest name", "@Alice Smith: hello", []ob.Segment{atSegment("102"), textSegment(": hello")}},
		{"ambiguous longer name", "@Alice Jones hi", []ob.Segment{textSegment("@Alice Jones hi")}},
		{"ambiguous and unknown", "@同名 @unknown", []ob.Segment{textSegment("@同名 @unknown")}},
		{"no partial name", "@AliceSuffix @Alice.example.org", []ob.Segment{textSegment("@AliceSuffix @Alice.example.org")}},
		{"invalid IDs", "@0 @0123 @-123 @9223372036854775808 @123abc", []ob.Segment{textSegment("@0 @0123 @-123 @9223372036854775808 @123abc")}},
		{"email and URLs", "alice@123.com alice@Alice https://example.org/?@Alice www.example.org/@123 https://host/@张三", []ob.Segment{textSegment("alice@123.com alice@Alice https://example.org/?@Alice www.example.org/@123 https://host/@张三")}},
		{"literal CQ", "[CQ:at,qq=123] [CQ:text,text=@Alice] @123", []ob.Segment{textSegment("[CQ:at,qq=123] [CQ:text,text=@Alice] "), atSegment("123")}},
		{"escaped", `\@123 @@Alice`, []ob.Segment{textSegment(`\@123 @@Alice`)}},
		{"everyone disabled", "@all @全体成员", []ob.Segment{textSegment("@all @全体成员")}},
		{"punctuation and newlines", "(@123).\n@Alice。", []ob.Segment{textSegment("("), atSegment("123"), textSegment(").\n"), atSegment("101"), textSegment("。")}},
		{"empty", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := parseMentions(tc.text, names, true, false)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v; want %#v", got, tc.want)
			}
		})
	}
}

type mentionRequest struct {
	Action string `json:"action"`
	Echo   string `json:"echo"`
	Params struct {
		GroupID int64        `json:"group_id"`
		Message []ob.Segment `json:"message"`
		NoCache bool         `json:"no_cache"`
	} `json:"params"`
}

type ignoreMemberRequest struct{}

func mentionBridge(t *testing.T, members func(int64) any) (*Bridge, <-chan mentionRequest) {
	t.Helper()
	requests := make(chan mentionRequest, 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			var request mentionRequest
			if conn.ReadJSON(&request) != nil {
				return
			}
			requests <- request
			var data any = map[string]any{"message_id": 456}
			if request.Action == "get_group_member_list" {
				data = members(request.Params.GroupID)
				if _, ignore := data.(ignoreMemberRequest); ignore {
					continue
				}
				if _, failed := data.(error); failed {
					_ = conn.WriteJSON(map[string]any{"status": "failed", "retcode": 1200, "echo": request.Echo})
					continue
				}
			}
			if conn.WriteJSON(map[string]any{"status": "ok", "retcode": 0, "echo": request.Echo, "data": data}) != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	b := testBridge(t)
	b.Config.Config.SetVal("onebot.qq.Server", "ws"+strings.TrimPrefix(server.URL, "http"))
	for _, group := range []string{"123", "456"} {
		if err := b.JoinChannel(config.ChannelInfo{Name: group}); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Connect(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Disconnect() })
	return b, requests
}

func nextMentionRequest(t *testing.T, requests <-chan mentionRequest, action string, group int64) mentionRequest {
	t.Helper()
	select {
	case request := <-requests:
		if request.Action != action || request.Params.GroupID != group {
			t.Fatalf("got request %+v; want %s for group %d", request, action, group)
		}
		return request
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for OneBot request")
		return mentionRequest{}
	}
}

func sendMentionMessage(t *testing.T, b *Bridge, msg config.Message) {
	t.Helper()
	if msg.Channel == "" {
		msg.Channel = "123"
	}
	if id, err := b.Send(msg); err != nil || id != "456" {
		t.Fatalf("send: %q %v", id, err)
	}
}

func assertMentionSegments(t *testing.T, request mentionRequest, want ...ob.Segment) {
	t.Helper()
	if !reflect.DeepEqual(request.Params.Message, want) {
		got, _ := json.Marshal(request.Params.Message)
		expected, _ := json.Marshal(want)
		t.Fatalf("got %s; want %s", got, expected)
	}
}

func TestOutgoingMentionsAndLiteralMetadata(t *testing.T) {
	b, requests := mentionBridge(t, func(int64) any { return []ob.GroupMember{} })
	sendMentionMessage(t, b, config.Message{
		Username: "[irc] @999", Event: config.EventUserAction,
		Text:  "hello @123456 [CQ:at,qq=all] @all",
		Extra: map[string][]any{"file": {config.FileInfo{Name: "@777", URL: "https://example.org/@888", Comment: "@666"}}},
	})
	request := nextMentionRequest(t, requests, "send_group_msg", 123)
	assertMentionSegments(t, request, textSegment("* [irc] @999 hello "), atSegment("123456"), textSegment(" [CQ:at,qq=all] @all\n[文件 @777] https://example.org/@888 @666"))
}

func TestOutgoingNamedMentionsCacheAndGroupScope(t *testing.T) {
	b, requests := mentionBridge(t, func(group int64) any {
		// OneBot accepts both strings and numbers for user IDs.
		if group == 456 {
			return json.RawMessage(`[{"user_id":"222","nickname":"Alice","card":"张三"}]`)
		}
		return json.RawMessage(`[
			{"user_id":111,"nickname":"Alice","card":"张三"},
			{"user_id":333,"nickname":"同名","card":"同名"},
			{"user_id":444,"nickname":"Bob","card":"同名"}
		]`)
	})
	for _, tc := range []struct {
		channel string
		group   int64
		qq      string
		lookup  bool
	}{
		{"123", 123, "111", true},
		{"123", 123, "111", false},
		{"456", 456, "222", true},
	} {
		sendMentionMessage(t, b, config.Message{Channel: tc.channel, Text: "@Alice @张三 @同名 @missing"})
		if tc.lookup {
			request := nextMentionRequest(t, requests, "get_group_member_list", tc.group)
			if !request.Params.NoCache {
				t.Fatal("member refresh did not bypass server cache")
			}
		}
		assertMentionSegments(t, nextMentionRequest(t, requests, "send_group_msg", tc.group), atSegment(tc.qq), textSegment(" "), atSegment(tc.qq), textSegment(" @同名 @missing"))
	}
	// Expired memberships must be fetched again.
	b.mu.Lock()
	cached := b.members[123]
	cached.expires = time.Now().Add(-time.Second)
	b.members[123] = cached
	b.mu.Unlock()
	sendMentionMessage(t, b, config.Message{Text: "@Alice"})
	nextMentionRequest(t, requests, "get_group_member_list", 123)
	assertMentionSegments(t, nextMentionRequest(t, requests, "send_group_msg", 123), atSegment("111"))
}

func TestOutgoingMentionLookupFailureKeepsMessage(t *testing.T) {
	for _, tc := range []struct {
		name string
		data any
	}{
		{"missing list", nil},
		{"failed API", errors.New("lookup failed")},
		{"no response", ignoreMemberRequest{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, requests := mentionBridge(t, func(int64) any { return tc.data })
			for i := 0; i < 2; i++ {
				sendMentionMessage(t, b, config.Message{Text: "@Alice hi @123"})
				if i == 0 {
					nextMentionRequest(t, requests, "get_group_member_list", 123)
				}
				assertMentionSegments(t, nextMentionRequest(t, requests, "send_group_msg", 123), textSegment("@Alice hi "), atSegment("123"))
			}
		})
	}
}

func TestOutgoingAllowMention(t *testing.T) {
	for _, tc := range []struct {
		name    string
		allowed []string
		want    []ob.Segment
	}{
		{"disabled", []string{}, []ob.Segment{textSegment("@123 @Alice @all @全体成员")}},
		{"everyone only", []string{"everyone"}, []ob.Segment{textSegment("@123 @Alice "), atSegment("all"), textSegment(" "), atSegment("all")}},
		{"users and everyone", []string{"users", "everyone"}, []ob.Segment{atSegment("123"), textSegment(" @Alice "), atSegment("all"), textSegment(" "), atSegment("all")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, requests := mentionBridge(t, func(int64) any { return []ob.GroupMember{} })
			b.Config.Config.SetVal("onebot.qq.AllowMention", tc.allowed)
			sendMentionMessage(t, b, config.Message{Text: "@123 @Alice @all @全体成员"})
			if tc.name == "users and everyone" {
				nextMentionRequest(t, requests, "get_group_member_list", 123)
			}
			assertMentionSegments(t, nextMentionRequest(t, requests, "send_group_msg", 123), tc.want...)
		})
	}
}
