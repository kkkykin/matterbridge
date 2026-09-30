package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/matterbridge-org/matterbridge/bridge/onebot/internal/socket"
	"github.com/sirupsen/logrus"
)

// ID accepts both JSON numbers and decimal strings without float64 rounding.
type ID int64

func (id *ID) UnmarshalJSON(b []byte) error {
	var value string
	if len(b) > 0 && b[0] == '"' {
		if err := json.Unmarshal(b, &value); err != nil {
			return err
		}
	} else {
		value = string(b)
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return err
	}
	*id = ID(n)
	return nil
}

type Event struct {
	PostType    string          `json:"post_type"`
	MessageType string          `json:"message_type"`
	SelfID      ID              `json:"self_id"`
	GroupID     ID              `json:"group_id"`
	UserID      ID              `json:"user_id"`
	MessageID   ID              `json:"message_id"`
	Message     json.RawMessage `json:"message"`
	Sender      struct {
		Nickname string `json:"nickname"`
		Card     string `json:"card"`
	} `json:"sender"`
}

type Response struct {
	Status  string          `json:"status"`
	RetCode int             `json:"retcode"`
	Data    json.RawMessage `json:"data"`
	Echo    string          `json:"echo"`
}

type Client struct {
	socket  *socket.Client
	log     *logrus.Entry
	events  chan Event
	seq     atomic.Uint64
	mu      sync.Mutex
	pending map[string]chan Response
}

func New(address, token string, log *logrus.Entry) *Client {
	return &Client{socket: socket.New(address, token, "onebot", log), log: log, events: make(chan Event, 256), pending: make(map[string]chan Response)}
}

func (c *Client) Events() <-chan Event    { return c.events }
func (c *Client) Run(ctx context.Context) { c.socket.Run(ctx, c.receive) }

func (c *Client) receive(data []byte) {
	var envelope struct {
		PostType string `json:"post_type"`
	}
	if json.Unmarshal(data, &envelope) != nil {
		c.log.Warn("invalid OneBot JSON")
		return
	}
	if envelope.PostType != "" {
		if envelope.PostType != "message" {
			return
		}
		var event Event
		if json.Unmarshal(data, &event) != nil {
			c.log.Warn("invalid OneBot event")
			return
		}
		if event.MessageType != "group" {
			return
		}
		select {
		case c.events <- event:
		default:
			c.log.WithFields(logrus.Fields{"group_id": event.GroupID, "message_id": event.MessageID}).Error("OneBot event queue full; message dropped")
		}
		return
	}
	var response Response
	if json.Unmarshal(data, &response) != nil {
		return
	}
	if response.Echo == "" && response.Status == "failed" {
		// NapCat upgrades the WebSocket before checking its token, then sends
		// an unsolicited 1403 response and closes the connection on rejection.
		c.log.WithField("retcode", response.RetCode).Warn("OneBot rejected connection")
		return
	}
	c.mu.Lock()
	ch := c.pending[response.Echo]
	if ch != nil {
		select {
		case ch <- response:
		default:
		}
	}
	c.mu.Unlock()
}

// Call never retries a write: a timeout/disconnect may mean the action ran but
// its acknowledgement was lost. Retrying could duplicate a QQ message.
func (c *Client) Call(ctx context.Context, action string, params any) (json.RawMessage, error) {
	s, err := c.socket.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	echo := strconv.FormatUint(c.seq.Add(1), 10)
	ch := make(chan Response, 1)
	c.mu.Lock()
	c.pending[echo] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, echo); c.mu.Unlock() }()
	request := struct {
		Action string `json:"action"`
		Params any    `json:"params"`
		Echo   string `json:"echo"`
	}{action, params, echo}
	if err := s.Write(ctx, request); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.Done():
		return nil, socket.ErrDisconnected
	case response := <-ch:
		if response.Status != "ok" || response.RetCode != 0 {
			return nil, fmt.Errorf("OneBot action failed (retcode=%d)", response.RetCode)
		}
		return response.Data, nil
	}
}

func (c *Client) SendGroup(ctx context.Context, groupID int64, message []Segment) (string, error) {
	data, err := c.Call(ctx, "send_group_msg", map[string]any{
		"group_id": groupID,
		"message":  message,
	})
	if err != nil {
		return "", err
	}
	var result struct {
		MessageID *ID `json:"message_id"`
	}
	if err := json.Unmarshal(data, &result); err != nil || result.MessageID == nil {
		return "", fmt.Errorf("OneBot send response missing message_id")
	}
	return strconv.FormatInt(int64(*result.MessageID), 10), nil
}

// GroupMessage validates both identity and scope before exposing quote context.
func (c *Client) GroupMessage(ctx context.Context, groupID int64, messageID string) (Event, error) {
	var result Event
	if !ValidMessageID(messageID) {
		return result, fmt.Errorf("invalid OneBot message ID")
	}
	id, _ := strconv.ParseInt(messageID, 10, 64)
	data, err := c.Call(ctx, "get_msg", map[string]any{"message_id": id})
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return result, err
	}
	if int64(result.GroupID) != groupID || int64(result.MessageID) != id || result.MessageType != "group" {
		return Event{}, fmt.Errorf("OneBot quote response does not match the requested group/message")
	}
	return result, nil
}

type Segment struct {
	Type string            `json:"type"`
	Data map[string]string `json:"data"`
}

type GroupMember struct {
	UserID   ID     `json:"user_id"`
	Nickname string `json:"nickname"`
	Card     string `json:"card"`
}

func (c *Client) GroupMembers(ctx context.Context, groupID int64) ([]GroupMember, error) {
	data, err := c.Call(ctx, "get_group_member_list", map[string]any{
		"group_id": groupID,
		"no_cache": true,
	})
	if err != nil {
		return nil, err
	}
	var members []GroupMember
	if err := json.Unmarshal(data, &members); err != nil {
		return nil, fmt.Errorf("OneBot group members: %w", err)
	}
	if members == nil {
		return nil, fmt.Errorf("OneBot group members response missing member list")
	}
	return members, nil
}
