package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// minimalValidConfig returns a Config that passes all ValidateSemantic checks.
// Tests should mutate a copy of this to exercise individual rejection paths.
func minimalValidConfig() *Config {
	return &Config{
		Port: 18789,
		AI: AIConfig{
			Providers: []ProviderConfig{
				{
					Name:   "anthropic",
					Type:   "anthropic",
					APIKey: "sk-test-key",
				},
			},
		},
		Tools: ToolsConfig{
			EnabledTools:  []string{"read"},
			MaxToolChains: 25,
		},
	}
}

// TestValidateSemantic_Success verifies that a fully valid config produces no error.
func TestValidateSemantic_Success(t *testing.T) {
	cfg := minimalValidConfig()
	if err := cfg.ValidateSemantic(); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Port
// ---------------------------------------------------------------------------

func TestValidateSemantic_PortZero(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.Port = 0
	assertSemanticError(t, cfg, "out of range")
}

func TestValidateSemantic_PortPrivileged(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.Port = 80
	assertSemanticError(t, cfg, "out of range")
}

func TestValidateSemantic_PortTooHigh(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.Port = 70000
	assertSemanticError(t, cfg, "out of range")
}

func TestValidateSemantic_PortBoundaryLow(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.Port = 1024
	if err := cfg.ValidateSemantic(); err != nil {
		t.Errorf("port 1024 should be valid, got: %v", err)
	}
}

func TestValidateSemantic_PortBoundaryHigh(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.Port = 65535
	if err := cfg.ValidateSemantic(); err != nil {
		t.Errorf("port 65535 should be valid, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// AI credentials
// ---------------------------------------------------------------------------

func TestValidateSemantic_AIBothCredentials(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.AI.Providers[0].Auth = &AuthConfig{
		Type:       "oauth",
		OAuthToken: "oauth-token-abc",
	}
	// api_key is already set — both are now present
	assertSemanticError(t, cfg, "both api_key and auth.oauth_token are set")
}

func TestValidateSemantic_AINoCredentials(t *testing.T) {
	// Neither api_key nor oauth_token set — treated as template / env-var mode.
	// This must NOT produce a validation error; credentials come from env at runtime.
	cfg := minimalValidConfig()
	cfg.AI.Providers[0].APIKey = ""
	cfg.AI.Providers[0].Auth = nil
	if err := cfg.ValidateSemantic(); err != nil {
		t.Errorf("provider with no credentials (template mode) should be valid, got: %v", err)
	}
}

func TestValidateSemantic_AIUnexpandedAPIKey(t *testing.T) {
	// An unexpanded placeholder (post env-expansion it becomes "") is template mode.
	cfg := minimalValidConfig()
	cfg.AI.Providers[0].APIKey = "${ANTHROPIC_API_KEY}"
	if err := cfg.ValidateSemantic(); err != nil {
		t.Errorf("unexpanded placeholder API key should be treated as template mode, got: %v", err)
	}
}

func TestValidateSemantic_AITrulyEmpty(t *testing.T) {
	// Both api_key and oauth_token empty (no credentials configured) — this is
	// valid at config-parse time; the operator provides them via env variables.
	cfg := minimalValidConfig()
	cfg.AI.Providers[0].APIKey = ""
	cfg.AI.Providers[0].Auth = nil
	if err := cfg.ValidateSemantic(); err != nil {
		t.Errorf("provider with empty credentials should be valid (template mode), got: %v", err)
	}
}

func TestValidateSemantic_AIOAuthOnly(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.AI.Providers[0].APIKey = ""
	cfg.AI.Providers[0].Auth = &AuthConfig{
		Type:       "oauth",
		OAuthToken: "tok_live_abc123",
	}
	if err := cfg.ValidateSemantic(); err != nil {
		t.Errorf("OAuth-only config should be valid, got: %v", err)
	}
}

func TestValidateSemantic_AIAPIKeyOnly(t *testing.T) {
	cfg := minimalValidConfig()
	// No Auth set — API key only (default fixture already satisfies this)
	if err := cfg.ValidateSemantic(); err != nil {
		t.Errorf("API-key-only config should be valid, got: %v", err)
	}
}

// TestValidateSemantic_AITemplateMode_ExpandedToEmpty documents the "neither
// set" acceptance path that is the core of conduit-l4w0.
//
// config.Load uses os.ExpandEnv to expand ${ENV_VAR} placeholders.  When the
// referenced variable is absent from the environment, os.ExpandEnv returns "".
// So a template config like:
//
//	"api_key": "${ANTHROPIC_API_KEY}"
//
// becomes api_key="" after Load — indistinguishable from "no key configured".
// ValidateSemantic must NOT reject this case; the operator is expected to
// supply credentials at runtime (env export, secrets_file, etc.).  The first
// failed AI call will surface a clear "no credentials" error at the right layer.
func TestValidateSemantic_AITemplateMode_ExpandedToEmpty(t *testing.T) {
	// Simulate what config.Load sees after os.ExpandEnv with an unset var:
	// the placeholder collapses to "" — neither api_key nor oauth_token is set.
	cfg := minimalValidConfig()
	cfg.AI.Providers[0].APIKey = "" // post-expansion of "${ANTHROPIC_API_KEY}" when unset
	cfg.AI.Providers[0].Auth = nil
	if err := cfg.ValidateSemantic(); err != nil {
		t.Errorf("template-mode provider (both credentials empty after env expansion) "+
			"must be valid — credentials are supplied at runtime; got: %v", err)
	}
}

// TestValidateSemantic_AITemplateMode_BothPlaceholdersRaw verifies that even
// when both api_key AND oauth_token are raw placeholder strings (as they appear
// in the Default() config before any env expansion), the validator does not
// flag a "both set" conflict, because isUnexpandedPlaceholder treats them as
// "not configured".
func TestValidateSemantic_AITemplateMode_BothPlaceholdersRaw(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.AI.Providers[0].APIKey = "${ANTHROPIC_API_KEY}"
	cfg.AI.Providers[0].Auth = &AuthConfig{
		Type:       "oauth",
		OAuthToken: "${ANTHROPIC_OAUTH_TOKEN}",
	}
	if err := cfg.ValidateSemantic(); err != nil {
		t.Errorf("both-unexpanded-placeholder config should not trigger 'both set' error; got: %v", err)
	}
}

func TestValidateSemantic_NonAnthropicProviderNoKey(t *testing.T) {
	// Non-anthropic providers are not subject to the credential check.
	cfg := minimalValidConfig()
	cfg.AI.Providers = append(cfg.AI.Providers, ProviderConfig{
		Name:  "openai",
		Type:  "openai",
		Model: "gpt-4",
		// no api_key — should not trigger a validation error
	})
	if err := cfg.ValidateSemantic(); err != nil {
		t.Errorf("non-anthropic provider without key should be allowed, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Channels
// ---------------------------------------------------------------------------

func TestValidateSemantic_TelegramMissingToken(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.Channels = []ChannelConfig{
		{
			Name:    "telegram",
			Type:    "telegram",
			Enabled: true,
			Config:  map[string]interface{}{},
		},
	}
	assertSemanticError(t, cfg, "bot_token is required")
}

func TestValidateSemantic_TelegramUnexpandedToken(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.Channels = []ChannelConfig{
		{
			Name:    "telegram",
			Type:    "telegram",
			Enabled: true,
			Config: map[string]interface{}{
				"bot_token": "${TELEGRAM_BOT_TOKEN}",
			},
		},
	}
	assertSemanticError(t, cfg, "bot_token is required")
}

func TestValidateSemantic_TelegramDisabledNoToken(t *testing.T) {
	// Disabled channels are not validated.
	cfg := minimalValidConfig()
	cfg.Channels = []ChannelConfig{
		{
			Name:    "telegram",
			Type:    "telegram",
			Enabled: false,
			Config:  map[string]interface{}{},
		},
	}
	if err := cfg.ValidateSemantic(); err != nil {
		t.Errorf("disabled telegram channel without token should be valid, got: %v", err)
	}
}

func TestValidateSemantic_TelegramValidToken(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.Channels = []ChannelConfig{
		{
			Name:    "telegram",
			Type:    "telegram",
			Enabled: true,
			Config: map[string]interface{}{
				"bot_token": "1234567890:ABCdefGHIjkl",
			},
		},
	}
	if err := cfg.ValidateSemantic(); err != nil {
		t.Errorf("telegram with valid token should be valid, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Workspace paths
// ---------------------------------------------------------------------------

func TestValidateSemantic_WorkspacePathNonExistentDir(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.Workspace = WorkspaceConfig{
		Files: WorkspaceFilesConfig{
			Core: []string{"/nonexistent/dir/file.md"},
		},
	}
	assertSemanticError(t, cfg, "does not exist")
}

func TestValidateSemantic_WorkspacePathValidDirMissingFile(t *testing.T) {
	// Parent directory exists — the file itself may not.
	tmpDir := t.TempDir()
	cfg := minimalValidConfig()
	cfg.Workspace = WorkspaceConfig{
		Files: WorkspaceFilesConfig{
			Core: []string{filepath.Join(tmpDir, "SOUL.md")},
		},
	}
	if err := cfg.ValidateSemantic(); err != nil {
		t.Errorf("existing parent dir with missing file should be valid, got: %v", err)
	}
}

func TestValidateSemantic_WorkspacePathAbsoluteExistingDir(t *testing.T) {
	tmpDir := t.TempDir()
	existingFile := filepath.Join(tmpDir, "memory.md")
	if err := os.WriteFile(existingFile, []byte("# memory"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := minimalValidConfig()
	cfg.Workspace = WorkspaceConfig{
		Files: WorkspaceFilesConfig{
			Core: []string{existingFile},
		},
	}
	if err := cfg.ValidateSemantic(); err != nil {
		t.Errorf("existing workspace file should be valid, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Rate limiting
// ---------------------------------------------------------------------------

func TestValidateSemantic_RateLimitZeroWindow(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.RateLimiting = RateLimitingConfig{
		Enabled: true,
		Anonymous: RateLimitTierConfig{
			WindowSeconds: 0,
			MaxRequests:   100,
		},
		Authenticated: RateLimitTierConfig{
			WindowSeconds: 60,
			MaxRequests:   1000,
		},
	}
	assertSemanticError(t, cfg, "windowSeconds must be > 0")
}

func TestValidateSemantic_RateLimitZeroMaxRequests(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.RateLimiting = RateLimitingConfig{
		Enabled: true,
		Anonymous: RateLimitTierConfig{
			WindowSeconds: 60,
			MaxRequests:   0,
		},
		Authenticated: RateLimitTierConfig{
			WindowSeconds: 60,
			MaxRequests:   1000,
		},
	}
	assertSemanticError(t, cfg, "maxRequests must be > 0")
}

func TestValidateSemantic_RateLimitNegative(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.RateLimiting = RateLimitingConfig{
		Enabled: true,
		Anonymous: RateLimitTierConfig{
			WindowSeconds: 60,
			MaxRequests:   100,
		},
		Authenticated: RateLimitTierConfig{
			WindowSeconds: -1,
			MaxRequests:   1000,
		},
	}
	assertSemanticError(t, cfg, "windowSeconds must be > 0")
}

func TestValidateSemantic_RateLimitDisabledZeroValues(t *testing.T) {
	// Disabled rate limiting should not trigger validation errors.
	cfg := minimalValidConfig()
	cfg.RateLimiting = RateLimitingConfig{
		Enabled:       false,
		Anonymous:     RateLimitTierConfig{WindowSeconds: 0, MaxRequests: 0},
		Authenticated: RateLimitTierConfig{WindowSeconds: 0, MaxRequests: 0},
	}
	if err := cfg.ValidateSemantic(); err != nil {
		t.Errorf("disabled rate limiting with zero values should be valid, got: %v", err)
	}
}

func TestValidateSemantic_RateLimitValidConfig(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.RateLimiting = RateLimitingConfig{
		Enabled: true,
		Anonymous: RateLimitTierConfig{
			WindowSeconds: 60,
			MaxRequests:   100,
		},
		Authenticated: RateLimitTierConfig{
			WindowSeconds: 60,
			MaxRequests:   1000,
		},
	}
	if err := cfg.ValidateSemantic(); err != nil {
		t.Errorf("valid rate-limiting config should pass, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tools list
// ---------------------------------------------------------------------------

func TestValidateSemantic_NoEnabledTools(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.Tools.EnabledTools = []string{}
	assertSemanticError(t, cfg, "enabled_tools is empty")
}

func TestValidateSemantic_NilEnabledTools(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.Tools.EnabledTools = nil
	assertSemanticError(t, cfg, "enabled_tools is empty")
}

func TestValidateSemantic_OneEnabledTool(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.Tools.EnabledTools = []string{"read"}
	if err := cfg.ValidateSemantic(); err != nil {
		t.Errorf("single enabled tool should be valid, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Multi-error aggregation
// ---------------------------------------------------------------------------

func TestValidateSemantic_MultipleErrors(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.Port = 80                 // bad port
	cfg.Tools.EnabledTools = nil  // no tools

	err := cfg.ValidateSemantic()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "2 errors") {
		t.Errorf("expected '2 errors' in message, got: %s", msg)
	}
	if !strings.Contains(msg, "out of range") {
		t.Errorf("expected port error in message, got: %s", msg)
	}
	if !strings.Contains(msg, "enabled_tools is empty") {
		t.Errorf("expected tools error in message, got: %s", msg)
	}
}

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

// assertSemanticError calls ValidateSemantic and fails if there is no error or
// if the error message does not contain the expected substring.
func assertSemanticError(t *testing.T, cfg *Config, wantSubstr string) {
	t.Helper()
	err := cfg.ValidateSemantic()
	if err == nil {
		t.Fatalf("expected a validation error containing %q, got nil", wantSubstr)
	}
	if !strings.Contains(err.Error(), wantSubstr) {
		t.Errorf("expected error containing %q, got: %v", wantSubstr, err)
	}
}
