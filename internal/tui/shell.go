package tui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Default configuration for shell commands
const (
	// DefaultCommandTimeout is the default timeout for shell commands
	DefaultCommandTimeout = 5 * time.Minute
	// MaxOutputLines is the maximum number of lines to show before truncation
	MaxOutputLines = 100
	// MaxOutputBytes bounds the output retained per command (head + tail)
	// so `yes` or a huge cat cannot grow the TUI's memory without limit.
	// conduit-31jg.69
	MaxOutputBytes = 256 * 1024
	// JobCleanupAge is how long to keep completed jobs before cleanup
	JobCleanupAge = 10 * time.Minute
)

// ShellState tracks the working directory for shell escape commands
type ShellState struct {
	// CurrentDir is the current working directory for shell commands
	CurrentDir string
	// PrevDir is the previous directory (for cd -)
	PrevDir string
	// EnvVars holds custom environment variables set via export
	EnvVars map[string]string
	// Jobs manages background jobs for this shell session
	Jobs *JobManager
	// RunningCmd tracks a foreground command that can be cancelled
	RunningCmd *exec.Cmd
	// RunningCancel cancels the running foreground command
	RunningCancel context.CancelFunc
}

// inheritEnvironment returns a map of inherited environment variables
func inheritEnvironment() map[string]string {
	env := make(map[string]string)
	for _, e := range os.Environ() {
		if idx := strings.Index(e, "="); idx != -1 {
			env[e[:idx]] = e[idx+1:]
		}
	}
	return env
}

// NewShellState creates a new ShellState with the default directory
func NewShellState() ShellState {
	// Default to user's home directory
	homeDir, err := os.UserHomeDir()
	if err != nil {
		// Fall back to current working directory
		homeDir, _ = os.Getwd()
	}
	return ShellState{
		CurrentDir: homeDir,
		PrevDir:    homeDir,
		EnvVars:    inheritEnvironment(),
		Jobs:       NewJobManager(),
	}
}

// NewShellStateWithDir creates a ShellState with a specific starting directory
func NewShellStateWithDir(dir string) ShellState {
	if dir == "" {
		return NewShellState()
	}
	// Resolve to absolute path
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return NewShellState()
	}
	return ShellState{
		CurrentDir: absDir,
		PrevDir:    absDir,
		EnvVars:    inheritEnvironment(),
		Jobs:       NewJobManager(),
	}
}

// CancelRunningCommand cancels any foreground command that may be running
func (s *ShellState) CancelRunningCommand() bool {
	if s.RunningCancel != nil {
		s.RunningCancel()
		s.RunningCancel = nil
		s.RunningCmd = nil
		return true
	}
	return false
}

// FormatPrompt returns a shell prompt string showing the current directory.
// If abbreviateHome is true, replaces home directory with ~.
func (s ShellState) FormatPrompt(abbreviateHome bool) string {
	dir := s.CurrentDir

	if abbreviateHome {
		if homeDir, err := os.UserHomeDir(); err == nil {
			if dir == homeDir {
				dir = "~"
			} else if strings.HasPrefix(dir, homeDir+string(os.PathSeparator)) {
				dir = "~" + dir[len(homeDir):]
			}
		}
	}

	return dir + " $ "
}
