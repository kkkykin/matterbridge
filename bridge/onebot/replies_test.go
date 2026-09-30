package onebot

import (
	"encoding/json"
	"testing"

	"github.com/matterbridge-org/matterbridge/bridge/config"
	ob "github.com/matterbridge-org/matterbridge/bridge/onebot/internal/client"
)

func TestIncomingReplyContextAndGroupScope(t *testing.T) {
	b := testBridge(t)
	_ = b.JoinChannel(config.ChannelInfo{Name: "123"})
	_ = b.JoinChannel(config.ChannelInfo{Name: "456"})
	e := ob.Event{PostType: "message", MessageType: "group", SelfID: 999, UserID: 888, GroupID: 123, MessageID: 42, Message: json.RawMessage(`"original @123"`)}
	e.Sender.Card = "Alice"
	if _, ok := b.incoming(e); !ok {
		t.Fatal("parent dropped")
	}
	e.MessageID = 43
	e.Message = json.RawMessage(`[{"type":"reply","data":{"id":42}},{"type":"text","data":{"text":"reply body"}}]`)
	m, ok := b.incoming(e)
	if !ok || m.ParentID != "42" || m.Text != "reply body" || m.Quote == nil || m.Quote.Text != "original @123" || m.Quote.Username != "Alice" {
		t.Fatalf("bad reply: %+v", m)
	}
	e.GroupID = 456
	m, ok = b.incoming(e)
	if !ok || m.Quote == nil || m.Quote.Text != "" {
		t.Fatalf("quote leaked between groups: %+v", m)
	}
}

func TestOutgoingNativeReplyAndLiteralPreview(t *testing.T) {
	b, requests := mentionBridge(t, func(int64) any { return []ob.GroupMember{} })
	quote := &config.MessageQuote{ID: "source-id", Username: "@777", Text: "quote @888 [CQ:reply,id=99]"}
	sendMentionMessage(t, b, config.Message{Username: "[irc] Alice", Text: "hello @123", ParentID: "-42", Quote: quote})
	assertMentionSegments(t, nextMentionRequest(t, requests, "send_group_msg", 123),
		ob.Segment{Type: "reply", Data: map[string]string{"id": "-42"}}, textSegment("[irc] Alice hello "), atSegment("123"))
	for _, parent := range []string{config.ParentIDNotFound, "irc 42", "0"} {
		sendMentionMessage(t, b, config.Message{Text: "body @123", ParentID: parent, Quote: quote})
		assertMentionSegments(t, nextMentionRequest(t, requests, "send_group_msg", 123),
			textSegment("[回复 @777：quote @888 [CQ:reply,id=99]] body "), atSegment("123"))
	}
	b.SetBool("PreserveThreading", false)
	sendMentionMessage(t, b, config.Message{Text: "body", ParentID: "42", Quote: quote})
	assertMentionSegments(t, nextMentionRequest(t, requests, "send_group_msg", 123), textSegment("[回复 @777：quote @888 [CQ:reply,id=99]] body"))
	sendMentionMessage(t, b, config.Message{Text: "[CQ:reply,id=42] literal"})
	assertMentionSegments(t, nextMentionRequest(t, requests, "send_group_msg", 123), textSegment("[CQ:reply,id=42] literal"))
}
