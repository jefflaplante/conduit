package gateway

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"conduit/internal/auth"
	"conduit/internal/config"
	"conduit/internal/database"
)

// conduit-31jg.3 regression tests: a token minted through the `conduit token`
// CLI code path must validate through the server's auth path.

type tokenTestEnv struct {
	dir     string
	dataDir string
	cfgPath string
	dbPath  string
}

// newTokenTestEnv writes a gateway config into a temp dir. secret is written
// verbatim into auth.token_secret (omitted when empty).
func newTokenTestEnv(t *testing.T, secret string) *tokenTestEnv {
	t.Helper()
	env := &tokenTestEnv{dir: t.TempDir(), dataDir: t.TempDir()}
	env.cfgPath = filepath.Join(env.dir, "config.json")
	env.dbPath = filepath.Join(env.dir, "gateway.db")

	t.Setenv("CONDUIT_DATA_DIR", env.dataDir)
	t.Setenv(auth.TokenSecretEnvVar, "")
	os.Unsetenv(auth.TokenSecretEnvVar)

	raw := map[string]interface{}{
		"port":     18789,
		"database": map[string]string{"path": env.dbPath},
		"tools":    map[string]interface{}{"enabled_tools": []string{"read"}, "max_tool_chains": 5, "sandbox": map[string]string{"workspace_dir": env.dir}},
		"ai": map[string]interface{}{
			"default_provider": "anthropic",
			"providers": []map[string]string{
				{"name": "anthropic", "type": "anthropic", "api_key": "test-key", "model": "claude-test"},
			},
		},
	}
	if secret != "" {
		raw["auth"] = map[string]string{"token_secret": secret}
	}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.cfgPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	return env
}

// cliCreateToken runs `token create` through the real cobra command and
// returns the raw token printed to stdout.
func (e *tokenTestEnv) cliCreateToken(t *testing.T) (token, diag string) {
	t.Helper()
	var stderr bytes.Buffer
	cmd := auth.TokenRootCmd(&auth.CLIConfig{ConfigPath: e.cfgPath, Stderr: &stderr})
	cmd.SetArgs([]string{"create", "--client-name", "regression", "--role", "automation"})
	cmd.SetErr(io.Discard)
	cmd.SetOut(io.Discard)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	runErr := cmd.Execute()
	os.Stdout = origStdout
	w.Close()
	out, _ := io.ReadAll(r)
	r.Close()

	if runErr != nil {
		t.Fatalf("token create failed: %v (stderr: %s)", runErr, stderr.String())
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "Token: ") {
			return strings.TrimPrefix(line, "Token: "), stderr.String()
		}
	}
	t.Fatalf("no token in CLI output: %s", out)
	return "", ""
}

// serverValidate loads the config and validates the token through the
// server's NewAuthService path, simulating a fresh server process.
func (e *tokenTestEnv) serverValidate(t *testing.T, token string) error {
	t.Helper()
	cfg, err := config.Load(e.cfgPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	db, err := sql.Open("sqlite", auth.ResolveDatabasePath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.ConfigureDatabase(db); err != nil {
		t.Fatal(err)
	}
	svc, err := NewAuthService(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), db)
	if err != nil {
		t.Fatalf("NewAuthService: %v", err)
	}
	_, err = svc.AuthStorage.ValidateToken(token)
	return err
}

func (e *tokenTestEnv) persistedSecretPath() string {
	return filepath.Join(e.dataDir, "auth", auth.PersistedSecretFilename)
}

// Scenario 1 from the bead: token_secret set literally in config.json and
// CONDUIT_TOKEN_SECRET unset. On main the CLI ignored the config and hashed
// with a random ephemeral key, so this validation failed (401 forever).
func TestTokenCLI_SecretFromConfig_ValidatesOnServer(t *testing.T) {
	env := newTokenTestEnv(t, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")

	token, diag := env.cliCreateToken(t)
	if !strings.Contains(diag, "config (auth.token_secret)") {
		t.Errorf("CLI should report config as the secret source, got: %q", diag)
	}
	if strings.Contains(diag, "0123456789abcdef0123") {
		t.Fatal("CLI diagnostics leaked the secret")
	}
	if err := env.serverValidate(t, token); err != nil {
		t.Fatalf("CLI-created token rejected by server path: %v", err)
	}
	if _, err := os.Stat(env.persistedSecretPath()); !os.IsNotExist(err) {
		t.Fatal("no persisted key may be created when config supplies the secret")
	}
}

// ${ENV} expansion in config goes through config.Load for both paths.
func TestTokenCLI_SecretFromConfigEnvExpansion(t *testing.T) {
	env := newTokenTestEnv(t, "${CONDUIT_TEST_31JG3_SECRET}")
	t.Setenv("CONDUIT_TEST_31JG3_SECRET", "expanded-secret-value")

	token, diag := env.cliCreateToken(t)
	if !strings.Contains(diag, "config (auth.token_secret)") {
		t.Errorf("expected config source, got: %q", diag)
	}
	if err := env.serverValidate(t, token); err != nil {
		t.Fatalf("server rejected token: %v", err)
	}
}

// CONDUIT_TOKEN_SECRET is honored by both sides when config has no secret,
// and config still wins over it when both are present.
func TestTokenCLI_EnvPrecedenceMatchesServer(t *testing.T) {
	t.Run("env used when config empty", func(t *testing.T) {
		env := newTokenTestEnv(t, "")
		t.Setenv(auth.TokenSecretEnvVar, "env-secret-value")

		token, diag := env.cliCreateToken(t)
		if !strings.Contains(diag, auth.TokenSecretEnvVar) {
			t.Errorf("expected env source, got: %q", diag)
		}
		if err := env.serverValidate(t, token); err != nil {
			t.Fatalf("server rejected token: %v", err)
		}
	})
	t.Run("config beats env", func(t *testing.T) {
		env := newTokenTestEnv(t, "config-secret-value")
		t.Setenv(auth.TokenSecretEnvVar, "env-secret-value")

		token, diag := env.cliCreateToken(t)
		if !strings.Contains(diag, "config (auth.token_secret)") {
			t.Errorf("expected config source, got: %q", diag)
		}
		if err := env.serverValidate(t, token); err != nil {
			t.Fatalf("server rejected token: %v", err)
		}
		// Server keyed with env only must reject: proves config was used.
		os.Unsetenv(auth.TokenSecretEnvVar)
		db, _ := sql.Open("sqlite", env.dbPath)
		defer db.Close()
		if _, err := auth.NewTokenStorage(db, "env-secret-value").ValidateToken(token); err == nil {
			t.Fatal("token should have been hashed with the config secret, not the env secret")
		}
	})
}

// Scenario 2: nothing configured. The CLI and server share a persisted key
// and tokens survive a server restart.
func TestTokenCLI_NoSecret_PersistedKeySurvivesRestart(t *testing.T) {
	env := newTokenTestEnv(t, "")

	token, diag := env.cliCreateToken(t)
	if !strings.Contains(diag, "persisted file") {
		t.Errorf("expected persisted-file source, got: %q", diag)
	}
	info, err := os.Stat(env.persistedSecretPath())
	if err != nil {
		t.Fatalf("persisted key not created: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("persisted key perms = %#o, want 0600", perm)
	}
	before, _ := os.ReadFile(env.persistedSecretPath())

	// Two independent "server starts".
	for i := 0; i < 2; i++ {
		if err := env.serverValidate(t, token); err != nil {
			t.Fatalf("server start %d rejected CLI token: %v", i+1, err)
		}
	}
	after, _ := os.ReadFile(env.persistedSecretPath())
	if !bytes.Equal(before, after) {
		t.Fatal("persisted key must be reused, not regenerated")
	}
}
