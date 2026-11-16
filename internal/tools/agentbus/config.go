//go:build with_agentbus

// Package agentbus is conduit's outgoing client for the a2a/v1 agent bus
// (github.com/jefflaplante/agentbus). Conduit joins the bus as a single
// agent (default "conduit-home"); the AgentBus tool lets the model list
// agents, send events, make requests and delegate tasks to capability
// work queues. Every publish except a response/error reply passes through
// conduit's human approval gate (approvalgate) via client.Options.
// BeforePublish, so a model can never put conduit on the bus unattended.
package agentbus

import (
	"fmt"
	"os"
	"strings"

	"github.com/jefflaplante/agentbus/a2a"
)

// agentConfig is the resolved AgentBus tool configuration.
type agentConfig struct {
	brokerURL string // AGENTBUS_BROKER_URL, e.g. "tcp://host:1883"
	agentID   string // AGENTBUS_AGENT, default "conduit-home"
	token     string // AGENTBUS_TOKEN (optional; LAN brokers may be open)
}

// defaultAgentID is the agent ID conduit uses when AGENTBUS_AGENT is unset
// (per the integration plan: conduit joins as one agent, "conduit-home").
const defaultAgentID = "conduit-home"

// envLookup abstracts the environment so tests run hermetically.
type envLookup func(string) (string, bool)

// loadConfig resolves the AgentBus tool configuration from lookup.
// The broker URL is required; agent ID defaults to defaultAgentID after a
// contract name check, so a typo'd AGENTBUS_AGENT fails at load time
// instead of surfacing as a broker auth failure.
func loadConfig(lookup envLookup) (agentConfig, error) {
	cfg := agentConfig{}
	broker, ok := lookup("AGENTBUS_BROKER_URL")
	if !ok || strings.TrimSpace(broker) == "" {
		return cfg, fmt.Errorf("agentbus: %s is required (e.g. tcp://host:1883 or ssl://host:8883)", "AGENTBUS_BROKER_URL")
	}
	cfg.brokerURL = strings.TrimSpace(broker)
	cfg.agentID = defaultAgentID
	if v, ok := lookup("AGENTBUS_AGENT"); ok && strings.TrimSpace(v) != "" {
		cfg.agentID = strings.TrimSpace(v)
	}
	if !a2a.ValidName(cfg.agentID) {
		return cfg, fmt.Errorf("agentbus: invalid agent id %q: must be 1-64 chars of a-z, 0-9, '.', '_' or '-', not starting with punctuation", cfg.agentID)
	}
	if v, ok := lookup("AGENTBUS_TOKEN"); ok {
		cfg.token = v
	}
	return cfg, nil
}

// loadConfigFromOS is the production lookup: the process environment,
// matching how the unifi tool reads UNVR_URL/UNVR_API_KEY.
func loadConfigFromOS() (agentConfig, error) {
	return loadConfig(os.LookupEnv)
}
