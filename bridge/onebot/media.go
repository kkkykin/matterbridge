package onebot

import (
	"context"
	"encoding/base64"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/matterbridge-org/matterbridge/bridge/config"
	ob "github.com/matterbridge-org/matterbridge/bridge/onebot/internal/client"
)

const (
	mediaFile  = "file"
	mediaText  = "text"
	mediaImage = "image"
	// IRC supplies filename requests in the same order as Extra["file"].
	ircMediaFilename = "irc_media_filename"
)

// Transfer only bytes supplied by a source bridge. URL-only attachments keep
// their textual representation; never ask NapCat to fetch arbitrary links.
func (b *Bridge) outgoingFiles(ctx context.Context, client *ob.Client, group int64, m config.Message) ([]ob.Segment, config.Message) {
	rendered := m
	rendered.Extra = make(map[string][]any)
	rendered.Extra[ircMediaFilename] = m.Extra[ircMediaFilename]

	var images []ob.Segment

	for _, raw := range m.Extra[mediaFile] {
		file, ok := raw.(config.FileInfo)
		if !ok {
			continue
		}

		if ctx.Err() != nil || file.Data == nil || len(*file.Data) == 0 || file.Avatar {
			rendered.Extra[mediaFile] = append(rendered.Extra[mediaFile], file)
			continue
		}

		encoded := "base64://" + base64.StdEncoding.EncodeToString(*file.Data)
		switch http.DetectContentType(*file.Data) {
		case "image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp":
			images = append(images, ob.Segment{Type: mediaImage, Data: map[string]string{mediaFile: encoded}})
			// Keep descriptions literal, without duplicating the source link.
			file.URL = ""
		default:
			name := path.Base(strings.ReplaceAll(file.Name, "\\", "/"))
			if name == "" || name == "." || name == "/" || name == ".." {
				name = "attachment"
			}

			_, err := client.Call(ctx, "upload_group_file", map[string]any{
				"group_id": group,
				mediaFile:  encoded,
				"name":     name,
			})
			if err == nil {
				file.URL = ""
			} else {
				b.Log.Warn("OneBot file upload failed; relaying link")
			}
		}

		rendered.Extra[mediaFile] = append(rendered.Extra[mediaFile], file)
	}

	return images, rendered
}

func mediaMessage(base []ob.Segment, tail string, images []ob.Segment) []ob.Segment {
	message := append([]ob.Segment(nil), base...)
	// appendText merges with the final text segment; keep the base reusable for
	// a fallback without mutating its data map.
	if n := len(message); n > 0 && message[n-1].Type == mediaText {
		message[n-1].Data = map[string]string{mediaText: message[n-1].Data[mediaText]}
	}

	appendText(&message, tail)

	return append(message, images...)
}

// A rejected or timed-out media send falls back to text, without resending
// images or repeating upload_group_file (the first action may have succeeded).
func (b *Bridge) sendMediaFallback(ctx context.Context, client *ob.Client, group int64, message []ob.Segment) (string, error) {
	fallbackCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	return client.SendGroup(fallbackCtx, group, message)
}
