package auth

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"conduit/internal/config"
)

// isolateSecretEnv points the data dir at a temp dir and clears
// CONDUIT_TOKEN_SECRET for the duration of the test. Returns the data dir.
func isolateSecretEnv(t *testing.T) string {
	t.Helper()
	dataDir := t.TempDir()
	t.Setenv("CONDUIT_DATA_DIR", dataDir)
	t.Setenv(TokenSecretEnvVar, "")
	os.Unsetenv(TokenSecretEnvVar)
	return dataDir
}

func TestResolveTokenStore_ConfigTakesPrecedenceOverEnv(t *testing.T) {
	isolateSecretEnv(t)
	t.Setenv(TokenSecretEnvVar, "env-secret")

	cfg := &config.Config{}
	cfg.Database.Path = "x.db"
	cfg.Auth.TokenSecret = "config-secret"

	s, err := ResolveTokenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if s.Secret != "config-secret" || s.SecretSource != SecretSourceConfig {
		t.Fatalf("got source %q, want config", s.SecretSource)
	}
	if s.DatabasePath != "x.db" {
		t.Fatalf("DatabasePath = %q", s.DatabasePath)
	}
}

func TestResolveTokenStore_EnvFallback(t *testing.T) {
	dataDir := isolateSecretEnv(t)
	t.Setenv(TokenSecretEnvVar, "env-secret")

	s, err := ResolveTokenStore(&config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Secret != "env-secret" || s.SecretSource != SecretSourceEnv {
		t.Fatalf("got source %q, want env", s.SecretSource)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "auth", PersistedSecretFilename)); !os.IsNotExist(err) {
		t.Fatalf("persisted secret must not be created when env is set (stat err=%v)", err)
	}
}

func TestResolveTokenStore_PersistedFileCreatedAndReused(t *testing.T) {
	dataDir := isolateSecretEnv(t)

	first, err := ResolveTokenStore(&config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if first.SecretSource != SecretSourceFile || !first.Generated {
		t.Fatalf("first resolve: source=%q generated=%v", first.SecretSource, first.Generated)
	}
	wantPath := filepath.Join(dataDir, "auth", PersistedSecretFilename)
	if first.SecretPath != wantPath {
		t.Fatalf("SecretPath = %q, want %q", first.SecretPath, wantPath)
	}
	info, err := os.Stat(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("persisted secret perms = %#o, want 0600", perm)
	}
	if raw, _ := hex.DecodeString(first.Secret); len(raw) != 32 {
		t.Fatalf("persisted secret should be 32 hex-encoded bytes, got %d bytes", len(raw))
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(filepath.Dir(wantPath))
	if len(entries) != 1 {
		t.Fatalf("expected only the key file in auth dir, found %d entries", len(entries))
	}
	// The secret must never appear in the log-safe description.
	if strings.Contains(first.Describe(), first.Secret) {
		t.Fatal("Describe() leaked the secret")
	}

	second, err := ResolveTokenStore(&config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if second.Generated || second.Secret != first.Secret {
		t.Fatalf("second resolve should reuse the persisted key (generated=%v)", second.Generated)
	}

	// Token survives a simulated restart: a fresh TokenStorage with the
	// re-resolved secret validates a token minted by the first instance.
	db := setupTestDB(t)
	defer db.Close()
	resp, err := NewTokenStorage(db, first.Secret).CreateToken(CreateTokenRequest{ClientName: "c"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewTokenStorage(db, second.Secret).ValidateToken(resp.Token); err != nil {
		t.Fatalf("token should survive restart with persisted key: %v", err)
	}
}

func TestResolveTokenStore_PersistedFileRejectsLoosePermsAndEmpty(t *testing.T) {
	dataDir := isolateSecretEnv(t)
	path := filepath.Join(dataDir, "auth", PersistedSecretFilename)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte("abcd\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveTokenStore(&config.Config{}); err == nil {
		t.Fatal("expected error for group/world-readable key file")
	}

	if err := os.WriteFile(path, []byte("  \n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveTokenStore(&config.Config{}); err == nil {
		t.Fatal("expected error for empty key file (must not silently regenerate)")
	}
}

// v1 (plain SHA256) tokens still validate and migrate to v2 when the secret
// comes from the persisted file.
func TestResolveTokenStore_V1FallbackWithPersistedSecret(t *testing.T) {
	isolateSecretEnv(t)
	s, err := ResolveTokenStore(&config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	db := setupTestDB(t)
	defer db.Close()

	raw := "conduit_legacy_v1_token"
	sum := sha256.Sum256([]byte(raw))
	if _, err := db.Exec(`INSERT INTO auth_tokens (token_id, client_name, hashed_token, hash_version, created_at, is_active, metadata)
		VALUES (?, ?, ?, ?, ?, 1, '{}')`, "v1-id", "legacy", hex.EncodeToString(sum[:]), HashVersionPlainSHA256, time.Now()); err != nil {
		t.Fatal(err)
	}

	storage := NewTokenStorage(db, s.Secret)
	if _, err := storage.ValidateToken(raw); err != nil {
		t.Fatalf("v1 token should validate: %v", err)
	}
	var version int
	if err := db.QueryRow(`SELECT hash_version FROM auth_tokens WHERE token_id = ?`, "v1-id").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != HashVersionHMACSHA256 {
		t.Fatalf("v1 token should be rehashed to v2, hash_version=%d", version)
	}
	// And it keeps validating after the migration (restart with same key).
	if _, err := NewTokenStorage(db, s.Secret).ValidateToken(raw); err != nil {
		t.Fatalf("migrated token should validate after restart: %v", err)
	}
}

func TestTokenCLI_MissingConfigFailsLoudly(t *testing.T) {
	isolateSecretEnv(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")

	cmd := TokenRootCmd(&CLIConfig{ConfigPath: cfgPath, Stderr: &bytes.Buffer{}})
	cmd.SetArgs([]string{"create", "--client-name", "x"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error when the config file does not exist")
	}
	if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
		t.Fatal("token CLI must not create a default config file")
	}
}
