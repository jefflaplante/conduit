package auth

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"conduit/internal/config"
	"conduit/internal/database"
	tokenspkg "conduit/internal/tokens"

	"github.com/spf13/cobra"
)

// CLIConfig holds configuration for CLI commands.
//
// conduit-31jg.3: token commands load the same config file as the server
// (ConfigPath, i.e. --config) and resolve the database path and HMAC secret
// through ResolveTokenStore, so tokens minted here validate on the server.
type CLIConfig struct {
	// ConfigPath is the gateway config file (the --config flag). Required.
	ConfigPath string
	// DatabasePath, when non-empty, is an explicit --database override. When
	// empty the server's database.path from the config is used.
	DatabasePath string
	Verbose      bool
	// Stderr receives diagnostic output (secret source etc.). Defaults to
	// os.Stderr.
	Stderr io.Writer
}

// CreateTokenCmd creates the token create command
func CreateTokenCmd(config *CLIConfig) *cobra.Command {
	var (
		clientName string
		expiresIn  string
		role       string
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new authentication token",
		Long: `Create a new authentication token for a client. The token will be displayed once and cannot be retrieved again.

--role is required:
  owner       the human operator: any session, may approve owner actions
  automation  scripts/services: only sessions it creates, never approves`,
		Example: `  conduit token create --client-name "laptop" --role owner --expires-in "1y"
  conduit token create --client-name "backup-bot" --role automation`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return createToken(config, clientName, expiresIn, role)
		},
	}

	cmd.Flags().StringVar(&clientName, "client-name", "", "Name of the client (required)")
	cmd.Flags().StringVar(&expiresIn, "expires-in", "", "Expiration duration (e.g., '1y', '30d', '24h') - optional")
	cmd.Flags().StringVar(&role, "role", "", "Token role: owner or automation (required)")
	cmd.MarkFlagRequired("client-name")
	cmd.MarkFlagRequired("role")

	return cmd
}

// ListTokensCmd creates the token list command
func ListTokensCmd(config *CLIConfig) *cobra.Command {
	var includeRevoked bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List authentication tokens",
		Long:  `List all active authentication tokens. Use --include-revoked to show revoked tokens as well.`,
		Example: `  conduit token list
  conduit token list --include-revoked`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return listTokens(config, includeRevoked)
		},
	}

	cmd.Flags().BoolVar(&includeRevoked, "include-revoked", false, "Include revoked tokens in the list")

	return cmd
}

// RevokeTokenCmd creates the token revoke command
func RevokeTokenCmd(config *CLIConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "revoke <token-id-prefix>",
		Short:   "Revoke an authentication token",
		Long:    `Revoke an authentication token by a prefix of its token ID (the ID column of 'conduit token list'). The token will be marked as inactive and can no longer be used.`,
		Example: `  conduit token revoke 3f2a9c1e`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return revokeToken(config, args[0])
		},
	}

	return cmd
}

// SetRoleCmd creates the token set-role command (conduit-31jg.67).
func SetRoleCmd(config *CLIConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "set-role <token-id-prefix> <owner|automation>",
		Short: "Set a token's role",
		Long: `Set the role of an existing token. Tokens created before roles existed have
none and are treated as owner until tagged. The role applies to new
connections; reconnect clients for it to take effect.`,
		Example: `  conduit token set-role 3f2a9c1e automation`,
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setTokenRole(config, args[0], args[1])
		},
	}
}

// ExportTokenCmd creates the token export command
func ExportTokenCmd(config *CLIConfig) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:     "export <token-id-prefix>",
		Short:   "Export an authentication token for easy setup",
		Long:    `Export an authentication token in various formats for easy environment setup.`,
		Example: `  conduit token export 3f2a9c1e --format env`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return exportToken(config, args[0], format)
		},
	}

	cmd.Flags().StringVar(&format, "format", "env", "Export format (env)")

	return cmd
}

// TokenRootCmd creates the root token command with subcommands
func TokenRootCmd(config *CLIConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Manage authentication tokens",
		Long:  `Create, list, revoke, and export authentication tokens for Conduit Gateway access.`,
	}

	// Add subcommands
	cmd.AddCommand(CreateTokenCmd(config))
	cmd.AddCommand(ListTokensCmd(config))
	cmd.AddCommand(RevokeTokenCmd(config))
	cmd.AddCommand(SetRoleCmd(config))
	cmd.AddCommand(ExportTokenCmd(config))
	cmd.AddCommand(InfoTokenCmd(config)) // conduit-31jg.48

	return cmd
}

// createToken handles token creation
func createToken(config *CLIConfig, clientName, expiresIn, role string) error {
	// Validate input
	if strings.TrimSpace(clientName) == "" {
		return fmt.Errorf("client-name is required")
	}
	role, err := ParseRole(role)
	if err != nil {
		return err
	}

	// Parse expiration if provided
	var expiresAt *time.Time
	if expiresIn != "" {
		duration, err := parseDuration(expiresIn)
		if err != nil {
			return fmt.Errorf("invalid expires-in format: %w", err)
		}
		expiry := time.Now().Add(duration)
		expiresAt = &expiry
	}

	// Open database + token storage exactly as the server would.
	storage, db, err := openTokenStorage(config)
	if err != nil {
		return err
	}
	defer db.Close()

	resp, err := storage.CreateToken(CreateTokenRequest{
		ClientName: clientName,
		ExpiresAt:  expiresAt,
		Metadata:   map[string]string{MetadataKeyRole: role},
	})
	if err != nil {
		return fmt.Errorf("failed to create token: %w", err)
	}

	// Display success message
	fmt.Printf("✅ Token created successfully!\n\n")
	fmt.Printf("Token: %s\n", resp.Token)
	fmt.Printf("Client: %s\n", resp.TokenInfo.ClientName)
	fmt.Printf("Role: %s\n", role)
	fmt.Printf("Created: %s\n", resp.TokenInfo.CreatedAt.Format(time.RFC3339))
	if resp.TokenInfo.ExpiresAt != nil {
		fmt.Printf("Expires: %s\n", resp.TokenInfo.ExpiresAt.Format(time.RFC3339))
	} else {
		fmt.Printf("Expires: Never\n")
	}
	fmt.Printf("Token ID: %s (use %s with revoke/export/set-role)\n", resp.TokenInfo.TokenID, displayID(resp.TokenInfo.TokenID))

	fmt.Printf("\n⚠️  Save this token now! It cannot be retrieved again.\n")
	fmt.Printf("\nTo use this token, set the environment variable:\n")
	fmt.Printf("export CONDUIT_TOKEN=\"%s\"\n", resp.Token)

	return nil
}

// listTokens handles token listing
func listTokens(config *CLIConfig, includeRevoked bool) error {
	// Open database + token storage exactly as the server would.
	storage, db, err := openTokenStorage(config)
	if err != nil {
		return err
	}
	defer db.Close()

	// List tokens
	tokenList, err := storage.ListTokens("", includeRevoked)
	if err != nil {
		return fmt.Errorf("failed to list tokens: %w", err)
	}

	if len(tokenList) == 0 {
		if includeRevoked {
			fmt.Println("No tokens found.")
		} else {
			fmt.Println("No active tokens found. Use --include-revoked to see all tokens.")
		}
		return nil
	}

	// Display tokens in table format
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tCLIENT\tROLE\tCREATED\tEXPIRES\tLAST USED\tSTATUS")
	fmt.Fprintln(w, "--\t------\t----\t-------\t-------\t---------\t------")

	for _, token := range tokenList {
		prefix := displayID(token.TokenID)

		status := "Active"
		if !token.IsActive {
			status = "Revoked"
		}

		expires := "Never"
		if token.ExpiresAt != nil {
			if time.Now().After(*token.ExpiresAt) {
				expires = "EXPIRED"
				status = "Expired"
			} else {
				expires = token.ExpiresAt.Format("2006-01-02")
			}
		}

		lastUsed := "Never"
		if token.LastUsedAt != nil {
			lastUsed = token.LastUsedAt.Format("2006-01-02")
		}

		role, explicit := TokenRole(token.Metadata)
		if !explicit {
			role += " (default)"
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			prefix,
			token.ClientName,
			role,
			token.CreatedAt.Format("2006-01-02"),
			expires,
			lastUsed,
			status,
		)
	}

	w.Flush()
	return nil
}

// revokeToken handles token revocation
func revokeToken(config *CLIConfig, tokenPrefix string) error {
	// Open database + token storage exactly as the server would.
	storage, db, err := openTokenStorage(config)
	if err != nil {
		return err
	}
	defer db.Close()

	// Find token by prefix
	tokenID, err := findTokenByPrefix(storage, tokenPrefix)
	if err != nil {
		return err
	}

	// Get token info before revocation
	tokenInfo, err := storage.GetTokenInfo(tokenID)
	if err != nil {
		return fmt.Errorf("failed to get token info: %w", err)
	}

	// Revoke the token
	err = storage.RevokeToken(tokenID)
	if err != nil {
		return fmt.Errorf("failed to revoke token: %w", err)
	}

	fmt.Printf("✅ Token revoked successfully!\n\n")
	fmt.Printf("Token: %s...\n", tokenPrefix)
	fmt.Printf("Client: %s\n", tokenInfo.ClientName)
	fmt.Printf("Created: %s\n", tokenInfo.CreatedAt.Format(time.RFC3339))
	fmt.Printf("Revoked: %s\n", time.Now().Format(time.RFC3339))

	return nil
}

// setTokenRole handles token set-role (conduit-31jg.67).
func setTokenRole(config *CLIConfig, tokenPrefix, role string) error {
	role, err := ParseRole(role)
	if err != nil {
		return err
	}
	storage, db, err := openTokenStorage(config)
	if err != nil {
		return err
	}
	defer db.Close()

	tokenID, err := findTokenByPrefix(storage, tokenPrefix)
	if err != nil {
		return err
	}
	if err := storage.SetTokenRole(tokenID, role); err != nil {
		return fmt.Errorf("failed to set role: %w", err)
	}
	info, err := storage.GetTokenInfo(tokenID)
	if err != nil {
		return fmt.Errorf("failed to get token info: %w", err)
	}
	fmt.Printf("✅ Role set to %s for %s (%s).\n", role, tokenPrefix, info.ClientName)
	fmt.Printf("Clients already connected keep their old role until they reconnect.\n")
	return nil
}

// exportToken handles token export
func exportToken(config *CLIConfig, tokenPrefix, format string) error {
	// Open database + token storage exactly as the server would.
	storage, db, err := openTokenStorage(config)
	if err != nil {
		return err
	}
	defer db.Close()

	// Find token by prefix
	tokenID, err := findTokenByPrefix(storage, tokenPrefix)
	if err != nil {
		return err
	}

	// Get token info
	tokenInfo, err := storage.GetTokenInfo(tokenID)
	if err != nil {
		return fmt.Errorf("failed to get token info: %w", err)
	}

	// Check if token is active
	if !tokenInfo.IsActive {
		return fmt.Errorf("cannot export revoked token")
	}

	// Check if token is expired
	if tokenInfo.ExpiresAt != nil && time.Now().After(*tokenInfo.ExpiresAt) {
		return fmt.Errorf("cannot export expired token")
	}

	// We can't retrieve the actual token value from storage since it's hashed
	// This is a limitation we need to document
	fmt.Printf("❌ Cannot export token: token value is not stored in retrievable format.\n\n")
	fmt.Printf("Token information:\n")
	fmt.Printf("Client: %s\n", tokenInfo.ClientName)
	fmt.Printf("Created: %s\n", tokenInfo.CreatedAt.Format(time.RFC3339))
	if tokenInfo.ExpiresAt != nil {
		fmt.Printf("Expires: %s\n", tokenInfo.ExpiresAt.Format(time.RFC3339))
	} else {
		fmt.Printf("Expires: Never\n")
	}
	fmt.Printf("\n💡 Tokens can only be exported immediately after creation for security reasons.\n")

	return nil
}

// Helper functions

// openTokenStorage loads the gateway config and opens the token store with
// the same database path and HMAC secret the server resolves.
// conduit-31jg.3: previously the CLI only read CONDUIT_TOKEN_SECRET and fell
// back to a random ephemeral key, printing success for tokens the server
// would reject. Any resolution failure is now a hard error.
func openTokenStorage(c *CLIConfig) (*TokenStorage, *sql.DB, error) {
	stderr := c.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	if c.ConfigPath == "" {
		return nil, nil, fmt.Errorf("no config file specified: token commands need the gateway config (--config) to resolve the token secret")
	}
	// config.Load writes a default config when the file is missing; a token
	// minted against a fresh default config would never match the server.
	if _, err := os.Stat(c.ConfigPath); err != nil {
		return nil, nil, fmt.Errorf("cannot read gateway config %q (use --config to point at the server's config): %w", c.ConfigPath, err)
	}
	cfg, err := config.Load(c.ConfigPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load gateway config %q: %w", c.ConfigPath, err)
	}

	settings, err := ResolveTokenStore(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot resolve the server's token secret; refusing to continue: %w", err)
	}

	dbPath := settings.DatabasePath
	if c.DatabasePath != "" && c.DatabasePath != dbPath {
		fmt.Fprintf(stderr, "WARNING: --database %q overrides database.path %q from %s; the server will not see tokens stored there\n",
			c.DatabasePath, dbPath, c.ConfigPath)
		dbPath = c.DatabasePath
	}
	if dbPath == "" {
		return nil, nil, fmt.Errorf("gateway config %q has no database.path; pass --database explicitly", c.ConfigPath)
	}

	// Log the source, never the secret.
	fmt.Fprintf(stderr, "Using config %s, database %s, token secret from %s\n", c.ConfigPath, dbPath, settings.Describe())

	db, err := openDatabase(dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open database %q: %w", dbPath, err)
	}
	ts, err := OpenTokenStorage(db, settings.Secret) // conduit-31jg.48
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return ts, db, nil
}

// openDatabase opens the SQLite database and runs migrations
func openDatabase(dbPath string) (*sql.DB, error) {
	// conduit-31jg.3: same DSN (busy_timeout etc.) as the server's session store,
	// so CLI writes wait on a running server instead of failing with SQLITE_BUSY.
	db, err := sql.Open("sqlite", database.BuildDSN(dbPath))
	if err != nil {
		return nil, err
	}

	// Test the connection
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}

	// Configure database and run migrations
	if err := database.ConfigureDatabase(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to configure database: %w", err)
	}

	return db, nil
}

// parseDuration parses duration strings like "1y", "30d", "24h"
func parseDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf("empty duration string")
	}

	// Handle common suffixes
	if strings.HasSuffix(s, "y") {
		// Years
		years, err := strconv.Atoi(s[:len(s)-1])
		if err != nil {
			return 0, fmt.Errorf("invalid year format: %w", err)
		}
		return time.Duration(years) * 365 * 24 * time.Hour, nil
	}

	if strings.HasSuffix(s, "d") {
		// Days
		days, err := strconv.Atoi(s[:len(s)-1])
		if err != nil {
			return 0, fmt.Errorf("invalid day format: %w", err)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}

	// Fall back to standard time.ParseDuration for hours, minutes, seconds
	return time.ParseDuration(s)
}

// displayID is the short token-ID prefix shown by `token list` and
// accepted by revoke/export/set-role. Token IDs are UUIDs; the raw token is
// never stored, so it cannot be shown (conduit-3ryz: this used to print a
// made-up "claw_v1_" token prefix).
func displayID(tokenID string) string {
	return tokenspkg.GetTokenPrefix(tokenID, 8)
}

// findTokenByPrefix finds a token by a prefix of its token ID.
func findTokenByPrefix(storage *TokenStorage, prefix string) (string, error) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return "", fmt.Errorf("token ID prefix is required")
	}
	// List all tokens (including revoked ones for potential match)
	tokens, err := storage.ListTokens("", true)
	if err != nil {
		return "", fmt.Errorf("failed to list tokens: %w", err)
	}

	var matches []TokenInfo
	for _, token := range tokens {
		if strings.HasPrefix(token.TokenID, prefix) {
			matches = append(matches, token)
		}
	}

	if len(matches) == 0 {
		return "", fmt.Errorf("no token found with ID prefix %q (see `conduit token list`)", prefix)
	}

	if len(matches) > 1 {
		fmt.Printf("Multiple tokens match prefix '%s':\n", prefix)
		for _, match := range matches {
			status := "Active"
			if !match.IsActive {
				status = "Revoked"
			}
			fmt.Printf("  %s (%s) - %s\n", displayID(match.TokenID), match.ClientName, status)
		}
		return "", fmt.Errorf("ambiguous prefix, please provide more characters")
	}

	return matches[0].TokenID, nil
}
