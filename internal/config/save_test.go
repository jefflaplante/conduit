package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveConfig_Permissions(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	cfg := Default()
	if err := cfg.Save(configPath); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}

	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("Expected file permissions 0600, got %04o", perm)
	}
}
