package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"conduit/internal/backup"

	"github.com/spf13/cobra"
)

var backupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Backup and restore gateway data",
	Long:  `Create, restore, and inspect portable snapshots of the gateway database, config, workspace, and optional SSH keys and skills.`,
}

// backup create flags
var (
	backupOutput     string
	backupSSHKeys    bool
	backupSkills     bool
	backupJSONOutput bool
)

var backupCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a backup archive",
	Long:  `Create a .tar.gz archive containing the gateway database, config, and workspace files.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		opts := backup.BackupOptions{
			ConfigPath:     cfgFile,
			OutputPath:     backupOutput,
			IncludeSSHKeys: backupSSHKeys,
			IncludeSkills:  backupSkills,
			Verbose:        verbose,
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		result, err := backup.CreateBackup(ctx, opts)
		if err != nil {
			return fmt.Errorf("backup failed: %w", err)
		}

		if backupJSONOutput {
			return json.NewEncoder(os.Stdout).Encode(result)
		}

		fmt.Printf("Backup created: %s\n", result.ArchivePath)
		fmt.Printf("Files: %d\n", result.FileCount)
		fmt.Printf("Size: %s\n", formatSize(result.TotalSize))
		fmt.Printf("Components: %s\n", result.Components)
		fmt.Printf("Duration: %v\n", result.Duration.Round(time.Millisecond))

		for _, w := range result.Warnings {
			fmt.Printf("WARNING: %s\n", w)
		}

		return nil
	},
}

// backup restore flags
var (
	restoreDryRun     bool
	restoreForce      bool
	restoreSkipConfig bool
	restoreSSHKeys    bool
	restoreConfigPath string
	restoreDBPath     string
	restoreWSPath     string
	restoreJSONOutput bool
)

var backupRestoreCmd = &cobra.Command{
	Use:   "restore <backup-file>",
	Short: "Restore from a backup archive",
	Long:  `Restore gateway data from a previously created backup archive.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		opts := backup.RestoreOptions{
			BackupPath:     args[0],
			DryRun:         restoreDryRun,
			Force:          restoreForce,
			SkipConfig:     restoreSkipConfig,
			RestoreSSHKeys: restoreSSHKeys,
			ConfigPath:     restoreConfigPath,
			DatabasePath:   restoreDBPath,
			WorkspacePath:  restoreWSPath,
			Verbose:        verbose,
		}

		result, err := backup.RestoreBackup(opts)
		if err != nil {
			return fmt.Errorf("restore failed: %w", err)
		}

		if restoreJSONOutput {
			return json.NewEncoder(os.Stdout).Encode(result)
		}

		if !opts.DryRun {
			fmt.Printf("Restore complete.\n")
			fmt.Printf("Files restored: %d\n", result.FilesRestored)
			fmt.Printf("Files skipped: %d\n", result.FilesSkipped)
			fmt.Printf("Components: %s\n", result.Components)
		}

		for _, w := range result.Warnings {
			fmt.Printf("WARNING: %s\n", w)
		}

		return nil
	},
}

// backup list flags
var (
	listJSONOutput bool
	listVerbose    bool
)

var backupListCmd = &cobra.Command{
	Use:   "list <backup-file>",
	Short: "Inspect a backup archive",
	Long:  `Display the contents and metadata of a backup archive.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		opts := backup.ListOptions{
			BackupPath: args[0],
			JSONOutput: listJSONOutput,
			Verbose:    listVerbose || verbose,
		}

		result, err := backup.ListBackup(opts)
		if err != nil {
			return fmt.Errorf("list failed: %w", err)
		}

		return backup.PrintListResult(result, opts)
	},
}

func init() {
	// Create subcommand flags
	backupCreateCmd.Flags().StringVarP(&backupOutput, "output", "o", "", "Output file path (default: conduit-backup-YYYYMMDD-HHMMSS.tar.gz)")
	backupCreateCmd.Flags().BoolVar(&backupSSHKeys, "include-ssh-keys", false, "Include SSH keys in backup")
	backupCreateCmd.Flags().BoolVar(&backupSkills, "include-skills", false, "Include custom skills directories")
	backupCreateCmd.Flags().BoolVar(&backupJSONOutput, "json", false, "Output results in JSON format")

	// Restore subcommand flags
	backupRestoreCmd.Flags().BoolVar(&restoreDryRun, "dry-run", false, "Preview restore without writing files")
	backupRestoreCmd.Flags().BoolVar(&restoreForce, "force", false, "Skip confirmation prompt")
	backupRestoreCmd.Flags().BoolVar(&restoreSkipConfig, "skip-config", false, "Don't restore config file")
	backupRestoreCmd.Flags().BoolVar(&restoreSSHKeys, "restore-ssh-keys", false, "Restore SSH keys (explicit opt-in)")
	backupRestoreCmd.Flags().StringVar(&restoreConfigPath, "config-path", "", "Override config destination path")
	backupRestoreCmd.Flags().StringVar(&restoreDBPath, "database-path", "", "Override database destination path")
	backupRestoreCmd.Flags().StringVar(&restoreWSPath, "workspace-path", "", "Override workspace destination path")
	backupRestoreCmd.Flags().BoolVar(&restoreJSONOutput, "json", false, "Output results in JSON format")

	// List subcommand flags
	backupListCmd.Flags().BoolVar(&listJSONOutput, "json", false, "Output in JSON format")
	backupListCmd.Flags().BoolVar(&listVerbose, "verbose", false, "Show all files in archive")

	// Wire subcommands
	backupCmd.AddCommand(backupCreateCmd)
	backupCmd.AddCommand(backupRestoreCmd)
	backupCmd.AddCommand(backupListCmd)

	// Add to root
	rootCmd.AddCommand(backupCmd)
}

func formatSize(b int64) string {
	switch {
	case b >= 1024*1024*1024:
		return fmt.Sprintf("%.1f GB", float64(b)/(1024*1024*1024))
	case b >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(b)/(1024*1024))
	case b >= 1024:
		return fmt.Sprintf("%.1f KB", float64(b)/1024)
	default:
		return fmt.Sprintf("%d B", b)
	}
}
