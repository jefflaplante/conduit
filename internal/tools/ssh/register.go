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
		tool, err := newRegisteredTool(services, cfg)
		if err != nil {
			return nil, err
		}
		return tool, nil
	})
}

// newRegisteredTool builds the SSH tool exactly as the gateway registers it:
// sandboxed SCP paths and the real pool-backed client, so exec, exec_group,
// session_send, SCP and tunnels all reach configured hosts (conduit-enf0).
// Connections are dialled lazily on first use, verified against known_hosts
// (see buildHostKeyCallback) and closed by SSHTool.Close, which the registry
// calls on gateway shutdown. Every remote operation still goes through the
// security engine and the approval gate first.
func newRegisteredTool(services *types.ToolServices, cfg *config.Config) (*SSHTool, error) {
	tool, err := NewSSHTool(services, &cfg.RemoteSSH)
	if err != nil {
		return nil, err
	}
	tool.SetSandbox(sandbox.FromConfig(cfg.Tools.Sandbox)) // conduit-31jg.69
	tool.SetClient(NewPoolClient(tool.pool))
	return tool, nil
}
