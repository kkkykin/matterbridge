package birc

import (
	"io"
	"testing"

	"github.com/lrstanley/girc"
	"github.com/matterbridge-org/matterbridge/bridge"
	"github.com/matterbridge-org/matterbridge/bridge/config"
	"github.com/sirupsen/logrus"
)

func replyTestBridge(t *testing.T) *Birc {
	t.Helper()
	log := logrus.New()
	log.Out = io.Discard
	cfg := config.NewConfigFromString(log, []byte("[irc.test]\nServer=\"localhost:6667\"\nNick=\"bridge\"\nCharset=\"utf-8\"\n"))
	base := bridge.New(&config.Bridge{Account: "irc.test"})
	base.Config, base.Log = cfg, logrus.NewEntry(log)
	b := New(&bridge.Config{Bridge: base, Remote: make(chan config.Message, 10)}).(*Birc)
	var err error
	b.i, err = b.getClient()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestIRCReplyAliasesAndChannelScope(t *testing.T) {
	r := newIRCReplies()
	r.remember(config.Message{ID: "mb-first", Channel: "#a", Username: "Alice", Text: "original"}, "")
	if r.wireID("#a", "mb-first") != "" {
		t.Fatal("unacknowledged local ID treated as native ID")
	}
	r.echo("#a", "mb-first", "server-first")
	r.echo("#a", "mb-first", "server-second-fragment")
	if id, q := r.parent("#a", "server-second-fragment"); id != "mb-first" || q.Text != "original" {
		t.Fatalf("split reply: %q %+v", id, q)
	}
	if id := r.wireID("#a", "mb-first"); id != "server-first" {
		t.Fatalf("reply must target first fragment, got %s", id)
	}
	if id, q := r.parent("#b", "server-first"); id != "server-first" || q.Text != "" {
		t.Fatal("alias leaked between channels")
	}
	if r.wireID("#b", "mb-first") != "" {
		t.Fatal("local ID leaked between channels")
	}
	r.echo("#a", "mb-forged", "forged-id")
	if id, q := r.parent("#a", "forged-id"); id != "forged-id" || q.Text != "" {
		t.Fatal("unknown token established an alias")
	}
}

func TestIRCIncomingReplyAndLiteralFallback(t *testing.T) {
	b := replyTestBridge(t)
	b.replies.remember(config.Message{ID: "mb-parent", Channel: "#a", Username: "Alice", Text: "你好\n@123"}, "")
	b.replies.echo("#a", "mb-parent", "server-parent")
	m := config.Message{Channel: "#a", Username: "Bob", Text: "reply"}
	e := girc.Event{Tags: girc.ParseTags("msgid=server-child;+draft/reply=server-parent")}
	b.incomingReply(e, &m)
	if m.ID != "server-child" || m.ParentID != "mb-parent" || m.Quote.Text != "你好 @123" {
		t.Fatalf("bad metadata: %+v", m)
	}
	// A classic IRC connection has no message-tags capability.
	b.prepareReply(&m)
	if m.ParentID != "" || m.Text != "[回复 Alice：你好 @123] reply" {
		t.Fatalf("bad fallback: %+v", m)
	}
	for _, id := range []string{"", "a\nb", "a b", "a;b", config.ParentIDNotFound} {
		if validIRCMessageID(id) {
			t.Errorf("accepted invalid reply ID %q", id)
		}
	}
}

func TestIRCEchoCannotBeSpoofedByAnotherNick(t *testing.T) {
	b := replyTestBridge(t)
	b.replies.remember(config.Message{ID: "mb-parent", Channel: "#a", Text: "parent"}, "")
	e := girc.Event{Command: girc.PRIVMSG, Params: []string{"#a", "parent"}, Source: &girc.Source{Name: "attacker"}, Tags: girc.ParseTags("msgid=evil;+matterbridge/id=mb-parent")}
	b.handleReplyEcho(b.i, e)
	if b.replies.wireID("#a", "mb-parent") != "" {
		t.Fatal("another nick forged an echo")
	}
	e.Source.Name = b.i.GetNick()
	b.handleReplyEcho(b.i, e)
	if b.replies.wireID("#a", "mb-parent") != "evil" {
		t.Fatal("own echo was not recorded")
	}
}
