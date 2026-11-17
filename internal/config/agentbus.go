package config

// AgentBusConfig configures the optional AgentBus tool (a2a/v1 agent bus
// client, github.com/jefflaplante/agentbus). It is inert unless the
// gateway is built with the with_agentbus build tag AND enabled is true.
// Connection material comes from the environment (AGENTBUS_BROKER_URL
// required, AGENTBUS_AGENT default "conduit-home", AGENTBUS_TOKEN), the
// same env-var pattern the UniFi tool uses for UNVR_URL/UNVR_API_KEY.
type AgentBusConfig struct {
	// Enabled controls whether the AgentBus tool is registered.
	Enabled bool `json:"enabled"`
}

// AgentBusOrDefaults returns the AgentBus config, applying defaults for a
// nil section so a missing agentbus block behaves as disabled.
func (c *Config) AgentBusOrDefaults() (*AgentBusConfig, error) {
	cfg := c.AgentBus
	return &cfg, nil
}
