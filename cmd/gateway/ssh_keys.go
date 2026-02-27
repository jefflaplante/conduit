package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	internalssh "conduit/internal/ssh"

	"github.com/spf13/cobra"
)

var (
	sshKeysPath    string
	sshHostKeyPath string
)

var sshKeysCmd = &cobra.Command{
	Use:   "ssh-keys",
	Short: "Manage SSH authorized keys",
	Long:  "Add, list, and remove SSH public keys for TUI SSH access.",
}

var sshKeysListCmd = &cobra.Command{
	Use:   "list",
	Short: "List authorized SSH public keys",
	RunE: func(cmd *cobra.Command, args []string) error {
		entries, err := internalssh.ListAuthorizedKeys(sshKeysPath)
		if err != nil {
			return fmt.Errorf("failed to list keys: %w", err)
		}

		if len(entries) == 0 {
			fmt.Println("No authorized keys found.")
			fmt.Println("Add one with: conduit ssh-keys add <key-file-or-string>")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "FINGERPRINT\tCOMMENT")
		fmt.Fprintln(w, "-----------\t-------")
		for _, entry := range entries {
			comment := entry.Comment
			if comment == "" {
				comment = "(no comment)"
			}
			fmt.Fprintf(w, "%s\t%s\n", entry.Fingerprint, comment)
		}
		w.Flush()
		return nil
	},
}

var sshKeysAddCmd = &cobra.Command{
	Use:   "add <key-file-or-string>",
	Short: "Add an SSH public key",
	Long: `Add an SSH public key to the authorized keys list.
The argument can be a path to a public key file (e.g., ~/.ssh/id_ed25519.pub)
or the key string itself.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		keyData := args[0]

		// Check if it's a file path
		if _, err := os.Stat(keyData); err == nil {
			data, err := os.ReadFile(keyData)
			if err != nil {
				return fmt.Errorf("failed to read key file: %w", err)
			}
			keyData = strings.TrimSpace(string(data))
		}

		if err := internalssh.AddAuthorizedKey(sshKeysPath, keyData); err != nil {
			return err
		}

		fmt.Println("SSH public key added successfully.")
		return nil
	},
}

var sshKeysRemoveCmd = &cobra.Command{
	Use:   "remove <fingerprint>",
	Short: "Remove an SSH public key by fingerprint",
	Long: `Remove an SSH public key from the authorized keys list.
Use 'conduit ssh-keys list' to find the fingerprint.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := internalssh.RemoveAuthorizedKey(sshKeysPath, args[0]); err != nil {
			return err
		}
		fmt.Println("SSH public key removed successfully.")
		return nil
	},
}

var sshKeysInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize SSH key infrastructure",
	Long:  "Create the host key directory and empty authorized_keys file.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return internalssh.InitSSHKeys(sshHostKeyPath, sshKeysPath)
	},
}

func init() {
	sshKeysCmd.PersistentFlags().StringVar(&sshKeysPath, "authorized-keys", "", "Path to authorized_keys file (default: ~/.conduit/authorized_keys)")
	sshKeysInitCmd.Flags().StringVar(&sshHostKeyPath, "host-key", "", "Path to SSH host key (default: ~/.conduit/ssh_host_key)")

	sshKeysCmd.AddCommand(sshKeysListCmd)
	sshKeysCmd.AddCommand(sshKeysAddCmd)
	sshKeysCmd.AddCommand(sshKeysRemoveCmd)
	sshKeysCmd.AddCommand(sshKeysInitCmd)
}
