package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// bd-8sib: the brain.session_state config block — parsed, defaulted and
// validated with the same strictness as workspace.files.state_file, but
// default-OFF and otherwise unused until the flag flips.

func TestSessionStateConfigDefaults(t *testing.T) {
	var cfg Config
	if err := json.Unmarshal([]byte(`{}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Brain.SessionState.Enabled {
		t.Error("session_state.enabled must default to false")
	}
	if cfg.Brain.SessionState.IdleDistill {
		t.Error("idle_distill must default to false")
	}
	if got := cfg.Brain.SessionState.SoftCapTokensEffective(); got != DefaultSessionStateSoftCapTokens {
		t.Errorf("soft cap default = %d, want %d", got, DefaultSessionStateSoftCapTokens)
	}
	if got := cfg.Brain.SessionState.FileEffective(); got != DefaultWorkspaceStateFile {
		t.Errorf("file default = %q, want %q (workspace.files.state_file default)", got, DefaultWorkspaceStateFile)
	}
}

func TestSessionStateConfigDefaultsApplied(t *testing.T) {
	var cfg Config
	if err := json.Unmarshal([]byte(`{"brain":{"session_state":{}}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Brain.SessionState.SoftCapTokensEffective(); got != 500 {
		t.Errorf("empty block soft cap = %d, want 500", got)
	}
}

func TestSessionStateConfigOverrides(t *testing.T) {
	var cfg Config
	body := `{"brain":{"session_state":{
		"enabled": true, "idle_distill": true, "soft_cap_tokens": 800,
		"file": "memory/alt-state.json"}}}`
	if err := json.Unmarshal([]byte(body), &cfg); err != nil {
		t.Fatal(err)
	}
	sc := cfg.Brain.SessionState
	if !sc.Enabled || !sc.IdleDistill {
		t.Errorf("flags not parsed: %+v", sc)
	}
	if sc.SoftCapTokensEffective() != 800 {
		t.Errorf("soft cap = %d, want 800", sc.SoftCapTokensEffective())
	}
	if sc.FileEffective() != "memory/alt-state.json" {
		t.Errorf("file = %q", sc.FileEffective())
	}
}

func TestSessionStateConfigFileFallsBackToWorkspaceStateFile(t *testing.T) {
	var cfg Config
	body := `{"workspace":{"files":{"state_file":"memory/ws-state.json"}}}`
	if err := json.Unmarshal([]byte(body), &cfg); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Brain.SessionState.FileEffectiveFor(cfg.Workspace.Files); got != "memory/ws-state.json" {
		t.Errorf("FileEffectiveFor = %q, want the workspace override", got)
	}
	// session_state.file wins over the workspace default.
	cfg.Brain.SessionState.File = "memory/hook-state.json"
	if got := cfg.Brain.SessionState.FileEffectiveFor(cfg.Workspace.Files); got != "memory/hook-state.json" {
		t.Errorf("FileEffectiveFor with hook override = %q", got)
	}
}

func TestSessionStateConfigValidate(t *testing.T) {
	cases := []struct {
		name    string
		json    string
		wantErr string
	}{
		{name: "defaults valid", json: `{"brain":{"session_state":{}}}`},
		{name: "valid full block", json: `{"brain":{"session_state":{
			"enabled": true, "soft_cap_tokens": 1000, "file": "memory/s.json"}}}`},
		{name: "cap below min", json: `{"brain":{"session_state":{"soft_cap_tokens": 99}}}`,
			wantErr: "soft_cap_tokens"},
		{name: "cap above max", json: `{"brain":{"session_state":{"soft_cap_tokens": 2001}}}`,
			wantErr: "soft_cap_tokens"},
		{name: "cap min edge", json: `{"brain":{"session_state":{"soft_cap_tokens": 100}}}`},
		{name: "cap max edge", json: `{"brain":{"session_state":{"soft_cap_tokens": 2000}}}`},
		{name: "file absolute", json: `{"brain":{"session_state":{"file": "/etc/state.json"}}}`,
			wantErr: "brain.session_state.file"},
		{name: "file traversal", json: `{"brain":{"session_state":{"file": "memory/../../etc/state.json"}}}`,
			wantErr: "brain.session_state.file"},
		{name: "file wrong suffix", json: `{"brain":{"session_state":{"file": "memory/state.txt"}}}`,
			wantErr: "brain.session_state.file"},
		{name: "file dot-dot element only", json: `{"brain":{"session_state":{"file": "../state.json"}}}`,
			wantErr: "brain.session_state.file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var cfg Config
			cfg.Port = 18789
			cfg.Tools.EnabledTools = []string{"read"}
			if err := json.Unmarshal([]byte(tc.json), &cfg); err != nil {
				t.Fatal(err)
			}
			err := cfg.ValidateSemantic()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got none", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not mention %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// The strict parser question: unknown keys inside brain.session_state must
// not crash parse and the block must round-trip through the config struct.
// (Top-level json.Unmarshal is non-strict repo-wide; validation is where
// value constraints live — mirror that split.)
func TestSessionStateConfigRoundTrip(t *testing.T) {
	var cfg Config
	if err := json.Unmarshal([]byte(`{"brain":{"session_state":{"enabled":true,"soft_cap_tokens":750}}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(cfg.Brain.SessionState)
	if err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"\"enabled\":true", "\"soft_cap_tokens\":750"} {
		if !strings.Contains(string(out), sub) {
			t.Errorf("round-trip lost %s: %s", sub, out)
		}
	}
}

// Full Parse must accept the block in a baseline-valid document (the
// config-file path real deployments take).
func TestSessionStateConfigParsesViaLoadPath(t *testing.T) {
	doc := `{"port":18789,"ai":{"providers":[{"name":"p","type":"openai","api_key":"${X_KEY}"}],
		"default_provider":"p"},
		"tools":{"max_tool_chains":25,"enabled_tools":["read"]},
		"brain":{"session_state":{"enabled":true,"idle_distill":true,
		"soft_cap_tokens":800,"file":"memory/hook-state.json"}}}`
	cfg, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	sc := cfg.Brain.SessionState
	if !sc.Enabled || !sc.IdleDistill || sc.File != "memory/hook-state.json" || sc.SoftCapTokensEffective() != 800 {
		t.Errorf("block lost through Parse: %+v", sc)
	}
}
