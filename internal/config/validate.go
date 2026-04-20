package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// multiError accumulates multiple validation errors and renders them as a
// single user-facing message.
type multiError struct {
	errs []string
}

func (m *multiError) add(format string, args ...interface{}) {
	m.errs = append(m.errs, fmt.Sprintf(format, args...))
}

func (m *multiError) addIf(cond bool, format string, args ...interface{}) {
	if cond {
		m.add(format, args...)
	}
}

func (m *multiError) hasErrors() bool { return len(m.errs) > 0 }

func (m *multiError) toError() error {
	if !m.hasErrors() {
		return nil
	}
	if len(m.errs) == 1 {
		return fmt.Errorf("%s", m.errs[0])
	}
	return fmt.Errorf("configuration has %d errors:\n  - %s",
		len(m.errs), strings.Join(m.errs, "\n  - "))
}

// validatePort checks that the port is in the valid non-privileged range.
func validatePort(me *multiError, port int) {
	if port < 1024 || port > 65535 {
		me.add("port %d is out of range: must be between 1024 and 65535 (got %d; "+
			"use a value ≥ 1024 to avoid requiring root privileges)", port, port)
	}
}

// validateAICredentials checks that each Anthropic provider does not have both
// an API key AND OAuth token configured simultaneously (conflicting credentials).
//
// # Why we allow "neither set"
//
// The original spec (conduit-1jrv) called for "exactly one credential" to be
// present.  That was softened (conduit-l4w0) after examining the real template
// configs in configs/examples/:
//
//   config.example.json uses:  "api_key": "${ANTHROPIC_API_KEY}"
//
// config.Load calls os.ExpandEnv on every cfg:"env"-tagged field.  When the
// environment variable is absent, os.ExpandEnv("${ANTHROPIC_API_KEY}") returns
// "" — an empty string, not the literal placeholder text.  So by the time
// ValidateSemantic runs, a template config with an unset env var is
// indistinguishable from a config that has no api_key at all.
//
// Requiring exactly-one credential at parse time would therefore:
//   - Reject every templated startup where ANTHROPIC_API_KEY / ANTHROPIC_OAUTH_TOKEN
//     has not been exported yet (common in fresh installs and CI pipelines).
//   - Force users to pre-export credentials before the binary even starts,
//     conflicting with the secrets_file / environment-expansion workflow.
//
// The "neither set" case is intentionally deferred to the first AI call, where
// the Anthropic provider returns a clear "no credentials configured" error.
// This is the right place for that check: it has full context (which provider,
// which request) and fires only when credentials are actually needed.
//
// NOTE: isUnexpandedPlaceholder is kept for direct calls to ValidateSemantic()
// that bypass Load (e.g., unit tests constructing a Config in memory where the
// raw "${...}" string was never expanded).  In the Load path it is always dead.
func validateAICredentials(me *multiError, ai AIConfig) {
	for _, p := range ai.Providers {
		if p.Type != "anthropic" {
			continue
		}
		apiKeySet := p.APIKey != "" && !isUnexpandedPlaceholder(p.APIKey)
		oauthSet := p.Auth != nil &&
			p.Auth.OAuthToken != "" &&
			!isUnexpandedPlaceholder(p.Auth.OAuthToken)

		// Conflict: both real credentials configured at the same time.
		if apiKeySet && oauthSet {
			me.add("AI provider %q (type=anthropic): both api_key and auth.oauth_token are set; "+
				"use exactly one — remove api_key to use OAuth, or remove auth.oauth_token to use an API key",
				p.Name)
		}
		// Neither set: allowed — operator supplies credentials at runtime via
		// environment variables or a secrets file.  See the comment above for the
		// full rationale (conduit-l4w0).
	}
}

// isUnexpandedPlaceholder returns true when a value still looks like an
// unexpanded ${ENV_VAR} token.  This only occurs when ValidateSemantic is
// called directly (e.g., in unit tests) without going through config.Load,
// since Load expands all placeholders via os.ExpandEnv before validation.
func isUnexpandedPlaceholder(v string) bool {
	return strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}")
}

// validateChannels checks that each enabled channel has the required fields.
func validateChannels(me *multiError, channels []ChannelConfig) {
	for _, ch := range channels {
		if !ch.Enabled {
			continue
		}
		switch ch.Type {
		case "telegram":
			validateTelegramChannel(me, ch)
		}
	}
}

func validateTelegramChannel(me *multiError, ch ChannelConfig) {
	token, _ := ch.Config["bot_token"].(string)
	if token == "" || isUnexpandedPlaceholder(token) {
		me.add("channel %q (type=telegram): bot_token is required when the channel is enabled; "+
			"set config.bot_token to your Telegram Bot API token or the ${ENV_VAR} that holds it",
			ch.Name)
	}
}

// validateWorkspacePaths verifies that the parent directories of any configured
// workspace file paths exist and are readable. Non-existent files inside a
// valid directory are permitted (they may be created at runtime).
func validateWorkspacePaths(me *multiError, ws WorkspaceConfig) {
	for _, path := range ws.Files.Core {
		if path == "" {
			continue
		}
		checkParentDir(me, "workspace.files.core", path)
	}
}

// checkParentDir stats the parent directory of a file path and adds an error
// if it is absent or inaccessible.
func checkParentDir(me *multiError, fieldName, filePath string) {
	dir := filepath.Dir(filePath)
	if dir == "." || dir == "" {
		// Relative path with no directory component — skip; the CWD may vary.
		return
	}
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			me.add("%s path %q: parent directory %q does not exist; "+
				"create the directory or update the path", fieldName, filePath, dir)
		} else {
			me.add("%s path %q: cannot access parent directory %q: %v",
				fieldName, filePath, dir, err)
		}
	}
}

// validateRateLimiting checks that enabled rate-limiting tiers have positive
// window and request limits.
func validateRateLimiting(me *multiError, rl RateLimitingConfig) {
	if !rl.Enabled {
		return
	}
	if rl.Anonymous.WindowSeconds <= 0 {
		me.add("rateLimiting.anonymous.windowSeconds must be > 0 (got %d)", rl.Anonymous.WindowSeconds)
	}
	if rl.Anonymous.MaxRequests <= 0 {
		me.add("rateLimiting.anonymous.maxRequests must be > 0 (got %d)", rl.Anonymous.MaxRequests)
	}
	if rl.Authenticated.WindowSeconds <= 0 {
		me.add("rateLimiting.authenticated.windowSeconds must be > 0 (got %d)", rl.Authenticated.WindowSeconds)
	}
	if rl.Authenticated.MaxRequests <= 0 {
		me.add("rateLimiting.authenticated.maxRequests must be > 0 (got %d)", rl.Authenticated.MaxRequests)
	}
}

// validateToolsList checks that at least one tool is enabled.
func validateToolsList(me *multiError, tools ToolsConfig) {
	if len(tools.EnabledTools) == 0 {
		me.add("tools.enabled_tools is empty; at least one tool must be listed " +
			"(e.g. \"read\", \"write\", \"exec\") for the agent to function")
	}
}

// ValidateSemantic runs all semantic checks on the Config and returns a
// combined error listing every problem found.  It is called by Validate()
// after the existing structural/subsystem checks.
func (c *Config) ValidateSemantic() error {
	var me multiError

	validatePort(&me, c.Port)
	validateAICredentials(&me, c.AI)
	validateChannels(&me, c.Channels)
	validateWorkspacePaths(&me, c.Workspace)
	validateRateLimiting(&me, c.RateLimiting)
	validateToolsList(&me, c.Tools)

	return me.toError()
}
