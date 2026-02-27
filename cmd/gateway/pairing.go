package main

import (
	telegram "conduit/internal/channels/telegram"

	"github.com/spf13/cobra"
)

// PairingRootCmd creates the root pairing command with channel-specific subcommands
func PairingRootCmd(databasePath string, verbose bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pairing",
		Short: "Manage pairing codes for different channels",
		Long:  `Manage pairing codes for Conduit Gateway channel integrations. Each channel has its own set of pairing commands.`,
	}

	// Create Telegram pairing config
	telegramConfig := &telegram.PairingCLIConfig{
		DatabasePath: databasePath,
		Verbose:      verbose,
	}

	// Add Telegram pairing commands
	cmd.AddCommand(telegram.TelegramPairingRootCmd(telegramConfig))

	return cmd
}
