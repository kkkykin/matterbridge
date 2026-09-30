package onebot

import (
	"context"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/matterbridge-org/matterbridge/bridge/config"
	ob "github.com/matterbridge-org/matterbridge/bridge/onebot/internal/client"
)

// Bound the whole event, including nested forwards and repeated references.
func (b *Bridge) enrichForwards(ctx context.Context, client *ob.Client, event ob.Event, message *config.Message) {
	refs := ob.ForwardRefs(event.Message)
	if len(refs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	seen := map[string]bool{}
	calls, count, bytesLeft := 8, 200, 128*1024
	var fetch func(ob.ForwardRef, int) []config.ForwardNode
	fetch = func(ref ob.ForwardRef, depth int) []config.ForwardNode {
		if count == 0 || bytesLeft == 0 {
			return nil
		}
		inline := len(ref.Content) > 0 && string(ref.Content) != "null"
		if (ref.ID != "" && seen[ref.ID]) || (!inline && calls == 0) || depth > 8 {
			count--
			return []config.ForwardNode{{Text: "[合并转发未展开：重复引用或达到上限]"}}
		}
		if ref.ID != "" {
			seen[ref.ID] = true
		}
		var nodes []ob.ForwardNode
		var err error
		if inline {
			nodes, err = ob.InlineForward(ref.Content)
		} else {
			calls--
			nodes, err = client.ForwardMessage(ctx, ref.ID)
		}
		if err != nil {
			count--
			b.Log.WithError(err).Debug("OneBot forward unavailable")
			return []config.ForwardNode{{Text: "[合并转发读取失败]"}}
		}
		var out []config.ForwardNode
		for _, node := range nodes {
			if count == 0 || bytesLeft == 0 {
				out = append(out, config.ForwardNode{Text: "[合并转发已截断]"})
				break
			}
			text, _, err := ob.MessageText(node.Content)
			if err != nil {
				text = "[无法解析的转发节点]"
			}
			name := ob.CleanName(node.Nickname)
			if name == "" {
				name = strconv.FormatInt(int64(node.UserID), 10)
			}
			name = forwardClip(name, min(bytesLeft, 256))
			bytesLeft -= len(name)
			clipped := len(text) > bytesLeft
			text = forwardClip(text, bytesLeft)
			bytesLeft -= len(text)
			count--
			out = append(out, config.ForwardNode{Username: strings.Repeat("↳ ", depth) + name, Text: text})
			if clipped {
				out = append(out, config.ForwardNode{Text: "[合并转发已截断]"})
				break
			}
			for _, nested := range ob.ForwardRefs(node.Content) {
				if count == 0 || bytesLeft == 0 {
					break
				}
				out = append(out, fetch(nested, depth+1)...)
			}
		}
		return out
	}
	for _, ref := range refs {
		if ref.ID != "" && seen[ref.ID] {
			continue
		}
		if count == 0 || bytesLeft == 0 {
			break
		}
		forward := &config.MessageForward{ID: ref.ID, Nodes: fetch(ref, 0)}
		if message.Extra == nil {
			message.Extra = map[string][]any{}
		}
		message.Extra[config.ExtraForward] = append(message.Extra[config.ExtraForward], forward)
	}
}

func forwardClip(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit]
}
