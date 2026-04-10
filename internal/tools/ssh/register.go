//go:build with_ssh

package ssh

import (
	"conduit/internal/config"
	"conduit/internal/tools"
	"conduit/internal/tools/types"
)

func init() {
	tools.RegisterOptional("SSH", func(services *types.ToolServices, cfg *config.Config) (types.Tool, error) {
		if cfg == nil || !cfg.RemoteSSH.Enabled {
			return nil, nil
		}
		return NewSSHTool(services, &cfg.RemoteSSH)
	})
}
