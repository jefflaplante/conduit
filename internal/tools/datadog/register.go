//go:build with_datadog

package datadog

import (
	"conduit/internal/config"
	"conduit/internal/tools"
	"conduit/internal/tools/types"
)

func init() {
	tools.RegisterOptional("Datadog", func(services *types.ToolServices, cfg *config.Config) (types.Tool, error) {
		if cfg == nil || !cfg.Datadog.Enabled {
			return nil, nil
		}
		return NewDatadogTool(services, &cfg.Datadog)
	})
	tools.RegisterOptional("DatadogMonitor", func(services *types.ToolServices, cfg *config.Config) (types.Tool, error) {
		if cfg == nil || !cfg.Datadog.Enabled {
			return nil, nil
		}
		return NewMonitorTool(services, &cfg.Datadog)
	})
}
