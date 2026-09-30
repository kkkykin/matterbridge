package onebot

import (
	"context"
	"strconv"
	"time"

	"github.com/matterbridge-org/matterbridge/bridge/config"
	"github.com/matterbridge-org/matterbridge/bridge/helper"
	ob "github.com/matterbridge-org/matterbridge/bridge/onebot/internal/client"
)

type quoteKey struct {
	group int64
	id    string
}

// A failed preview lookup must not prevent delivery of the reply itself.
func (b *Bridge) enrichReply(ctx context.Context, client *ob.Client, group ob.ID, message *config.Message) {
	if message.Quote == nil || message.Quote.Text != "" {
		return
	}
	lookup, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	parent, err := client.GroupMessage(lookup, int64(group), message.ParentID)
	if err != nil {
		b.Log.Debug("OneBot reply preview unavailable; preserving the reply reference")
		return
	}
	text, _, err := ob.MessageText(parent.Message)
	if err != nil {
		return
	}
	name := ob.CleanName(parent.Sender.Card)
	if name == "" {
		name = ob.CleanName(parent.Sender.Nickname)
	}
	if name == "" && parent.UserID > 0 {
		name = strconv.FormatInt(int64(parent.UserID), 10)
	}
	message.Quote = helper.QuotePreview(message.ParentID, name, text)
	b.quotes.Add(quoteKey{int64(group), message.ParentID}, message.Quote)
}
