package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/matterbridge-org/matterbridge/bridge/config"
)

var cqPattern = regexp.MustCompile(`\[CQ:([a-zA-Z0-9_]+)((?:,[^\]]*)?)\]`)
var cqTextEscapes = strings.NewReplacer("&#91;", "[", "&#93;", "]", "&amp;", "&")
var cqDataEscapes = strings.NewReplacer("&#91;", "[", "&#93;", "]", "&#44;", ",", "&amp;", "&")

// MessageText extracts one native reply without interpreting escaped CQ text.
func MessageText(raw json.RawMessage) (text, parentID string, err error) {
	text, parentID, _, err = MessageTextMentions(raw, "")
	return
}

// MessageTextMentions retains native @bot identity and plain-text boundaries.
// Keep rendered media separate from native mentions and ordinary text.
func MessageTextMentions(raw json.RawMessage, selfID string) (text, parentID string, parts []config.MentionPart, err error) {
	text, err = messageText(raw, func(kind string, data map[string]string) string {
		if kind == "reply" && parentID == "" && ValidMessageID(data["id"]) {
			parentID = data["id"]
			return ""
		}
		rendered := render(kind, data)

		partKind := config.MentionLiteral
		if kind == "text" {
			partKind = config.MentionText
		} else if kind == "at" {
			partKind = config.MentionNative
			if selfID != "" && data["qq"] == selfID {
				partKind = config.MentionBot
			}
		}

		if n := len(parts); n > 0 && partKind == config.MentionText && parts[n-1].Kind == partKind {
			parts[n-1].Text += rendered
		} else {
			parts = append(parts, config.MentionPart{Text: rendered, Kind: partKind})
		}
		return rendered
	})
	for i := range parts {
		parts[i].Text = CleanText(parts[i].Text)
	}
	return
}

func ValidMessageID(id string) bool {
	n, err := strconv.ParseInt(id, 10, 64)
	return err == nil && n != 0 && strconv.FormatInt(n, 10) == id
}

func messageText(raw json.RawMessage, renderSegment func(string, map[string]string) string) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "", errors.New("missing message")
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return "", err
		}
		return CleanText(parseCQ(text, renderSegment)), nil
	}
	if raw[0] != '[' {
		return "", errors.New("message must be string or segment array")
	}
	var segments []struct {
		Type string                     `json:"type"`
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &segments); err != nil {
		return "", err
	}
	var out strings.Builder
	for _, s := range segments {
		data := make(map[string]string, len(s.Data))
		for key, value := range s.Data {
			var str string
			if json.Unmarshal(value, &str) == nil {
				data[key] = str
				continue
			}
			var num json.Number
			if json.Unmarshal(value, &num) == nil {
				data[key] = num.String()
			}
		}
		out.WriteString(renderSegment(s.Type, data))
	}
	return CleanText(out.String()), nil
}

func parseCQ(text string, renderSegment func(string, map[string]string) string) string {
	var out strings.Builder
	pos := 0
	for _, loc := range cqPattern.FindAllStringSubmatchIndex(text, -1) {
		out.WriteString(renderSegment("text", map[string]string{"text": cqTextEscapes.Replace(text[pos:loc[0]])}))
		data := map[string]string{}
		for _, param := range strings.Split(strings.TrimPrefix(text[loc[4]:loc[5]], ","), ",") {
			if k, v, ok := strings.Cut(param, "="); ok {
				data[k] = cqDataEscapes.Replace(v)
			}
		}
		out.WriteString(renderSegment(text[loc[2]:loc[3]], data))
		pos = loc[1]
	}
	out.WriteString(renderSegment("text", map[string]string{"text": cqTextEscapes.Replace(text[pos:])}))
	return out.String()
}

func render(kind string, d map[string]string) string {
	switch kind {
	case "text":
		return d["text"]
	case "at":
		if d["qq"] == "all" {
			return "@全体成员"
		}
		return "@" + d["qq"]
	case "reply":
		return "[回复 #" + d["id"] + "] "
	case "face":
		return "[表情:" + d["id"] + "]"
	case "image", "record", "video", "file":
		label := map[string]string{"image": "图片", "record": "语音", "video": "视频", "file": "文件"}[kind]
		if name := d["name"]; name != "" {
			label += " " + name
		}
		if link := httpURL(d["url"]); link != "" {
			return "[" + label + "] " + link + " "
		}
		return "[" + label + "]"
	case "share", "music":
		return "[分享 " + d["title"] + "] " + httpURL(d["url"]) + " "
	case "forward", "node":
		return "[合并转发]"
	case "json", "xml":
		return "[卡片消息]"
	case "poke":
		return "[戳一戳]"
	default:
		return "[不支持的消息:" + kind + "]"
	}
}

func httpURL(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return s
}

// CleanText removes IRC/CTCP control characters but preserves text and line breaks.
func CleanText(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	return strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

func CleanName(s string) string { return strings.Join(strings.Fields(CleanText(s)), " ") }
