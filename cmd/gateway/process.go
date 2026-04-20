package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// isAncestorPID reports whether candidate is pid or any ancestor of pid by
// walking /proc/<pid>/status PPid fields. Guards against the footgun where
// the gateway (or one of its tool-subprocess descendants) invokes
// `conduit restart|stop` and ends up signalling its own parent tree — which
// kills every descendant including the signaller.
func isAncestorPID(candidate int) bool {
	if candidate <= 1 {
		return false
	}
	pid := os.Getpid()
	for i := 0; i < 64 && pid > 1; i++ {
		if pid == candidate {
			return true
		}
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
		if err != nil {
			return false
		}
		var next int
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "PPid:") {
				fmt.Sscanf(strings.TrimSpace(strings.TrimPrefix(line, "PPid:")), "%d", &next)
				break
			}
		}
		if next == 0 || next == pid {
			return false
		}
		pid = next
	}
	return false
}

var restartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Send restart signal to running Conduit process",
	Long:  "Sends SIGHUP to the running Conduit process, triggering a graceful drain and restart.",
	RunE: func(cmd *cobra.Command, args []string) error {
		pid, err := readPidfile(resolvePidfilePath())
		if err != nil {
			return err
		}
		if isAncestorPID(pid) {
			return fmt.Errorf("refusing to signal PID %d: target is an ancestor of this process (self-kill); use the in-process gateway_control tool or `systemctl restart conduit`", pid)
		}
		if err := syscall.Kill(pid, syscall.SIGHUP); err != nil {
			return fmt.Errorf("failed to send SIGHUP to PID %d: %w", pid, err)
		}
		fmt.Printf("Restart signal sent to PID %d\n", pid)
		return nil
	},
}

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Send stop signal to running Conduit process",
	Long:  "Sends SIGTERM to the running Conduit process, triggering a graceful shutdown.",
	RunE: func(cmd *cobra.Command, args []string) error {
		pid, err := readPidfile(resolvePidfilePath())
		if err != nil {
			return err
		}
		if isAncestorPID(pid) {
			return fmt.Errorf("refusing to signal PID %d: target is an ancestor of this process (self-kill); use `systemctl stop conduit`", pid)
		}
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
			return fmt.Errorf("failed to send SIGTERM to PID %d: %w", pid, err)
		}
		fmt.Printf("Stop signal sent to PID %d\n", pid)
		return nil
	},
}

var processStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check if Conduit is running",
	RunE: func(cmd *cobra.Command, args []string) error {
		pidpath := resolvePidfilePath()
		pid, err := readPidfile(pidpath)
		if err != nil {
			fmt.Println("Conduit is not running (no pidfile)")
			os.Exit(1)
			return nil
		}
		if err := syscall.Kill(pid, 0); err != nil {
			fmt.Printf("Conduit is not running (stale pidfile at %s, PID %d)\n", pidpath, pid)
			os.Exit(1)
			return nil
		}
		fmt.Printf("Conduit is running (PID %d)\n", pid)

		statusPort, _ := cmd.Flags().GetInt("port")
		client := &http.Client{Timeout: 2 * time.Second}
		resp, err := client.Get(fmt.Sprintf("http://localhost:%d/health", statusPort))
		if err != nil {
			fmt.Printf("  Health check failed: %v\n", err)
			return nil
		}
		defer resp.Body.Close()

		var health map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&health); err == nil {
			if s, ok := health["status"].(string); ok {
				fmt.Printf("  Status: %s\n", s)
			}
			if v, ok := health["version"].(string); ok && v != "" {
				fmt.Printf("  Version: %s\n", v)
			}
			if u, ok := health["uptime"].(string); ok && u != "" {
				fmt.Printf("  Uptime: %s\n", u)
			}
		}
		return nil
	},
}

func readPidfile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("cannot read pidfile %s: %w", path, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("invalid PID in %s", path)
	}
	return pid, nil
}

func resolvePidfilePath() string {
	if p, _ := rootCmd.PersistentFlags().GetString("pidfile"); p != "" {
		return p
	}
	return "/tmp/conduit.pid"
}

func init() {
	processStatusCmd.Flags().Int("port", 18789, "gateway port for health check")
	rootCmd.AddCommand(restartCmd)
	rootCmd.AddCommand(stopCmd)
	rootCmd.AddCommand(processStatusCmd)
}
