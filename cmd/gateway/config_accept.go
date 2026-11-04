package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"conduit/internal/config"
	"conduit/internal/datadir"

	"github.com/spf13/cobra"
)

// ConfigRootCmd is `conduit config`.
func ConfigRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Approve the gateway config file",
	}
	cmd.AddCommand(configAcceptCmd())
	return cmd
}

func configAcceptCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "accept",
		Short: "Approve the config file as it is now, so the gateway starts with it",
		Long: `The gateway only starts with the config file the owner last approved:
either written by an approved update_config, or accepted here. After you edit
the config file by hand, run this to review the change (key by key, secrets
redacted) and approve it.

It refuses to run from inside the agent's own tools (the Bash tool, or any
process the gateway started), so the agent cannot approve its own change.`,
		Example: `  conduit --config ~/app/config.json config accept
  conduit --config ~/app/config.json config accept --yes`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigAccept(acceptEnv{
				path:        cfgFile,
				stateDir:    acceptStateDir(cfgFile),
				yes:         yes,
				in:          os.Stdin,
				out:         cmd.OutOrStdout(),
				interactive: stdinIsTerminal(),
				agentShell:  agentShellReason(),
			})
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "approve without asking (for scripts run by you, not the agent)")
	return cmd
}

// acceptEnv is everything runConfigAccept reads from the process.
type acceptEnv struct {
	path, stateDir string
	yes            bool
	in             io.Reader
	out            io.Writer
	interactive    bool   // stdin is a terminal
	agentShell     string // non-empty: why this looks like the agent's shell
}

var errNotApproved = errors.New("not approved; nothing changed")

func runConfigAccept(e acceptEnv) error {
	if e.agentShell != "" {
		return fmt.Errorf("refusing to approve the config from inside the gateway's own tools (%s); run it yourself in a terminal", e.agentShell)
	}
	data, err := os.ReadFile(e.path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	if _, err := config.Parse(data); err != nil {
		return fmt.Errorf("%s does not load, so it cannot be approved; fix it first: %w", e.path, err)
	}
	snap, problem := config.ReadApprovedSnapshot(e.path, e.stateDir)
	switch {
	case problem != "":
		fmt.Fprintf(e.out, "The approved snapshot cannot be used: %s.\nApproving replaces it with the whole current file.\n", problem)
	case snap == nil:
		fmt.Fprintf(e.out, "No approved snapshot yet. Approving records the whole current file.\n")
	case string(snap) == string(data):
		fmt.Fprintf(e.out, "%s is already the approved config.\n", e.path)
		return config.RecordApproved(e.path, e.stateDir, data) // restores a missing hash record
	default:
		printConfigDiff(e.out, snap, data)
	}

	if !e.yes {
		if !e.interactive {
			return errors.New("stdin is not a terminal: run this in a terminal to confirm, or pass --yes")
		}
		fmt.Fprintf(e.out, "Approve %s? Type yes to approve: ", e.path)
		line, _ := bufio.NewReader(e.in).ReadString('\n')
		if strings.TrimSpace(strings.ToLower(line)) != "yes" {
			return errNotApproved
		}
	}
	if err := config.RecordApproved(e.path, e.stateDir, data); err != nil {
		return err
	}
	log.Printf("config accept: approved %s", e.path)
	fmt.Fprintf(e.out, "Approved. The gateway will start with this config.\n")
	return nil
}

func printConfigDiff(out io.Writer, old, next []byte) {
	lines := config.DiffConfigFiles(old, next)
	fmt.Fprintf(out, "Changes since the last approved config (%d):\n", len(lines))
	for _, l := range lines {
		note := ""
		if l.Risk != "" {
			note = "   SECURITY-SENSITIVE: " + l.Risk
		}
		fmt.Fprintf(out, "  %s: %s -> %s%s\n", l.Path, l.Old, l.New, note)
	}
	if oc, err := config.Parse(old); err == nil {
		if nc, err := config.Parse(next); err == nil {
			for _, w := range config.ApprovalWeakenings(oc, nc) {
				fmt.Fprintf(out, "  WARNING: LOWERS HUMAN APPROVAL: %s\n", w)
			}
		}
	}
}

// verifyApprovedConfig is the server's startup check.
func verifyApprovedConfig(path string, cfg *config.Config) error {
	state, err := config.VerifyApproved(path, approvedStateDirFor(cfg))
	if err != nil {
		return err
	}
	if state == config.ApprovedBootstrapped {
		log.Printf("Recorded %s as the approved config (first start); later changes need update_config or `conduit config accept`", path)
	}
	return nil
}

func acceptStateDir(path string) string {
	return approvedStateDirFor(loadConfigIfPresent(path))
}

// approvedStateDirFor mirrors gateway.approvedStateDir.
func approvedStateDirFor(cfg *config.Config) string {
	dataDir := ""
	if cfg != nil {
		dataDir = cfg.DataDir
	}
	dd, err := datadir.New(dataDir)
	if err != nil {
		return dataDir
	}
	return dd.ConfigDir()
}

func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// agentShellReason reports why this process looks like it was started by
// the gateway (the agent's Bash tool), or "".
func agentShellReason() string {
	if os.Getenv(config.ToolShellEnv) != "" {
		return config.ToolShellEnv + " is set"
	}
	if gatewayAncestor() {
		return "a parent process is the conduit gateway"
	}
	return ""
}
