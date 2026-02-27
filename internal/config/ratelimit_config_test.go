package config

import (
	"encoding/json"
	"os"
	"testing"
)

func TestRateLimitingConfig_Default(t *testing.T) {
	cfg := Default()

	// Check that rate limiting is enabled by default
	if !cfg.RateLimiting.Enabled {
		t.Error("Expected rate limiting to be enabled by default")
	}

	// Check anonymous settings
	if cfg.RateLimiting.Anonymous.WindowSeconds != 60 {
		t.Errorf("Expected anonymous window 60 seconds, got %d", cfg.RateLimiting.Anonymous.WindowSeconds)
	}
	if cfg.RateLimiting.Anonymous.MaxRequests != 100 {
		t.Errorf("Expected anonymous max requests 100, got %d", cfg.RateLimiting.Anonymous.MaxRequests)
	}

	// Check authenticated settings
	if cfg.RateLimiting.Authenticated.WindowSeconds != 60 {
		t.Errorf("Expected authenticated window 60 seconds, got %d", cfg.RateLimiting.Authenticated.WindowSeconds)
	}
	if cfg.RateLimiting.Authenticated.MaxRequests != 1000 {
		t.Errorf("Expected authenticated max requests 1000, got %d", cfg.RateLimiting.Authenticated.MaxRequests)
	}

	// Check cleanup interval
	if cfg.RateLimiting.CleanupIntervalSeconds != 300 {
		t.Errorf("Expected cleanup interval 300 seconds, got %d", cfg.RateLimiting.CleanupIntervalSeconds)
	}
}

func TestRateLimitingConfig_JSON(t *testing.T) {
	cfg := Default()

	// Marshal to JSON
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal config: %v", err)
	}

	// Verify rate limiting section is present
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal config: %v", err)
	}

	rateLimiting, ok := parsed["rateLimiting"].(map[string]interface{})
	if !ok {
		t.Fatal("Rate limiting section not found in JSON")
	}

	if enabled, ok := rateLimiting["enabled"].(bool); !ok || !enabled {
		t.Error("Expected rate limiting enabled in JSON")
	}

	anonymous, ok := rateLimiting["anonymous"].(map[string]interface{})
	if !ok {
		t.Fatal("Anonymous section not found in rate limiting config")
	}

	if windowSec, ok := anonymous["windowSeconds"].(float64); !ok || int(windowSec) != 60 {
		t.Errorf("Expected anonymous windowSeconds 60, got %v", windowSec)
	}

	if maxReq, ok := anonymous["maxRequests"].(float64); !ok || int(maxReq) != 100 {
		t.Errorf("Expected anonymous maxRequests 100, got %v", maxReq)
	}
}

func TestRateLimitingConfig_LoadAndSave(t *testing.T) {
	// Create temporary config file
	tempFile := "/tmp/test_config_ratelimit.json"
	defer os.Remove(tempFile)

	// Save default config
	cfg := Default()
	if err := cfg.Save(tempFile); err != nil {
		t.Fatalf("Failed to save config: %v", err)
	}

	// Load it back
	loadedCfg, err := Load(tempFile)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// Verify rate limiting configuration is preserved
	if loadedCfg.RateLimiting.Enabled != cfg.RateLimiting.Enabled {
		t.Errorf("Rate limiting enabled mismatch: expected %v, got %v",
			cfg.RateLimiting.Enabled, loadedCfg.RateLimiting.Enabled)
	}

	if loadedCfg.RateLimiting.Anonymous.MaxRequests != cfg.RateLimiting.Anonymous.MaxRequests {
		t.Errorf("Anonymous max requests mismatch: expected %d, got %d",
			cfg.RateLimiting.Anonymous.MaxRequests, loadedCfg.RateLimiting.Anonymous.MaxRequests)
	}

	if loadedCfg.RateLimiting.Authenticated.MaxRequests != cfg.RateLimiting.Authenticated.MaxRequests {
		t.Errorf("Authenticated max requests mismatch: expected %d, got %d",
			cfg.RateLimiting.Authenticated.MaxRequests, loadedCfg.RateLimiting.Authenticated.MaxRequests)
	}
}

func TestRateLimitingConfig_CustomValues(t *testing.T) {
	// Test loading config with custom rate limiting values
	customConfigJSON := `{
  "port": 18890,
  "database": {"path": "test.db"},
  "ai": {
    "default_provider": "anthropic",
    "providers": [{"name": "anthropic", "type": "anthropic", "model": "claude-3"}]
  },
  "agent": {
    "name": "Test",
    "personality": "helpful",
    "identity": {"oauth_identity": "test", "api_key_identity": "test"},
    "capabilities": {"memory_recall": true, "tool_chaining": true}
  },
  "tools": {"enabled_tools": [], "max_tool_chains": 25, "sandbox": {"workspace_dir": "./test"}},
  "channels": [],
  "heartbeat": {
    "enabled": true,
    "interval_seconds": 30,
    "timeout_seconds": 10,
    "max_retry_attempts": 3,
    "retry_backoff_seconds": 5,
    "channels": []
  },
  "agent_heartbeat": {
    "enabled": false,
    "interval_seconds": 60,
    "timeout_seconds": 10,
    "max_retry_attempts": 3,
    "retry_backoff_seconds": 5
  },
  "rateLimiting": {
    "enabled": false,
    "anonymous": {
      "windowSeconds": 30,
      "maxRequests": 50
    },
    "authenticated": {
      "windowSeconds": 120,
      "maxRequests": 2000
    },
    "cleanupIntervalSeconds": 600
  }
}`

	// Write custom config to temp file
	tempFile := "/tmp/test_custom_ratelimit.json"
	defer os.Remove(tempFile)

	if err := os.WriteFile(tempFile, []byte(customConfigJSON), 0644); err != nil {
		t.Fatalf("Failed to write custom config: %v", err)
	}

	// Load custom config
	cfg, err := Load(tempFile)
	if err != nil {
		t.Fatalf("Failed to load custom config: %v", err)
	}

	// Verify custom rate limiting values
	if cfg.RateLimiting.Enabled {
		t.Error("Expected rate limiting to be disabled in custom config")
	}

	if cfg.RateLimiting.Anonymous.WindowSeconds != 30 {
		t.Errorf("Expected anonymous window 30 seconds, got %d", cfg.RateLimiting.Anonymous.WindowSeconds)
	}

	if cfg.RateLimiting.Anonymous.MaxRequests != 50 {
		t.Errorf("Expected anonymous max requests 50, got %d", cfg.RateLimiting.Anonymous.MaxRequests)
	}

	if cfg.RateLimiting.Authenticated.WindowSeconds != 120 {
		t.Errorf("Expected authenticated window 120 seconds, got %d", cfg.RateLimiting.Authenticated.WindowSeconds)
	}

	if cfg.RateLimiting.Authenticated.MaxRequests != 2000 {
		t.Errorf("Expected authenticated max requests 2000, got %d", cfg.RateLimiting.Authenticated.MaxRequests)
	}

	if cfg.RateLimiting.CleanupIntervalSeconds != 600 {
		t.Errorf("Expected cleanup interval 600 seconds, got %d", cfg.RateLimiting.CleanupIntervalSeconds)
	}
}
