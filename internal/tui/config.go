package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"conduit/internal/datadir"
)

// DataDirConfig holds the optional config value for the data directory.
// Set this before calling TUI config functions so the config-level
// data_dir override is respected.
var DataDirConfig string

// TUIConfig holds configuration for the TUI client
type TUIConfig struct {
	GatewayURL    string `json:"gateway_url"`
	Token         string `json:"token"`
	ClientName    string `json:"client_name,omitempty"`
	DatabasePath  string `json:"database_path,omitempty"`
	UserID        string `json:"user_id,omitempty"`
	AssistantName string `json:"assistant_name,omitempty"`
}

// configDir returns the conduit data directory for TUI config.
func configDir() (string, error) {
	return datadir.Resolve(DataDirConfig)
}

// configPath returns the TUI config file path
func configPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tui.json"), nil
}

// LoadOrCreateConfig loads existing config or creates a new one.
// URL and token from arguments override any saved config.
// If url is empty, falls back to saved config, then to a hardcoded default.
func LoadOrCreateConfig(url, token, dbPath string) (*TUIConfig, error) {
	cfg := &TUIConfig{}

	// Try to load existing saved config
	cfgPath, err := configPath()
	if err == nil {
		if data, err := os.ReadFile(cfgPath); err == nil {
			json.Unmarshal(data, cfg) // ignore parse errors, use defaults
		}
	}

	// Override with provided values (empty string means "not provided")
	if url != "" {
		cfg.GatewayURL = url
	}
	if token != "" {
		cfg.Token = token
	}
	if dbPath != "" {
		cfg.DatabasePath = dbPath
	}

	// Apply default if still empty after config load + overrides
	if cfg.GatewayURL == "" {
		cfg.GatewayURL = "ws://localhost:18789/ws"
	}

	// Set user ID from hostname if not set
	if cfg.UserID == "" {
		hostname, err := os.Hostname()
		if err != nil {
			hostname = "tui-user"
		}
		cfg.UserID = hostname
	}

	// Set client name if not set
	if cfg.ClientName == "" {
		hostname, _ := os.Hostname()
		if hostname == "" {
			hostname = "local"
		}
		cfg.ClientName = fmt.Sprintf("tui-%s", hostname)
	}

	// If no token is available, auto-generate one from the database
	if cfg.Token == "" && cfg.DatabasePath != "" {
		token, err := autoGenerateToken(cfg.DatabasePath, cfg.ClientName)
		if err != nil {
			return nil, fmt.Errorf("failed to auto-generate token (provide one with --token): %w", err)
		}
		cfg.Token = token
	}

	// Save config for next time
	if cfgPath != "" {
		cfg.save(cfgPath)
	}

	// Validate
	if cfg.Token == "" {
		return nil, fmt.Errorf("no authentication token available; use --token or run 'conduit token create --client-name tui'")
	}

	return cfg, nil
}

// save writes the config to disk
func (c *TUIConfig) save(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// LoadSavedToken reads the token from the saved TUI config file (~/.conduit/tui.json).
// Returns the token and true if found, or empty string and false if not available.
func LoadSavedToken() (string, bool) {
	cfgPath, err := configPath()
	if err != nil {
		return "", false
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return "", false
	}
	var cfg TUIConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return "", false
	}
	if cfg.Token == "" {
		return "", false
	}
	return cfg.Token, true
}

// autoGenerateToken creates a token directly in the gateway database
func autoGenerateToken(dbPath, clientName string) (string, error) {
	// Import the auth and database packages to create a token
	// We do this inline to avoid circular dependencies
	// The token creation logic mirrors internal/auth/cli.go:createToken()

	// For now, require the user to provide a token explicitly
	// Auto-generation requires opening the DB which may conflict with a running gateway
	return "", fmt.Errorf("auto-generation not available; database at %s", dbPath)
}
