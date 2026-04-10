//go:build with_mqtt

package mqtt

import (
	"conduit/internal/config"
	"conduit/internal/tools"
	"conduit/internal/tools/types"
)

func init() {
	tools.RegisterOptional("MQTT", func(services *types.ToolServices, cfg *config.Config) (types.Tool, error) {
		if services.MQTTService == nil {
			return nil, nil
		}
		return NewMQTTTool(services), nil
	})
}
