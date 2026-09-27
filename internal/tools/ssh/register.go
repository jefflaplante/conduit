//go:build with_ssh

package ssh

import (
	"conduit/internal/config"
	"conduit/internal/sandbox"
	"conduit/internal/tools"
	"conduit/internal/tools/types"
)

func init() {
	tools.RegisterOptional("SSH", func(services *types.ToolServices, cfg *config.Config) (types.Tool, error) {
		if cfg == nil || !cfg.RemoteSSH.Enabled {
			return nil, nil
		}
		tool, err := NewSSHTool(services, &cfg.RemoteSSH)
		if err != nil {
			return nil, err
		}
		tool.SetSandbox(sandbox.FromConfig(cfg.Tools.Sandbox)) // conduit-31jg.69
		return tool, nil
	})
}
