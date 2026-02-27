package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	internalssh "conduit/internal/ssh"
	"conduit/internal/tui"

	"github.com/spf13/cobra"
)

var (
	sshListen         string
	sshHostKey        string
	sshAuthorizedKeys string
	sshGatewayURL     string
	sshGatewayToken   string
)

var sshCmd = &cobra.Command{
	Use:   "ssh-server",
	Short: "Start standalone SSH server for TUI access",
	Long: `Start a standalone SSH server that serves the Conduit TUI over SSH.
Users connect with their SSH client and get a full terminal chat interface.

  ssh -p 2222 user@localhost

The SSH server connects to the running gateway over WebSocket, so the gateway
must be running separately. The gateway port is read from --config automatically.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		effectiveGatewayURL := sshGatewayURL

		// Derive gateway URL and agent name from config if --gateway-url wasn't explicitly set
		assistantName := ""
		if !cmd.Flags().Changed("gateway-url") {
			gatewayCfg, err := loadGatewayConfigForTUI(cfgFile)
			if err != nil {
				log.Printf("Warning: could not read gateway config %s: %v (using default URL)", cfgFile, err)
				effectiveGatewayURL = "ws://localhost:18789/ws"
			} else {
				effectiveGatewayURL = fmt.Sprintf("ws://localhost:%d/ws", gatewayCfg.Port)
				log.Printf("Using gateway port %d from %s", gatewayCfg.Port, cfgFile)
				assistantName = gatewayCfg.Agent.Name
			}
		}

		// Auto-detect token from saved TUI config if not explicitly provided
		effectiveToken := sshGatewayToken
		if effectiveToken == "" {
			if saved, ok := tui.LoadSavedToken(); ok {
				effectiveToken = saved
				log.Printf("Using saved token from ~/.conduit/tui.json")
			}
		}
		if effectiveToken == "" {
			return fmt.Errorf("no gateway token available; use --gateway-token or run 'conduit tui' first to save a token")
		}

		// Load timezone from config if available
		var location *time.Location
		if gatewayCfg, err := loadGatewayConfigForTUI(cfgFile); err == nil {
			location = gatewayCfg.GetLocation()
		}

		config := internalssh.SSHConfig{
			ListenAddr:         sshListen,
			HostKeyPath:        sshHostKey,
			AuthorizedKeysPath: sshAuthorizedKeys,
			GatewayURL:         effectiveGatewayURL,
			GatewayToken:       effectiveToken,
			AssistantName:      assistantName,
			Location:           location,
		}

		server, err := internalssh.NewServer(config)
		if err != nil {
			return err
		}

		// Graceful shutdown
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

		go func() {
			<-sigCh
			log.Println("Shutting down SSH server...")
			cancel()
			server.Close()
		}()

		log.Printf("SSH server listening on %s", config.ListenAddr)
		if err := server.ListenAndServe(); err != nil {
			select {
			case <-ctx.Done():
				return nil // graceful shutdown
			default:
				return err
			}
		}
		return nil
	},
}

func init() {
	sshCmd.Flags().StringVar(&sshListen, "listen", ":2222", "SSH listen address")
	sshCmd.Flags().StringVar(&sshHostKey, "host-key", "", "Path to SSH host key (auto-generated if not specified)")
	sshCmd.Flags().StringVar(&sshAuthorizedKeys, "authorized-keys", "", "Path to authorized_keys file")
	sshCmd.Flags().StringVar(&sshGatewayURL, "gateway-url", "", "Gateway WebSocket URL (default: derived from --config port)")
	sshCmd.Flags().StringVar(&sshGatewayToken, "gateway-token", "", "Gateway authentication token")
}
