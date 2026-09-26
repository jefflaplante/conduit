package telegram

import (
	"database/sql"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"conduit/internal/auth"
	"conduit/internal/config"
	"conduit/internal/database"

	"github.com/spf13/cobra"
)

// PairingCLIConfig holds configuration for pairing CLI commands.
//
// conduit-31jg.48: the pairing CLI resolves its database exactly like the
// server and the token CLI (conduit-31jg.3): it loads the gateway config
// (ConfigPath, i.e. --config) and uses auth.ResolveDatabasePath. It used to
// derive "<config-name>.db" from the config FILE NAME and pass it through a
// CONDUIT_DB_PATH environment variable, which could open a different
// database than the running server.
type PairingCLIConfig struct {
	// ConfigPath is the gateway config file (the --config flag).
	ConfigPath string
	// DatabasePath, when non-empty, is an explicit --database override.
	DatabasePath string
	Verbose      bool
}

// resolveDatabasePath returns the database the pairing commands operate on.
func (c *PairingCLIConfig) resolveDatabasePath() (string, error) {
	if c.DatabasePath != "" {
		return c.DatabasePath, nil
	}
	if c.ConfigPath == "" {
		return "", fmt.Errorf("no config file specified: pairing commands need the gateway config (--config) to find the database")
	}
	// config.Load writes a default config when the file is missing; that
	// would point at a fresh database the server never reads.
	if _, err := os.Stat(c.ConfigPath); err != nil {
		return "", fmt.Errorf("cannot read gateway config %q (use --config to point at the server's config): %w", c.ConfigPath, err)
	}
	cfg, err := config.Load(c.ConfigPath)
	if err != nil {
		return "", fmt.Errorf("failed to load gateway config %q: %w", c.ConfigPath, err)
	}
	path := auth.ResolveDatabasePath(cfg)
	if path == "" {
		return "", fmt.Errorf("gateway config %q has no database.path; pass --database explicitly", c.ConfigPath)
	}
	return path, nil
}

// openConfiguredDatabase resolves and opens the pairing database.
func (c *PairingCLIConfig) openConfiguredDatabase() (*sql.DB, error) {
	dbPath, err := c.resolveDatabasePath()
	if err != nil {
		return nil, err
	}
	if c.Verbose {
		fmt.Fprintf(os.Stderr, "Using config %s, database %s\n", c.ConfigPath, dbPath)
	}
	db, err := openPairingDatabase(dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database %q: %w", dbPath, err)
	}
	return db, nil
}

// ListPairingsCmd creates the pairing list command for Telegram
func ListPairingsCmd(config *PairingCLIConfig) *cobra.Command {
	var includeExpired bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List pending Telegram pairing codes",
		Long:  `List all active (non-expired) Telegram pairing codes. Use --include-expired to show expired codes as well.`,
		Example: `  conduit pairing telegram list
  conduit pairing telegram list --include-expired`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return listPairings(config, includeExpired)
		},
	}

	cmd.Flags().BoolVar(&includeExpired, "include-expired", false, "Include expired pairing codes in the list")

	return cmd
}

// ApprovePairingCmd creates the pairing approve command for Telegram
func ApprovePairingCmd(config *PairingCLIConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "approve <CODE>",
		Short: "Approve a Telegram pairing code",
		Long:  `Approve a Telegram pairing code by marking it as inactive. The code will no longer be available for pairing.`,
		Example: `  conduit pairing telegram approve 550e8400-e29b-41d4-a716-446655440000
  conduit pairing telegram approve 550e8400`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return approvePairing(config, args[0])
		},
	}

	return cmd
}

// TelegramPairingRootCmd creates the root pairing command for Telegram with subcommands
func TelegramPairingRootCmd(config *PairingCLIConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "telegram",
		Short: "Manage Telegram pairing codes",
		Long:  `List and approve Telegram pairing codes for Conduit Gateway Telegram integration.`,
	}

	// Add subcommands
	cmd.AddCommand(ListPairingsCmd(config))
	cmd.AddCommand(ApprovePairingCmd(config))

	return cmd
}

// listPairings handles listing of pairing codes
func listPairings(config *PairingCLIConfig, includeExpired bool) error {
	db, err := config.openConfiguredDatabase()
	if err != nil {
		return err
	}
	defer db.Close()

	// Create pairing storage
	storage := NewPairingStorage(db)

	var pairings []PairingInfo
	if includeExpired {
		// Get all pairings (including expired ones)
		pairings, err = storage.getAllPairings()
		if err != nil {
			return fmt.Errorf("failed to list pairings: %w", err)
		}
	} else {
		// Get only active, non-expired pairings
		pairings, err = storage.ListPendingPairings()
		if err != nil {
			return fmt.Errorf("failed to list pending pairings: %w", err)
		}
	}

	if len(pairings) == 0 {
		if includeExpired {
			fmt.Println("No Telegram pairing codes found.")
		} else {
			fmt.Println("No active Telegram pairing codes found. Use --include-expired to see all codes.")
		}
		return nil
	}

	// Display pairings in table format
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "CODE\tUSER ID\tCREATED\tEXPIRES\tSTATUS")
	fmt.Fprintln(w, "----\t-------\t-------\t-------\t------")

	for _, pairing := range pairings {
		// Show first 8 characters of UUID for readability
		codePrefix := pairing.Code
		if len(codePrefix) > 8 {
			codePrefix = codePrefix[:8] + "..."
		}

		status := "Active"
		if !pairing.IsActive {
			status = "Used"
		} else if time.Now().After(pairing.ExpiresAt) {
			status = "Expired"
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			codePrefix,
			pairing.UserID,
			pairing.CreatedAt.Format("2006-01-02 15:04"),
			pairing.ExpiresAt.Format("2006-01-02 15:04"),
			status,
		)
	}

	w.Flush()
	return nil
}

// approvePairing handles approving a pairing code
func approvePairing(config *PairingCLIConfig, codeInput string) error {
	db, err := config.openConfiguredDatabase()
	if err != nil {
		return err
	}
	defer db.Close()

	// Create pairing storage
	storage := NewPairingStorage(db)

	// Try to find the pairing by exact code first
	var actualCode string
	pairing, err := storage.GetPairingByCode(codeInput)
	if err == nil {
		actualCode = codeInput
	} else {
		// If not found, try prefix matching
		matches, prefixErr := storage.findPairingByCodePrefix(codeInput)
		if prefixErr != nil {
			return fmt.Errorf("failed to search for pairing code: %w", prefixErr)
		}

		if len(matches) == 0 {
			return fmt.Errorf("no pairing code found matching: %s", codeInput)
		}

		if len(matches) > 1 {
			fmt.Printf("Multiple pairing codes match prefix '%s':\n", codeInput)
			for _, match := range matches {
				status := "Active"
				if !match.IsActive {
					status = "Used"
				} else if time.Now().After(match.ExpiresAt) {
					status = "Expired"
				}
				fmt.Printf("  %s (User: %s) - %s\n", match.Code[:8]+"...", match.UserID, status)
			}
			return fmt.Errorf("ambiguous prefix, please provide more characters")
		}

		actualCode = matches[0].Code
		pairing = &matches[0]
	}

	// Display pairing info before approval
	fmt.Printf("Pairing code details:\n")
	fmt.Printf("Code: %s\n", actualCode)
	fmt.Printf("User ID: %s\n", pairing.UserID)
	fmt.Printf("Created: %s\n", pairing.CreatedAt.Format(time.RFC3339))
	fmt.Printf("Expires: %s\n", pairing.ExpiresAt.Format(time.RFC3339))

	// Check if already used
	if !pairing.IsActive {
		fmt.Printf("\n❌ Pairing code is already used/inactive.\n")
		return fmt.Errorf("pairing code is already used: %s", actualCode)
	}

	// Check if expired
	if time.Now().After(pairing.ExpiresAt) {
		fmt.Printf("\n❌ Pairing code has expired.\n")
		return fmt.Errorf("pairing code has expired: %s", actualCode)
	}

	// Approve the pairing
	err = storage.ApprovePairing(actualCode)
	if err != nil {
		return fmt.Errorf("failed to approve pairing: %w", err)
	}

	fmt.Printf("\n✅ Pairing code approved successfully!\n")
	fmt.Printf("Approved at: %s\n", time.Now().Format(time.RFC3339))
	fmt.Printf("\nThe pairing code can no longer be used for new connections.\n")

	return nil
}

// Helper functions

// openPairingDatabase opens the SQLite database and runs migrations
func openPairingDatabase(dbPath string) (*sql.DB, error) {
	// Same DSN (busy_timeout, WAL, ...) as the server so CLI writes wait on a
	// running gateway instead of failing with SQLITE_BUSY. conduit-31jg.48
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

// getAllPairings is a helper method for listing all pairings including expired ones
func (ps *PairingStorage) getAllPairings() ([]PairingInfo, error) {
	query := `
		SELECT code, user_id, created_at, expires_at, is_active, metadata
		FROM telegram_pairings
		ORDER BY created_at DESC
	`

	rows, err := ps.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to query all pairings: %w", err)
	}
	defer rows.Close()

	var pairings []PairingInfo
	for rows.Next() {
		var p PairingInfo
		var metadataJSON string
		var createdAtStr, expiresAtStr string

		err := rows.Scan(&p.Code, &p.UserID, &createdAtStr, &expiresAtStr, &p.IsActive, &metadataJSON)
		if err != nil {
			return nil, fmt.Errorf("failed to scan pairing row: %w", err)
		}

		// Parse timestamps
		if p.CreatedAt, err = parseTimestamp(createdAtStr); err != nil {
			return nil, fmt.Errorf("failed to parse created_at: %w", err)
		}
		if p.ExpiresAt, err = parseTimestamp(expiresAtStr); err != nil {
			return nil, fmt.Errorf("failed to parse expires_at: %w", err)
		}

		// Parse metadata (skip errors for simplicity in listing)
		p.Metadata = make(map[string]string)

		pairings = append(pairings, p)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating pairing rows: %w", err)
	}

	return pairings, nil
}
