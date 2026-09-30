//go:build !noonebot

package bridgemap_test

import (
	"io"
	"os"
	"testing"

	"github.com/matterbridge-org/matterbridge/bridge/config"
	"github.com/matterbridge-org/matterbridge/bridge/onebot"
	"github.com/matterbridge-org/matterbridge/gateway"
	"github.com/matterbridge-org/matterbridge/gateway/bridgemap"
	"github.com/sirupsen/logrus"
)

// Exercise config parsing and the real registry without connecting to QQ.
func TestOneBotRouterConfiguration(t *testing.T) {
	data, err := os.ReadFile("../../docs/protocols/onebot/qq-only.example.toml")
	if err != nil {
		t.Fatal(err)
	}
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	cfg := config.NewConfigFromString(logger, data)
	router, err := gateway.NewRouter(logger, cfg, bridgemap.FullMap)
	if err != nil {
		t.Fatal(err)
	}
	gw := router.Gateways["qq-groups"]
	if gw == nil {
		t.Fatal("QQ gateway missing")
	}
	br := gw.Bridges["onebot.qq"]
	if br == nil {
		t.Fatal("OneBot account missing")
	}
	if _, ok := br.Bridger.(*onebot.Bridge); !ok {
		t.Fatalf("registered bridge is %T", br.Bridger)
	}
	if len(br.Channels) != 2 || br.GetString("Server") != "ws://127.0.0.1:3001/" {
		t.Fatal("OneBot settings or group channels were not loaded")
	}
	t.Setenv("MATTERBRIDGE_ONEBOT_QQ_TOKEN", "env-token")
	if br.GetString("Token") != "env-token" {
		t.Fatal("OneBot token environment override was not applied")
	}
}
