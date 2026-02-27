package auth

import (
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"conduit/internal/database"
	tokenspkg "conduit/pkg/tokens"

	"github.com/spf13/cobra"
)

// CLIConfig holds configuration for CLI commands
type CLIConfig struct {
	DatabasePath string
	Verbose      bool
}

// CreateTokenCmd creates the token create command
func CreateTokenCmd(config *CLIConfig) *cobra.Command {
	var (
		clientName string
		expiresIn  string
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new authentication token",
		Long:  `Create a new authentication token for a client. The token will be displayed once and cannot be retrieved again.`,
		Example: `  conduit token create --client-name "jules-main" --expires-in "1y"
  conduit token create --client-name "production-server"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return createToken(config, clientName, expiresIn)
		},
	}

	cmd.Flags().StringVar(&clientName, "client-name", "", "Name of the client (required)")
	cmd.Flags().StringVar(&expiresIn, "expires-in", "", "Expiration duration (e.g., '1y', '30d', '24h') - optional")
	cmd.MarkFlagRequired("client-name")

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
		Use:   "revoke <token-prefix>",
		Short: "Revoke an authentication token",
		Long:  `Revoke an authentication token by its prefix. The token will be marked as inactive and can no longer be used.`,
		Example: `  conduit token revoke claw_v1_8KzABC
  conduit token revoke claw_v1_ABC123DEF456`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return revokeToken(config, args[0])
		},
	}

	return cmd
}

// ExportTokenCmd creates the token export command
func ExportTokenCmd(config *CLIConfig) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "export <token-prefix>",
		Short: "Export an authentication token for easy setup",
		Long:  `Export an authentication token in various formats for easy environment setup.`,
		Example: `  conduit token export claw_v1_8KzABC --format env
  conduit token export claw_v1_ABC123DEF456`,
		Args: cobra.ExactArgs(1),
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
	cmd.AddCommand(ExportTokenCmd(config))

	return cmd
}

// createToken handles token creation
func createToken(config *CLIConfig, clientName, expiresIn string) error {
	// Update config from environment if not set
	if config.DatabasePath == "" {
		if dbPath := os.Getenv("CONDUIT_DB_PATH"); dbPath != "" {
			config.DatabasePath = dbPath
		} else {
			config.DatabasePath = "gateway.db" // default
		}
	}
	if verbose := os.Getenv("CONDUIT_VERBOSE"); verbose == "true" {
		config.Verbose = true
	}
	// Validate input
	if strings.TrimSpace(clientName) == "" {
		return fmt.Errorf("client-name is required")
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

	// Open database
	db, err := openDatabase(config.DatabasePath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer db.Close()

	// Create token storage
	storage := NewTokenStorage(db)

	// Generate new token
	token, err := tokenspkg.GenerateToken()
	if err != nil {
		return fmt.Errorf("failed to generate token: %w", err)
	}

	// Create token request (we need to modify the existing CreateToken method)
	req := CreateTokenRequest{
		ClientName: clientName,
		ExpiresAt:  expiresAt,
		Metadata:   make(map[string]string),
	}

	// Store the token with our custom format
	resp, err := storage.CreateTokenWithCustomFormat(req, token)
	if err != nil {
		return fmt.Errorf("failed to create token: %w", err)
	}

	// Display success message
	fmt.Printf("✅ Token created successfully!\n\n")
	fmt.Printf("Token: %s\n", resp.Token)
	fmt.Printf("Client: %s\n", resp.TokenInfo.ClientName)
	fmt.Printf("Created: %s\n", resp.TokenInfo.CreatedAt.Format(time.RFC3339))
	if resp.TokenInfo.ExpiresAt != nil {
		fmt.Printf("Expires: %s\n", resp.TokenInfo.ExpiresAt.Format(time.RFC3339))
	} else {
		fmt.Printf("Expires: Never\n")
	}
	fmt.Printf("Token ID: %s\n", resp.TokenInfo.TokenID)

	fmt.Printf("\n⚠️  Save this token now! It cannot be retrieved again.\n")
	fmt.Printf("\nTo use this token, set the environment variable:\n")
	fmt.Printf("export CONDUIT_TOKEN=\"%s\"\n", resp.Token)

	return nil
}

// listTokens handles token listing
func listTokens(config *CLIConfig, includeRevoked bool) error {
	// Update config from environment if not set
	if config.DatabasePath == "" {
		if dbPath := os.Getenv("CONDUIT_DB_PATH"); dbPath != "" {
			config.DatabasePath = dbPath
		} else {
			config.DatabasePath = "gateway.db" // default
		}
	}
	// Open database
	db, err := openDatabase(config.DatabasePath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer db.Close()

	// Create token storage
	storage := NewTokenStorage(db)

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
	fmt.Fprintln(w, "PREFIX\tCLIENT\tCREATED\tEXPIRES\tLAST USED\tSTATUS")
	fmt.Fprintln(w, "------\t------\t-------\t-------\t---------\t------")

	for _, token := range tokenList {
		prefix := tokenspkg.GetTokenPrefix("claw_v1_"+token.TokenID[:8], 12) // Show first 12 chars as prefix

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

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			prefix,
			token.ClientName,
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
	// Update config from environment if not set
	if config.DatabasePath == "" {
		if dbPath := os.Getenv("CONDUIT_DB_PATH"); dbPath != "" {
			config.DatabasePath = dbPath
		} else {
			config.DatabasePath = "gateway.db" // default
		}
	}
	// Open database
	db, err := openDatabase(config.DatabasePath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer db.Close()

	// Create token storage
	storage := NewTokenStorage(db)

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

// exportToken handles token export
func exportToken(config *CLIConfig, tokenPrefix, format string) error {
	// Update config from environment if not set
	if config.DatabasePath == "" {
		if dbPath := os.Getenv("CONDUIT_DB_PATH"); dbPath != "" {
			config.DatabasePath = dbPath
		} else {
			config.DatabasePath = "gateway.db" // default
		}
	}
	// Open database
	db, err := openDatabase(config.DatabasePath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer db.Close()

	// Create token storage
	storage := NewTokenStorage(db)

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

// openDatabase opens the SQLite database and runs migrations
func openDatabase(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbPath)
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

// findTokenByPrefix finds a token ID by matching a prefix
func findTokenByPrefix(storage *TokenStorage, prefix string) (string, error) {
	// List all tokens (including revoked ones for potential match)
	tokens, err := storage.ListTokens("", true)
	if err != nil {
		return "", fmt.Errorf("failed to list tokens: %w", err)
	}

	var matches []TokenInfo
	for _, token := range tokens {
		// Create a display prefix from token ID (since we don't store the actual token)
		displayPrefix := tokenspkg.GetTokenPrefix("claw_v1_"+token.TokenID[:8], 12)
		if strings.HasPrefix(displayPrefix, prefix) {
			matches = append(matches, token)
		}
	}

	if len(matches) == 0 {
		return "", fmt.Errorf("no token found matching prefix: %s", prefix)
	}

	if len(matches) > 1 {
		fmt.Printf("Multiple tokens match prefix '%s':\n", prefix)
		for _, match := range matches {
			displayPrefix := tokenspkg.GetTokenPrefix("claw_v1_"+match.TokenID[:8], 12)
			status := "Active"
			if !match.IsActive {
				status = "Revoked"
			}
			fmt.Printf("  %s (%s) - %s\n", displayPrefix, match.ClientName, status)
		}
		return "", fmt.Errorf("ambiguous prefix, please provide more characters")
	}

	return matches[0].TokenID, nil
}
