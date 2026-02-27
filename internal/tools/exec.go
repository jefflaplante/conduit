package tools

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"conduit/internal/tools/types"
)

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

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = cwd

	output, err := cmd.CombinedOutput()
	if err != nil {
		// Enhanced error categorization with detailed context
		errorType := "command_failed"
		suggestions := []string{"Check command syntax", "Verify the command exists"}

		if exitError, ok := err.(*exec.ExitError); ok {
			exitCode := exitError.ExitCode()
			errorType = "command_failed"

			switch exitCode {
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
					fmt.Sprintf("Command exited with code %d", exitCode),
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
