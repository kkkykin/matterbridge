//go:build noonebot

package bridgemap_test

import (
	"testing"

	"github.com/matterbridge-org/matterbridge/gateway/bridgemap"
)

func TestOneBotDisabled(t *testing.T) {
	if _, ok := bridgemap.FullMap["onebot"]; ok {
		t.Fatal("OneBot registered despite the noonebot build tag")
	}
}
