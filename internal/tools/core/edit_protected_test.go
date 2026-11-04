package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"conduit/internal/config"
	"conduit/internal/sandbox"
	"conduit/internal/tools/types"
)

// The Edit tool refuses the protected config, also through a symlink.
func TestEditTool_RefusesProtectedFile(t *testing.T) {
	t.Cleanup(sandbox.ResetProtected)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{"port": 19999}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(cfgPath, link); err != nil {
		t.Fatal(err)
	}
	sandbox.Protect(cfgPath)
	tool := NewEditTool(&types.ToolServices{ConfigMgr: &config.Config{Tools: config.ToolsConfig{
		Sandbox: config.SandboxConfig{WorkspaceDir: dir, AllowedPaths: []string{dir}},
	}}})
	for _, p := range []string{cfgPath, link} {
		res, err := tool.Execute(context.Background(), map[string]interface{}{"path": p, "old_string": "19999", "new_string": "1"})
		if err != nil {
			t.Fatal(err)
		}
		if res.Success || res.ErrorDetails == nil || res.ErrorDetails.Type != "path_protected" {
			t.Errorf("Edit %s: %+v, want path_protected", p, res)
		}
	}
	if b, _ := os.ReadFile(cfgPath); string(b) != `{"port": 19999}` {
		t.Fatalf("config changed: %s", b)
	}
}
