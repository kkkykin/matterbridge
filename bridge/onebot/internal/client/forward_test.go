package client

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestForwardFormats(t *testing.T) {
	for _, raw := range []string{
		`{"message":[{"type":"node","data":{"user_id":"9007199254740993","nickname":"Alice","content":[{"type":"text","data":{"text":"hello"}}]}}]}`,
		`{"messages":[{"user_id":9007199254740993,"sender":{"nickname":"Alice"},"message":"hello"}]}`,
	} {
		nodes, err := decodeForward(json.RawMessage(raw))
		if err != nil || len(nodes) != 1 {
			t.Fatalf("decode: %v, %v", nodes, err)
		}
		text, _, err := MessageText(nodes[0].Content)
		if err != nil || text != "hello" || nodes[0].UserID != 9007199254740993 || nodes[0].Nickname != "Alice" {
			t.Fatalf("node: %+v, %v", nodes[0], err)
		}
	}
	for _, raw := range []string{`{}`, `{"message":[]}`, `{"messages":null}`, `{"message":[{"type":"text","data":{}}]}`, `{"message":[{"type":"node","data":{"id":"42"}}]}`} {
		if _, err := decodeForward(json.RawMessage(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}

func TestNativeForwardRefs(t *testing.T) {
	for _, raw := range []string{
		`"&#91;CQ:forward,id=literal&#93;[CQ:forward,id=a&amp;b]"`,
		`[{"type":"text","data":{"text":"[CQ:forward,id=literal]"}},{"type":"forward","data":{"id":"a&b"}}]`,
	} {
		if refs := ForwardRefs(json.RawMessage(raw)); !reflect.DeepEqual(refs, []ForwardRef{{ID: "a&b"}}) {
			t.Fatal(refs)
		}
	}
}

func TestInlineForwardRefs(t *testing.T) {
	raw := json.RawMessage(`[{"type":"forward","data":{"id":"not-fetchable","content":[{"sender":{"nickname":"Alice"},"message":"inner body"}]}},{"type":"node","data":{"user_id":"123","nickname":"Bob","content":"standard body"}}]`)
	refs := ForwardRefs(raw)
	if len(refs) != 2 || refs[0].ID != "not-fetchable" {
		t.Fatal("lost inline forward")
	}
	for i, want := range []string{"inner body", "standard body"} {
		nodes, err := InlineForward(refs[i].Content)
		if err != nil || len(nodes) != 1 {
			t.Fatalf("inline decode: %v", err)
		}
		text, _, err := MessageText(nodes[0].Content)
		if err != nil || text != want {
			t.Fatalf("inline text: %q %v", text, err)
		}
	}
}
