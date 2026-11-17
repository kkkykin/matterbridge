package birc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/lrstanley/girc"
	"github.com/matterbridge-org/matterbridge/bridge/config"
)

var mediaLinkRE = regexp.MustCompile(`!?\[([^\]\r\n]+)\]\((https?://)`)

var errIRCMedia = errors.New("IRC media")

// Keep downloads out of the router and bound concurrent buffers per IRC account.
type ircMedia struct {
	client    *http.Client
	downloads chan struct{}
}

type ircMediaLink struct {
	start, end int
	label, url string
}

func newIRCMedia() *ircMedia {
	return &ircMedia{
		client: &http.Client{
			Transport: &http.Transport{
				DialContext:         dialPublicMedia,
				TLSHandshakeTimeout: 10 * time.Second,
				DisableKeepAlives:   true,
			},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return fmt.Errorf("%w: redirects are disabled", errIRCMedia)
			},
		},
		downloads: make(chan struct{}, 4),
	}
}

func (b *Birc) validateMediaLinkConfig() error {
	token := b.GetString("MediaLinkToken")
	if token == "" {
		return nil
	}

	if strings.ContainsAny(token, ":[]()") || strings.IndexFunc(token, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) >= 0 {
		return fmt.Errorf("%w: MediaLinkToken cannot contain whitespace, control characters or :[]()", errIRCMedia)
	}

	hosts := b.GetStringSlice("MediaLinkHosts")
	if len(hosts) == 0 {
		return fmt.Errorf("%w: MediaLinkToken requires MediaLinkHosts", errIRCMedia)
	}

	for _, host := range hosts {
		if !validMediaHost(host) {
			return fmt.Errorf("%w: MediaLinkHosts must contain exact hostnames without ports or wildcards", errIRCMedia)
		}
	}

	if b.GetInt("MediaLinkTimeout") < 0 || b.GetInt("MediaDownloadSize") < 0 {
		return fmt.Errorf("%w: timeout and download size must be positive", errIRCMedia)
	}

	for _, pattern := range b.GetStringSlice("MediaDownloadBlacklist") {
		_, err := regexp.Compile(pattern)
		if err != nil {
			return fmt.Errorf("invalid MediaDownloadBlacklist: %w", err)
		}
	}

	return nil
}

func validMediaHost(host string) bool {
	u, err := url.Parse("https://" + host)

	return err == nil && u.Hostname() != "" && u.Host == host && u.User == nil && u.Port() == "" &&
		u.Path == "" && u.RawQuery == "" && u.Fragment == "" && !strings.ContainsAny(host, "* \\")
}

func (b *Birc) handleMediaLink(event girc.Event, msg *config.Message) {
	token := b.GetString("MediaLinkToken")
	if token == "" {
		return
	}

	// Strip the token before any operation that can fail. Fallbacks and quote
	// previews must only contain the URL, never the original trigger message.
	original := msg.Text
	links := findMediaLinks(original)
	msg.Text = mediaLinkText(original, token, links, nil)

	if len(links) == 0 || event.Command != girc.PRIVMSG || msg.Event != "" || !b.mediaSourceAllowed(event, msg.Channel) {
		return
	}

	select {
	case b.media.downloads <- struct{}{}:
		defer func() { <-b.media.downloads }()
	default:
		b.Log.Warn("IRC media downloads busy; relaying link")
		return
	}

	timeout := b.GetInt("MediaLinkTimeout")
	if timeout == 0 {
		timeout = 30
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	files := b.downloadMediaLinks(ctx, token, links)
	for i, file := range files {
		if file.Data == nil {
			continue
		}

		if msg.Extra == nil {
			msg.Extra = make(map[string][]any)
		}

		msg.Extra["file"] = append(msg.Extra["file"], file)
		// Keep filename requests parallel to successful attachments. A colon,
		// including an empty name, opts into OneBot's filename description.
		msg.Extra["irc_media_filename"] = append(msg.Extra["irc_media_filename"], strings.Contains(links[i].label, ":"))
	}

	msg.Text = strings.TrimSpace(mediaLinkText(original, token, links, files))
}

func findMediaLinks(text string) []ircMediaLink {
	var (
		links       []ircMediaLink
		previousEnd int
	)

	for _, match := range mediaLinkRE.FindAllStringSubmatchIndex(text, -1) {
		if match[0] < previousEnd {
			continue
		}

		end := mediaLinkEnd(text, match[4])
		if end < 0 {
			continue
		}

		previousEnd = end + 1
		links = append(links, ircMediaLink{
			start: match[0], end: previousEnd,
			label: text[match[2]:match[3]], url: text[match[4]:end],
		})
	}

	return links
}

// Match the closing Markdown parenthesis without swallowing adjacent links.
// Parentheses inside a URL, such as /photo_(1).png, remain part of that URL.
func mediaLinkEnd(text string, start int) int {
	depth := 1

	for offset, r := range text[start:] {
		switch {
		case unicode.IsSpace(r) || unicode.IsControl(r):
			return -1
		case r == '(':
			depth++
		case r == ')':
			depth--
			if depth == 0 {
				return start + offset
			}
		}
	}

	return -1
}

func mediaLinkText(text, token string, links []ircMediaLink, files []config.FileInfo) string {
	var (
		body        strings.Builder
		offset      int
		previousURL bool
	)

	cleanFragment := func(fragment string) string {
		// Incomplete HTTP(S) markup and other malformed markers must not leak
		// the token. Complete URLs are kept intact outside this cleanup.
		result := mediaLinkRE.ReplaceAllString(fragment, "${2}")
		result = strings.ReplaceAll(result, "["+token+":", "[")

		return strings.ReplaceAll(result, "["+token+"]", "[]")
	}

	for i, link := range links {
		fragment := cleanFragment(text[offset:link.start])
		body.WriteString(fragment)

		if fragment != "" {
			previousURL = false
		}

		if files == nil || files[i].Data == nil {
			// Adjacent links must remain individually usable in a text fallback,
			// including when a successful attachment between them was removed.
			if previousURL {
				body.WriteByte(' ')
			}

			body.WriteString(link.url)

			previousURL = true
		}

		offset = link.end
	}

	body.WriteString(cleanFragment(text[offset:]))

	return body.String()
}

// The caller holds one download slot. Use any other available slots to fetch
// this batch, then assemble the results in source order rather than completion
// order. All links share the message's timeout, including queued links.
func (b *Birc) downloadMediaLinks(ctx context.Context, token string, links []ircMediaLink) []config.FileInfo {
	files := make([]config.FileInfo, len(links))

	jobs := make(chan int, len(links))
	for i := range links {
		jobs <- i
	}

	close(jobs)

	worker := func() {
		for i := range jobs {
			files[i] = b.downloadMediaLink(ctx, token, links[i])
		}
	}

	var workers sync.WaitGroup

startWorkers:
	for range min(len(links), cap(b.media.downloads)) - 1 {
		select {
		case b.media.downloads <- struct{}{}:
			workers.Go(func() {
				defer func() { <-b.media.downloads }()

				worker()
			})
		default:
			break startWorkers
		}
	}

	worker()
	workers.Wait()

	return files
}

func (b *Birc) downloadMediaLink(ctx context.Context, token string, link ircMediaLink) config.FileInfo {
	label, name, _ := strings.Cut(link.label, ":")

	u, err := url.Parse(link.url)
	if label != token || err != nil || !mediaURLAllowed(u, b.GetStringSlice("MediaLinkHosts")) {
		return config.FileInfo{}
	}

	file, err := b.downloadMedia(ctx, u, name)
	if err != nil {
		b.Log.Warn("IRC media download failed; relaying link")
	}

	return file
}

func (b *Birc) mediaSourceAllowed(event girc.Event, channel string) bool {
	if accounts := b.GetStringSlice("MediaLinkAccounts"); len(accounts) > 0 {
		account, ok := event.Tags.Get("account")
		if !ok || account == "*" || !mediaHostAllowed(account, accounts) {
			return false
		}
	}
	// Only configured channels may cause downloads, even if another channel
	// happens to deliver an event to this connection.
	b.channelsMu.RLock()
	defer b.channelsMu.RUnlock()

	for configured := range b.channels {
		if strings.EqualFold(configured, channel) {
			return true
		}
	}

	return false
}

func mediaHostAllowed(host string, allowed []string) bool {
	for _, entry := range allowed {
		if strings.EqualFold(host, entry) {
			return true
		}
	}

	return false
}

func mediaURLAllowed(u *url.URL, hosts []string) bool {
	return u.Scheme == "https" && u.User == nil && u.Hostname() != "" && mediaHostAllowed(u.Hostname(), hosts)
}

func (b *Birc) downloadMedia(ctx context.Context, u *url.URL, name string) (config.FileInfo, error) {
	var file config.FileInfo

	explicitName := name != ""

	limit := b.GetInt("MediaDownloadSize")
	if limit == 0 {
		limit = 1000000
	}

	if name != "" {
		name = mediaFilename(name)
	} else {
		name = mediaFilename(path.Base(u.Path))
	}

	err := b.checkMediaFilename(name)
	if err != nil {
		return file, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return file, err
	}

	resp, err := b.media.client.Do(req)
	if err != nil {
		return file, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return file, fmt.Errorf("%w: HTTP status %d", errIRCMedia, resp.StatusCode)
	}

	if resp.ContentLength > int64(limit) {
		return file, fmt.Errorf("%w: exceeds MediaDownloadSize", errIRCMedia)
	}
	// An explicit name wins; otherwise prefer the server's original filename.
	if !explicitName {
		name = mediaResponseFilename(resp.Header.Get("Content-Disposition"), name)

		err = b.checkMediaFilename(name)
		if err != nil {
			return file, err
		}
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return file, err
	}

	if len(data) > limit {
		return file, fmt.Errorf("%w: exceeds MediaDownloadSize", errIRCMedia)
	}

	return config.FileInfo{Name: name, Data: &data, Size: int64(len(data)), URL: u.String()}, nil
}

func mediaResponseFilename(disposition, fallback string) string {
	_, params, err := mime.ParseMediaType(disposition)
	if err == nil && params["filename"] != "" {
		return mediaFilename(params["filename"])
	}

	return fallback
}

func (b *Birc) checkMediaFilename(name string) error {
	for _, pattern := range b.GetStringSlice("MediaDownloadBlacklist") {
		match, err := regexp.MatchString(pattern, name)
		if err != nil || match {
			return fmt.Errorf("%w: filename rejected by MediaDownloadBlacklist", errIRCMedia)
		}
	}

	return nil
}

func mediaFilename(name string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))

	name = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}

		return r
	}, name))
	if name == "" || name == "." || name == ".." || name == "/" {
		return "attachment"
	}

	return name
}

// Resolve once, check every address, then dial those exact addresses. This
// prevents DNS rebinding from changing a checked hostname into a private IP.
func dialPublicMedia(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}

	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}

	if len(addresses) == 0 {
		return nil, fmt.Errorf("%w: host has no addresses", errIRCMedia)
	}

	for _, ip := range addresses {
		if !publicMediaIP(ip) {
			return nil, fmt.Errorf("%w: host resolves to a non-public address", errIRCMedia)
		}
	}

	var conn net.Conn
	for _, ip := range addresses {
		conn, err = (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
	}

	return nil, err
}

func publicMediaIP(ip netip.Addr) bool {
	ip = ip.Unmap()

	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() &&
		!netip.MustParsePrefix("100.64.0.0/10").Contains(ip) &&
		!netip.MustParsePrefix("0.0.0.0/8").Contains(ip) &&
		!netip.MustParsePrefix("198.18.0.0/15").Contains(ip) &&
		!netip.MustParsePrefix("240.0.0.0/4").Contains(ip)
}
