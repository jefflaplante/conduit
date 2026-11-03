package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// minimalParseConfig is the smallest JSON that passes config.Parse's full
// Validate (port, one provider, tools) so Parse-level tests only exercise
// the workspace keys.
func minimalParseConfig(t *testing.T, workspace map[string]interface{}) []byte {
	t.Helper()
	cfg := map[string]interface{}{
		"port": 18789,
		"ai": map[string]interface{}{
			"default_provider": "anthropic",
			"providers": []interface{}{map[string]interface{}{
				"name": "anthropic", "type": "anthropic", "model": "claude-3-5-sonnet-20241022",
			}},
		},
		"tools": map[string]interface{}{
			"enabled_tools": []interface{}{"read"}, "max_tool_chains": 25,
		},
	}
	if workspace != nil {
		cfg["workspace"] = workspace
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

// TestParse_WorkspaceFilesDefaultsOmitted: a config without the new keys
// parses (backward compatible) and yields the documented defaults via the
// accessors (bd-3ia phase 0 — keys parsed, defaulted, not yet consumed).
func TestParse_WorkspaceFilesDefaultsOmitted(t *testing.T) {
	cfg, err := Parse(minimalParseConfig(t, map[string]interface{}{
		"context_dir": "./ws",
		"files":       map[string]interface{}{"core": []interface{}{"SOUL.md"}},
	}))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := cfg.Workspace.Files.StateFilePath(); got != DefaultWorkspaceStateFile {
		t.Errorf("state_file default = %q, want %q", got, DefaultWorkspaceStateFile)
	}
	if !cfg.Workspace.Files.MemoryMDInjectEnabled() {
		t.Error("memory_md_inject default should be true")
	}
}

// TestParse_WorkspaceFilesExplicitValues: explicit values round-trip through
// the JSON path.
func TestParse_WorkspaceFilesExplicitValues(t *testing.T) {
	cfg, err := Parse(minimalParseConfig(t, map[string]interface{}{
		"files": map[string]interface{}{
			"state_file":       "memory/custom.json",
			"memory_md_inject": false,
		},
	}))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := cfg.Workspace.Files.StateFilePath(); got != "memory/custom.json" {
		t.Errorf("state_file = %q, want memory/custom.json", got)
	}
	if cfg.Workspace.Files.MemoryMDInjectEnabled() {
		t.Error("memory_md_inject=false should parse to disabled")
	}
}

// TestParse_WorkspaceStateFileRejected: invalid state_file values fail the
// full Parse pipeline (not just ValidateSemantic).
func TestParse_WorkspaceStateFileRejected(t *testing.T) {
	for _, bad := range []string{"/abs/state.json", "memory/../../state.json", "memory/state.md"} {
		_, err := Parse(minimalParseConfig(t, map[string]interface{}{
			"files": map[string]interface{}{"state_file": bad},
		}))
		if err == nil {
			t.Errorf("state_file %q should fail Parse", bad)
			continue
		}
		if !strings.Contains(err.Error(), "workspace.files.state_file") {
			t.Errorf("state_file %q: error %q should name workspace.files.state_file", bad, err)
		}
	}
}

// bd-3ia phase 0: workspace.files.state_file / memory_md_inject defaults.
func TestWorkspaceFilesConfig_Defaults(t *testing.T) {
	var f WorkspaceFilesConfig
	if got := f.StateFilePath(); got != DefaultWorkspaceStateFile {
		t.Errorf("StateFilePath() = %q, want %q", got, DefaultWorkspaceStateFile)
	}
	if got := DefaultWorkspaceStateFile; got != "memory/jules-state.json" {
		t.Errorf("DefaultWorkspaceStateFile = %q, want memory/jules-state.json", got)
	}
	if !f.MemoryMDInjectEnabled() {
		t.Error("MemoryMDInjectEnabled() should default to true")
	}
}

func TestWorkspaceFilesConfig_Overrides(t *testing.T) {
	off := false
	f := WorkspaceFilesConfig{StateFile: "memory/state.json", MemoryMDInject: &off}
	if got := f.StateFilePath(); got != "memory/state.json" {
		t.Errorf("StateFilePath() = %q, want memory/state.json", got)
	}
	if f.MemoryMDInjectEnabled() {
		t.Error("MemoryMDInjectEnabled() should honor explicit false")
	}
	on := true
	if !(WorkspaceFilesConfig{MemoryMDInject: &on}).MemoryMDInjectEnabled() {
		t.Error("MemoryMDInjectEnabled() should honor explicit true")
	}
}

// TestValidateSemantic_WorkspaceStateFile exercises the state_file validation
// rules: non-empty-when-set is structural ("" = default), absolute rejected,
// ".." traversal rejected, non-.json rejected, valid relatives accepted.
func TestValidateSemantic_WorkspaceStateFile(t *testing.T) {
	cases := []struct {
		name      string
		stateFile string
		wantErr   string // empty = must pass
	}{
		{"omitted means default", "", ""},
		{"valid nested path", "memory/jules-state.json", ""},
		{"valid bare filename", "state.json", ""},
		{"absolute path", "/etc/state.json", "must be a relative path"},
		{"parent traversal", "../state.json", "must be a relative path"},
		{"nested traversal", "memory/../../state.json", "must be a relative path"},
		{"dot-dot element", "a/../b.json", `".." path elements`},
		{"non-json extension", "memory/jules-state.md", "must be a .json file"},
		{"no extension", "memory/jules-state", "must be a .json file"},
		{"uppercase JSON not accepted", "memory/jules-state.JSON", "must be a .json file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := minimalValidConfig()
			cfg.Workspace = WorkspaceConfig{Files: WorkspaceFilesConfig{StateFile: tc.stateFile}}
			err := cfg.ValidateSemantic()
			if tc.wantErr == "" {
				if err != nil {
					t.Errorf("state_file %q should be valid, got: %v", tc.stateFile, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("state_file %q should be rejected", tc.stateFile)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("state_file %q: error %q does not contain %q", tc.stateFile, err, tc.wantErr)
			}
		})
	}
}

// TestValidateSemantic_WorkspaceMemoryMDInject: bool needs no value checks —
// both explicit values and omitted must pass.
func TestValidateSemantic_WorkspaceMemoryMDInject(t *testing.T) {
	for _, inject := range []*bool{nil, wsTestBoolPtr(true), wsTestBoolPtr(false)} {
		cfg := minimalValidConfig()
		cfg.Workspace = WorkspaceConfig{Files: WorkspaceFilesConfig{MemoryMDInject: inject}}
		if err := cfg.ValidateSemantic(); err != nil {
			t.Errorf("memory_md_inject=%v should be valid, got: %v", derefBool(inject), err)
		}
	}
}

func wsTestBoolPtr(b bool) *bool { return &b }

func derefBool(p *bool) bool {
	if p == nil {
		return true // documented default
	}
	return *p
}
