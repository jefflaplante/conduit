package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

// setup returns (workspace, outside) real directories under a temp dir.
func setup(t *testing.T) (string, string) {
	t.Helper()
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
	if err := os.WriteFile(filepath.Join(out, "secret.txt"), []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	return ws, out
}

func TestResolve(t *testing.T) {
	ws, out := setup(t)
	sb := New(ws, nil)

	// conduit-31jg.6: symlinked dir and file pointing outside.
	if err := os.Symlink(out, filepath.Join(ws, "linkdir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(out, "secret.txt"), filepath.Join(ws, "linkfile")); err != nil {
		t.Fatal(err)
	}
	// Dangling link whose target is outside: writing through it would escape.
	if err := os.Symlink(filepath.Join(out, "new.txt"), filepath.Join(ws, "dangling")); err != nil {
		t.Fatal(err)
	}
	// In-sandbox symlink stays allowed.
	if err := os.MkdirAll(filepath.Join(ws, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(ws, "real"), filepath.Join(ws, "inner")); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		path string
		want bool
	}{
		{"root itself", ws, true},
		{"existing child dir", filepath.Join(ws, "real"), true},
		{"dotdot-prefixed name is allowed", filepath.Join(ws, "..notes"), true},
		{"dotdot-prefixed nested", filepath.Join(ws, "..notes", "a.md"), true},
		{"nonexistent file under allowed dir", filepath.Join(ws, "new", "deep", "f.txt"), true},
		{"in-sandbox symlink", filepath.Join(ws, "inner", "x.txt"), true},
		{"lexical traversal", filepath.Join(ws, "..", "outside", "secret.txt"), false},
		{"raw traversal string", ws + "/../outside/secret.txt", false},
		{"symlinked dir escape", filepath.Join(ws, "linkdir", "secret.txt"), false},
		{"symlinked dir itself", filepath.Join(ws, "linkdir"), false},
		{"symlinked file escape", filepath.Join(ws, "linkfile"), false},
		{"nonexistent under symlinked dir", filepath.Join(ws, "linkdir", "new", "f.txt"), false},
		{"dangling symlink", filepath.Join(ws, "dangling"), false},
		{"prefix overlap sibling", ws + "-private/x", false},
		{"absolute elsewhere", "/etc/passwd", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sb.Allowed(tc.path)
			if got != tc.want {
				_, err := sb.Resolve(tc.path)
				t.Fatalf("Allowed(%q) = %v, want %v (err=%v)", tc.path, got, tc.want, err)
			}
		})
	}
}

func TestResolveReturnsCanonicalPath(t *testing.T) {
	ws, _ := setup(t)
	if err := os.MkdirAll(filepath.Join(ws, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(ws, "real"), filepath.Join(ws, "inner")); err != nil {
		t.Fatal(err)
	}
	got, err := New(ws, nil).Resolve(filepath.Join(ws, "inner", "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(ws, "real", "a.txt"); got != want {
		t.Fatalf("Resolve = %q, want %q", got, want)
	}
}

func TestSymlinkedRootIsHonored(t *testing.T) {
	// A root that is itself a symlink (e.g. /tmp on macOS) must still work.
	ws, _ := setup(t)
	alias := filepath.Join(filepath.Dir(ws), "alias")
	if err := os.Symlink(ws, alias); err != nil {
		t.Fatal(err)
	}
	sb := New("", []string{alias})
	if !sb.Allowed(filepath.Join(ws, "f")) || !sb.Allowed(filepath.Join(alias, "f")) {
		t.Fatal("paths under a symlinked root should be allowed via either spelling")
	}
}

func TestNoRootsDeniesAll(t *testing.T) {
	sb := New("", nil)
	if sb.Allowed("/tmp") {
		t.Fatal("empty sandbox must deny")
	}
}

func TestWorkspaceDirIsRoot(t *testing.T) {
	ws, out := setup(t)
	sb := New(ws, []string{out})
	if !sb.Allowed(filepath.Join(ws, "a")) || !sb.Allowed(filepath.Join(out, "a")) {
		t.Fatal("both workspace_dir and allowed_paths must be roots")
	}
}

func TestRelativeRoot(t *testing.T) {
	ws, _ := setup(t)
	t.Chdir(filepath.Dir(ws))
	sb := New("", []string{"./workspace"})
	if !sb.Allowed(filepath.Join(ws, "x.md")) {
		t.Fatal("relative allowed path should be resolved against cwd")
	}
}

func TestEscapingSymlinks(t *testing.T) {
	ws, out := setup(t)
	if err := os.Symlink(out, filepath.Join(ws, "shared")); err != nil {
		t.Fatal(err)
	}
	got := New(ws, nil).EscapingSymlinks()
	if got[filepath.Join(ws, "shared")] != out {
		t.Fatalf("expected shared -> %s, got %v", out, got)
	}
}

func TestWithin(t *testing.T) {
	cases := []struct {
		root, target string
		want         bool
	}{
		{"/a", "/a", true},
		{"/a", "/a/b", true},
		{"/a", "/a/..b", true},
		{"/a", "/ab", false},
		{"/a", "/", false},
		{"/a/b", "/a", false},
	}
	for _, c := range cases {
		if got := Within(c.root, c.target); got != c.want {
			t.Errorf("Within(%q,%q)=%v want %v", c.root, c.target, got, c.want)
		}
	}
}
