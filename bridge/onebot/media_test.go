//nolint:goconst,gosmopolitan // Keep protocol fixtures literal and verify Unicode filenames.
package onebot

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/matterbridge-org/matterbridge/bridge/config"
	ob "github.com/matterbridge-org/matterbridge/bridge/onebot/internal/client"
)

type mediaAction struct {
	Action string `json:"action"`
	Echo   string `json:"echo"`
	Params struct {
		GroupID int64        `json:"group_id"`
		File    string       `json:"file"`
		Name    string       `json:"name"`
		Message []ob.Segment `json:"message"`
	} `json:"params"`
}

func mediaSocketBridge(t *testing.T, failImage, failFile bool) (*Bridge, <-chan mediaAction) {
	t.Helper()

	actions := make(chan mediaAction, 16)
	ready := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		close(ready)

		for {
			var a mediaAction
			if conn.ReadJSON(&a) != nil {
				return
			}

			actions <- a

			status, code := "ok", 0
			if failFile && a.Action == "upload_group_file" {
				status, code = "failed", 100
			}

			for _, segment := range a.Params.Message {
				if failImage && segment.Type == "image" {
					status, code = "failed", 100
				}
			}

			_ = conn.WriteJSON(map[string]any{"status": status, "retcode": code, "echo": a.Echo,
				"data": map[string]any{"message_id": 456, "file_id": "file-id"}})
		}
	}))
	t.Cleanup(server.Close)
	b := testBridge(t)
	b.Config.Config.SetVal("onebot.qq.Server", "ws"+strings.TrimPrefix(server.URL, "http"))

	err := b.JoinChannel(config.ChannelInfo{Name: "123"})
	if err != nil {
		t.Fatal(err)
	}

	err = b.Connect()
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = b.Disconnect() })

	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("OneBot did not connect")
	}

	return b, actions
}

func receiveMediaAction(t *testing.T, actions <-chan mediaAction) mediaAction {
	t.Helper()

	select {
	case a := <-actions:
		return a
	case <-time.After(3 * time.Second):
		t.Fatal("missing OneBot action")
	}

	return mediaAction{}
}

func mediaActionText(a mediaAction) string {
	var text strings.Builder

	for _, s := range a.Params.Message {
		if s.Type == "text" {
			text.WriteString(s.Data["text"])
		}
	}

	return text.String()
}

//nolint:funlen // Keep each media/fallback case visible alongside its assertions.
func TestOneBotNativeMediaAndFallback(t *testing.T) {
	var encodedPNG bytes.Buffer

	err := png.Encode(&encodedPNG, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	if err != nil {
		t.Fatal(err)
	}

	photo := encodedPNG.Bytes()

	document := []byte("document contents")
	for _, tc := range []struct {
		name                string
		files               []config.FileInfo
		failImage, failFile bool
		limit               int
		wantUploads         int
		wantImages          bool
		wantLink            bool
	}{
		{name: "image", files: []config.FileInfo{{Name: "photo.png", Data: &photo, URL: "https://cdn.example/photo.png"}}, wantImages: true},
		{name: "file", files: []config.FileInfo{{Name: "报告.txt", Data: &document, URL: "https://cdn.example/report"}}, wantUploads: 1},
		{name: "URL only", files: []config.FileInfo{{Name: "link.png", URL: "https://cdn.example/link.png"}}, wantLink: true},
		{name: "image rejection", files: []config.FileInfo{{Name: "photo.png", Data: &photo, URL: "https://cdn.example/photo.png"}}, failImage: true, wantLink: true},
		{name: "file rejection", files: []config.FileInfo{{Name: "报告.txt", Data: &document, URL: "https://cdn.example/report"}}, failFile: true, wantUploads: 1, wantLink: true},
		{name: "image exceeds download limit", files: []config.FileInfo{{Name: "photo.png", Data: &photo, URL: "https://cdn.example/photo.png"}}, limit: 1, wantImages: true},
		{name: "file exceeds download limit", files: []config.FileInfo{{Name: "报告.txt", Data: &document, URL: "https://cdn.example/report"}}, limit: 1, wantUploads: 1},
		{name: "content sniffing", files: []config.FileInfo{{Name: "fake.png", Data: &document, URL: "https://cdn.example/fake.png"}}, wantUploads: 1},
		{name: "mixed fallback uploads once", files: []config.FileInfo{{Name: "报告.txt", Data: &document, URL: "https://cdn.example/report"}, {Name: "photo.png", Data: &photo, URL: "https://cdn.example/photo.png"}}, failImage: true, wantUploads: 1, wantLink: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, actions := mediaSocketBridge(t, tc.failImage, tc.failFile)
			if tc.limit != 0 {
				b.Config.Config.SetVal("onebot.qq.MediaDownloadSize", tc.limit)
			}

			m := config.Message{Channel: "123", Username: "[irc] alice ", Text: "caption @123", ParentID: "42", Extra: map[string][]any{"file": {}}}
			for _, f := range tc.files {
				m.Extra["file"] = append(m.Extra["file"], f)
			}

			id, err := b.Send(m)
			if err != nil || id != "456" {
				t.Fatalf("send: %q %v", id, err)
			}

			for range tc.wantUploads {
				a := receiveMediaAction(t, actions)
				assertMediaUpload(t, a, document)
			}

			a := receiveMediaAction(t, actions)
			if tc.failImage {
				if a.Action != "send_group_msg" || a.Params.Message[len(a.Params.Message)-1].Type != "image" {
					t.Fatalf("image not attempted: %+v", a)
				}

				a = receiveMediaAction(t, actions)
			}

			assertMediaSend(t, a, photo, tc.wantImages, tc.wantLink)

			if len(actions) != 0 {
				t.Fatalf("unexpected retry: %+v", receiveMediaAction(t, actions))
			}

			assertSourceFiles(t, m, tc.files)
		})
	}
}

func TestOneBotLargeMediaWithZeroDownloadSize(t *testing.T) {
	photo := mediaTestPNG(t, color.RGBA{R: 255, A: 255})
	// Preserve the PNG signature while exceeding the former 1 MB default.
	photo = append(photo, make([]byte, 1000001-len(photo))...)
	document := bytes.Repeat([]byte("x"), 1000001)

	b, actions := mediaSocketBridge(t, false, false)
	b.Config.Config.SetVal("general.MediaDownloadSize", 30000000)
	b.Config.Config.SetVal("onebot.qq.MediaDownloadSize", 0)
	files := []config.FileInfo{
		{Name: "report.txt", Data: &document, URL: "https://cdn.example/report"},
		{Name: "photo.png", Data: &photo, URL: "https://cdn.example/photo.png"},
	}
	m := config.Message{Channel: "123", Username: "[irc] alice ", Text: "caption @123", ParentID: "42",
		Extra: map[string][]any{"file": {files[0], files[1]}}}

	id, err := b.Send(m)
	if err != nil || id != "456" {
		t.Fatalf("send: %q %v", id, err)
	}

	assertMediaUpload(t, receiveMediaAction(t, actions), document)
	assertMediaSend(t, receiveMediaAction(t, actions), photo, true, false)
	if len(actions) != 0 {
		t.Fatal("unexpected media retry")
	}

	assertSourceFiles(t, m, files)
}

// A destination must not alter files still destined for other bridges.
func assertSourceFiles(t *testing.T, m config.Message, files []config.FileInfo) {
	t.Helper()

	for i, raw := range m.Extra["file"] {
		file, ok := raw.(config.FileInfo)
		if !ok || file.URL != files[i].URL {
			t.Fatal("mutated source attachment")
		}
	}
}

func assertMediaUpload(t *testing.T, a mediaAction, want []byte) {
	t.Helper()

	if a.Action != "upload_group_file" || a.Params.GroupID != 123 || !strings.HasPrefix(a.Params.File, "base64://") {
		t.Fatalf("bad file upload: %+v", a)
	}

	assertMediaBytes(t, a.Params.File, want)

	if a.Params.Name == "" {
		t.Fatalf("uploaded without a name: %+v", a)
	}
}

func assertMediaSend(t *testing.T, a mediaAction, photo []byte, wantImages, wantLink bool) {
	t.Helper()

	if a.Action != "send_group_msg" || a.Params.GroupID != 123 || len(a.Params.Message) == 0 || a.Params.Message[0].Type != "reply" || a.Params.Message[0].Data["id"] != "42" {
		t.Fatalf("lost target or reply: %+v", a)
	}

	images, mentions := 0, 0

	for _, s := range a.Params.Message {
		if s.Type == "at" && s.Data["qq"] == "123" {
			mentions++
		}

		if s.Type == "image" {
			images++

			assertMediaBytes(t, s.Data["file"], photo)
		}
	}

	text := mediaActionText(a)
	if mentions != 1 || (images > 0) != wantImages || strings.Contains(text, "https://cdn.example/") != wantLink || !strings.HasPrefix(text, "[irc] alice caption ") {
		t.Fatalf("bad final message: %+v", a)
	}
}

func assertMediaBytes(t *testing.T, uri string, want []byte) {
	t.Helper()

	if !strings.HasPrefix(uri, "base64://") {
		t.Fatal("media did not use base64 transport")
	}

	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, "base64://"))
	if err != nil || !bytes.Equal(data, want) {
		t.Fatal("media bytes changed")
	}
}

func TestOneBotTextAndMultipleImages(t *testing.T) {
	photos := [][]byte{mediaTestPNG(t, color.RGBA{R: 255, A: 255}), mediaTestPNG(t, color.RGBA{B: 255, A: 255})}
	for _, tc := range []struct {
		name string
		fail bool
	}{{"native images", false}, {"image fallback", true}} {
		t.Run(tc.name, func(t *testing.T) {
			b, actions := mediaSocketBridge(t, tc.fail, false)
			m := config.Message{Channel: "123", Username: "[irc] alice ", Text: "图文 @123 混排", ParentID: "42",
				Extra: map[string][]any{"file": {
					config.FileInfo{Name: "one.png", Data: &photos[0], URL: "https://cdn.example/one.png"},
					config.FileInfo{Name: "two.png", Data: &photos[1], URL: "https://cdn.example/two.png"},
				}}}

			id, err := b.Send(m)
			if err != nil || id != "456" {
				t.Fatalf("send: %q %v", id, err)
			}

			a := receiveMediaAction(t, actions)
			assertImageTail(t, a, photos)

			if tc.fail {
				a = receiveMediaAction(t, actions)
				for _, segment := range a.Params.Message {
					if segment.Type == "image" {
						t.Fatal("fallback retried an image")
					}
				}
			}

			text := mediaActionText(a)
			if !strings.Contains(text, "图文") || !strings.Contains(text, "混排") || strings.Contains(text, "https://cdn.example/") != tc.fail {
				t.Fatalf("caption or fallback links lost: %+v", a)
			}

			if len(actions) != 0 {
				t.Fatal("multiple images were split into separate messages")
			}
		})
	}
}

func mediaTestPNG(t *testing.T, pixel color.RGBA) []byte {
	t.Helper()

	photo := image.NewRGBA(image.Rect(0, 0, 1, 1))
	photo.SetRGBA(0, 0, pixel)

	var encoded bytes.Buffer

	err := png.Encode(&encoded, photo)
	if err != nil {
		t.Fatal(err)
	}

	return encoded.Bytes()
}

func assertImageTail(t *testing.T, a mediaAction, photos [][]byte) {
	t.Helper()

	if a.Action != "send_group_msg" || a.Params.GroupID != 123 || len(a.Params.Message) < len(photos)+1 {
		t.Fatalf("bad image message: %+v", a)
	}

	if a.Params.Message[0].Type != "reply" || a.Params.Message[0].Data["id"] != "42" {
		t.Fatal("image message lost its native reply")
	}

	start := len(a.Params.Message) - len(photos)
	for _, segment := range a.Params.Message[:start] {
		if segment.Type == "image" {
			t.Fatal("image appeared before the remaining text")
		}
	}

	for i, photo := range photos {
		segment := a.Params.Message[start+i]
		if segment.Type != "image" {
			t.Fatal("text appeared after an image")
		}

		assertMediaBytes(t, segment.Data["file"], photo)
	}
}
