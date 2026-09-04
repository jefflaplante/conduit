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

	// Script skills always receive a JSON envelope on stdin containing the
	// action name plus any args. Scripts select on the action key to dispatch
	// subcommands (e.g. water-tank monitor.py). The executor runs
	// `python3 monitor.py` with no argv, so stdin is the only channel.
	envelope := make(map[string]interface{}, len(args)+1)
	for k, v := range args {
		envelope[k] = v
	}
	envelope["action"] = action
	stdinJSON, err := json.Marshal(envelope)
	if err != nil {
		return &ExecutionResult{
			Success: false,
			Error:   fmt.Sprintf("error marshaling action envelope: %v", err),
		}, nil
	}

	return e.runCommand(ctx, cmd, skill, stdinJSON)
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

	// For subprocess skills (gog/email), args are interpolated into the
	// command line; keep legacy stdin behavior (pipe raw args JSON only
	// when args exist) for any script reading them that way.
	var stdinJSON []byte
	if len(args) > 0 {
		var err error
		stdinJSON, err = json.Marshal(args)
		if err != nil {
			return &ExecutionResult{
				Success: false,
				Error:   fmt.Sprintf("error marshaling arguments: %v", err),
			}, nil
		}
	}

	return e.runCommand(ctx, cmd, skill, stdinJSON)
}

// runCommand executes a command with proper environment and argument handling.
// stdinJSON, when non-nil, is piped to the command's stdin.
func (e *Executor) runCommand(ctx context.Context, cmd *exec.Cmd, skill Skill, stdinJSON []byte) (*ExecutionResult, error) {
	// Set working directory
	cmd.Dir = skill.Location

	// Set environment
	cmd.Env = e.buildEnvironment(skill)

	// Pipe stdin payload when present. For gog/email skills args are already
	// interpolated into the command line; piping JSON to their stdin causes
	// "no TTY available" errors, so skip those.
	if stdinJSON != nil {
		skipStdin := skill.Name == "email" || skill.Name == "gog"
		if !skipStdin {
			cmd.Stdin = bytes.NewReader(stdinJSON)
		}
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
	var command strings.Builder
	action = normalizeAction(action)

	// Source environment setup — try standard locations
	homeDir, _ := os.UserHomeDir()
	secretsPaths := []string{
		filepath.Join(homeDir, "ocgo", ".ocgo-secrets.env"),
		filepath.Join(homeDir, ".conduit-secrets.env"),
	}
	for _, p := range secretsPaths {
		if _, err := os.Stat(p); err == nil {
			command.WriteString(fmt.Sprintf(". %s\n", p))
			break
		}
	}

	// Export PATH for gog and other tools
	command.WriteString("export PATH=\"$HOME/google-cloud-sdk/bin:$HOME/.local/bin:$PATH\"\n")

	// Try skill-specific command builders first
	if built := e.buildSkillSpecificCommand(skill.Name, action, args, &command); built {
		return command.String()
	}

	// Look for export statements in the skill content
	content := skill.Content
	for _, line := range strings.Split(content, "\n") {
		trimmedLine := strings.TrimSpace(line)
		if strings.HasPrefix(trimmedLine, "export ") {
			command.WriteString(trimmedLine + "\n")
		}
	}

	// Fallback: extract single-line command from skill content
	if actionCmd := e.extractSingleLineCommand(content, action); actionCmd != "" {
		command.WriteString(actionCmd)
		return command.String()
	}

	// Final fallback: echo the action (quoted — action names may contain quotes)
	command.WriteString(fmt.Sprintf("echo 'Executed action: %s'\n", shellQuote(action)))
	return command.String()
}

// normalizeAction maps legacy heading-derived action names onto the executor's
// canonical verbs (search/read/send/list/cleanup/status). Heading-derived names
// like "send_email_(as_jules_—_via_jules's_own_account)" missed every case,
// fell through to the echo fallback, and their embedded quotes broke the
// generated shell command (gog send skill triage, Sep 2026).
func normalizeAction(action string) string {
	a := strings.ToLower(action)
	switch {
	case strings.Contains(a, "cleanup") || strings.Contains(a, "organize"):
		return "cleanup"
	case strings.Contains(a, "send") || strings.Contains(a, "reply") || strings.Contains(a, "compose"):
		return "send"
	case strings.Contains(a, "search") || strings.Contains(a, "find") || strings.Contains(a, "query"):
		return "search"
	case strings.Contains(a, "read") || strings.Contains(a, "thread_get"):
		return "read"
	case strings.Contains(a, "list"):
		return "list"
	default:
		return action
	}
}

// buildSkillSpecificCommand builds commands for known skill types using args
func (e *Executor) buildSkillSpecificCommand(skillName, action string, args map[string]interface{}, command *strings.Builder) bool {
	switch skillName {
	case "email", "gog":
		return e.buildGogCommand(action, args, command)
	default:
		return false
	}
}

// buildGogCommand builds gog CLI commands from action and args
func (e *Executor) buildGogCommand(action string, args map[string]interface{}, command *strings.Builder) bool {
	// Determine which account to use
	account := "$GOG_ACCOUNT" // default to Jeff's account
	if acct, ok := args["account"].(string); ok && acct != "" {
		if acct == "jules" || acct == "jules@laplante.dev" {
			account = "$JULES_ACCOUNT"
		}
	} else if inbox, ok := args["inbox"].(string); ok {
		if inbox == "jules" || inbox == "jules@laplante.dev" {
			account = "$JULES_ACCOUNT"
		}
	}

	// Helper to get raw string arg (unquoted). Callers shell-quote exactly
	// once at emission — pre-quoting here caused double-quoting
	// (''\''val'\'' garbage) in every generated command (Sep 2026 triage).
	getArg := func(key string) string {
		if v, ok := args[key].(string); ok {
			return v
		}
		return ""
	}

	switch action {
	case "search":
		query := getArg("query")
		if query == "" {
			query = getArg("q")
		}
		if query == "" {
			query = "is:unread"
		}
		maxResults := "20"
		if m, ok := args["max"].(string); ok && m != "" {
			maxResults = m
		} else if m, ok := args["max"].(float64); ok {
			maxResults = fmt.Sprintf("%.0f", m)
		} else if m, ok := args["limit"].(string); ok && m != "" {
			maxResults = m
		} else if m, ok := args["limit"].(float64); ok {
			maxResults = fmt.Sprintf("%.0f", m)
		}
		command.WriteString(fmt.Sprintf("/usr/local/bin/gog gmail search %s --account %s --max %s\n", shellQuote(query), account, maxResults))

	case "read":
		msgID := getArg("message_id")
		if msgID == "" {
			msgID = getArg("id")
		}
		threadID := getArg("thread_id")
		if threadID == "" {
			threadID = getArg("threadId")
		}
		if msgID != "" {
			command.WriteString(fmt.Sprintf("/usr/local/bin/gog gmail read %s --account %s\n", shellQuote(msgID), account))
		} else if threadID != "" {
			command.WriteString(fmt.Sprintf("/usr/local/bin/gog gmail thread get %s --account %s\n", shellQuote(threadID), account))
		} else {
			return false
		}

	case "send":
		to := getArg("to")
		subject := getArg("subject")
		body := getArg("body")
		from := getArg("from")
		// Safety default: sends without an explicit identity go from Jules's
		// account, never Jeff's. Matches email-safety policy (autonomous
		// sends must use $JULES_ACCOUNT); $GOG_ACCOUNT requires Jeff's
		// explicit approval in args.
		sendAccount := "$JULES_ACCOUNT"
		if acct, ok := args["account"].(string); ok && acct != "" &&
			acct != "jules" && acct != "jules@laplante.dev" {
			sendAccount = "$GOG_ACCOUNT"
		} else if from == "jeff@jefflaplante.com" || from == "jeff@laplante.dev" || from == "jeff" {
			sendAccount = "$GOG_ACCOUNT"
		}
		if to == "" {
			return false
		}
		cmd := fmt.Sprintf("/usr/local/bin/gog gmail send --to %s", shellQuote(to))
		if subject != "" {
			cmd += fmt.Sprintf(" --subject %s", shellQuote(subject))
		}
		if body != "" {
			cmd += fmt.Sprintf(" --body %s", shellQuote(body))
		}
		cmd += fmt.Sprintf(" --account %s --force", sendAccount)
		command.WriteString(cmd + "\n")

	case "cleanup":
		// Cleanup runs the blocklist-based junk removal. Delegates to the
		// workspace sweep script (searches last 24h against the junk
		// blocklist, trashes thread matches, prints SUMMARY|total|trashed|errs).
		command.WriteString("/home/jules/ocgo/workspace/scripts/hygiene-junk-sweep.sh\n")

	case "list":
		listAccount := account
		if args["inbox"] == "jules" || args["inbox"] == "jules@laplante.dev" {
			listAccount = "$JULES_ACCOUNT"
		}
		maxResults := "20"
		if m, ok := args["max"].(string); ok && m != "" {
			maxResults = m
		} else if m, ok := args["max"].(float64); ok {
			maxResults = fmt.Sprintf("%.0f", m)
		}
		query := "is:unread"
		if q := getArg("query"); q != "" {
			query = q
		}
		command.WriteString(fmt.Sprintf("/usr/local/bin/gog gmail search %s --account %s --max %s\n", shellQuote(query), listAccount, maxResults))

	case "status":
		command.WriteString(fmt.Sprintf("/usr/local/bin/gog gmail search \"is:unread\" --account %s --max 5\n", account))

	default:
		return false
	}

	return true
}

// shellQuote wraps a string in single quotes, escaping any embedded single quotes
func shellQuote(s string) string {
	// Replace ' with '\'' (end quote, escaped quote, start quote)
	escaped := strings.ReplaceAll(s, "'", "'\\''")
	return "'" + escaped + "'"
}

// extractSingleLineCommand extracts the first single-line command relevant to the action
func (e *Executor) extractSingleLineCommand(content, action string) string {
	lines := strings.Split(content, "\n")
	inCodeBlock := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inCodeBlock = !inCodeBlock
			continue
		}
		if inCodeBlock {
			// Only consider single-line commands (not comments, not empty)
			if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
				if e.isRelevantCommand(trimmed, action) {
					return trimmed
				}
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
