package skills

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Executor handles skill execution through various methods
type Executor struct {
	workspaceDir string
	timeout      time.Duration
	environment  map[string]string
}

// NewExecutor creates a new skill executor
func NewExecutor(cfg ExecutionConfig) *Executor {
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second // Default timeout
	}

	return &Executor{
		timeout:     timeout,
		environment: cfg.Environment,
	}
}

// ExecuteSkill executes a skill with the given action and arguments
func (e *Executor) ExecuteSkill(ctx context.Context, skill Skill, action string, args map[string]interface{}) (*ExecutionResult, error) {
	// Create a context with timeout
	timeoutCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	// Determine execution method
	method := e.determineExecutionMethod(skill)

	switch method {
	case ExecutionMethodScript:
		return e.executeScript(timeoutCtx, skill, action, args)
	case ExecutionMethodSubprocess:
		return e.executeSubprocess(timeoutCtx, skill, action, args)
	default:
		return &ExecutionResult{
			Success: false,
			Error:   fmt.Sprintf("unsupported execution method: %s", method),
		}, nil
	}
}

// determineExecutionMethod decides how to execute the skill
func (e *Executor) determineExecutionMethod(skill Skill) ExecutionMethod {
	// If skill has scripts, prefer script execution
	if len(skill.Scripts) > 0 {
		return ExecutionMethodScript
	}

	// Default to subprocess execution (shell-based)
	return ExecutionMethodSubprocess
}

// executeScript executes a specific script from the skill
func (e *Executor) executeScript(ctx context.Context, skill Skill, action string, args map[string]interface{}) (*ExecutionResult, error) {
	// Find the appropriate script for this action
	script := e.findScript(skill, action)
	if script == nil {
		return &ExecutionResult{
			Success: false,
			Error:   fmt.Sprintf("no script found for action: %s", action),
		}, nil
	}

	scriptPath := filepath.Join(skill.Location, script.Path)

	// Build command
	var cmd *exec.Cmd
	switch script.Language {
	case "python":
		cmd = exec.CommandContext(ctx, "python3", scriptPath)
	case "javascript":
		cmd = exec.CommandContext(ctx, "node", scriptPath)
	case "bash":
		cmd = exec.CommandContext(ctx, "bash", scriptPath)
	default:
		// Try to execute directly
		cmd = exec.CommandContext(ctx, scriptPath)
	}

	return e.runCommand(ctx, cmd, skill, args)
}

// executeSubprocess executes the skill through a shell subprocess
func (e *Executor) executeSubprocess(ctx context.Context, skill Skill, action string, args map[string]interface{}) (*ExecutionResult, error) {
	// Build shell command based on skill content and action
	command := e.buildShellCommand(skill, action, args)
	if command == "" {
		return &ExecutionResult{
			Success: false,
			Error:   fmt.Sprintf("no command found for action: %s", action),
		}, nil
	}

	// Execute through bash
	cmd := exec.CommandContext(ctx, "bash", "-c", command)

	return e.runCommand(ctx, cmd, skill, args)
}

// runCommand executes a command with proper environment and argument handling
func (e *Executor) runCommand(ctx context.Context, cmd *exec.Cmd, skill Skill, args map[string]interface{}) (*ExecutionResult, error) {
	// Set working directory
	cmd.Dir = skill.Location

	// Set environment
	cmd.Env = e.buildEnvironment(skill)

	// Prepare arguments as JSON input
	if len(args) > 0 {
		argsJSON, err := json.Marshal(args)
		if err != nil {
			return &ExecutionResult{
				Success: false,
				Error:   fmt.Sprintf("error marshaling arguments: %v", err),
			}, nil
		}
		cmd.Stdin = bytes.NewReader(argsJSON)
	}

	// Execute command
	log.Printf("Executing skill %s: %s", skill.Name, strings.Join(cmd.Args, " "))

	output, err := cmd.CombinedOutput()
	outputStr := string(output)

	if err != nil {
		// Check if it's a timeout
		if ctx.Err() == context.DeadlineExceeded {
			return &ExecutionResult{
				Success: false,
				Error:   fmt.Sprintf("skill execution timed out after %v", e.timeout),
				Output:  outputStr,
			}, nil
		}

		return &ExecutionResult{
			Success: false,
			Error:   err.Error(),
			Output:  outputStr,
		}, nil
	}

	// Try to parse output as JSON for structured data
	var data map[string]interface{}
	if strings.TrimSpace(outputStr) != "" {
		if err := json.Unmarshal(output, &data); err != nil {
			// Not JSON, that's fine - use as plain text output
			data = nil
		}
	}

	return &ExecutionResult{
		Success: true,
		Output:  outputStr,
		Data:    data,
	}, nil
}

// findScript finds an appropriate script for the given action
func (e *Executor) findScript(skill Skill, action string) *SkillScript {
	// Look for exact match
	for _, script := range skill.Scripts {
		if script.Name == action {
			return &script
		}
	}

	// Look for partial match
	for _, script := range skill.Scripts {
		if strings.Contains(strings.ToLower(script.Name), strings.ToLower(action)) {
			return &script
		}
	}

	// If only one script, use it as default
	if len(skill.Scripts) == 1 {
		return &skill.Scripts[0]
	}

	return nil
}

// buildShellCommand creates a shell command for the given skill and action
func (e *Executor) buildShellCommand(skill Skill, action string, args map[string]interface{}) string {
	// This is a simplified approach - in practice, skills would define their own command structure
	// For now, we'll look for common patterns in the skill content

	content := skill.Content
	lines := strings.Split(content, "\n")

	var command strings.Builder

	// Source environment setup if mentioned
	if strings.Contains(content, "source ~/.conduit-secrets.env") {
		command.WriteString("source ~/.conduit-secrets.env\n")
	}

	// Look for export statements in the content
	for _, line := range lines {
		trimmedLine := strings.TrimSpace(line)
		if strings.HasPrefix(trimmedLine, "export ") {
			command.WriteString(trimmedLine + "\n")
		}
	}

	// Try to find a command that matches the action
	actionCommand := e.extractActionCommand(content, action)
	if actionCommand != "" {
		command.WriteString(actionCommand)
		return command.String()
	}

	// Fallback: just echo the action
	command.WriteString(fmt.Sprintf("echo 'Executed action: %s'", action))

	return command.String()
}

// extractActionCommand attempts to find a command for the given action in skill content
func (e *Executor) extractActionCommand(content, action string) string {
	lines := strings.Split(content, "\n")
	inCodeBlock := false
	var currentCommand strings.Builder

	for i, line := range lines {
		trimmedLine := strings.TrimSpace(line)

		// Track code blocks
		if strings.HasPrefix(trimmedLine, "```") {
			if inCodeBlock {
				// End of code block
				command := strings.TrimSpace(currentCommand.String())
				if e.isRelevantCommand(command, action) {
					return command
				}
				currentCommand.Reset()
			}
			inCodeBlock = !inCodeBlock
			continue
		}

		if inCodeBlock {
			currentCommand.WriteString(line + "\n")
		} else if strings.HasPrefix(trimmedLine, "gog ") ||
			strings.HasPrefix(trimmedLine, "curl ") ||
			strings.HasPrefix(trimmedLine, "ha ") {
			// Check if this looks like a relevant command
			if e.isRelevantCommand(trimmedLine, action) {
				// Look for multi-line commands
				fullCommand := e.extractMultiLineCommand(lines, i)
				return fullCommand
			}
		}
	}

	return ""
}

// isRelevantCommand checks if a command is relevant to the given action
func (e *Executor) isRelevantCommand(command, action string) bool {
	actionLower := strings.ToLower(action)
	commandLower := strings.ToLower(command)

	// Simple relevance check
	actionWords := []string{action, actionLower}

	// Add common action synonyms
	synonyms := map[string][]string{
		"search": {"search", "find", "query", "list"},
		"read":   {"read", "get", "fetch", "show"},
		"send":   {"send", "create", "post"},
		"list":   {"list", "ls", "show", "get"},
		"status": {"status", "state", "check", "info"},
	}

	if syns, exists := synonyms[actionLower]; exists {
		actionWords = append(actionWords, syns...)
	}

	for _, word := range actionWords {
		if strings.Contains(commandLower, word) {
			return true
		}
	}

	return false
}

// extractMultiLineCommand handles commands that span multiple lines
func (e *Executor) extractMultiLineCommand(lines []string, startIndex int) string {
	var command strings.Builder
	command.WriteString(strings.TrimSpace(lines[startIndex]))

	// Look for continuation lines
	for i := startIndex + 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])

		// Empty line ends the command
		if line == "" {
			break
		}

		// Check for continuation patterns
		if strings.HasPrefix(line, " ") ||
			strings.HasPrefix(line, "\t") ||
			strings.HasPrefix(line, "-") ||
			strings.HasPrefix(line, "--") {
			command.WriteString(" " + line)
		} else {
			break
		}
	}

	return command.String()
}

// buildEnvironment creates the environment for skill execution
func (e *Executor) buildEnvironment(skill Skill) []string {
	env := os.Environ()

	// Add skill-specific environment variables
	for key, value := range e.environment {
		env = append(env, fmt.Sprintf("%s=%s", key, value))
	}

	// Add common Conduit environment
	homeDir, _ := os.UserHomeDir()
	env = append(env,
		fmt.Sprintf("CONDUIT_SKILL=%s", skill.Name),
		fmt.Sprintf("CONDUIT_SKILL_DIR=%s", skill.Location),
		fmt.Sprintf("HOME=%s", homeDir),
	)

	return env
}
