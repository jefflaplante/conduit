package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"conduit/internal/procutil"

	tea "github.com/charmbracelet/bubbletea"
)

// TruncateOutput truncates output if it exceeds maxLines
// Returns the truncated output and the number of remaining lines
func TruncateOutput(output string, maxLines int) (string, int) {
	lines := strings.Split(output, "\n")
	if len(lines) <= maxLines {
		return output, 0
	}
	truncated := strings.Join(lines[:maxLines], "\n")
	remaining := len(lines) - maxLines
	return truncated + fmt.Sprintf("\n[truncated, %d more lines]", remaining), remaining
}

// ShellResultWithDirMsg delivers the result of a shell escape command with directory info
type ShellResultWithDirMsg struct {
	SessionKey string
	Output     string
	Err        error
	NewDir     string // Updated directory (for pwd command output, etc.)
}

// ShellStreamMsg delivers streaming output from a running command
type ShellStreamMsg struct {
	SessionKey string
	Line       string
	IsStderr   bool
}

// ShellCommandCancelledMsg signals that a command was cancelled
type ShellCommandCancelledMsg struct {
	SessionKey string
}

// runShellCommand runs cmdLine under `sh -c` and returns its result message.
//
// conduit-31jg.69: stdout and stderr go straight into one bounded
// procutil.CappedBuffer via cmd.Stdout/cmd.Stderr instead of StdoutPipe +
// line readers. The old code waited for its readers before cmd.Wait, so a
// background child still holding the pipe after a normal exit (`sleep 60 &`)
// blocked forever and WaitDelay never applied; it also buffered without
// limit and raced two goroutines on one strings.Builder. Now exec's own copy
// goroutines are bounded by WaitDelay (set by ConfigureGroupKill), and an
// ErrWaitDelay-only result counts as success.
func runShellCommand(sessionKey, cmdLine, workDir string, env []string, timeout time.Duration) ShellResultMsg {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", cmdLine)
	cmd.Dir = workDir
	if env != nil {
		cmd.Env = env
	}
	procutil.ConfigureGroupKill(cmd, 0, 0) // conduit-31jg.20: kill the whole group on timeout/cancel

	output := procutil.NewCappedBuffer(MaxOutputBytes)
	cmd.Stdout = output
	cmd.Stderr = output

	err := cmd.Run()
	if procutil.IsWaitDelayOnly(err) {
		err = nil // exited cleanly; a background child kept the pipe open
	}

	// Check if we timed out
	if ctx.Err() == context.DeadlineExceeded {
		return ShellResultMsg{
			SessionKey: sessionKey,
			Output:     output.String() + fmt.Sprintf("\n[command timed out after %s]", timeout),
			Err:        ctx.Err(),
		}
	}

	// Truncate output if needed
	finalOutput := output.String()
	truncated, remaining := TruncateOutput(finalOutput, MaxOutputLines)
	if remaining > 0 {
		finalOutput = truncated
	}

	return ShellResultMsg{
		SessionKey: sessionKey,
		Output:     finalOutput,
		Err:        err,
	}
}

// executeShellCmdWithEnv returns a tea.Cmd that executes a shell command with custom environment
func executeShellCmdWithEnv(sessionKey, cmdLine, workDir string, envVars map[string]string) tea.Cmd {
	return func() tea.Msg {
		env := os.Environ()
		for k, v := range envVars {
			env = append(env, k+"="+v)
		}
		return runShellCommand(sessionKey, cmdLine, workDir, env, DefaultCommandTimeout)
	}
}
