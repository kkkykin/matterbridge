package client

import (
	"encoding/json"
	"testing"
)

//nolint:gosmopolitan // Verify Unicode text and localized media labels.
func TestMessageText(t *testing.T) {
	for _, tt := range []struct{ name, raw, want string }{
		{"array", `[{"type":"text","data":{"text":"你好 &amp; "}},{"type":"at","data":{"qq":123}},{"type":"image","data":{"url":"https://example.org/a.png"}}]`, "你好 &amp; @123[图片] https://example.org/a.png "},
		{"cq", `"你好[CQ:at,qq=all] [CQ:reply,id=-12][CQ:image,url=https://example.org/a?x=1&#44;2&amp;y=3]"`, "你好@全体成员 [图片] https://example.org/a?x=1,2&y=3 "},
		{"escaped CQ", `"&#91;CQ:at,qq=all&#93; &amp;#91; &#44;"`, "[CQ:at,qq=all] &#91; &#44;"},
		{"media", `[{"type":"record","data":{"file":"file:///tmp/private"}},{"type":"json","data":{"data":"secret"}},{"type":"forward","data":{"id":"abc"}}]`, "[语音][卡片消息][合并转发]"},
		{"controls", `"hello\r\nworld\u0001ACTION\u0000"`, "hello\nworldACTION"},
		{"empty", `[]`, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, _, err := MessageText(json.RawMessage(tt.raw))
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
	for _, bad := range []string{"", `null`, `{}`, `[`, `[{"data":42}]`} {
		_, _, err := MessageText(json.RawMessage(bad))
		if err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestIDsPreservePrecision(t *testing.T) {
	for _, raw := range []string{`9007199254740993`, `"9007199254740993"`} {
		var id ID
		if err := json.Unmarshal([]byte(raw), &id); err != nil || id != 9007199254740993 {
			t.Fatalf("lost integer precision: %d %v", id, err)
		}
	}
}
