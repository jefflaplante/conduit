package auth

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"conduit/internal/config"
)

// conduit-31jg.48: `token info` reports the secret SOURCE and never the
// secret, and does not create a key file.
func TestInspectTokenStore(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CONDUIT_DATA_DIR", dataDir)
	t.Setenv(TokenSecretEnvVar, "")

	cfg := &config.Config{}
	cfg.Database.Path = "/srv/conduit/gateway.db"

	r, err := InspectTokenStore("cfg.json", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if r.SecretSource != "" || r.SecretExists || r.SecretPath == "" {
		t.Fatalf("unexpected report %+v", r)
	}
	if _, err := os.Stat(r.SecretPath); !os.IsNotExist(err) {
		t.Fatalf("info must not create the key file")
	}

	cfg.Auth.TokenSecret = "super-secret-value"
	r, err = InspectTokenStore("cfg.json", cfg)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	r.Write(&buf)
	out := buf.String()
	if strings.Contains(out, "super-secret-value") {
		t.Fatal("secret leaked in output")
	}
	if !strings.Contains(out, "config (auth.token_secret)") || !strings.Contains(out, "/srv/conduit/gateway.db") {
		t.Fatalf("unexpected output:\n%s", out)
	}

	// Persisted file present.
	cfg.Auth.TokenSecret = ""
	s, err := ResolveTokenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r, err = InspectTokenStore("cfg.json", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if r.SecretSource != SecretSourceFile || !r.SecretExists || filepath.Clean(r.SecretPath) != filepath.Clean(s.SecretPath) {
		t.Fatalf("unexpected report %+v", r)
	}
}
