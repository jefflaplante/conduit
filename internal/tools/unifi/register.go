//go:build with_unifi

package unifi

import (
	"conduit/internal/config"
	"conduit/internal/tools"
	"conduit/internal/tools/types"
)

func init() {
	tools.RegisterOptional("UniFi", func(services *types.ToolServices, cfg *config.Config) (types.Tool, error) {
		return NewUniFiTool(services), nil
	})
}
