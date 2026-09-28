package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

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

	// Success - update state (preserve EnvVars and Jobs)
	newState := ShellState{
		CurrentDir:    newDir,
		PrevDir:       s.CurrentDir,
		EnvVars:       s.EnvVars,
		Jobs:          s.Jobs,
		RunningCmd:    s.RunningCmd,
		RunningCancel: s.RunningCancel,
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

// ExpandVariables expands $VAR and ${VAR} references in a string
func (s ShellState) ExpandVariables(input string) string {
	if s.EnvVars == nil {
		return input
	}

	result := input

	// First handle ${VAR} style (must do this first to avoid partial matches)
	for name, value := range s.EnvVars {
		result = strings.ReplaceAll(result, "${"+name+"}", value)
	}

	// Handle $VAR style by scanning left to right
	// Build the result character by character to avoid replacement order issues
	var sb strings.Builder
	i := 0
	for i < len(result) {
		if result[i] == '$' && i+1 < len(result) {
			// Try to match a variable name
			j := i + 1
			// Variable names start with letter or underscore
			if (result[j] >= 'a' && result[j] <= 'z') ||
				(result[j] >= 'A' && result[j] <= 'Z') ||
				result[j] == '_' {
				// Consume alphanumeric and underscore
				for j < len(result) &&
					((result[j] >= 'a' && result[j] <= 'z') ||
						(result[j] >= 'A' && result[j] <= 'Z') ||
						(result[j] >= '0' && result[j] <= '9') ||
						result[j] == '_') {
					j++
				}
				varName := result[i+1 : j]
				if value, ok := s.EnvVars[varName]; ok {
					sb.WriteString(value)
					i = j
					continue
				}
			}
		}
		sb.WriteByte(result[i])
		i++
	}

	return sb.String()
}

// IsExportCommand checks if the command is an export command
// Returns (isExport, varName, value, showOnly)
// If showOnly is true, this is "export" or "export VAR" to show values
func IsExportCommand(cmdLine string) (bool, string, string, bool) {
	cmdLine = strings.TrimSpace(cmdLine)

	// Check for bare "export" to show all variables
	if cmdLine == "export" {
		return true, "", "", true
	}

	// Must start with "export "
	if !strings.HasPrefix(cmdLine, "export ") {
		return false, "", "", false
	}

	rest := strings.TrimSpace(cmdLine[7:])
	if rest == "" {
		return true, "", "", true
	}

	// Check for VAR=value format
	if idx := strings.Index(rest, "="); idx != -1 {
		varName := strings.TrimSpace(rest[:idx])
		value := rest[idx+1:]
		// Remove surrounding quotes if present
		value = strings.Trim(value, `"'`)
		return true, varName, value, false
	}

	// Just "export VAR" to show a specific variable
	return true, rest, "", true
}

// HandleExportCommand processes an export command
// Returns (newState, output) where output is for showing variables
func (s ShellState) HandleExportCommand(varName, value string, showOnly bool) (ShellState, string) {
	newState := ShellState{
		CurrentDir:    s.CurrentDir,
		PrevDir:       s.PrevDir,
		EnvVars:       s.EnvVars,
		Jobs:          s.Jobs,
		RunningCmd:    s.RunningCmd,
		RunningCancel: s.RunningCancel,
	}

	if newState.EnvVars == nil {
		newState.EnvVars = make(map[string]string)
	}

	if showOnly {
		if varName == "" {
			// Show all variables
			if len(newState.EnvVars) == 0 {
				return newState, "(no exported variables)"
			}
			var lines []string
			for k, v := range newState.EnvVars {
				lines = append(lines, fmt.Sprintf("%s=%s", k, v))
			}
			return newState, strings.Join(lines, "\n")
		}
		// Show specific variable
		if val, ok := newState.EnvVars[varName]; ok {
			return newState, fmt.Sprintf("%s=%s", varName, val)
		}
		return newState, fmt.Sprintf("%s: not set", varName)
	}

	// Set the variable
	newState.EnvVars[varName] = value
	return newState, ""
}

// IsUnsetCommand checks if the command is an unset command
// Returns (isUnset, varName)
func IsUnsetCommand(cmdLine string) (bool, string) {
	cmdLine = strings.TrimSpace(cmdLine)

	if !strings.HasPrefix(cmdLine, "unset ") {
		return false, ""
	}

	varName := strings.TrimSpace(cmdLine[6:])
	if varName == "" {
		return false, ""
	}

	return true, varName
}

// HandleUnsetCommand processes an unset command
// Returns (newState, output)
func (s ShellState) HandleUnsetCommand(varName string) (ShellState, string) {
	newState := ShellState{
		CurrentDir:    s.CurrentDir,
		PrevDir:       s.PrevDir,
		EnvVars:       s.EnvVars,
		Jobs:          s.Jobs,
		RunningCmd:    s.RunningCmd,
		RunningCancel: s.RunningCancel,
	}

	if newState.EnvVars == nil {
		return newState, ""
	}

	if _, ok := newState.EnvVars[varName]; !ok {
		return newState, fmt.Sprintf("%s: not set", varName)
	}

	// Make a copy to avoid mutating shared map
	newEnv := make(map[string]string)
	for k, v := range newState.EnvVars {
		if k != varName {
			newEnv[k] = v
		}
	}
	newState.EnvVars = newEnv
	return newState, ""
}
