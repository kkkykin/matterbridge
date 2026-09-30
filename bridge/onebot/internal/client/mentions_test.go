package client

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/matterbridge-org/matterbridge/bridge/config"
)

//nolint:gosmopolitan // Verify Unicode mentions and literal media labels.
func TestMessageTextMentionIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, raw, text, parent string
		parts                   []config.MentionPart
	}{
		{"array", `[{"type":"at","data":{"qq":999}},{"type":"text","data":{"text":" hi @Bob"}}]`, "@999 hi @Bob", "", []config.MentionPart{{Text: "@999", Kind: config.MentionBot}, {Text: " hi @Bob", Kind: config.MentionText}}},
		{"CQ", `"[CQ:reply,id=42][CQ:at,qq=999] hi @Bob"`, "@999 hi @Bob", "42", []config.MentionPart{{Kind: config.MentionText}, {Text: "@999", Kind: config.MentionBot}, {Text: " hi @Bob", Kind: config.MentionText}}},
		{"ordinary at is not native", `"@999 @Bob &#91;CQ:at,qq=999&#93;"`, "@999 @Bob [CQ:at,qq=999]", "", []config.MentionPart{{Text: "@999 @Bob [CQ:at,qq=999]", Kind: config.MentionText}}},
		{"other mentions", `[{"type":"at","data":{"qq":"123"}},{"type":"at","data":{"qq":"all"}}]`, "@123@全体成员", "", []config.MentionPart{{Text: "@123", Kind: config.MentionNative}, {Text: "@全体成员", Kind: config.MentionNative}}},
		{"media", `[{"type":"image","data":{"name":"@Bob","url":"https://example.org/@Bob"}}]`, "[图片 @Bob] https://example.org/@Bob ", "", []config.MentionPart{{Text: "[图片 @Bob] https://example.org/@Bob ", Kind: config.MentionLiteral}}},
		{"split text and cleaning", `[{"type":"text","data":{"text":"\u0001@Al"}},{"type":"text","data":{"text":"ice\r"}},{"type":"text","data":{"text":"\n你好"}}]`, "@Alice\n你好", "", []config.MentionPart{{Text: "@Alice\n你好", Kind: config.MentionText}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, parent, parts, err := MessageTextMentions(json.RawMessage(tc.raw), "999")
			if err != nil || text != tc.text || parent != tc.parent || !reflect.DeepEqual(parts, tc.parts) {
				t.Fatalf("text=%q parent=%q parts=%+v err=%v", text, parent, parts, err)
			}
		})
	}
}
