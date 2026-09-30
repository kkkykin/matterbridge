package helper

import (
	"strings"
	"unicode"

	"github.com/matterbridge-org/matterbridge/bridge/config"
)

// QuotePreview bounds cached reply context and keeps it on one display line.
func QuotePreview(id, username, text string) *config.MessageQuote {
	return &config.MessageQuote{ID: quoteLine(id, 128), Username: quoteLine(username, 80), Text: quoteLine(text, 240)}
}

func quoteLine(text string, limit int) string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, text)
	runes := []rune(strings.Join(strings.Fields(text), " "))
	if len(runes) > limit {
		return string(runes[:limit-1]) + "…"
	}
	return string(runes)
}

// QuotePrefix is literal metadata, not part of the body used for @ matching.
func QuotePrefix(quote *config.MessageQuote) string {
	if quote == nil {
		return ""
	}
	q := QuotePreview(quote.ID, quote.Username, quote.Text)
	if q.Text != "" {
		if q.Username != "" {
			return "[回复 " + q.Username + "：" + q.Text + "] "
		}
		return "[回复：" + q.Text + "] "
	}
	if q.ID != "" {
		return "[回复 #" + q.ID + "] "
	}
	return "[回复] "
}
