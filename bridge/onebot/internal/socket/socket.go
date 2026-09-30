// Package socket provides one reconnecting WebSocket with serialized writes.
package socket

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

var ErrDisconnected = errors.New("websocket disconnected")

type Session struct {
	conn *websocket.Conn
	mu   sync.Mutex
	done chan struct{}
}

func (s *Session) Done() <-chan struct{} { return s.done }

func (s *Session) Write(ctx context.Context, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return ErrDisconnected
	default:
	}
	deadline := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := s.conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	if err := s.conn.WriteJSON(value); err != nil {
		// A failed write leaves the connection unusable. Wake the read loop so
		// Run reconnects immediately instead of waiting for the read deadline.
		s.conn.Close()
		return err
	}
	return nil
}

type Client struct {
	url, token, name string
	log              *logrus.Entry
	mu               sync.Mutex
	current          *Session
	changed          chan struct{}
}

func New(address, token, name string, log *logrus.Entry) *Client {
	return &Client{url: address, token: token, name: name, log: log, changed: make(chan struct{})}
}

func (c *Client) Acquire(ctx context.Context) (*Session, error) {
	for {
		c.mu.Lock()
		s, changed := c.current, c.changed
		c.mu.Unlock()
		if s != nil {
			return s, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

// Run invokes receive synchronously; receive must not block on downstream I/O.
// Run must be called once. Cancellation closes the active connection.
func (c *Client) Run(ctx context.Context, receive func([]byte)) {
	backoff := time.Second
	for ctx.Err() == nil {
		headers := http.Header{}
		if c.token != "" {
			headers.Set("Authorization", "Bearer "+c.token)
		}
		dialer := *websocket.DefaultDialer
		dialer.HandshakeTimeout = 10 * time.Second
		conn, resp, err := dialer.DialContext(ctx, c.url, headers)
		if err != nil {
			status := 0
			if resp != nil {
				status = resp.StatusCode
				resp.Body.Close()
			}
			// Avoid logging URLs, server-controlled bodies, tokens or message text.
			c.log.WithFields(logrus.Fields{"peer": c.name, "http_status": status, "retry_in": backoff}).Warn("websocket connection failed")
		} else {
			started := time.Now()
			c.serve(ctx, conn, receive)
			if time.Since(started) > 30*time.Second {
				backoff = time.Second
			}
		}
		if ctx.Err() != nil {
			return
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (c *Client) serve(ctx context.Context, conn *websocket.Conn, receive func([]byte)) {
	s := &Session{conn: conn, done: make(chan struct{})}
	conn.SetReadLimit(4 << 20)
	_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(90 * time.Second)) })
	c.mu.Lock()
	c.current = s
	close(c.changed)
	c.changed = make(chan struct{})
	c.mu.Unlock()
	c.log.WithField("peer", c.name).Info("websocket connected")
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				conn.Close()
				return
			case <-s.done:
				return
			case <-ticker.C:
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)); err != nil {
					conn.Close()
					return
				}
			}
		}
	}()
	defer func() {
		c.mu.Lock()
		c.current = nil
		close(s.done)
		c.mu.Unlock()
		conn.Close()
		<-stopped
		c.log.WithField("peer", c.name).Info("websocket disconnected")
	}()
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		receive(data)
	}
}
