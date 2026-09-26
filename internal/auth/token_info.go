package auth

import (
	"fmt"
	"io"
	"os"
	"strings"

	"conduit/internal/config"
	"conduit/internal/datadir"

	"github.com/spf13/cobra"
)

// TokenInfoReport is a log-safe description of how the token store resolves
// for a given config. It never contains the secret. conduit-31jg.48.
type TokenInfoReport struct {
	ConfigPath   string
	DatabasePath string
	SecretSource SecretSource // "" when no secret is available yet
	SecretPath   string       // persisted key file (when relevant)
	SecretExists bool         // whether the persisted key file exists
	Note         string
}

// InspectTokenStore reports where the token database and HMAC secret come
// from, WITHOUT creating a persisted key file (unlike ResolveTokenStore).
func InspectTokenStore(cfgPath string, cfg *config.Config) (*TokenInfoReport, error) {
	r := &TokenInfoReport{ConfigPath: cfgPath, DatabasePath: ResolveDatabasePath(cfg)}
	if strings.TrimSpace(cfg.Auth.TokenSecret) != "" {
		r.SecretSource = SecretSourceConfig
		return r, nil
	}
	if strings.TrimSpace(os.Getenv(TokenSecretEnvVar)) != "" {
		r.SecretSource = SecretSourceEnv
		return r, nil
	}
	dd, err := datadir.New(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("resolve data directory: %w", err)
	}
	r.SecretPath = dd.AuthFilePath(PersistedSecretFilename)
	if _, err := os.Stat(r.SecretPath); err == nil {
		r.SecretSource = SecretSourceFile
		r.SecretExists = true
	} else {
		r.Note = "no secret configured yet; the server (or `conduit token create`) will generate the key file on first use"
	}
	return r, nil
}

// Write prints the report. The secret itself is never printed.
func (r *TokenInfoReport) Write(w io.Writer) {
	fmt.Fprintf(w, "Config:        %s\n", r.ConfigPath)
	fmt.Fprintf(w, "Database:      %s\n", r.DatabasePath)
	src := string(r.SecretSource)
	switch r.SecretSource {
	case SecretSourceConfig:
		src = "config (auth.token_secret)"
	case SecretSourceEnv:
		src = "environment (" + TokenSecretEnvVar + ")"
	case SecretSourceFile:
		src = "persisted file"
	case "":
		src = "none"
	}
	fmt.Fprintf(w, "Secret source: %s\n", src)
	if r.SecretPath != "" {
		state := "missing"
		if r.SecretExists {
			state = "present"
		}
		fmt.Fprintf(w, "Key file:      %s (%s)\n", r.SecretPath, state)
	}
	if r.Note != "" {
		fmt.Fprintf(w, "Note:          %s\n", r.Note)
	}
}

// InfoTokenCmd creates `conduit token info`.
func InfoTokenCmd(c *CLIConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "info",
		Short: "Show which database and token-secret source the token commands use",
		Long: `Show the gateway config, database path and HMAC token-secret SOURCE
(config, environment, or persisted key file path). The secret itself is never printed.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if c.ConfigPath == "" {
				return fmt.Errorf("no config file specified (--config)")
			}
			if _, err := os.Stat(c.ConfigPath); err != nil {
				return fmt.Errorf("cannot read gateway config %q: %w", c.ConfigPath, err)
			}
			cfg, err := config.Load(c.ConfigPath)
			if err != nil {
				return fmt.Errorf("failed to load gateway config %q: %w", c.ConfigPath, err)
			}
			r, err := InspectTokenStore(c.ConfigPath, cfg)
			if err != nil {
				return err
			}
			if c.DatabasePath != "" {
				r.DatabasePath = c.DatabasePath + " (--database override)"
			}
			r.Write(cmd.OutOrStdout())
			return nil
		},
	}
}
