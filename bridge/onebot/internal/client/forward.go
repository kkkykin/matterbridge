package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

type ForwardRef struct {
	ID      string          `json:"id"`
	Content json.RawMessage `json:"content"`
}

// ForwardRefs only recognizes native segments, never escaped CQ text.
func ForwardRefs(raw json.RawMessage) []ForwardRef {
	var refs []ForwardRef
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '[' {
		var segments []struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(raw, &segments) != nil {
			return nil
		}
		for _, segment := range segments {
			switch segment.Type {
			case "forward":
				var ref ForwardRef
				if json.Unmarshal(segment.Data, &ref) == nil && (ref.ID != "" || len(ref.Content) > 0) {
					refs = append(refs, ref)
				}
			case "node":
				// Inline standard nodes are a one-node forward, without an API lookup.
				data, _ := json.Marshal([]any{segment})
				refs = append(refs, ForwardRef{Content: data})
			}
		}
		return refs
	}
	_, _ = messageText(raw, func(kind string, data map[string]string) string {
		if kind == "forward" && data["id"] != "" {
			refs = append(refs, ForwardRef{ID: data["id"]})
		}
		return ""
	})
	return refs
}

type ForwardNode struct {
	UserID   ID              `json:"user_id"`
	Nickname string          `json:"nickname"`
	Content  json.RawMessage `json:"content"`
}

// NapCat embeds nested forwards as data.content containing full message
// objects. Those inner IDs need not be valid get_forward_msg resource IDs.
func InlineForward(content json.RawMessage) ([]ForwardNode, error) {
	var items []struct{ Type string }
	if err := json.Unmarshal(content, &items); err != nil || len(items) == 0 {
		return nil, fmt.Errorf("invalid inline forward")
	}
	key := "messages"
	if items[0].Type != "" {
		key = "message"
	}
	data, err := json.Marshal(map[string]json.RawMessage{key: content})
	if err != nil {
		return nil, err
	}
	return decodeForward(data)
}

func (c *Client) ForwardMessage(ctx context.Context, id string) ([]ForwardNode, error) {
	data, err := c.Call(ctx, "get_forward_msg", map[string]string{"id": id})
	if err != nil {
		return nil, err
	}
	return decodeForward(data)
}

func decodeForward(data json.RawMessage) ([]ForwardNode, error) {
	var result struct {
		Message []struct {
			Type string      `json:"type"`
			Data ForwardNode `json:"data"`
		} `json:"message"`
		Messages []Event `json:"messages"` // NapCat returns full message objects.
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	var nodes []ForwardNode
	switch {
	case result.Message != nil:
		for _, segment := range result.Message {
			if segment.Type != "node" || len(segment.Data.Content) == 0 {
				return nil, fmt.Errorf("invalid OneBot forward node")
			}
			nodes = append(nodes, segment.Data)
		}
	case result.Messages != nil:
		for _, message := range result.Messages {
			name := message.Sender.Card
			if name == "" {
				name = message.Sender.Nickname
			}
			nodes = append(nodes, ForwardNode{UserID: message.UserID, Nickname: name, Content: message.Message})
		}
	default:
		return nil, fmt.Errorf("OneBot forward response missing message")
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("empty OneBot forward")
	}
	return nodes, nil
}
