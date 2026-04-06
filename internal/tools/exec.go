package tools

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	"conduit/internal/tools/types"
)

// DefaultCommandDenylist contains patterns that should be blocked by default
// when no custom denylist is configured. These patterns match obviously
// dangerous commands that could cause system damage.
var DefaultCommandDenylist = []string{
	"rm -rf /",
	"rm -rf /*",
	"rm -rf ~",
	"rm -rf ~/*",
	"rm -rf $HOME",
	"shutdown",
	"reboot",
	"poweroff",
	"halt",
	"init 0",
	"init 6",
	"mkfs",
	"mkfs.",
	"dd if=/dev/zero",
	"dd if=/dev/random",
	"dd if=/dev/urandom",
	":(){ :|:& };:",
	"> /dev/sda",
	"chmod -R 777 /",
	"chown -R",
	"mv /* ",
	"|sh",
	"|bash",
	"| sh",
	"| bash",
	"> /etc/passwd",
	"> /etc/shadow",
	"mkswap /dev/sda",
	"fdisk",
}

// ExecTool implements command execution functionality
type ExecTool struct {
	registry *Registry
}

func (t *ExecTool) Name() string {
	return "Bash"
}

func (t *ExecTool) Description() string {
	return "Execute a shell command"
}

func (t *ExecTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{
				"type":        "string",
				"description": "Command to execute",
			},
			"cwd": map[string]interface{}{
				"type":        "string",
				"description": "Working directory (optional)",
			},
		},
		"required": []string{"command"},
	}
}

// getEffectiveDenylist returns the configured denylist or the default if none configured.
func (t *ExecTool) getEffectiveDenylist() []string {
	if len(t.registry.sandboxCfg.CommandDenylist) > 0 {
		return t.registry.sandboxCfg.CommandDenylist
	}
	return DefaultCommandDenylist
}

// checkCommandDenylist checks the command against all denylist patterns.
// Returns the matched pattern if denied, or empty string if allowed.
func (t *ExecTool) checkCommandDenylist(command string) string {
	normalized := strings.ToLower(strings.TrimSpace(command))
	// Collapse multiple spaces for more robust matching
	for strings.Contains(normalized, "  ") {
		normalized = strings.ReplaceAll(normalized, "  ", " ")
	}

	for _, pattern := range t.getEffectiveDenylist() {
		lowerPattern := strings.ToLower(pattern)
		if strings.Contains(normalized, lowerPattern) {
			return pattern
		}
	}
	return ""
}

func (t *ExecTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	command, ok := args["command"].(string)
	if !ok {
		return types.NewErrorResult("missing_parameter",
			"Command parameter is required and must be a string").
			WithParameter("command", args["command"]).
			WithExamples([]string{"ls -la", "echo 'Hello World'", "cat file.txt", "grep -n 'pattern' *.go"}).
			WithSuggestions([]string{
				"Provide a shell command to execute",
				"Use standard bash/sh syntax",
				"Commands run in sandbox environment",
			}), nil
	}

	if strings.TrimSpace(command) == "" {
		return types.NewErrorResult("invalid_parameter",
			"Command cannot be empty or contain only whitespace").
			WithParameter("command", command).
			WithExamples([]string{"ls", "pwd", "echo 'test'"}).
			WithSuggestions([]string{
				"Provide a valid shell command",
			}), nil
	}

	cwd, _ := args["cwd"].(string)
	if cwd == "" {
		cwd = t.registry.sandboxCfg.WorkspaceDir
	}

	if !t.registry.isPathAllowed(cwd) {
		return types.NewErrorResult("path_not_allowed",
			fmt.Sprintf("Working directory '%s' is not allowed in sandbox", cwd)).
			WithParameter("cwd", cwd).
			WithAvailableValues(t.registry.sandboxCfg.AllowedPaths).
			WithContext(map[string]interface{}{
				"default_cwd":   t.registry.sandboxCfg.WorkspaceDir,
				"sandbox_mode":  true,
				"allowed_paths": t.registry.sandboxCfg.AllowedPaths,
			}).
			WithSuggestions([]string{
				"Use the default workspace directory",
				"Specify a working directory within allowed paths",
				"Remove the 'cwd' parameter to use workspace default",
			}), nil
	}

	// Check command against denylist before execution
	if matched := t.checkCommandDenylist(command); matched != "" {
		log.Printf("[Exec] DENIED command=%q matched_pattern=%q cwd=%q", command, matched, cwd)
		return types.NewErrorResult("command_denied",
			fmt.Sprintf("Command blocked by security denylist (matched pattern: %q)", matched)).
			WithParameter("command", command).
			WithContext(map[string]interface{}{
				"matched_pattern":   matched,
				"working_directory": cwd,
			}).
			WithSuggestions([]string{
				"This command matches a dangerous pattern and has been blocked",
				"Use a safer alternative command",
				"Contact the administrator if you believe this is a false positive",
			}), nil
	}

	// Audit log: command execution start
	startTime := time.Now()
	log.Printf("[Exec] START command=%q cwd=%q", command, cwd)

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = cwd

	output, err := cmd.CombinedOutput()

	// Audit log: command execution complete
	duration := time.Since(startTime)
	exitCode := 0
	if err != nil {
		exitCode = getExitCode(err)
		log.Printf("[Exec] DONE command=%q cwd=%q exit_code=%d duration=%s error=%q",
			command, cwd, exitCode, duration, err.Error())

		// Enhanced error categorization with detailed context
		errorType := "command_failed"
		suggestions := []string{"Check command syntax", "Verify the command exists"}

		if exitError, ok := err.(*exec.ExitError); ok {
			ec := exitError.ExitCode()
			errorType = "command_failed"

			switch ec {
			case 1:
				suggestions = []string{
					"Command executed but returned error status",
					"Check command syntax and arguments",
					"Review command output for error details",
				}
			case 126:
				suggestions = []string{
					"Command found but not executable",
					"Check file permissions",
					"Ensure the command has execute permissions",
				}
			case 127:
				suggestions = []string{
					"Command not found",
					"Check if the command is installed",
					"Verify the command path",
				}
			default:
				suggestions = []string{
					fmt.Sprintf("Command exited with code %d", ec),
					"Check command output for error details",
					"Review command syntax and arguments",
				}
			}
		} else if strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "context deadline") {
			errorType = "timeout_error"
			suggestions = []string{
				"Command execution timed out",
				"Try a simpler or faster command",
				"Use commands that complete quickly",
			}
		} else if strings.Contains(err.Error(), "permission denied") {
			errorType = "permission_denied"
			suggestions = []string{
				"Insufficient permissions to execute command",
				"Try a different command that doesn't require elevated privileges",
				"Check sandbox restrictions",
			}
		}

		result := types.NewErrorResult(errorType,
			fmt.Sprintf("Command execution failed: %v", err)).
			WithParameter("command", command).
			WithContext(map[string]interface{}{
				"working_directory": cwd,
				"output":            string(output),
				"error_detail":      err.Error(),
				"command_length":    len(command),
				"exit_code":         getExitCode(err),
				"has_output":        len(output) > 0,
			}).
			WithSuggestions(suggestions)

		// Also include the output in the content for failed commands
		result.Content = string(output)

		return result, nil
	}

	log.Printf("[Exec] DONE command=%q cwd=%q exit_code=0 duration=%s", command, cwd, duration)

	return &types.ToolResult{
		Success: true,
		Content: string(output),
		Data: map[string]interface{}{
			"command":           command,
			"working_directory": cwd,
			"output_length":     len(output),
			"exit_code":         0,
		},
	}, nil
}

// GetUsageExamples implements types.UsageExampleProvider for ExecTool.
func (t *ExecTool) GetUsageExamples() []types.ToolExample {
	return []types.ToolExample{
		{
			Name:        "List directory contents",
			Description: "Get a detailed listing of files in the current directory",
			Args: map[string]interface{}{
				"command": "ls -la",
			},
			Expected: "Returns detailed file listing with permissions, sizes, and dates",
		},
		{
			Name:        "Check disk usage",
			Description: "Check available disk space",
			Args: map[string]interface{}{
				"command": "df -h",
			},
			Expected: "Returns human-readable disk usage information",
		},
		{
			Name:        "Search for text in files",
			Description: "Search for a pattern across Go source files",
			Args: map[string]interface{}{
				"command": "grep -n 'func main' *.go",
			},
			Expected: "Returns line numbers and matches for main functions",
		},
		{
			Name:        "Run tests",
			Description: "Execute Go tests in the current directory",
			Args: map[string]interface{}{
				"command": "go test -v ./...",
				"cwd":     ".",
			},
			Expected: "Runs all tests with verbose output in the specified directory",
		},
	}
}

// getExitCode extracts the exit code from an exec error
func getExitCode(err error) int {
	if exitError, ok := err.(*exec.ExitError); ok {
		return exitError.ExitCode()
	}
	return -1
}

// SelfTest implements types.SelfTester for ExecTool.
func (t *ExecTool) SelfTest(ctx context.Context, opts *types.SelfTestOptions) *types.SelfTestResult {
	start := time.Now()

	if opts == nil {
		opts = types.DefaultSelfTestOptions()
	}

	result := &types.SelfTestResult{
		Status:       types.SelfTestStatusOK,
		Message:      "Bash tool is functional",
		Capabilities: []string{"execute_commands", "working_directory", "command_denylist"},
		TestedAt:     time.Now(),
	}

	deps := []types.DependencyStatus{}

	// Check shell availability by running a simple command
	shellStatus := types.DependencyStatus{
		Name:     "Shell",
		Required: true,
	}

	testCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(testCtx, "sh", "-c", "echo ok")
	output, err := cmd.CombinedOutput()
	if err != nil {
		shellStatus.Available = false
		shellStatus.Status = "unavailable"
		shellStatus.Message = fmt.Sprintf("shell test failed: %v", err)
		result.Status = types.SelfTestStatusFailed
		result.Message = "Shell is not available"
		result.Suggestions = []string{
			"Verify that /bin/sh is accessible",
			"Check system PATH configuration",
		}
	} else if strings.TrimSpace(string(output)) == "ok" {
		shellStatus.Available = true
		shellStatus.Status = "ready"
	} else {
		shellStatus.Available = false
		shellStatus.Status = "unexpected_output"
		shellStatus.Message = fmt.Sprintf("expected 'ok', got: %s", strings.TrimSpace(string(output)))
		result.Status = types.SelfTestStatusDegraded
		result.Message = "Shell returned unexpected output"
	}
	deps = append(deps, shellStatus)

	// Check sandbox configuration
	sandboxStatus := types.DependencyStatus{
		Name:     "SandboxConfig",
		Required: false,
	}

	if t.registry != nil && t.registry.sandboxCfg.WorkspaceDir != "" {
		sandboxStatus.Available = true
		sandboxStatus.Status = "configured"
		sandboxStatus.Message = fmt.Sprintf("workspace: %s", t.registry.sandboxCfg.WorkspaceDir)

		// Add sandbox details in verbose mode
		if opts.Verbose {
			if result.Details == nil {
				result.Details = make(map[string]interface{})
			}
			result.Details["workspace_dir"] = t.registry.sandboxCfg.WorkspaceDir
			result.Details["allowed_paths"] = t.registry.sandboxCfg.AllowedPaths
			result.Details["denylist_patterns"] = len(t.getEffectiveDenylist())
		}
	} else {
		sandboxStatus.Available = false
		sandboxStatus.Status = "not_configured"
		sandboxStatus.Message = "no workspace directory configured"
		result.UnavailableCapabilities = append(result.UnavailableCapabilities, "path_restrictions")
	}
	deps = append(deps, sandboxStatus)

	// Check command denylist
	denylistStatus := types.DependencyStatus{
		Name:     "CommandDenylist",
		Required: false,
	}

	denylist := t.getEffectiveDenylist()
	if len(denylist) > 0 {
		denylistStatus.Available = true
		denylistStatus.Status = "active"
		denylistStatus.Message = fmt.Sprintf("%d patterns configured", len(denylist))
	} else {
		denylistStatus.Available = false
		denylistStatus.Status = "empty"
		denylistStatus.Message = "no denylist patterns configured"
		result.UnavailableCapabilities = append(result.UnavailableCapabilities, "command_filtering")
	}
	deps = append(deps, denylistStatus)

	result.Dependencies = deps
	result.TestDuration = time.Since(start)

	if opts.IncludeExamples && result.IsFunctional() {
		result.Examples = []types.ToolExample{
			{
				Name:        "List directory contents",
				Description: "Get a detailed listing of files in the current directory",
				Args: map[string]interface{}{
					"command": "ls -la",
				},
				Expected: "Returns detailed file listing with permissions, sizes, and dates",
			},
			{
				Name:        "Run command in specific directory",
				Description: "Execute a command with a custom working directory",
				Args: map[string]interface{}{
					"command": "pwd",
					"cwd":     "/tmp",
				},
				Expected: "Returns /tmp as the working directory",
			},
		}
	}

	return result
}
