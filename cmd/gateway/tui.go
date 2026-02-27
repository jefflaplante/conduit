package main

import (
	"fmt"
	"log"
	"path/filepath"

	"conduit/internal/config"
	"conduit/internal/tui"

	"github.com/spf13/cobra"
)

var (
	tuiURL   string
	tuiToken string
)

var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Launch interactive TUI chat client",
	Long: `Launch a BubbleTea terminal UI that connects to the running gateway
over WebSocket. Provides the same slash commands and messaging capabilities
as Telegram, with streaming responses and tool activity visibility.

The TUI reads the gateway's config file (--config) to determine the port
and database path automatically. If no token is provided, the TUI will
look for a saved token in ~/.conduit/tui.json.

Key bindings:
  Enter           Send message
  Ctrl+T          New session tab
  Ctrl+W          Close tab
  Alt+Left/Right  Switch tabs
  Tab             Toggle sidebar
  Shift+Tab       Cycle sidebar tabs
  PageUp/PageDown Scroll chat history
  Ctrl+C          Quit`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Derive connection details from the gateway config file
		effectiveURL := tuiURL
		effectiveDBPath := dbPath
		assistantName := ""

		urlExplicit := cmd.Flags().Changed("url")

		if !urlExplicit || effectiveDBPath == "" {
			gatewayCfg, err := loadGatewayConfigForTUI(cfgFile)
			if err != nil {
				if !urlExplicit {
					log.Printf("Warning: could not read gateway config %s: %v (using defaults)", cfgFile, err)
				}
			} else {
				// Derive WebSocket URL from gateway port if --url wasn't explicitly set
				if !urlExplicit {
					effectiveURL = fmt.Sprintf("ws://localhost:%d/ws", gatewayCfg.Port)
					log.Printf("Using gateway port %d from %s", gatewayCfg.Port, cfgFile)
				}

				// Derive database path from config filename if not explicitly set
				if effectiveDBPath == "" {
					effectiveDBPath = deriveDBPath(cfgFile, gatewayCfg)
				}

				// Use agent name from config for the assistant label
				assistantName = gatewayCfg.Agent.Name
			}
		}

		tuiConfig, err := tui.LoadOrCreateConfig(effectiveURL, tuiToken, effectiveDBPath)
		if err != nil {
			return err
		}
		if assistantName != "" {
			tuiConfig.AssistantName = assistantName
		}
		return tui.Run(tuiConfig)
	},
}

func init() {
	tuiCmd.Flags().StringVar(&tuiURL, "url", "", "Gateway WebSocket URL (default: derived from --config port)")
	tuiCmd.Flags().StringVar(&tuiToken, "token", "", "Authentication token (auto-detected if not specified)")
}

// loadGatewayConfigForTUI reads the gateway config to extract port and database info.
// Uses a lightweight approach - reads the JSON without full validation since the
// gateway may not be fully configured for TUI purposes.
func loadGatewayConfigForTUI(configPath string) (*config.Config, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// deriveDBPath determines the database path from the config file path,
// matching the same logic used by the server command and initConfig.
func deriveDBPath(configPath string, cfg *config.Config) string {
	// If the config specifies a database path, use it
	if cfg.Database.Path != "" && cfg.Database.Path != "gateway.db" {
		return cfg.Database.Path
	}

	// Derive from config filename (same logic as initConfig/PersistentPreRunE)
	if configPath != "" && configPath != "config.json" {
		dir := filepath.Dir(configPath)
		base := filepath.Base(configPath)
		ext := filepath.Ext(base)
		name := base[:len(base)-len(ext)]
		return filepath.Join(dir, name+".db")
	}

	return "gateway.db"
}
