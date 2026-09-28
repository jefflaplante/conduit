package config

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"conduit/internal/reflection"
	"conduit/internal/skills"
)

// Config represents the gateway configuration
type Config struct {
	Port           int                          `json:"port"`
	Timezone       string                       `json:"timezone,omitempty"`
	DataDir        string                       `json:"data_dir,omitempty" cfg:"env,path"`
	SecretsFile    string                       `json:"secrets_file,omitempty" cfg:"env,path"`
	AllowedOrigins []string                     `json:"allowed_origins,omitempty"` // WebSocket allowed origins (empty = same-origin + localhost only)
	WebSocket      WebSocketConfig              `json:"websocket,omitempty"`
	Database       DatabaseConfig               `json:"database"`
	Search         SearchDatabaseConfig         `json:"search,omitempty"`
	AI             AIConfig                     `json:"ai"`
	Agent          AgentConfig                  `json:"agent"`
	Workspace      WorkspaceConfig              `json:"workspace,omitempty"`
	Skills         skills.SkillsConfig          `json:"skills,omitempty"`
	Tools          ToolsConfig                  `json:"tools"`
	Channels       []ChannelConfig              `json:"channels"`
	Debug          DebugConfig                  `json:"debug,omitempty"`
	RateLimiting   RateLimitingConfig           `json:"rateLimiting,omitempty"`
	Diagnostics    DiagnosticsConfig            `json:"diagnostics,omitempty"`
	Heartbeat      HeartbeatConfig              `json:"heartbeat,omitempty"`
	AgentHeartbeat AgentHeartbeatConfig         `json:"agent_heartbeat,omitempty"`
	SSH            SSHServerConfig              `json:"ssh,omitempty"`
	TUI            TUIConfig                    `json:"tui,omitempty"`
	Vector         VectorConfig                 `json:"vector,omitempty"`
	RemoteSSH      RemoteSSHConfig              `json:"remote_ssh,omitempty"`
	MQTT           MQTTConfig                   `json:"mqtt,omitempty"`
	Kubernetes     KubernetesConfig             `json:"kubernetes,omitempty"`
	PagerDuty      PagerDutyConfig              `json:"pagerduty,omitempty"`
	Datadog        DatadogConfig                `json:"datadog,omitempty"`
	Auth           AuthTokenConfig              `json:"auth,omitempty"`
	Logging        LoggingConfig                `json:"logging,omitempty"`
	Brain          BrainConfig                  `json:"brain,omitempty"`
	Reflection     *reflection.ReflectionConfig `json:"reflection,omitempty"`
	STT            STTConfig                    `json:"stt,omitempty"`
	MCP            MCPConfig                    `json:"mcp,omitempty"` // conduit-31jg.8
	// RestartResume selects what happens after a restart to interactive
	// turns the shutdown drain cut off: "notice" (default), "auto" or "off".
	// See RestartResumeMode. conduit-31jg.88
	RestartResume string `json:"restart_resume,omitempty"`
}

// restart_resume values (conduit-31jg.88).
const (
	// RestartResumeNotice tells the owner on the turn's channel and in the
	// transcript that the request was cut off (default).
	RestartResumeNotice = "notice"
	// RestartResumeAuto also wakes the session to continue the turn.
	RestartResumeAuto = "auto"
	// RestartResumeOff disables per-turn notices.
	RestartResumeOff = "off"
)

// RestartResumeMode returns the normalized restart_resume value; empty or
// unknown values mean RestartResumeNotice.
func (c *Config) RestartResumeMode() string {
	if c == nil {
		return RestartResumeNotice
	}
	switch v := strings.ToLower(strings.TrimSpace(c.RestartResume)); v {
	case RestartResumeAuto, RestartResumeOff:
		return v
	}
	return RestartResumeNotice
}

// DatabaseConfig contains database settings
type DatabaseConfig struct {
	Path string `json:"path" cfg:"path"`
}

// SearchDatabaseConfig contains settings for the dedicated search database.
// The search database (search.db) holds FTS5 indices and optional vector storage,
// separated from the main gateway.db for independent index management.
type SearchDatabaseConfig struct {
	// Path to the search database file. If empty, derives from gateway.db path
	// (e.g., gateway.db → gateway.search.db)
	Path string `json:"path,omitempty"`

	// BeadsDir is the directory containing .beads/issues.jsonl for beads indexing.
	// Defaults to ".beads" relative to the workspace or current directory.
	BeadsDir string `json:"beads_dir,omitempty"`

	// Enabled controls whether the search database is used. Defaults to true.
	// When disabled, search falls back to grep-based search.
	Enabled *bool `json:"enabled,omitempty"`
}

// IsEnabled returns whether the search database is enabled.
// Defaults to true if not explicitly set.
func (s *SearchDatabaseConfig) IsEnabled() bool {
	if s.Enabled == nil {
		return true
	}
	return *s.Enabled
}

// SkillsConfig is imported from skills package

// Default returns a default configuration
func Default() *Config {
	return &Config{
		Port:      18789,
		WebSocket: DefaultWebSocketConfig(),
		Database: DatabaseConfig{
			Path: "gateway.db",
		},
		AI: AIConfig{
			DefaultProvider: "anthropic",
			Providers: []ProviderConfig{
				{
					Name:   "anthropic",
					Type:   "anthropic",
					APIKey: "${ANTHROPIC_API_KEY}", // Fallback
					Model:  "claude-3-5-sonnet-20241022",
					Auth: &AuthConfig{
						Type:       "oauth",
						OAuthToken: "${ANTHROPIC_OAUTH_TOKEN}",
					},
				},
				{
					Name:   "openai",
					Type:   "openai",
					APIKey: "${OPENAI_API_KEY}",
					Model:  "gpt-4",
				},
			},
			ModelAliases: DefaultModelAliases(),
		},
		Agent: AgentConfig{
			Name:        "Conduit",
			Personality: "conduit",
			Identity: AgentIdentity{
				OAuthIdentity:  "You are Claude Code, Anthropic's official CLI for Claude.",
				APIKeyIdentity: "You are Conduit, an AI assistant powered by Claude.",
			},
			Capabilities: AgentCapabilities{
				MemoryRecall:      true,
				ToolChaining:      true,
				SkillsIntegration: true,
				Heartbeats:        true,
				SilentReplies:     true,
			},
			History:       DefaultHistoryConfig(),
			PromptScaling: DefaultPromptScalingConfig(),
		},
		Tools: ToolsConfig{
			EnabledTools:  []string{"read", "write", "exec", "web_search"},
			MaxToolChains: 25, // Allow complex workflows, configurable per deployment
			Sandbox: SandboxConfig{
				WorkspaceDir: "./workspace",
				AllowedPaths: []string{"./workspace", "/tmp"},
			},
		},
		Debug: DebugConfig{
			LogMessageContent: false, // Privacy-safe by default
			VerboseLogging:    false,
		},
		RateLimiting: RateLimitingConfig{
			Enabled: true,
			Anonymous: RateLimitTierConfig{
				WindowSeconds: 60,  // 1 minute window
				MaxRequests:   100, // 100 requests per minute for anonymous (per IP)
			},
			Authenticated: RateLimitTierConfig{
				WindowSeconds: 60,   // 1 minute window
				MaxRequests:   1000, // 1000 requests per minute for authenticated (per client)
			},
			CleanupIntervalSeconds: 300, // Clean up expired buckets every 5 minutes
		},
		Diagnostics:    DefaultDiagnosticsConfig(),
		Heartbeat:      DefaultHeartbeatConfig(),
		AgentHeartbeat: DefaultAgentHeartbeatConfig(),
		RemoteSSH:      DefaultRemoteSSHConfig(),
		MQTT:           DefaultMQTTConfig(),
		Kubernetes:     DefaultKubernetesConfig(),
		PagerDuty:      DefaultPagerDutyConfig(),
		Datadog:        DefaultDatadogConfig(),
		Logging:        DefaultLoggingConfig(),
		Brain:          DefaultBrainConfig(),
		Channels: []ChannelConfig{
			{
				Name:    "telegram",
				Type:    "telegram",
				Enabled: false,
				Config: map[string]interface{}{
					"bot_token": "${TELEGRAM_BOT_TOKEN}",
				},
			},
			{
				Name:    "whatsapp",
				Type:    "whatsapp",
				Enabled: false,
				Config: map[string]interface{}{
					"session_dir": "./sessions/whatsapp",
				},
			},
		},
	}
}

// Load loads configuration from a file
func Load(path string) (*Config, error) {
	// Check if file exists, create default if not
	if _, err := os.Stat(path); os.IsNotExist(err) {
		cfg := Default()
		if err := cfg.Save(path); err != nil {
			return nil, fmt.Errorf("failed to save default config: %w", err)
		}
		fmt.Printf("Created default configuration at %s\n", path)
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Expand tilde in path fields before anything else so that
	// secrets_file can reference ~/... paths.
	cfg.expandTilde()

	// Load secrets file (KEY=VALUE) into the environment before
	// expanding ${ENV_VAR} placeholders in the config.
	if err := cfg.loadSecretsFile(); err != nil {
		return nil, fmt.Errorf("failed to load secrets file: %w", err)
	}

	// Expand environment variables
	if err := cfg.expandEnvVars(); err != nil {
		return nil, fmt.Errorf("failed to expand environment variables: %w", err)
	}

	// conduit-31jg.57: fold the deprecated smart_routing.pricing_overrides
	// alias into ai.pricing_overrides.
	cfg.AI.normalizePricingOverrides()
	cfg.warnDeprecatedKeys()
	cfg.applyDerivedDefaults() // conduit-31jg.40

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("configuration validation failed: %w", err)
	}

	return &cfg, nil
}

// Save saves the configuration to a file
func (c *Config) Save(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// expandEnvVars expands ${ENV_VAR} placeholders in configuration values.
// Struct fields tagged with cfg:"env" are expanded automatically via reflection.
// map[string]interface{} fields (channels, tool services) are expanded manually
// since map values can't carry struct tags.
func (c *Config) expandEnvVars() error {
	c.expandEnvTagged()
	c.expandEnvMaps()
	return nil
}

// Validate validates the entire configuration.
// It runs both subsystem-specific checks (heartbeat, MQTT, etc.) and the
// top-level semantic checks added in ValidateSemantic (port range, AI
// credentials, channel config, workspace paths, rate-limit values, tools list).
func (c *Config) Validate() error {
	// Validate heartbeat configuration
	if err := c.Heartbeat.Validate(); err != nil {
		return fmt.Errorf("invalid heartbeat configuration: %w", err)
	}

	// Validate agent heartbeat configuration
	if err := c.AgentHeartbeat.Validate(); err != nil {
		return fmt.Errorf("invalid agent heartbeat configuration: %w", err)
	}

	// Validate tools max-chain setting
	if c.Tools.MaxToolChains <= 0 {
		return fmt.Errorf("max_tool_chains must be greater than 0")
	}
	if c.Tools.MaxToolChains < 10 {
		fmt.Printf("WARNING: max_tool_chains is set to %d, which may be too low for complex tasks. Consider using 25 or higher.\n", c.Tools.MaxToolChains)
	}

	// Validate timezone if set
	if c.Timezone != "" {
		if _, err := time.LoadLocation(c.Timezone); err != nil {
			return fmt.Errorf("invalid timezone '%s': %w", c.Timezone, err)
		}
	}

	// Validate remote SSH configuration
	if err := c.RemoteSSH.Validate(); err != nil {
		return fmt.Errorf("invalid remote SSH configuration: %w", err)
	}

	// Validate MQTT configuration
	if err := c.MQTT.Validate(); err != nil {
		return fmt.Errorf("invalid MQTT configuration: %w", err)
	}

	// Validate Kubernetes configuration
	if err := c.Kubernetes.Validate(); err != nil {
		return fmt.Errorf("invalid kubernetes configuration: %w", err)
	}

	// Validate PagerDuty configuration
	if err := c.PagerDuty.Validate(); err != nil {
		return fmt.Errorf("invalid PagerDuty configuration: %w", err)
	}

	// Validate Datadog configuration
	if err := c.Datadog.Validate(); err != nil {
		return fmt.Errorf("invalid Datadog configuration: %w", err)
	}

	// Validate Brain configuration
	if err := c.Brain.Validate(); err != nil {
		return fmt.Errorf("invalid brain configuration: %w", err)
	}

	// Run semantic checks (port, AI credentials, channels, workspace paths,
	// rate-limit values, tools list) — reports all problems at once.
	if err := c.ValidateSemantic(); err != nil {
		return err
	}

	return nil
}

// GetLocation returns the configured timezone as a *time.Location.
// Falls back to AgentHeartbeat.Timezone if the top-level timezone is empty,
// then to time.Local.
func (c *Config) GetLocation() *time.Location {
	tz := c.Timezone
	if tz == "" {
		tz = c.AgentHeartbeat.Timezone
	}
	if tz == "" {
		return time.Local
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.Local
	}
	return loc
}

// expandTilde replaces a leading "~/" with the user's home directory in
// all string fields tagged with cfg:"path". Called before env-var expansion
// so that both "~/foo" and "${SOME_PATH}" work.
func (c *Config) expandTilde() {
	c.expandTildeTagged()
}

// loadSecretsFile reads a KEY=VALUE file into the process environment.
// Blank lines and lines starting with '#' are ignored.
// Existing environment variables are NOT overridden (shell/systemd wins).
// If SecretsFile is empty or the file doesn't exist, this is a no-op.
func (c *Config) loadSecretsFile() error {
	if c.SecretsFile == "" {
		return nil
	}

	f, err := os.Open(c.SecretsFile)
	if os.IsNotExist(err) {
		return nil // missing file is fine
	}
	if err != nil {
		return fmt.Errorf("cannot open secrets file %s: %w", c.SecretsFile, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)

		// Strip optional surrounding quotes from value
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}

		// Don't override existing env vars
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, value)
		}
	}
	return scanner.Err()
}
