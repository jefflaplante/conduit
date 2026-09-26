package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"conduit/internal/auth"
	"conduit/internal/config"
	"conduit/internal/datadir"
	"conduit/internal/gateway"
	internalssh "conduit/internal/ssh"
	"conduit/internal/version"

	"github.com/spf13/cobra"
)

var (
	cfgFile string
	dbPath  string
	verbose bool
	port    int

	// tokenCLIConfig is shared with the `token` command tree.
	tokenCLIConfig = &auth.CLIConfig{}
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "conduit",
	Short: "Conduit Gateway - WebSocket gateway with authentication",
	Long: `Conduit Gateway is a high-performance WebSocket gateway server
that provides secure authentication and message routing for Conduit clients.

The gateway can run as a server or be used as a CLI tool for token management.`,
	Version: version.Full(),
}

// serverCmd represents the server command
var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Start the Conduit Gateway server",
	Long: `Start the Conduit Gateway WebSocket server. This is the main server mode
that accepts client connections and handles message routing.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runServer()
	},
}

// versionCmd represents the version command
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show version information",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Printf("Conduit Gateway %s\n", version.Full())
		buildInfo := version.GetBuildInfo()

		if buildInfo.GitCommit != "unknown" {
			fmt.Printf("Git commit: %s\n", buildInfo.GitCommit)
		}
		if buildInfo.GitTag != "" {
			fmt.Printf("Git tag: %s\n", buildInfo.GitTag)
		}
		if buildInfo.GitDirty {
			fmt.Printf("Git status: dirty (uncommitted changes)\n")
		}
		if buildInfo.BuildDate != "unknown" {
			fmt.Printf("Build date: %s\n", buildInfo.BuildDate)
		}
		fmt.Printf("Go version: %s\n", buildInfo.GoVersion)

		return nil
	},
}

func init() {
	cobra.OnInitialize(initConfig)

	// Global flags
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "config.json", "config file path")
	rootCmd.PersistentFlags().StringVar(&dbPath, "database", "", "database file path (auto-detected if not specified)")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "enable verbose logging")
	rootCmd.PersistentFlags().String("pidfile", "", "path to PID file (default: /tmp/conduit.pid)")

	// Server command flags
	serverCmd.Flags().IntVarP(&port, "port", "p", 18789, "WebSocket server port")

	// Add server command
	rootCmd.AddCommand(serverCmd)

	// Add version command
	rootCmd.AddCommand(versionCmd)

	// Add token management commands (populated in PersistentPreRunE).
	rootCmd.AddCommand(auth.TokenRootCmd(tokenCLIConfig))

	// Add pairing management commands
	rootCmd.AddCommand(PairingRootCmd("", false)) // Will be updated in PersistentPreRunE

	// Add tools discovery commands
	rootCmd.AddCommand(ToolsRootCmd())

	// Add TUI command
	rootCmd.AddCommand(tuiCmd)

	// Add OAuth auth commands
	rootCmd.AddCommand(AuthRootCmd())

	// Add SSH commands
	rootCmd.AddCommand(sshCmd)
	rootCmd.AddCommand(sshKeysCmd)
	rootCmd.AddCommand(BrainRootCmd())

	// If no command is specified, default to server
	rootCmd.RunE = func(cmd *cobra.Command, args []string) error {
		return serverCmd.RunE(cmd, args)
	}
}

func initConfig() {
	// Load .env files early so CLI commands get env vars
	dd, err := datadir.New("")
	if err == nil {
		_ = datadir.LoadEnv(dd.Root())
	}

	// Set up CLI config for auth and pairing commands
	for _, cmd := range rootCmd.Commands() {
		if cmd.Use == "token" || cmd.Use == "pairing" {
			// Auto-detect database path if not specified
			if dbPath == "" {
				// Auto-detect database path based on config
				if cfgFile != "" && cfgFile != "config.json" {
					// Custom config file specified, derive database name from it
					dir := filepath.Dir(cfgFile)
					base := filepath.Base(cfgFile)
					ext := filepath.Ext(base)
					name := base[:len(base)-len(ext)]
					dbPath = filepath.Join(dir, name+".db")
				} else {
					// Default config file, use standard database name
					dbPath = "gateway.db"
				}
			}
		}
	}

	if verbose {
		log.SetFlags(log.LstdFlags | log.Lshortfile)
		log.Println("Verbose logging enabled")
	}
}

func isContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	return os.Getenv("KUBERNETES_SERVICE_HOST") != ""
}

// isUnderSystemd reports whether the current process was launched by systemd.
// systemd sets INVOCATION_ID on every unit invocation; it is the canonical
// signal per systemd.exec(5). Falling back to PPID==1 catches the (rare) case
// where the variable was stripped but the parent is still PID 1.
func isUnderSystemd() bool {
	if os.Getenv("INVOCATION_ID") != "" {
		return true
	}
	return os.Getppid() == 1
}

func reExec() {
	exe, err := os.Executable()
	if err != nil {
		log.Fatalf("Failed to resolve executable path: %v", err)
	}
	log.Printf("Re-executing %s", exe)
	if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
		log.Fatalf("Failed to re-exec: %v", err)
	}
}

func runServer() error {
	// Initialize data directory and load .env files before config
	// so environment variables are available for ${ENV_VAR} expansion.
	dd, err := datadir.New("")
	if err != nil {
		log.Printf("WARNING: Could not resolve data directory: %v", err)
	} else {
		if err := dd.EnsureDirs(); err != nil {
			log.Printf("WARNING: Could not create data directories: %v", err)
		}

		if err := datadir.LoadEnv(dd.Root()); err != nil {
			log.Printf("WARNING: Failed to load .env files: %v", err)
		}
	}

	// Load and validate configuration. Validation errors are printed directly
	// to stderr and exit 1 so the operator sees an actionable message without
	// cobra wrapping it in an extra "Error: ..." prefix.
	cfg, err := config.Load(cfgFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}

	// Wire data_dir into SSH key resolution
	internalssh.DataDirConfig = cfg.DataDir

	// Override port if specified
	if port != 18789 {
		cfg.Port = port
	}

	// Create gateway instance
	gw, err := gateway.New(cfg)
	if err != nil {
		return fmt.Errorf("failed to create gateway: %w", err)
	}

	// Write pidfile
	pidPath := resolvePidfilePath()
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0644); err != nil {
		log.Printf("Warning: failed to write pidfile %s: %v", pidPath, err)
	} else {
		defer os.Remove(pidPath)
		log.Printf("PID %d written to %s", os.Getpid(), pidPath)
	}

	// Setup graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var gatewayReady atomic.Bool

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigCh)

	// conduit-31jg.27: SIGTERM/SIGINT go through ShutdownManager's drain
	// (bounded, under systemd's TimeoutStopSec) instead of cancelling
	// immediately; a second one forces exit. See signals.go.
	sm := gw.ShutdownManager()
	sigCtl := newSignalController(sm, cancel, &gatewayReady)
	go func() {
		for sig := range sigCh {
			sigCtl.handle(sig)
		}
	}()

	// Start the gateway
	log.Printf("Starting Conduit Gateway on port %d", cfg.Port)
	gatewayReady.Store(true)
	if err := gw.Start(ctx); err != nil {
		return fmt.Errorf("gateway failed: %w", err)
	}

	// If a drain was in progress, let the shutdown sequence finish (it may
	// re-exec on SIGHUP) before main returns and the process exits.
	if sm.State() != gateway.StateRunning {
		select {
		case <-sm.Done():
		case <-time.After(5 * time.Second):
			log.Println("Timed out waiting for shutdown sequence to finish")
		}
	}

	log.Println("Gateway stopped gracefully")
	return nil
}

func main() {
	// Update CLI config with actual values after flags are parsed
	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		// conduit-31jg.3: token commands load the server's config (--config)
		// and use its database.path + token secret via auth.ResolveTokenStore.
		// Only an explicit --database overrides the path; the filename-derived
		// auto-detection below does not match what the server opens.
		tokenCLIConfig.ConfigPath = cfgFile
		tokenCLIConfig.Verbose = verbose
		if rootCmd.PersistentFlags().Changed("database") {
			tokenCLIConfig.DatabasePath = dbPath
		}

		// Auto-detect database path if still empty
		if dbPath == "" {
			if cfgFile != "" && cfgFile != "config.json" {
				// Custom config file specified, derive database name from it
				dir := filepath.Dir(cfgFile)
				base := filepath.Base(cfgFile)
				ext := filepath.Ext(base)
				name := base[:len(base)-len(ext)]
				dbPath = filepath.Join(dir, name+".db")
			} else {
				// Default config file, use standard database name
				dbPath = "gateway.db"
			}
		}

		for _, subCmd := range rootCmd.Commands() {
			if subCmd.Use == "pairing" {
				// Update the pairing CLI config
				updatePairingConfig(subCmd, dbPath, verbose)
			}
		}
		return nil
	}

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// updatePairingConfig recursively updates pairing CLI config in pairing command tree
func updatePairingConfig(cmd *cobra.Command, dbPath string, verbose bool) {
	// Use the same environment variables as auth commands for consistency
	os.Setenv("CONDUIT_DB_PATH", dbPath)
	os.Setenv("CONDUIT_VERBOSE", fmt.Sprintf("%t", verbose))

	if verbose {
		log.Printf("main.go:updatePairingConfig: Set CONDUIT_DB_PATH=%s", dbPath)
	}

	// Recursively update subcommands
	for _, subCmd := range cmd.Commands() {
		updatePairingConfig(subCmd, dbPath, verbose)
	}
}
