package gateway

import (
	"strings"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/matterbridge-org/matterbridge/bridge"
	"github.com/matterbridge-org/matterbridge/bridge/config"
	"github.com/matterbridge-org/matterbridge/bridge/helper"
)

const onebotProtocol = "onebot"

// Message IDs are local to an account and channel, not globally to a protocol.
type replyKey struct{ account, channel, id string }
type replyTarget struct{ account, channel string }
type replyRecord struct {
	ids   map[replyTarget]string
	quote *config.MessageQuote
}

func (gw *Gateway) rememberReply(message *config.Message, destinations []*BrMsgID) {
	if message.ID == "" || (message.Event != "" && message.Event != config.EventUserAction) {
		return
	}
	if gw.replies == nil {
		gw.replies, _ = lru.New[replyKey, *replyRecord](5000)
	}
	key := replyKey{message.Account, message.Channel, message.ID}
	record, found := gw.replies.Get(key)
	if !found {
		record = &replyRecord{ids: map[replyTarget]string{}, quote: helper.QuotePreview(message.ID, message.Username, message.Text)}
	}
	record.ids[replyTarget{message.Account, message.Channel}] = message.ID
	gw.replies.Add(key, record)
	for _, destination := range destinations {
		channel := gw.Channels[destination.ChannelID]
		if channel == nil {
			continue
		}
		id := strings.TrimPrefix(destination.ID, destination.br.Protocol+" ")
		if id == "" {
			continue
		}
		target := replyTarget{destination.br.Account, channel.Name}
		record.ids[target] = id
		gw.replies.Add(replyKey{target.account, target.channel, id}, record)
	}
}

func (gw *Gateway) replyForDestination(message *config.Message, destination *bridge.Bridge, channel *config.ChannelInfo) (string, *config.MessageQuote) {
	quote := message.Quote
	if message.ParentID == "" {
		return "", quote
	}
	if quote == nil {
		quote = helper.QuotePreview(message.ParentID, "", "")
	}
	var parent string
	if gw.replies != nil {
		if record, ok := gw.replies.Get(replyKey{message.Account, message.Channel, message.ParentID}); ok {
			parent = record.ids[replyTarget{destination.Account, channel.Name}]
			if quote.Text == "" {
				quote = helper.QuotePreview(message.ParentID, record.quote.Username, record.quote.Text)
			}
		}
	}
	if !destination.GetBool("PreserveThreading") {
		return "", quote
	}
	if parent == "" {
		return config.ParentIDNotFound, quote
	}
	return parent, quote
}

// Keep the scoped mapping isolated from upstream threading for other protocols.
func (gw *Gateway) prepareReply(source, message *config.Message, destination *bridge.Bridge, channel *config.ChannelInfo) {
	if destination.Protocol == onebotProtocol || destination.Protocol == ircProtocol || source.Protocol == onebotProtocol || source.Protocol == ircProtocol {
		message.ParentID, message.Quote = gw.replyForDestination(source, destination, channel)
	}

	if destination.Protocol != onebotProtocol && destination.Protocol != ircProtocol && message.Quote != nil && (!destination.GetBool("PreserveThreading") || !message.ParentValid()) {
		message.Text = helper.QuotePrefix(message.Quote) + message.Text
	}
}
