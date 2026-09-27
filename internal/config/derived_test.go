package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// conduit-31jg.40: values that used to be hardcoded for one deployment are
// now derived from config.

// loadMutated writes Default() after mutate, then loads it through Load.
func loadMutated(t *testing.T, mutate func(*Config)) *Config {
	t.Helper()
	d := Default()
	mutate(d)
	body, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

// Owner-shaped config (explicit timezones, explicit MQTT client_id, a
// workspace dir, no skills.gog block) resolves to exactly the values that
// were hardcoded before.
func TestDerivedDefaults_OwnerShape(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "ocgo", "workspace")
	cfg := loadMutated(t, func(c *Config) {
		c.Timezone = "America/Los_Angeles"
		c.Workspace.ContextDir = ws
		c.AgentHeartbeat.Timezone = "America/Los_Angeles"
		c.MQTT = MQTTConfig{Enabled: true, BrokerURL: "tcp://127.0.0.1:1883", ClientID: "conduit-test", Topics: []string{"a/#"}}
	})
	if got, want := cfg.RecallEventsPath(), filepath.Join(ws, "memory", "recall-events.jsonl"); got != want {
		t.Errorf("RecallEventsPath = %q, want %q", got, want)
	}
	if cfg.AgentHeartbeat.Timezone != "America/Los_Angeles" {
		t.Errorf("agent_heartbeat.timezone = %q", cfg.AgentHeartbeat.Timezone)
	}
	if cfg.Skills.WorkspaceDir != ws {
		t.Errorf("skills workspace = %q, want %q", cfg.Skills.WorkspaceDir, ws)
	}
	if cfg.MQTT.ClientID != "conduit-test" || cfg.MQTT.EffectiveClientID() != "conduit-test" {
		t.Errorf("explicit client_id must be kept verbatim, got %q", cfg.MQTT.EffectiveClientID())
	}
}

func TestDerivedDefaults_Inheritance(t *testing.T) {
	cfg := loadMutated(t, func(c *Config) {
		c.Timezone = "Europe/Paris"
		c.AgentHeartbeat.Timezone = ""
		c.Workspace.ContextDir = ""
	})
	if cfg.AgentHeartbeat.Timezone != "Europe/Paris" {
		t.Errorf("agent_heartbeat.timezone should inherit top-level, got %q", cfg.AgentHeartbeat.Timezone)
	}
	if got := cfg.RecallEventsPath(); got != "" {
		t.Errorf("no workspace => recall log disabled, got %q", got)
	}

	// An explicit heartbeat zone wins over the top-level one.
	cfg = loadMutated(t, func(c *Config) {
		c.Timezone = "Europe/Paris"
		c.AgentHeartbeat.Timezone = "Asia/Tokyo"
	})
	if cfg.AgentHeartbeat.Timezone != "Asia/Tokyo" {
		t.Errorf("explicit agent_heartbeat.timezone overridden: %q", cfg.AgentHeartbeat.Timezone)
	}

	// Neither set: no owner zone baked in; location falls back to UTC.
	hb := DefaultAgentHeartbeatConfig()
	if hb.Timezone != "" || hb.GetLocation().String() != "UTC" {
		t.Errorf("default heartbeat zone = %q (%s)", hb.Timezone, hb.GetLocation())
	}
}

func TestMQTTClientID_UniqueDefault(t *testing.T) {
	if DefaultMQTTConfig().ClientID != "" {
		t.Error("default client_id must be empty (generated per process)")
	}
	m := MQTTConfig{Enabled: true, BrokerURL: "tcp://x:1883", Topics: []string{"a"}}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	if m.ClientID != "" {
		t.Errorf("Validate must not pin a fixed client_id, got %q", m.ClientID)
	}
	a, b := m.EffectiveClientID(), m.EffectiveClientID()
	if a == b {
		t.Errorf("generated client IDs collide: %q", a)
	}
	for _, id := range []string{a, b} {
		if !strings.HasPrefix(id, "conduit-") || len(id) > 23 {
			t.Errorf("bad generated client id %q (len %d)", id, len(id))
		}
	}
	m.ClientID = "custom"
	if m.EffectiveClientID() != "custom" {
		t.Error("configured client_id must be used verbatim")
	}
}
