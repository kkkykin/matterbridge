//go:build !noonebot

package bridgemap

import (
	bonebot "github.com/matterbridge-org/matterbridge/bridge/onebot"
)

func init() { //nolint:gochecknoinits
	FullMap["onebot"] = bonebot.New
}
