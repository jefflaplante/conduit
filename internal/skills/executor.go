package skills

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"conduit/internal/procutil"
)

// maxSkillOutputBytes caps retained skill output (head + tail) so a runaway
// script cannot OOM the gateway (conduit-31jg.20). Truncated output is no
// longer valid JSON, so Data falls back to nil and Output carries the text.
const maxSkillOutputBytes = 1 << 20

// Executor handles skill execution through various methods
type Executor struct {
	workspaceDir string
	timeout      time.Duration
	environment  map[string]string

	// approver gates owner-account sends (conduit-31jg.43). Nil => such
	// sends fail closed.
	approverMu sync.RWMutex
	approver   Approver
	// runApproved overrides how an approved owner send is executed. Tests
	// only (never shell out to the real gog binary); nil => e.execute.
	runApproved func(ctx context.Context, skill Skill, action string, args map[string]interface{}) (*ExecutionResult, error)
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

// ExecuteSkill executes a skill with the given action and arguments.
// Owner-account email sends are diverted to the human approval gate and run
// only after the originating human approves (conduit-31jg.43).
func (e *Executor) ExecuteSkill(ctx context.Context, skill Skill, action string, args map[string]interface{}) (*ExecutionResult, error) {
	if res, gated := e.gateOwnerSend(ctx, skill, action, args); gated {
		return res, nil
	}
	return e.execute(ctx, skill, action, args)
}

// execute runs a skill with no approval gating. Only ExecuteSkill (after the
// gate) and an approved owner-send ExecuteFunc may call it.
func (e *Executor) execute(ctx context.Context, skill Skill, action string, args map[string]interface{}) (*ExecutionResult, error) {
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
	command, err := e.buildShellCommand(skill, action, args)
	if err != nil {
		// Invalid model-supplied args (conduit-31jg.2): refuse rather than
		// interpolate anything suspicious into the shell line.
		return &ExecutionResult{
			Success: false,
			Error:   fmt.Sprintf("invalid arguments for action %s: %v", action, err),
		}, nil
	}
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

	// conduit-31jg.20: process-group kill on timeout, bounded pipe wait and
	// capped output (was CombinedOutput: orphaned grandchildren blocked it
	// past the deadline and output was unbounded).
	procutil.ConfigureGroupKill(cmd, 0, 0)
	buf := procutil.NewCappedBuffer(maxSkillOutputBytes)
	cmd.Stdout = buf
	cmd.Stderr = buf
	err := cmd.Run()
	if err != nil && procutil.IsWaitDelayOnly(err) && ctx.Err() == nil {
		err = nil // exited 0; a background child held the pipe open
	}
	output := buf.Bytes()
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

// buildShellCommand creates a shell command for the given skill and action.
// It returns an error when model-supplied args fail validation
// (conduit-31jg.2); an empty command with a nil error means no command exists.
func (e *Executor) buildShellCommand(skill Skill, action string, args map[string]interface{}) (string, error) {
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
			command.WriteString(fmt.Sprintf(". %s\n", shellQuote(p)))
			break
		}
	}

	// Export PATH for gog and other tools
	command.WriteString("export PATH=\"$HOME/google-cloud-sdk/bin:$HOME/.local/bin:$PATH\"\n")

	// Try skill-specific command builders first
	built, err := e.buildSkillSpecificCommand(skill.Name, action, args, &command)
	if err != nil {
		return "", err
	}
	if built {
		return command.String(), nil
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
		return command.String(), nil
	}

	// No real command found. Return an empty command so executeSubprocess
	// reports an honest failure. The previous behavior echoed
	// "Executed action: X" and exited 0 — the executor reported Success:true
	// while doing nothing at all (state-skill silent-failure bug, Sep 2026).
	log.Printf("[skills] no command found for skill %q action %q — failing honestly (no echo fallback)", skill.Name, action)
	return "", nil
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
func (e *Executor) buildSkillSpecificCommand(skillName, action string, args map[string]interface{}, command *strings.Builder) (bool, error) {
	switch skillName {
	case "email", "gog":
		return e.buildGogCommand(action, args, command)
	default:
		return false, nil
	}
}

// Shell expansions for the gog account identities. These are the ONLY
// values ever emitted after --account; they are double-quoted so the shell
// expands the env var without word-splitting or globbing (conduit-31jg.2).
const (
	gogOwnerAccount = `"$GOG_ACCOUNT"`
	gogJulesAccount = `"$JULES_ACCOUNT"`
)

// Bounds for gog --max (conduit-31jg.2).
const (
	gogDefaultMax = 20
	gogMinMax     = 1
	gogMaxMax     = 100
)

// isJulesAccount / isOwnerAccount define the known account aliases the
// email skill already recognizes. Anything else is rejected.
func isJulesAccount(v string) bool { return v == "jules" || v == "agent@example.com" }

func isOwnerAccount(v string) bool {
	return v == "jeff" || v == "owner@example.com" || v == "owner-alt@example.com"
}

// gogSendUsesOwner reports whether a gog/email send with these args goes out
// as the owner ($GOG_ACCOUNT): any non-Jules account, or an owner-alias from.
// Single source of truth for buildGogCommand and the approval gate
// (conduit-31jg.43). Callers must run validateAccountArg first.
func gogSendUsesOwner(args map[string]interface{}) bool {
	if acct, ok := args["account"].(string); ok && acct != "" && !isJulesAccount(acct) {
		return true
	}
	from, _ := args["from"].(string)
	return isOwnerAccount(from)
}

// validateAccountArg rejects account/inbox values outside the known alias
// set. Model-supplied identities are never interpolated into the shell;
// they only select one of the fixed gog*Account expansions (conduit-31jg.2).
func validateAccountArg(args map[string]interface{}, key string) error {
	raw, present := args[key]
	if !present || raw == nil {
		return nil
	}
	v, ok := raw.(string)
	if !ok {
		return fmt.Errorf("%s must be a string", key)
	}
	if v == "" || isJulesAccount(v) || isOwnerAccount(v) {
		return nil
	}
	return fmt.Errorf("unknown %s %q (allowed: jules, agent@example.com, jeff, owner@example.com, owner-alt@example.com)", key, v)
}

// parseMaxResults reads the first present key from args as a whole number,
// accepting JSON numbers and numeric strings, and clamps it to
// [gogMinMax, gogMaxMax]. Non-numeric values are rejected so nothing but
// digits ever reaches the shell line (conduit-31jg.2).
func parseMaxResults(args map[string]interface{}, keys ...string) (int, error) {
	for _, key := range keys {
		raw, present := args[key]
		if !present || raw == nil {
			continue
		}
		var n float64
		switch v := raw.(type) {
		case float64:
			n = v
		case float32:
			n = float64(v)
		case int:
			n = float64(v)
		case int64:
			n = float64(v)
		case json.Number:
			f, err := v.Float64()
			if err != nil {
				return 0, fmt.Errorf("%s must be a number, got %q", key, v.String())
			}
			n = f
		case string:
			s := strings.TrimSpace(v)
			if s == "" {
				continue // legacy: empty string means "use default"
			}
			i, err := strconv.Atoi(s)
			if err != nil {
				return 0, fmt.Errorf("%s must be an integer, got %q", key, v)
			}
			n = float64(i)
		default:
			return 0, fmt.Errorf("%s must be a number, got %T", key, raw)
		}
		if math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) {
			return 0, fmt.Errorf("%s must be a whole number, got %v", key, raw)
		}
		if n < gogMinMax {
			return gogMinMax, nil
		}
		if n > gogMaxMax {
			return gogMaxMax, nil
		}
		return int(n), nil
	}
	return gogDefaultMax, nil
}

// buildGogCommand builds gog CLI commands from action and args.
//
// Security (conduit-31jg.2): every model-supplied value that reaches the
// bash -c line is either shellQuote'd (free text) or parsed to an int
// (max/limit). Account selection maps validated aliases onto fixed
// env-var expansions; raw account strings are never interpolated.
func (e *Executor) buildGogCommand(action string, args map[string]interface{}, command *strings.Builder) (bool, error) {
	if err := validateAccountArg(args, "account"); err != nil {
		return false, err
	}
	if err := validateAccountArg(args, "inbox"); err != nil {
		return false, err
	}

	// Determine which account to use
	account := gogOwnerAccount // default to Jeff's account
	if acct, ok := args["account"].(string); ok && acct != "" {
		if isJulesAccount(acct) {
			account = gogJulesAccount
		}
	} else if inbox, ok := args["inbox"].(string); ok {
		if isJulesAccount(inbox) {
			account = gogJulesAccount
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
		// conduit-31jg.2: max/limit were interpolated unquoted; parse to int.
		maxResults, err := parseMaxResults(args, "max", "limit")
		if err != nil {
			return false, err
		}
		command.WriteString(fmt.Sprintf("/usr/local/bin/gog gmail search %s --account %s --max %d\n", shellQuote(query), account, maxResults))

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
			return false, nil
		}

	case "send":
		to := getArg("to")
		subject := getArg("subject")
		body := getArg("body")
		// Safety default: sends without an explicit identity go from Jules's
		// account, never Jeff's. Matches email-safety policy (autonomous
		// sends must use $JULES_ACCOUNT). An owner-alias account/from selects
		// $GOG_ACCOUNT; ExecuteSkill only reaches this point for an owner
		// send after the human approved it in-channel (conduit-31jg.43,
		// see owner_approval.go).
		sendAccount := gogJulesAccount
		if gogSendUsesOwner(args) {
			sendAccount = gogOwnerAccount // validated owner alias above
		}
		if to == "" {
			return false, nil
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
		if inbox, ok := args["inbox"].(string); ok && isJulesAccount(inbox) {
			listAccount = gogJulesAccount
		}
		// conduit-31jg.2: max was interpolated unquoted; parse to int.
		maxResults, err := parseMaxResults(args, "max", "limit")
		if err != nil {
			return false, err
		}
		query := "is:unread"
		if q := getArg("query"); q != "" {
			query = q
		}
		command.WriteString(fmt.Sprintf("/usr/local/bin/gog gmail search %s --account %s --max %d\n", shellQuote(query), listAccount, maxResults))

	case "status":
		command.WriteString(fmt.Sprintf("/usr/local/bin/gog gmail search 'is:unread' --account %s --max 5\n", account))

	default:
		return false, nil
	}

	return true, nil
}

// shellQuote wraps a string in single quotes for POSIX sh/bash, escaping any
// embedded single quotes. Inside single quotes nothing ($, `, \, newlines)
// is special, so the result is always one literal word (conduit-31jg.2).
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
