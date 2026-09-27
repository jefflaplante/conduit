package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func mkdirs(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// conduit-31jg.70: an escaping link nested below the top level is reported.
func TestEscapingSymlinks_Nested(t *testing.T) {
	ws, out := setup(t)
	deep := filepath.Join(ws, "projects", "a", "b")
	mkdirs(t, deep)
	symlink(t, out, filepath.Join(deep, "escape"))
	symlink(t, filepath.Join(ws, "projects"), filepath.Join(deep, "inside")) // stays in: not reported

	got := New(ws, nil).EscapingSymlinks()
	if got[filepath.Join(deep, "escape")] != out {
		t.Fatalf("nested escape not found: %v", got)
	}
	if _, ok := got[filepath.Join(deep, "inside")]; ok {
		t.Fatalf("in-sandbox link reported: %v", got)
	}
}

// Links are never descended: a loop does not hang, and links inside an
// escaping link's target are not reported as workspace entries.
func TestEscapingSymlinks_DoesNotFollowLinks(t *testing.T) {
	ws, out := setup(t)
	symlink(t, ws, filepath.Join(ws, "loop"))
	symlink(t, out, filepath.Join(ws, "shared"))
	symlink(t, "/etc", filepath.Join(out, "etc"))

	got, truncated := New(ws, nil).ScanEscapingSymlinks(SymlinkScanOptions{})
	if truncated {
		t.Fatal("small tree should not be truncated")
	}
	if len(got) != 1 || got[filepath.Join(ws, "shared")] != out {
		t.Fatalf("want only shared -> %s, got %v", out, got)
	}
}

func TestEscapingSymlinks_DepthBound(t *testing.T) {
	ws, out := setup(t)
	deep := filepath.Join(ws, "l1", "l2", "l3", "l4")
	mkdirs(t, deep)
	symlink(t, out, filepath.Join(deep, "escape")) // entry at depth 4

	got, truncated := New(ws, nil).ScanEscapingSymlinks(SymlinkScanOptions{MaxDepth: 2})
	if !truncated {
		t.Fatal("expected truncated at depth 2")
	}
	if len(got) != 0 {
		t.Fatalf("link beyond depth bound reported: %v", got)
	}

	got, _ = New(ws, nil).ScanEscapingSymlinks(SymlinkScanOptions{MaxDepth: 4})
	if got[filepath.Join(deep, "escape")] != out {
		t.Fatalf("expected escape within depth 4, got %v", got)
	}
}

func TestEscapingSymlinks_EntryBound(t *testing.T) {
	ws, out := setup(t)
	many := filepath.Join(ws, "a-many")
	mkdirs(t, many)
	for i := 0; i < 200; i++ {
		if err := os.WriteFile(filepath.Join(many, fmt.Sprintf("f%03d", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mkdirs(t, filepath.Join(ws, "z"))
	symlink(t, out, filepath.Join(ws, "z", "escape"))

	got, truncated := New(ws, nil).ScanEscapingSymlinks(SymlinkScanOptions{MaxEntries: 50})
	if !truncated {
		t.Fatal("expected truncation by entry bound")
	}
	if len(got) != 0 {
		t.Fatalf("scan continued past the entry bound: %v", got)
	}
}

func TestEscapingSymlinks_SkipsGitDir(t *testing.T) {
	ws, out := setup(t)
	mkdirs(t, filepath.Join(ws, ".git", "objects"))
	symlink(t, out, filepath.Join(ws, ".git", "objects", "escape"))
	if got := New(ws, nil).EscapingSymlinks(); len(got) != 0 {
		t.Fatalf(".git should be skipped: %v", got)
	}
}
