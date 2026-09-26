package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"conduit/internal/config"
	"conduit/internal/tools/core"
	"conduit/internal/tools/types"
)

// conduit-31jg.6: Read, Write, Edit and Glob must share one sandbox verdict,
// resolve symlinks, and accept names that merely start with "..".
func TestSandboxConsistency_ReadWriteEdit(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(base, "workspace")
	out := filepath.Join(base, "outside")
	for _, d := range []string{ws, out} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(out, "secret.txt"), "hello secret")
	write(filepath.Join(ws, "..notes", "n.md"), "hello notes")
	write(filepath.Join(ws, "plain.md"), "hello plain")
	if err := os.Symlink(out, filepath.Join(ws, "linkdir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(out, "secret.txt"), filepath.Join(ws, "linkfile")); err != nil {
		t.Fatal(err)
	}

	// Mirrors the live shape: workspace_dir is set; allowed_paths does NOT
	// list it (previously Read/Write ignored workspace_dir, Edit honored it).
	cfg := &config.Config{Tools: config.ToolsConfig{Sandbox: config.SandboxConfig{
		WorkspaceDir: ws,
		AllowedPaths: []string{filepath.Join(base, "unrelated")},
	}}}
	reg := NewRegistry(cfg.Tools)
	reg.services = &types.ToolServices{ConfigMgr: cfg}
	read := &ReadFileTool{registry: reg}
	wr := &WriteFileTool{registry: reg}
	edit := core.NewEditTool(reg.services)
	glob := &ListFilesTool{registry: reg}

	cases := []struct {
		name    string
		path    string
		allowed bool
	}{
		{"plain file", filepath.Join(ws, "plain.md"), true},
		{"dotdot-prefixed dir", filepath.Join(ws, "..notes", "n.md"), true},
		{"symlinked file escape", filepath.Join(ws, "linkfile"), false},
		{"symlinked dir escape", filepath.Join(ws, "linkdir", "secret.txt"), false},
		{"lexical escape", ws + "/../outside/secret.txt", false},
	}
	ctx := context.Background()
	isDenied := func(r *types.ToolResult) bool {
		return r != nil && !r.Success && r.ErrorDetails != nil && r.ErrorDetails.Type == "path_not_allowed"
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr, _ := read.Execute(ctx, map[string]interface{}{"path": tc.path})
			er, _ := edit.Execute(ctx, map[string]interface{}{"path": tc.path, "old_string": "hello", "new_string": "hello"})
			wrr, _ := wr.Execute(ctx, map[string]interface{}{"path": tc.path, "content": "hello overwritten"})
			for name, r := range map[string]*types.ToolResult{"Read": rr, "Edit": er, "Write": wrr} {
				if isDenied(r) == tc.allowed {
					t.Errorf("%s(%s): denied=%v, want allowed=%v (result=%+v)", name, tc.path, isDenied(r), tc.allowed, r)
				}
			}
		})
	}

	// The escape targets must be untouched.
	if b, _ := os.ReadFile(filepath.Join(out, "secret.txt")); string(b) != "hello secret" {
		t.Fatalf("secret was modified through the sandbox: %q", b)
	}

	// Nonexistent file under an allowed dir: Write allowed.
	r, _ := wr.Execute(ctx, map[string]interface{}{"path": filepath.Join(ws, "new", "f.md"), "content": "x"})
	if isDenied(r) {
		t.Fatalf("write of new file in workspace denied: %+v", r)
	}
	// Nonexistent file under a symlinked dir pointing outside: denied.
	r, _ = wr.Execute(ctx, map[string]interface{}{"path": filepath.Join(ws, "linkdir", "new", "f.md"), "content": "x"})
	if !isDenied(r) {
		t.Fatalf("write through escaping symlinked dir was not denied: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(out, "new")); err == nil {
		t.Fatal("write created a directory outside the sandbox")
	}

	// Glob on the escaping symlinked dir is denied, on the workspace allowed.
	gr, _ := glob.Execute(ctx, map[string]interface{}{"path": filepath.Join(ws, "linkdir")})
	if gr.Success {
		t.Fatal("Glob listed a symlinked dir outside the sandbox")
	}
	gr, _ = glob.Execute(ctx, map[string]interface{}{"path": ws})
	if !gr.Success {
		t.Fatalf("Glob on workspace failed: %+v", gr)
	}
}
