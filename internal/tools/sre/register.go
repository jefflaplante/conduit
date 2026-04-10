//go:build with_sre

package sre

import (
	"fmt"

	"conduit/internal/config"
	"conduit/internal/tools"
	"conduit/internal/tools/types"
)

func init() {
	tools.RegisterOptional("SRE", func(services *types.ToolServices, cfg *config.Config) (types.Tool, error) {
		if cfg == nil {
			return nil, nil
		}

		// SRE requires both PagerDuty and Datadog to be enabled
		if !cfg.PagerDuty.Enabled || !cfg.Datadog.Enabled {
			return nil, nil
		}

		// Get registry as ToolExecutor (we need this for tool orchestration)
		// The registry is obtained through a package-level function
		executor := tools.GetRegistryAsExecutor()
		if executor == nil {
			return nil, fmt.Errorf("SRE tool requires registry as ToolExecutor")
		}

		return NewSRETool(services, executor)
	})
}
