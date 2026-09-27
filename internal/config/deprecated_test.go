package config

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Deprecated keys load without error and warn once per process.
func TestLoad_DeprecatedKeysWarnOnce(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	deprecationOnce.Clear() // other tests may already have fired the warnings

	// Start from a valid default config and swap in the owner's live shape.
	var doc map[string]json.RawMessage
	base, err := json.Marshal(Default())
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(base, &doc); err != nil {
		t.Fatal(err)
	}
	doc["ai"] = json.RawMessage(`{"default_provider": "z-ai",
	  "smart_routing": {"enabled": true, "track_usage": true, "cost_budget_daily": 25.0,
	    "pricing_overrides": {"glm-5.3": {"input_per_m_token": 1, "output_per_m_token": 2}}}}`)
	doc["agent_heartbeat"] = json.RawMessage(`{"enabled": true, "interval_minutes": 5,
	  "alert_queue_path": "memory/alerts/pending.json",
	  "alert_retry_policy": {"max_retries": 3, "retry_interval": 30000000000, "backoff_factor": 2}}`)
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("Load #%d: %v", i, err)
		}
		if _, ok := cfg.AI.PricingOverrides["glm-5.3"]; !ok {
			t.Fatalf("smart_routing.pricing_overrides alias not merged: %v", cfg.AI.PricingOverrides)
		}
		if cfg.AgentHeartbeat.AlertQueuePath != "memory/alerts/pending.json" {
			t.Fatalf("alert_queue_path not preserved: %q", cfg.AgentHeartbeat.AlertQueuePath)
		}
	}
	out := buf.String()
	for _, key := range deprecatedWarningsUnderTest {
		if n := strings.Count(out, key); n != 1 {
			t.Errorf("warning %q logged %d times, want 1\n%s", key, n, out)
		}
	}
}

var deprecatedWarningsUnderTest = []string{
	"agent_heartbeat.alert_queue_path is deprecated", // conduit-31jg.59
}
