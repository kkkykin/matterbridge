package birc

import (
	"errors"

	"github.com/matterbridge-org/matterbridge/bridge/helper"
)

var (
	errMessagePrefixTooLong  = errors.New("IRC message prefix exceeds MessageLength")
	errMessageBudgetTooSmall = errors.New("IRC message prefix leaves too little room to split text")
)

func (b *Birc) splitMessage(text string, prefix int) ([]string, error) {
	if prefix >= b.MessageLength {
		return nil, errMessagePrefixTooLong
	}

	clipped := b.GetString("MessageClipped")
	if clipped == "" {
		clipped = " <clipped message>"
	}

	budget := b.MessageLength - prefix
	if budget < len(clipped)+4 && len(text) > budget {
		return nil, errMessageBudgetTooSmall
	}

	if b.GetBool("UseRelayMsg") {
		// RELAYMSG bypasses girc's splitter.
		// Preserve UTF-8 boundaries in long Chinese text without spaces.
		return helper.GetSubLines(text, budget, clipped), nil
	}

	return helper.GetSubLinesWords(text, budget, clipped), nil
}
