package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// ShellState tracks the working directory for shell escape commands
type ShellState struct {
	// CurrentDir is the current working directory for shell commands
	CurrentDir string
	// PrevDir is the previous directory (for cd -)
	PrevDir string
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
	}
}

// HandleCdCommand processes a cd command and updates the directory state.
// Returns (newState, errorMessage). If errorMessage is non-empty, the cd failed.
func (s ShellState) HandleCdCommand(args string) (ShellState, string) {
	target := strings.TrimSpace(args)

	var newDir string

	switch {
	case target == "" || target == "~":
		// cd with no args or cd ~ goes to home directory
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return s, fmt.Sprintf("cd: cannot find home directory: %v", err)
		}
		newDir = homeDir

	case target == "-":
		// cd - goes to previous directory
		if s.PrevDir == "" {
			return s, "cd: OLDPWD not set"
		}
		newDir = s.PrevDir

	case strings.HasPrefix(target, "~/"):
		// Expand ~ at the start of the path
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return s, fmt.Sprintf("cd: cannot find home directory: %v", err)
		}
		newDir = filepath.Join(homeDir, target[2:])

	case filepath.IsAbs(target):
		// Absolute path
		newDir = target

	default:
		// Relative path - resolve against current directory
		newDir = filepath.Join(s.CurrentDir, target)
	}

	// Clean the path to resolve . and ..
	newDir = filepath.Clean(newDir)

	// Verify the directory exists and is accessible
	info, err := os.Stat(newDir)
	if err != nil {
		if os.IsNotExist(err) {
			return s, fmt.Sprintf("cd: no such file or directory: %s", target)
		}
		return s, fmt.Sprintf("cd: %s: %v", target, err)
	}

	if !info.IsDir() {
		return s, fmt.Sprintf("cd: not a directory: %s", target)
	}

	// Success - update state
	newState := ShellState{
		CurrentDir: newDir,
		PrevDir:    s.CurrentDir,
	}

	return newState, ""
}

// IsCdCommand checks if the command line is a cd command.
// Returns (isCd, args) where args is the target directory if it's a cd command.
func IsCdCommand(cmdLine string) (bool, string) {
	cmdLine = strings.TrimSpace(cmdLine)

	// Check for bare "cd"
	if cmdLine == "cd" {
		return true, ""
	}

	// Check for "cd " followed by arguments
	if strings.HasPrefix(cmdLine, "cd ") {
		return true, strings.TrimSpace(cmdLine[3:])
	}

	return false, ""
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

// ShellResultWithDirMsg delivers the result of a shell escape command with directory info
type ShellResultWithDirMsg struct {
	SessionKey string
	Output     string
	Err        error
	NewDir     string // Updated directory (for pwd command output, etc.)
}

// executeShellCmdWithDir returns a tea.Cmd that executes a shell command in the specified directory
func executeShellCmdWithDir(sessionKey, cmdLine, workDir string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, "sh", "-c", cmdLine)
		cmd.Dir = workDir

		output, err := cmd.CombinedOutput()

		return ShellResultMsg{
			SessionKey: sessionKey,
			Output:     string(output),
			Err:        err,
		}
	}
}
