package onebot

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/matterbridge-org/matterbridge/bridge/config"
	ob "github.com/matterbridge-org/matterbridge/bridge/onebot/internal/client"
)

func validateServer(address, token string) error {
	u, err := url.Parse(address)
	if err != nil || u.Hostname() == "" || (u.Scheme != "ws" && u.Scheme != "wss") {
		return errors.New("OneBot Server must be a ws:// or wss:// URL")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("OneBot Server cannot contain credentials, query or fragment; use Token")
	}
	if u.Path == "/api" || u.Path == "/api/" || u.Path == "/event" || u.Path == "/event/" {
		return errors.New("OneBot Server must provide both events and actions; use the root WebSocket path")
	}
	if strings.ContainsAny(token, "\r\n") {
		return errors.New("OneBot Token contains a newline")
	}
	return nil
}

func groupID(channel string) (int64, error) {
	id, err := strconv.ParseInt(channel, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != channel {
		return 0, fmt.Errorf("OneBot channel %q must be a positive decimal QQ group ID", channel)
	}
	return id, nil
}

func plainText(m config.Message) string {
	parts := []string{}
	if m.Text != "" {
		parts = append(parts, m.Text)
	}
	for _, raw := range m.Extra["file"] {
		f, ok := raw.(config.FileInfo)
		if !ok {
			continue
		}
		label := "[文件 " + f.Name + "]"
		if u, err := url.Parse(f.URL); err == nil && u.Hostname() != "" && u.User == nil && (u.Scheme == "http" || u.Scheme == "https") {
			label += " " + f.URL
		}
		if f.Comment != "" && f.Comment != m.Text {
			label += " " + f.Comment
		}
		parts = append(parts, label)
	}
	return ob.CleanText(strings.Join(parts, "\n"))
}
