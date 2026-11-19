//nolint:goconst,gosmopolitan // Keep test cases self-contained and exercise Unicode filenames.
package birc

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lrstanley/girc"
	"github.com/matterbridge-org/matterbridge/bridge/config"
	"github.com/spf13/viper"
)

func mediaTestBridge(t *testing.T, server *httptest.Server) *Birc {
	t.Helper()
	viper.Reset()
	t.Cleanup(viper.Reset)
	b := replyTestBridge(t)
	b.Config.Config.SetVal("irc.test.MediaLinkToken", "test-token")
	b.Config.Config.SetVal("irc.test.MediaLinkHostRegex", `127\.0\.0\.1`)
	b.Config.Config.SetVal("irc.test.MediaDownloadSize", 8)

	err := b.validateMediaLinkConfig()
	if err != nil {
		t.Fatal(err)
	}

	b.channels["#files"] = true
	if server != nil {
		// Exercise real TLS/HTTP without allowing private IPs in production.
		b.media.client.Transport = server.Client().Transport
	}

	return b
}

func mediaEvent(text string) girc.Event {
	return girc.Event{Command: girc.PRIVMSG, Source: &girc.Source{Name: "alice"},
		Params: []string{"#files", text}, Tags: girc.ParseTags("msgid=uploaded")}
}

func TestIRCMediaAttachmentAndReplyFallback(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''%E7%8C%AB.txt")
		_, _ = io.WriteString(w, "hello")
	}))
	defer server.Close()

	b := mediaTestBridge(t, server)
	for _, name := range []string{"", ":", ":../folder/原名.txt", ":..\\folder\\原名.txt"} {
		b.handlePrivMsg(b.i, mediaEvent("[test-token"+name+"]("+server.URL+"/random.txt?x=1&y=2)"))

		m := <-b.Remote
		if m.Text != "" || len(m.Extra["file"]) != 1 || m.ID != "uploaded" {
			t.Fatalf("bad attachment message: %+v", m)
		}
		if hints := m.Extra["irc_media_filename"]; len(hints) != 1 || hints[0] != (name != "") {
			t.Fatalf("lost explicit filename hint: %+v", m.Extra)
		}

		f, ok := m.Extra["file"][0].(config.FileInfo)
		if !ok {
			t.Fatal("attachment is not a FileInfo")
		}

		want := "猫.txt"
		if name != "" && name != ":" {
			want = "原名.txt"
		}

		if f.Name != want || f.Size != 5 || string(*f.Data) != "hello" || f.URL != server.URL+"/random.txt?x=1&y=2" {
			t.Fatalf("bad attachment: %+v", f)
		}
	}
	// A failed download must also leave sanitized text in the reply cache.
	b.Config.Config.SetVal("irc.test.MediaDownloadSize", 1)
	b.handlePrivMsg(b.i, mediaEvent("[test-token]("+server.URL+"/expired)"))

	m := <-b.Remote
	if m.Text != server.URL+"/expired" || len(m.Extra["file"]) != 0 {
		t.Fatalf("bad fallback: %+v", m)
	}

	e := mediaEvent("reply")
	e.Tags = girc.ParseTags("msgid=child;+reply=uploaded")
	b.handlePrivMsg(b.i, e)

	if child := <-b.Remote; child.Quote == nil || child.Quote.Text != server.URL+"/expired" {
		t.Fatalf("token leaked into quote: %+v", child)
	}
}

//nolint:funlen,gocyclo // Keep the failure table and its HTTP fixture together.
func TestIRCMediaFailuresStripToken(t *testing.T) {
	var requests atomic.Int64

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)

		switch r.URL.Path {
		case "/missing":
			http.NotFound(w, r)
		case "/redirect":
			http.Redirect(w, r, "/ok", http.StatusFound)
		case "/large":
			_, _ = io.WriteString(w, "123456789")
		case "/chunked":
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}

			_, _ = io.WriteString(w, "123456789")
		case "/timeout":
			<-r.Context().Done()
		case "/header":
			w.Header().Set("Content-Disposition", "attachment; filename=blocked.html")
			_, _ = io.WriteString(w, "ok")
		default:
			_, _ = io.WriteString(w, "ok")
		}
	}))
	defer server.Close()

	for _, tc := range []struct {
		name, target, token string
		configure           func(*Birc)
		request             bool
	}{
		{name: "HTTP status", target: "/missing", request: true},
		{name: "redirect", target: "/redirect", request: true},
		{name: "content length", target: "/large", request: true},
		{name: "chunked limit", target: "/chunked", request: true},
		{name: "timeout", target: "/timeout", request: true, configure: func(b *Birc) { b.Config.Config.SetVal("irc.test.MediaLinkTimeout", 1) }},
		{name: "wrong token", target: "/ok", token: "wrong-token"},
		{name: "unlisted host", target: "https://unlisted.example/a"},
		{name: "HTTP scheme", target: "http://127.0.0.1/a"},
		{name: "credentials", target: "https://user:pass@127.0.0.1/a"}, // #nosec G101 -- Deliberately invalid fixture credentials.
		{name: "invalid URL", target: "https://127.0.0.1/%zz"},
		{name: "unlisted account", target: "/ok", configure: func(b *Birc) { b.Config.Config.SetVal("irc.test.MediaLinkAccounts", []string{"trusted"}) }},
		{name: "blacklist", target: "/blocked.html", configure: func(b *Birc) { b.Config.Config.SetVal("irc.test.MediaDownloadBlacklist", []string{`\.html$`}) }},
		{name: "header blacklist", target: "/header", request: true, configure: func(b *Birc) { b.Config.Config.SetVal("irc.test.MediaDownloadBlacklist", []string{`\.html$`}) }},
		{name: "unconfigured channel", target: "/ok", configure: func(b *Birc) { b.channels = map[string]bool{} }},
		{name: "concurrency limit", target: "/ok", configure: func(b *Birc) {
			for range cap(b.media.downloads) {
				b.media.downloads <- struct{}{}
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := mediaTestBridge(t, server)
			if tc.configure != nil {
				tc.configure(b)
			}

			target := tc.target
			if strings.HasPrefix(target, "/") {
				target = server.URL + target
			}

			token := tc.token
			if token == "" {
				token = "test-token"
			}

			before := requests.Load()

			b.handlePrivMsg(b.i, mediaEvent(fmt.Sprintf("[%s](%s)", token, target)))

			m := <-b.Remote
			if m.Text != target || len(m.Extra["file"]) != 0 || m.Event != "" {
				t.Fatalf("unsafe fallback: %+v", m)
			}

			wantRequests := int64(0)
			if tc.request {
				wantRequests = 1
			}

			if got := requests.Load() - before; got != wantRequests {
				t.Fatalf("made %d requests, want %d", got, wantRequests)
			}
		})
	}
}

func TestIRCMediaDisabledPlainTextAndAccount(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer server.Close()

	b := mediaTestBridge(t, server)
	for _, text := range []string{"ordinary " + server.URL, "caption without a marked link " + server.URL} {
		m := config.Message{Text: text, Channel: "#files"}
		b.handleMediaLink(mediaEvent(text), &m)

		if m.Text != text || m.Extra != nil {
			t.Fatalf("ordinary message changed: %+v", m)
		}
	}

	text := "[test-token](" + server.URL + ")"

	b.Config.Config.SetVal("irc.test.MediaLinkToken", "")

	m := config.Message{Text: text, Channel: "#files"}
	b.handleMediaLink(mediaEvent(text), &m)

	if m.Text != text {
		t.Fatal("disabled feature changed message")
	}

	b.Config.Config.SetVal("irc.test.MediaLinkToken", "test-token")
	b.Config.Config.SetVal("irc.test.MediaLinkAccounts", []string{"Alice"})

	e := mediaEvent(text)
	e.Tags = girc.ParseTags("account=alice")
	b.handleMediaLink(e, &m)

	if len(m.Extra["file"]) != 1 {
		t.Fatal("trusted account was rejected")
	}

	for _, account := range []string{"ali", "a.*", "alice|bob"} {
		b.Config.Config.SetVal("irc.test.MediaLinkAccounts", []string{account})

		m = config.Message{Text: text, Channel: "#files"}
		b.handleMediaLink(e, &m)

		if len(m.Extra["file"]) != 0 {
			t.Fatalf("account allowlist was not exact: %q", account)
		}
	}
}

func TestIRCMediaHostRegex(t *testing.T) {
	b := mediaTestBridge(t, nil)
	b.Config.Config.SetVal("irc.test.MediaLinkHostRegex", `litter\.catbox\.moe|cdn[0-9]+\.example\.com`)

	err := b.validateMediaLinkConfig()
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		url     string
		allowed bool
	}{
		{"https://litter.catbox.moe/photo.png", true},
		{"https://LITTER.CATBOX.MOE:8443/photo.png?x=1", true},
		{"https://cdn1.example.com/photo.png", true},
		{"https://CDN123.EXAMPLE.COM/photo.png", true},
		{"https://xlitter.catbox.moe/photo.png", false},
		{"https://litter.catbox.moe.evil.org/photo.png", false},
		{"https://litterXcatboxYmoe/photo.png", false},
		{"https://xcdn1.example.com/photo.png", false},
		{"https://cdn1.example.com.evil.org/photo.png", false},
		{"https://cdn.example.com/photo.png", false},
		{"https://unlisted.example/litter.catbox.moe?host=cdn1.example.com", false},
		{"http://litter.catbox.moe/photo.png", false},
		{"https://user@litter.catbox.moe/photo.png", false},
	} {
		t.Run(tc.url, func(t *testing.T) {
			u, err := url.Parse(tc.url)
			if err != nil {
				t.Fatal(err)
			}

			if got := mediaURLAllowed(u, b.media.hostRegex.Load()); got != tc.allowed {
				t.Fatalf("allowed = %v, want %v", got, tc.allowed)
			}

			if mediaURLAllowed(u, nil) {
				t.Fatal("allowed URL without a configured host regex")
			}
		})
	}
}

func TestIRCMediaMixedTextPreservesImageOrder(t *testing.T) {
	secondStarted := make(chan struct{})

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/first.png" {
			select {
			case <-secondStarted:
			case <-r.Context().Done():
				return
			}

			time.Sleep(20 * time.Millisecond)

			_, _ = io.WriteString(w, "first")

			return
		}

		close(secondStarted)

		_, _ = io.WriteString(w, "second")
	}))
	defer server.Close()

	b := mediaTestBridge(t, server)
	b.Config.Config.SetVal("irc.test.MediaLinkTimeout", 3)

	text := "开头 [test-token:第一张.png](" + server.URL + "/first.png) 中间 [test-token](" + server.URL + "/second.png) 结尾"
	b.handlePrivMsg(b.i, mediaEvent(text))

	m := <-b.Remote
	if m.Text != "开头  中间  结尾" || len(m.Extra["file"]) != 2 {
		t.Fatalf("lost caption or attachments: %+v", m)
	}
	if hints := m.Extra["irc_media_filename"]; len(hints) != 2 || hints[0] != true || hints[1] != false {
		t.Fatalf("filename hints are out of order: %+v", m.Extra)
	}

	for i, want := range []struct{ name, data string }{{"第一张.png", "first"}, {"second.png", "second"}} {
		file, ok := m.Extra["file"][i].(config.FileInfo)
		if !ok || file.Name != want.name || file.Data == nil || string(*file.Data) != want.data {
			t.Fatalf("attachment %d is out of order: %+v", i, file)
		}
	}
}

func TestIRCMediaAdjacentLinksAndParentheses(t *testing.T) {
	var requests atomic.Int64

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)

		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	link := "[test-token](" + server.URL + "/photo.png)"

	parenthesized := server.URL + "/photo_(one(two)).png?query=(three)"
	for _, tc := range []struct {
		name, text, body string
		files            int
	}{
		{"adjacent five images", strings.Repeat(link, 5), "", 5},
		{"separated images", link + " " + link, "", 2},
		{"image Markdown", "前![test-token](" + server.URL + "/photo.png)后", "前后", 1},
		{"parentheses in URL", "[test-token](" + parenthesized + ")", "", 1},
		{"adjacent rejected links", "[wrong-token](" + server.URL + "/one.png)[wrong-token](" + server.URL + "/two.png)", server.URL + "/one.png " + server.URL + "/two.png", 0},
		{"nested markup in rejected URL", "[wrong-token](" + server.URL + "/photo?next=[other](https://outside.example/a))", server.URL + "/photo?next=[other](https://outside.example/a)", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := mediaTestBridge(t, server)
			before := requests.Load()

			b.handlePrivMsg(b.i, mediaEvent(tc.text))

			m := <-b.Remote
			if m.Text != tc.body || len(m.Extra["file"]) != tc.files || requests.Load()-before != int64(tc.files) {
				t.Fatalf("bad multi-link conversion: %+v", m)
			}

			if tc.name == "parentheses in URL" {
				file, ok := m.Extra["file"][0].(config.FileInfo)
				if !ok || file.URL != parenthesized {
					t.Fatalf("URL was truncated: %+v", file)
				}
			}
		})
	}
}

func TestIRCMediaPartialFailureAndQuotes(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing.png" {
			http.NotFound(w, r)
			return
		}

		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	b := mediaTestBridge(t, server)
	missing := server.URL + "/missing.png"
	wrongToken := server.URL + "/wrong.png"
	text := "开头 [test-token](" + missing + ") 中间 [test-token:成功.png](" + server.URL + "/ok.png) 结尾 [wrong-token](" + wrongToken + ")"
	b.handlePrivMsg(b.i, mediaEvent(text))

	m := <-b.Remote
	if m.Text != "开头 "+missing+" 中间  结尾 "+wrongToken || len(m.Extra["file"]) != 1 {
		t.Fatalf("lost partial success or fallback: %+v", m)
	}
	if hints := m.Extra["irc_media_filename"]; len(hints) != 1 || hints[0] != true {
		t.Fatalf("failed links shifted filename hints: %+v", m.Extra)
	}

	e := mediaEvent("reply")
	e.Tags = girc.ParseTags("msgid=child;+reply=uploaded")
	b.handlePrivMsg(b.i, e)

	child := <-b.Remote
	if child.Quote == nil || strings.Contains(child.Quote.Text, "token") || !strings.Contains(child.Quote.Text, "missing.png") {
		t.Fatalf("bad mixed-message quote: %+v", child)
	}
}

func TestIRCMediaTimeoutKeepsOtherAttachments(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/timeout.png" {
			<-r.Context().Done()
			return
		}

		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	b := mediaTestBridge(t, server)
	b.Config.Config.SetVal("irc.test.MediaLinkTimeout", 1)

	text := "[test-token](" + server.URL + "/timeout.png) [test-token](" + server.URL + "/ok.png)"
	b.handlePrivMsg(b.i, mediaEvent(text))

	m := <-b.Remote
	if m.Text != server.URL+"/timeout.png" || len(m.Extra["file"]) != 1 {
		t.Fatalf("timeout discarded a successful attachment: %+v", m)
	}
}

func TestIRCMediaMalformedAndActionMarkersStripToken(t *testing.T) {
	b := mediaTestBridge(t, nil)

	for _, text := range []string{
		"caption [test-token](https://example.org/a missing close",
		"caption [test-token:bad]name](https://example.org/a)",
		"caption [test-token](file:///tmp/private)",
	} {
		m := config.Message{Text: text, Channel: "#files"}
		b.handleMediaLink(mediaEvent(text), &m)

		if strings.Contains(m.Text, "test-token") || len(m.Extra["file"]) != 0 {
			t.Fatalf("malformed marker leaked a token: %+v", m)
		}
	}

	e := mediaEvent("\x01ACTION caption [test-token](https://example.org/a)\x01")
	b.handlePrivMsg(b.i, e)

	m := <-b.Remote
	if m.Text != "caption https://example.org/a" || m.Event != config.EventUserAction || len(m.Extra["file"]) != 0 {
		t.Fatalf("unsafe action fallback: %+v", m)
	}
}

func TestIRCMediaPublicAddressesAndConfiguration(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.0.1", "169.254.169.254", "100.64.0.1", "198.18.0.1", "0.1.2.3", "240.1.2.3", "::", "::1", "::ffff:127.0.0.1", "fe80::1", "fc00::1", "224.0.0.1"} {
		if publicMediaIP(netip.MustParseAddr(address)) {
			t.Errorf("accepted non-public address %s", address)
		}
	}

	for _, address := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if !publicMediaIP(netip.MustParseAddr(address)) {
			t.Errorf("rejected public address %s", address)
		}
	}
	// The production transport must refuse a private endpoint before connecting.
	var requests atomic.Int64

	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()

	b := mediaTestBridge(t, nil)

	u, _ := url.Parse(server.URL)

	_, downloadErr := b.downloadMedia(context.Background(), u, "")
	if downloadErr == nil || requests.Load() != 0 {
		t.Fatal("production downloader reached a private endpoint")
	}

	configErr := b.validateMediaLinkConfig()
	if configErr != nil {
		t.Fatal(configErr)
	}

	for _, tc := range []struct {
		key   string
		value any
	}{
		{"MediaLinkToken", "bad:token"}, {"MediaLinkToken", "bad token"},
		{"MediaLinkHostRegex", ""}, {"MediaLinkHostRegex", "["},
		{"MediaLinkHostRegex", "*.example"}, {"MediaLinkHostRegex", ")|.*|("},
		{"MediaLinkTimeout", -1}, {"MediaDownloadSize", -1}, {"MediaDownloadBlacklist", []string{"["}},
	} {
		b := mediaTestBridge(t, nil)
		b.Config.Config.SetVal("irc.test."+tc.key, tc.value)

		if b.validateMediaLinkConfig() == nil {
			t.Errorf("accepted invalid %s", tc.key)
		}
	}
}
