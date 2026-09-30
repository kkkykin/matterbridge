package helper

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestQuotePreviewBoundsUnicodeAndControls(t *testing.T) {
	quote := QuotePreview("42\r\n", "Alice\x01\nSmith", strings.Repeat("🙂", 300)+"\x00")
	if quote.ID != "42" || quote.Username != "Alice Smith" || utf8.RuneCountInString(quote.Text) != 240 || !strings.HasSuffix(quote.Text, "…") {
		t.Fatalf("invalid bounded quote: %+v", quote)
	}
	if strings.ContainsAny(QuotePrefix(quote), "\r\n\x00\x01") {
		t.Fatal("control character leaked into quote prefix")
	}
}
