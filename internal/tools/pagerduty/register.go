//go:build with_pagerduty

package pagerduty

import (
	"conduit/internal/config"
	"conduit/internal/tools"
	"conduit/internal/tools/types"
)

func init() {
	tools.RegisterOptional("PagerDuty", func(services *types.ToolServices, cfg *config.Config) (types.Tool, error) {
		if cfg == nil || !cfg.PagerDuty.Enabled {
			return nil, nil
		}
		return NewPagerDutyTool(services, &cfg.PagerDuty)
	})
}
