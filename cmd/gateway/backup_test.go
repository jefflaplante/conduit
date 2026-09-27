package main

import (
	"os"
	"path/filepath"
	"testing"
)

// conduit-31jg.73: the restore liveness check must probe the configured
// gateway, not just the default port.
func TestRestoreGatewayAddr(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "live.json")
	cli := filepath.Join(dir, "cli.json")
	broken := filepath.Join(dir, "broken.json")
	// A config config.Load would reject (unset credentials etc.) still
	// yields its port.
	if err := os.WriteFile(live, []byte(`{"port": 19001, "ai": {"providers": [{"api_key": "${UNSET_KEY}"}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cli, []byte(`{"port": 19002}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(broken, []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		override int
		paths    []string
		want     string
	}{
		{"override wins", 20000, []string{live, cli}, "127.0.0.1:20000"},
		{"restore destination config first", 0, []string{live, cli}, "127.0.0.1:19001"},
		{"falls through to --config", 0, []string{"", cli}, "127.0.0.1:19002"},
		{"unreadable configs skipped", 0, []string{filepath.Join(dir, "missing.json"), broken, cli}, "127.0.0.1:19002"},
		{"default", 0, []string{"", broken}, "127.0.0.1:18789"},
	}
	for _, c := range cases {
		if got := restoreGatewayAddr(c.override, c.paths...); got != c.want {
			t.Errorf("%s: restoreGatewayAddr = %q, want %q", c.name, got, c.want)
		}
	}
}

// The restore command accepts --pidfile (inherited global flag) and
// --gateway-port.
func TestBackupRestoreFlags(t *testing.T) {
	if backupRestoreCmd.Flags().Lookup("gateway-port") == nil {
		t.Error("restore is missing --gateway-port")
	}
	if backupRestoreCmd.InheritedFlags().Lookup("pidfile") == nil {
		t.Error("restore does not inherit --pidfile")
	}
}
