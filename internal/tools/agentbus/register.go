//go:build with_agentbus

package agentbus

import (
	"conduit/internal/config"
	"conduit/internal/tools"
	"conduit/internal/tools/types"
)

func init() {
	tools.RegisterOptional("AgentBus", func(services *types.ToolServices, cfg *config.Config) (types.Tool, error) {
		agentCfg, err := cfg.AgentBusOrDefaults()
		if err != nil {
			return nil, err
		}
		if !agentCfg.Enabled {
			return nil, nil // compiled in but disabled via config
		}
		ac, err := loadConfigFromOS()
		if err != nil {
			return nil, err
		}
		return NewAgentBusTool(services, &ac)
	})
}
