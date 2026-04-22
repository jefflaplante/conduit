package main

import (
	"os"
	"path/filepath"
	"testing"

	"conduit/internal/config"

	"github.com/spf13/cobra"
)

func TestIsUnderSystemd(t *testing.T) {
	orig, had := os.LookupEnv("INVOCATION_ID")
	t.Cleanup(func() {
		if had {
			os.Setenv("INVOCATION_ID", orig)
		} else {
			os.Unsetenv("INVOCATION_ID")
		}
	})

	tests := []struct {
		name         string
		invocationID string
		unsetVar     bool
		// ppidIs1 can't be reliably faked in-test, so we only assert
		// the INVOCATION_ID branch deterministically.
		wantWhenPPIDNot1 bool
	}{
		{name: "set", invocationID: "abc123", wantWhenPPIDNot1: true},
		{name: "empty", invocationID: "", unsetVar: false, wantWhenPPIDNot1: false},
		{name: "unset", unsetVar: true, wantWhenPPIDNot1: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.unsetVar {
				os.Unsetenv("INVOCATION_ID")
			} else {
				os.Setenv("INVOCATION_ID", tt.invocationID)
			}

			got := isUnderSystemd()
			// If PPID happens to be 1 (unlikely in tests), isUnderSystemd
			// will return true regardless. Only assert the negative case
			// when PPID != 1.
			if os.Getppid() != 1 {
				if got != tt.wantWhenPPIDNot1 {
					t.Errorf("isUnderSystemd() = %v, want %v (INVOCATION_ID=%q, PPID=%d)",
						got, tt.wantWhenPPIDNot1, os.Getenv("INVOCATION_ID"), os.Getppid())
				}
			}
		})
	}
}

func TestIsAncestorPID(t *testing.T) {
	// Our own PID is trivially an ancestor of ourselves.
	if !isAncestorPID(os.Getpid()) {
		t.Errorf("isAncestorPID(self) = false, want true")
	}

	// Our parent is an ancestor.
	if ppid := os.Getppid(); ppid > 1 {
		if !isAncestorPID(ppid) {
			t.Errorf("isAncestorPID(parent=%d) = false, want true", ppid)
		}
	}

	// A PID that cannot be an ancestor (0, 1 when we're not init, and a
	// clearly-out-of-tree PID) must return false.
	if isAncestorPID(0) {
		t.Errorf("isAncestorPID(0) = true, want false")
	}

	// PID 1 is an ancestor only when we're a direct/indirect child of init;
	// that's unusual in the `go test` runner (which is itself launched by
	// the shell/test harness), so don't assert either way.

	// A very large PID that almost certainly doesn't exist should return false.
	if isAncestorPID(1<<22 - 1) {
		t.Errorf("isAncestorPID(huge) = true, want false")
	}
}

// writeTestConfig writes a config file with the given port, using Default()
// so it passes Load-time validation.
func writeTestConfig(t *testing.T, port int) string {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	cfg := config.Default()
	cfg.Port = port
	cfg.Database.Path = filepath.Join(dir, "test.db")
	if err := cfg.Save(cfgPath); err != nil {
		t.Fatal(err)
	}
	return cfgPath
}

func TestResolveStatusPort(t *testing.T) {
	origCfgFile := cfgFile
	t.Cleanup(func() { cfgFile = origCfgFile })

	newCmd := func() *cobra.Command {
		c := &cobra.Command{}
		c.Flags().Int("port", defaultStatusPort, "")
		return c
	}

	t.Run("explicit flag wins when config empty", func(t *testing.T) {
		cfgFile = ""
		cmd := newCmd()
		if err := cmd.Flags().Set("port", "12345"); err != nil {
			t.Fatal(err)
		}
		if got := resolveStatusPort(cmd); got != 12345 {
			t.Errorf("resolveStatusPort() = %d, want 12345", got)
		}
	})

	t.Run("reads port from config when flag unset", func(t *testing.T) {
		cfgFile = writeTestConfig(t, 18890)
		cmd := newCmd()
		if got := resolveStatusPort(cmd); got != 18890 {
			t.Errorf("resolveStatusPort() = %d, want 18890 (from config)", got)
		}
	})

	t.Run("falls back to default when config unloadable", func(t *testing.T) {
		dir := t.TempDir()
		bad := filepath.Join(dir, "bad.json")
		if err := os.WriteFile(bad, []byte("{not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		cfgFile = bad
		cmd := newCmd()
		if got := resolveStatusPort(cmd); got != defaultStatusPort {
			t.Errorf("resolveStatusPort() = %d, want %d", got, defaultStatusPort)
		}
	})

	t.Run("explicit flag overrides config", func(t *testing.T) {
		cfgFile = writeTestConfig(t, 18890)
		cmd := newCmd()
		if err := cmd.Flags().Set("port", "9999"); err != nil {
			t.Fatal(err)
		}
		if got := resolveStatusPort(cmd); got != 9999 {
			t.Errorf("resolveStatusPort() = %d, want 9999 (flag over config)", got)
		}
	})
}
