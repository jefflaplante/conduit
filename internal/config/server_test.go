package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiagnosticsConfig_Defaults(t *testing.T) {
	// Test that DefaultDiagnosticsConfig returns secure defaults
	cfg := DefaultDiagnosticsConfig()

	if !cfg.RequireAuth {
		t.Error("RequireAuth should be true by default for security")
	}

	// HealthPublic should be nil (defaults to true)
	if cfg.HealthPublic != nil {
		t.Errorf("HealthPublic should be nil (default), got %v", *cfg.HealthPublic)
	}

	// IsHealthPublic should return true when nil
	if !cfg.IsHealthPublic() {
		t.Error("IsHealthPublic() should return true when HealthPublic is nil")
	}
}

func TestDiagnosticsConfig_IsHealthPublic(t *testing.T) {
	tests := []struct {
		name         string
		healthPublic *bool
		expected     bool
	}{
		{
			name:         "nil defaults to true (for load balancer compatibility)",
			healthPublic: nil,
			expected:     true,
		},
		{
			name:         "explicit true",
			healthPublic: boolPtr(true),
			expected:     true,
		},
		{
			name:         "explicit false requires auth",
			healthPublic: boolPtr(false),
			expected:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DiagnosticsConfig{
				HealthPublic: tt.healthPublic,
			}

			if got := cfg.IsHealthPublic(); got != tt.expected {
				t.Errorf("IsHealthPublic() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestDiagnosticsConfig_InDefaultConfig(t *testing.T) {
	// Verify that Default() includes secure DiagnosticsConfig
	cfg := Default()

	if !cfg.Diagnostics.RequireAuth {
		t.Error("Default config should have Diagnostics.RequireAuth=true")
	}
}

func TestDiagnosticsConfig_JSONSerialization(t *testing.T) {
	// Test that DiagnosticsConfig serializes correctly
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "cfg.json")

	cfgJSON := `{
		"port": 18789,
		"database": {"path": "test.db"},
		"ai": {"default_provider": "anthropic", "providers": [{"name": "anthropic", "type": "anthropic", "api_key": "test", "model": "test"}]},
		"tools": {"enabled_tools": ["read"], "max_tool_chains": 25},
		"diagnostics": {
			"require_auth": true,
			"health_public": false
		},
		"heartbeat": {"enabled": false},
		"agent_heartbeat": {"enabled": false}
	}`

	os.WriteFile(configPath, []byte(cfgJSON), 0644)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if !cfg.Diagnostics.RequireAuth {
		t.Error("RequireAuth should be true from JSON")
	}

	if cfg.Diagnostics.HealthPublic == nil {
		t.Error("HealthPublic should not be nil after loading from JSON")
	} else if *cfg.Diagnostics.HealthPublic {
		t.Error("HealthPublic should be false from JSON")
	}

	if cfg.Diagnostics.IsHealthPublic() {
		t.Error("IsHealthPublic() should return false when HealthPublic is false")
	}
}

func TestDiagnosticsConfig_LegacyBehavior(t *testing.T) {
	// Test that require_auth=false makes all endpoints public (legacy behavior)
	cfg := DiagnosticsConfig{
		RequireAuth:  false,
		HealthPublic: nil,
	}

	// With RequireAuth=false, all diagnostic endpoints should be public
	if cfg.RequireAuth {
		t.Error("RequireAuth should be false for legacy behavior")
	}

	// Health should still be public regardless
	if !cfg.IsHealthPublic() {
		t.Error("Health should be public regardless of require_auth setting")
	}
}

// boolPtr returns a pointer to a bool value
func boolPtr(b bool) *bool {
	return &b
}
