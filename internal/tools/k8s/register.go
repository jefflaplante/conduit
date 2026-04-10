//go:build with_k8s

package k8s

import (
	"conduit/internal/config"
	"conduit/internal/tools"
	"conduit/internal/tools/types"
)

func init() {
	tools.RegisterOptional("Kubernetes", func(services *types.ToolServices, cfg *config.Config) (types.Tool, error) {
		if cfg == nil || !cfg.Kubernetes.Enabled {
			return nil, nil
		}
		return NewK8sTool(services, &cfg.Kubernetes)
	})
}
