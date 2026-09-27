package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"conduit/internal/config"
	"conduit/internal/datadir"
)

// conduit-31jg.3: the server and the `conduit token` CLI used to resolve the
// HMAC secret (and the database path) independently, so tokens minted by the
// CLI could be hashed with a different key than the server validates with.
// Everything that constructs a production TokenStorage must go through
// ResolveTokenStore so the two can never drift again.

const (
	// TokenSecretEnvVar is honored when auth.token_secret is not set in config.
	TokenSecretEnvVar = "CONDUIT_TOKEN_SECRET"

	// PersistedSecretFilename is the file (inside the data dir's auth/
	// subdirectory) holding the auto-generated HMAC key used when no secret is
	// configured anywhere.
	PersistedSecretFilename = "token_secret"

	persistedSecretBytes = 32
)

// SecretSource describes where the token HMAC secret came from. It is safe to
// log; the secret itself must never be logged.
type SecretSource string

const (
	SecretSourceConfig SecretSource = "config"         // auth.token_secret (after ${ENV} expansion)
	SecretSourceEnv    SecretSource = "env"            // CONDUIT_TOKEN_SECRET
	SecretSourceFile   SecretSource = "persisted-file" // {datadir}/auth/token_secret
)

// TokenStoreSettings is the resolved location and key material for the token
// store. Secret is sensitive: never log or print it.
type TokenStoreSettings struct {
	DatabasePath string
	Secret       string
	SecretSource SecretSource
	// SecretPath is the persisted key file when SecretSource is
	// SecretSourceFile (empty otherwise).
	SecretPath string
	// Generated is true when the persisted key file was created by this call.
	Generated bool
}

// Describe returns a log-safe description of where the secret came from.
func (s *TokenStoreSettings) Describe() string {
	switch s.SecretSource {
	case SecretSourceConfig:
		return "config (auth.token_secret)"
	case SecretSourceEnv:
		return "environment (" + TokenSecretEnvVar + ")"
	case SecretSourceFile:
		if s.Generated {
			return "persisted file " + s.SecretPath + " (newly generated)"
		}
		return "persisted file " + s.SecretPath
	default:
		return string(s.SecretSource)
	}
}

// ResolveDatabasePath returns the token/session database path the server uses.
// conduit-31jg.3: the CLI previously derived it from the config file name.
func ResolveDatabasePath(cfg *config.Config) string {
	return cfg.Database.Path
}

// ResolveTokenStore resolves the database path and HMAC secret exactly as the
// gateway server does. cfg must come from config.Load so ${ENV} placeholders
// are already expanded.
//
// Secret precedence:
//  1. auth.token_secret in config (including ${ENV_VAR} expansion)
//  2. CONDUIT_TOKEN_SECRET environment variable
//  3. {datadir}/auth/token_secret, generated (32 random bytes, hex, 0600) on
//     first use and reused afterwards
//
// It never falls back to an ephemeral in-memory key.
func ResolveTokenStore(cfg *config.Config) (*TokenStoreSettings, error) {
	if cfg == nil {
		return nil, errors.New("token store: nil config")
	}
	s := &TokenStoreSettings{DatabasePath: ResolveDatabasePath(cfg)}

	if v := strings.TrimSpace(cfg.Auth.TokenSecret); v != "" {
		s.Secret, s.SecretSource = v, SecretSourceConfig
		return s, nil
	}
	if v := strings.TrimSpace(os.Getenv(TokenSecretEnvVar)); v != "" {
		s.Secret, s.SecretSource = v, SecretSourceEnv
		return s, nil
	}

	dd, err := datadir.New(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("token secret: no auth.token_secret or %s set, and data directory could not be resolved: %w", TokenSecretEnvVar, err)
	}
	path := dd.AuthFilePath(PersistedSecretFilename)
	secret, generated, err := loadOrCreatePersistedSecret(path)
	if err != nil {
		return nil, fmt.Errorf("token secret: no auth.token_secret or %s set, and persisted key %s is unusable: %w", TokenSecretEnvVar, path, err)
	}
	s.Secret, s.SecretSource, s.SecretPath, s.Generated = secret, SecretSourceFile, path, generated
	return s, nil
}

// LoadOrCreateSecretFile reads a secret file (must be 0600-or-stricter and
// non-empty), creating it atomically with 32 random bytes (hex, 0600) when it
// does not exist. Used for the MCP bearer token (conduit-31jg.8).
func LoadOrCreateSecretFile(path string) (secret string, generated bool, err error) {
	return loadOrCreatePersistedSecret(path)
}

// loadOrCreatePersistedSecret reads the key at path, creating it atomically
// with 0600 permissions if it does not exist. Concurrent first-time callers
// (e.g. server and CLI) converge on a single key: the file is written to a
// temp name and hard-linked into place, which fails if another process won.
func loadOrCreatePersistedSecret(path string) (secret string, generated bool, err error) {
	if s, err := readPersistedSecret(path); err == nil {
		return s, false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", false, fmt.Errorf("create %s: %w", dir, err)
	}

	key := make([]byte, persistedSecretBytes)
	if _, err := rand.Read(key); err != nil {
		return "", false, fmt.Errorf("generate key: %w", err)
	}
	encoded := hex.EncodeToString(key)

	tmp, err := os.CreateTemp(dir, ".token_secret-*")
	if err != nil {
		return "", false, fmt.Errorf("create temp key file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return "", false, fmt.Errorf("chmod temp key file: %w", err)
	}
	if _, err := tmp.WriteString(encoded + "\n"); err != nil {
		tmp.Close()
		return "", false, fmt.Errorf("write temp key file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", false, fmt.Errorf("sync temp key file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", false, fmt.Errorf("close temp key file: %w", err)
	}

	if err := os.Link(tmpName, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			// Another process created it first; use theirs.
			s, rerr := readPersistedSecret(path)
			return s, false, rerr
		}
		return "", false, fmt.Errorf("install key file: %w", err)
	}
	return encoded, true, nil
}

func readPersistedSecret(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}
	if info.Mode().Perm()&0077 != 0 {
		return "", fmt.Errorf("%s has permissions %#o; must not be group/world accessible (chmod 600)", path, info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(data))
	if s == "" {
		// Refuse rather than regenerate: regenerating would silently
		// invalidate every token hashed with the previous key.
		return "", fmt.Errorf("%s is empty", path)
	}
	return s, nil
}
