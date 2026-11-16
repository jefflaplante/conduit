//go:build with_agentbus

package agentbus

import (
	"strings"
	"testing"

	"github.com/jefflaplante/agentbus/a2a"
)

// configFromEnv lets tests inject a controlled environment without
// mutating the process env; production uses os.Getenv.
type envFunc = envLookup

func testEnv(pairs map[string]string) envFunc {
	return func(key string) (string, bool) {
		v, ok := pairs[key]
		return v, ok
	}
}

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    agentConfig
		wantErr string
	}{
		{
			name:    "missing everything",
			env:     map[string]string{},
			wantErr: "AGENTBUS_BROKER_URL",
		},
		{ // broker present, agent id and token default to conduit-home / none
			name: "broker only",
			env:  map[string]string{"AGENTBUS_BROKER_URL": "tcp://bus.lan:1883"},
			want: agentConfig{brokerURL: "tcp://bus.lan:1883", agentID: "conduit-home", token: ""},
		},
		{
			name: "full config",
			env: map[string]string{
				"AGENTBUS_BROKER_URL": "ssl://bus.lan:8883",
				"AGENTBUS_AGENT":      "conduit.lan",
				"AGENTBUS_TOKEN":      "s3cret",
			},
			want: agentConfig{brokerURL: "ssl://bus.lan:8883", agentID: "conduit.lan", token: "s3cret"},
		},
		{
			name:    "invalid agent id (uppercase)",
			env:     map[string]string{"AGENTBUS_BROKER_URL": "tcp://b:1883", "AGENTBUS_AGENT": "Conduit"},
			wantErr: "invalid agent id",
		},
		{
			name:    "invalid agent id (slash)",
			env:     map[string]string{"AGENTBUS_BROKER_URL": "tcp://b:1883", "AGENTBUS_AGENT": "a/b"},
			wantErr: "invalid agent id",
		},
		{
			name:    "empty broker url",
			env:     map[string]string{"AGENTBUS_BROKER_URL": "  ", "AGENTBUS_AGENT": "conduit-home"},
			wantErr: "AGENTBUS_BROKER_URL",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := loadConfig(testEnv(tt.env))
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("loadConfig() err = nil, want error containing %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("loadConfig() err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("loadConfig() unexpected err: %v", err)
			}
			if cfg != tt.want {
				t.Errorf("loadConfig() = %+v, want %+v", cfg, tt.want)
			}
			// agentID must be a contract-valid agent name in every success case.
			if !a2a.ValidName(cfg.agentID) {
				t.Errorf("agent id %q is not a valid a2a name", cfg.agentID)
			}
			a2a.ValidName("sanity") // exercise the a2a import in all builds
		})
	}
}

func TestValidateAgentID(t *testing.T) {
	tests := []struct {
		id   string
		want bool
	}{
		{"conduit-home", true},
		{"conduit.lan", true},
		{"a", true},
		{"", false},
		{"Conduit", false}, // uppercase rejected by a2a.ValidName
		{"-lead", false},   // must not start with punctuation
		{"with space", false},
		{"slash/ed", false},
		{strings.Repeat("a", 65), false}, // over the 64-char name cap
	}
	for _, tt := range tests {
		if got := a2a.ValidName(tt.id); got != tt.want {
			t.Errorf("a2a.ValidName(%q) = %v, want %v", tt.id, got, tt.want)
		}
	}
}
