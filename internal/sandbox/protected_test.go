package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A protected file is refused however it is named: directly, through a
// relative path, a symlink or a hard link. Other files are not.
func TestCheckWrite_ProtectedFile(t *testing.T) {
	t.Cleanup(ResetProtected)
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfg, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	Protect(cfg)

	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(cfg, link); err != nil {
		t.Fatal(err)
	}
	hard := filepath.Join(dir, "hard.json")
	if err := os.Link(cfg, hard); err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	rel, err := filepath.Rel(wd, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{cfg, rel, link, hard, filepath.Join(dir, "sub", "..", "config.json")} {
		if err := CheckWrite(p); !errors.Is(err, ErrProtected) {
			t.Errorf("CheckWrite(%s) = %v, want ErrProtected", p, err)
		}
	}
	other := filepath.Join(dir, "other.json")
	if err := os.WriteFile(other, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{other, filepath.Join(dir, "missing.json")} {
		if err := CheckWrite(p); err != nil {
			t.Errorf("CheckWrite(%s) = %v, want nil", p, err)
		}
	}
}

// A file protected before it exists is still refused once created.
func TestCheckWrite_ProtectedBeforeCreated(t *testing.T) {
	t.Cleanup(ResetProtected)
	p := filepath.Join(t.TempDir(), ".config.json.approved")
	Protect(p)
	if err := CheckWrite(p); !errors.Is(err, ErrProtected) {
		t.Fatalf("missing file: %v", err)
	}
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckWrite(p); !errors.Is(err, ErrProtected) {
		t.Fatalf("created file: %v", err)
	}
}
