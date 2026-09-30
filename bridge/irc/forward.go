package birc

import (
	"crypto/rand"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/lrstanley/girc"
	"github.com/matterbridge-org/matterbridge/bridge/config"
	"github.com/matterbridge-org/matterbridge/bridge/helper"
)

const forwardChannelPrefix = "#mb-forward-"

type forwardRoom struct {
	created        time.Time
	ready, visited bool
	readers        map[string]bool
	lines          []string
	next           int
}

// This viewer owns its channels and queue; they are never gateway channels or
// reconnect subscriptions. All transcript output shares one throttled worker.
type ircForwards struct {
	mu      sync.Mutex
	rooms   map[string]*forwardRoom
	stop    chan struct{}
	done    chan struct{}
	client  *girc.Client
	timeout time.Duration
}

func (b *Birc) startForwards(client *girc.Client) {
	f := &ircForwards{rooms: map[string]*forwardRoom{}, stop: make(chan struct{}), done: make(chan struct{}), client: client}
	f.timeout = time.Duration(b.GetInt("ForwardChannelTimeout")) * time.Second
	if f.timeout <= 0 {
		f.timeout = 5 * time.Minute
	}
	b.forwards = f
	client.Handlers.Add(girc.ALL_EVENTS, f.handle)
	go func() {
		defer close(f.done)
		ticker := time.NewTicker(time.Duration(max(10, b.MessageDelay)) * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-f.stop:
				return
			case now := <-ticker.C:
				f.tick(now)
			}
		}
	}()
}

func (f *ircForwards) close() {
	if f == nil {
		return
	}
	close(f.stop)
	<-f.done
	f.mu.Lock()
	defer f.mu.Unlock()
	for channel := range f.rooms {
		f.part(channel)
	}
}

func (b *Birc) prepareForwards(message *config.Message) {
	if b.forwards == nil {
		return
	}
	for _, item := range message.Extra[config.ExtraForward] {
		forward, ok := item.(*config.MessageForward)
		if !ok || len(forward.Nodes) == 0 {
			continue
		}
		channel := b.forwards.open(forward)
		if channel == "" {
			message.Text += " [临时频道已满，无法展开合并转发]"
		} else {
			message.Text += " [查看合并转发：/join " + channel + "]"
		}
	}
}

func (f *ircForwards) open(forward *config.MessageForward) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.rooms) >= 32 {
		return ""
	}
	channel := forwardChannelPrefix + strings.ToLower(rand.Text())
	room := &forwardRoom{created: time.Now(), readers: map[string]bool{}}
	room.lines = append(room.lines, fmt.Sprintf("[合并转发：%d 个节点；只读，离开后自动清理]", len(forward.Nodes)))
	for _, node := range forward.Nodes {
		name := forwardPlain(node.Username)
		text := forwardPlain(node.Text)
		if name != "" {
			text = "<" + name + "> " + text
		}
		// Leave enough room for the bot's source prefix on a 512-byte server.
		room.lines = append(room.lines, helper.GetSubLines(text, 300, " …")...)
		if len(room.lines) > 1000 {
			room.lines = append(room.lines[:1000:1000], "[合并转发已截断：超过 1000 行]")
			break
		}
	}
	room.lines = append(room.lines, "[合并转发结束]")
	room.next = len(room.lines) // Replay only after a reader arrives.
	f.rooms[channel] = room
	f.client.Cmd.Join(channel)
	return channel
}

func forwardPlain(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

func (f *ircForwards) part(channel string) {
	delete(f.rooms, channel) // Discard pending output before sending PART.
	f.client.Cmd.Part(channel)
}

func (f *ircForwards) tick(now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sent := false
	for channel, room := range f.rooms {
		if !room.visited && now.Sub(room.created) >= f.timeout {
			f.part(channel)
			continue
		}
		if !sent && room.ready && len(room.readers) > 0 && room.next < len(room.lines) {
			f.client.Cmd.Message(channel, room.lines[room.next])
			room.next++
			sent = true
		}
	}
}

// ALL_EVENTS runs before girc's state handlers. Track membership here rather
// than racing its JOIN/PART state updates. NAMES completes the initial roster.
func (f *ircForwards) handle(client *girc.Client, event girc.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if event.Command == girc.DISCONNECTED {
		clear(f.rooms)
		return
	}
	self := strings.ToLower(client.GetNick())
	source := ""
	if event.Source != nil {
		source = strings.ToLower(event.Source.Name)
	}
	if event.Command == girc.QUIT || event.Command == girc.NICK {
		for channel, room := range f.rooms {
			if !room.readers[source] {
				continue
			}
			delete(room.readers, source)
			if event.Command == girc.NICK && len(event.Params) > 0 {
				room.readers[strings.ToLower(event.Params[0])] = true
			}
			if len(room.readers) == 0 && room.visited {
				f.part(channel)
			}
		}
		return
	}
	if len(event.Params) == 0 {
		return
	}
	channel := strings.ToLower(event.Params[0])
	if event.Command == girc.RPL_NAMREPLY && len(event.Params) >= 4 {
		channel = strings.ToLower(event.Params[2])
	} else if (event.Command == girc.RPL_ENDOFNAMES || (event.Command >= "400" && event.Command <= "599")) && len(event.Params) >= 2 {
		channel = strings.ToLower(event.Params[1])
	}
	room := f.rooms[channel]
	if room == nil {
		return
	}
	switch event.Command {
	case girc.JOIN:
		if source == self {
			client.Cmd.Mode(channel, "+snmt")
			client.Cmd.Topic(channel, "合并转发 · 只读 · 最后一位读者离开后自动清理")
		} else if source != "" {
			room.readers[source] = true
			room.visited, room.next = true, 0
		}
	case girc.RPL_NAMREPLY:
		for _, nick := range strings.Fields(event.Last()) {
			nick = strings.ToLower(strings.TrimLeft(nick, "~&@%+"))
			nick, _, _ = strings.Cut(nick, "!") // userhost-in-names
			if nick != self {
				room.readers[nick] = true
				room.visited, room.next = true, 0
			}
		}
	case girc.RPL_ENDOFNAMES:
		room.ready = true
	case girc.PART, girc.KICK:
		if event.Command == girc.KICK && len(event.Params) >= 2 {
			source = strings.ToLower(event.Params[1])
		}
		if source == self {
			delete(f.rooms, channel)
			return
		}
		delete(room.readers, source)
		if room.visited && len(room.readers) == 0 {
			f.part(channel)
		}
	default:
		if event.Command >= "400" && event.Command <= "599" {
			f.part(channel)
		}
	}
}

func forwardChannelEvent(event girc.Event) bool {
	return len(event.Params) > 0 && strings.HasPrefix(strings.ToLower(event.Params[0]), forwardChannelPrefix)
}
