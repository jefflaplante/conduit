package main

import (
	telegram "conduit/internal/channels/telegram"

	"github.com/spf13/cobra"
)

// PairingRootCmd creates the root pairing command with channel-specific
// subcommands. cfg is shared with main's PersistentPreRunE, which fills in
// --config / --database / --verbose after flag parsing (conduit-31jg.48).
func PairingRootCmd(cfg *telegram.PairingCLIConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pairing",
		Short: "Manage pairing codes for different channels",
		Long: `Manage pairing codes for Conduit Gateway channel integrations. Each channel has its own set of pairing commands.

The database is the server's database.path from --config (override with --database).`,
	}

	// Add Telegram pairing commands
	cmd.AddCommand(telegram.TelegramPairingRootCmd(cfg))

	return cmd
}
