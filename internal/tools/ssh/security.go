//go:build with_ssh

// Package ssh implements the SSH remote execution tool with security controls.
package ssh

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"conduit/internal/config"
)

// SecurityTier represents the security classification of a command
type SecurityTier string

const (
	// TierRead - commands that only read/display information
	TierRead SecurityTier = "read"
	// TierModify - commands that change state but are generally safe
	TierModify SecurityTier = "modify"
	// TierDangerous - commands that could cause harm but may be needed
	TierDangerous SecurityTier = "dangerous"
	// TierBlocked - commands that should never be executed
	TierBlocked SecurityTier = "blocked"
)

// ClassificationResult contains the result of command security classification
type ClassificationResult struct {
	// Tier is the final security classification
	Tier SecurityTier

	// Command is the original command string
	Command string

	// BaseCommand is the primary command extracted (e.g., "ls" from "ls -la /tmp")
	BaseCommand string

	// Reason explains why this classification was assigned
	Reason string

	// RequiresApproval indicates if human approval is needed
	RequiresApproval bool

	// Blocked indicates if the command is completely blocked
	Blocked bool

	// Warnings contains any security concerns detected
	Warnings []string

	// PipeChain contains the commands in a pipe chain (if any)
	PipeChain []string

	// HasSubshell indicates if command substitution was detected
	HasSubshell bool

	// HasRedirection indicates if output/input redirection was detected
	HasRedirection bool
}

// SecurityEngine classifies commands and enforces security policies
type SecurityEngine struct {
	config          config.SSHSecurityConfig
	blockedPatterns []*regexp.Regexp
	readCommands    map[string]bool
	modifyCommands  map[string]bool
	dangerCommands  map[string]bool
	blockedCommands map[string]bool
}

// NewSecurityEngine creates a new security engine with the given configuration
func NewSecurityEngine(cfg config.SSHSecurityConfig) (*SecurityEngine, error) {
	engine := &SecurityEngine{
		config:          cfg,
		readCommands:    make(map[string]bool),
		modifyCommands:  make(map[string]bool),
		dangerCommands:  make(map[string]bool),
		blockedCommands: make(map[string]bool),
	}

	// Compile blocked patterns
	for _, pattern := range cfg.BlockedPatterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid blocked pattern %q: %w", pattern, err)
		}
		engine.blockedPatterns = append(engine.blockedPatterns, re)
	}

	// Build command lookup maps
	for _, cmd := range cfg.AllowedCommands.Read {
		engine.readCommands[cmd] = true
	}
	for _, cmd := range cfg.AllowedCommands.Modify {
		engine.modifyCommands[cmd] = true
	}
	for _, cmd := range cfg.AllowedCommands.Dangerous {
		engine.dangerCommands[cmd] = true
	}
	for _, cmd := range cfg.AllowedCommands.Blocked {
		engine.blockedCommands[cmd] = true
	}

	return engine, nil
}

// ClassifyCommand analyzes a command and returns its security classification
func (e *SecurityEngine) ClassifyCommand(command string) *ClassificationResult {
	result := &ClassificationResult{
		Command:  command,
		Warnings: []string{},
	}

	// Check command length
	maxLen := e.config.GetMaxCommandLength()
	if len(command) > maxLen {
		result.Tier = TierBlocked
		result.Blocked = true
		result.Reason = fmt.Sprintf("command exceeds maximum length (%d > %d)", len(command), maxLen)
		return result
	}

	// Check for blocked patterns first (highest priority)
	for _, pattern := range e.blockedPatterns {
		if pattern.MatchString(command) {
			result.Tier = TierBlocked
			result.Blocked = true
			result.Reason = fmt.Sprintf("matches blocked pattern: %s", pattern.String())
			return result
		}
	}

	// Check for subshells
	hasSubshell := e.detectSubshells(command)
	result.HasSubshell = hasSubshell
	if hasSubshell && !e.config.AllowSubshells {
		result.Tier = TierBlocked
		result.Blocked = true
		result.Reason = "command substitution (subshells) not allowed"
		result.Warnings = append(result.Warnings, "detected $() or backtick command substitution")
		return result
	}

	// Check for pipes
	pipeChain := e.extractPipeChain(command)
	result.PipeChain = pipeChain
	if len(pipeChain) > 1 && !e.config.AllowPipes {
		result.Tier = TierBlocked
		result.Blocked = true
		result.Reason = "pipe chains not allowed"
		return result
	}

	// Check for redirections
	result.HasRedirection = e.detectRedirection(command)
	if result.HasRedirection {
		result.Warnings = append(result.Warnings, "command contains I/O redirection")
	}

	// Classify based on pipe chain (worst tier wins)
	if len(pipeChain) > 1 {
		result.Tier, result.BaseCommand, result.Reason = e.classifyPipeChain(pipeChain)
	} else {
		// Single command classification
		result.Tier, result.BaseCommand, result.Reason = e.classifySingleCommand(command)
	}

	// Check if explicitly blocked command
	if e.isBlockedCommand(command) {
		result.Tier = TierBlocked
		result.Blocked = true
		result.Reason = "command is in the blocked list"
		return result
	}

	// Determine if approval is required
	result.RequiresApproval = e.requiresApproval(result.Tier)
	result.Blocked = result.Tier == TierBlocked

	return result
}

// detectSubshells checks for command substitution patterns
// Respects single quotes (which prevent substitution in shell)
func (e *SecurityEngine) detectSubshells(command string) bool {
	inSingleQuote := false
	inDoubleQuote := false
	escaped := false

	for i, c := range command {
		if escaped {
			escaped = false
			continue
		}

		if c == '\\' {
			escaped = true
			continue
		}

		// Track quote state
		if c == '\'' && !inDoubleQuote {
			inSingleQuote = !inSingleQuote
			continue
		}

		if c == '"' && !inSingleQuote {
			inDoubleQuote = !inDoubleQuote
			continue
		}

		// Only check for subshells outside single quotes
		// (single quotes prevent all substitution in shell)
		if !inSingleQuote {
			// Check for $( pattern
			if c == '$' && i+1 < len(command) && command[i+1] == '(' {
				return true
			}

			// Check for backticks
			if c == '`' {
				return true
			}
		}
	}

	return false
}

// extractPipeChain splits a command into its pipe components
func (e *SecurityEngine) extractPipeChain(command string) []string {
	// Handle quoted strings and escaped pipes
	var parts []string
	var current strings.Builder
	inSingleQuote := false
	inDoubleQuote := false
	escaped := false

	for _, c := range command {
		if escaped {
			current.WriteRune(c)
			escaped = false
			continue
		}

		if c == '\\' {
			escaped = true
			current.WriteRune(c)
			continue
		}

		if c == '\'' && !inDoubleQuote {
			inSingleQuote = !inSingleQuote
			current.WriteRune(c)
			continue
		}

		if c == '"' && !inSingleQuote {
			inDoubleQuote = !inDoubleQuote
			current.WriteRune(c)
			continue
		}

		if c == '|' && !inSingleQuote && !inDoubleQuote {
			// Check for || (logical OR) - not a pipe
			part := strings.TrimSpace(current.String())
			if part != "" {
				parts = append(parts, part)
			}
			current.Reset()
			continue
		}

		current.WriteRune(c)
	}

	// Add the last part
	if part := strings.TrimSpace(current.String()); part != "" {
		parts = append(parts, part)
	}

	return parts
}

// detectRedirection checks for I/O redirection operators
func (e *SecurityEngine) detectRedirection(command string) bool {
	// Check for common redirection patterns
	patterns := []string{
		">>", ">", "<<", "<", "2>&1", "2>", "&>",
	}

	// Be careful to not match within quotes
	inSingleQuote := false
	inDoubleQuote := false
	escaped := false

	for i, c := range command {
		if escaped {
			escaped = false
			continue
		}

		if c == '\\' {
			escaped = true
			continue
		}

		if c == '\'' && !inDoubleQuote {
			inSingleQuote = !inSingleQuote
			continue
		}

		if c == '"' && !inSingleQuote {
			inDoubleQuote = !inDoubleQuote
			continue
		}

		if !inSingleQuote && !inDoubleQuote {
			// Check for redirection patterns at this position
			for _, pattern := range patterns {
				if i+len(pattern) <= len(command) && command[i:i+len(pattern)] == pattern {
					return true
				}
			}
		}
	}

	return false
}

// classifyPipeChain classifies a pipe chain (worst tier wins)
func (e *SecurityEngine) classifyPipeChain(chain []string) (SecurityTier, string, string) {
	worstTier := TierRead
	var worstCommand string
	var reasons []string

	for _, cmd := range chain {
		tier, baseCmd, reason := e.classifySingleCommand(cmd)
		if tierSeverity(tier) > tierSeverity(worstTier) {
			worstTier = tier
			worstCommand = baseCmd
		}
		reasons = append(reasons, fmt.Sprintf("%s: %s", baseCmd, reason))
	}

	return worstTier, worstCommand, fmt.Sprintf("pipe chain classification (worst): %s", strings.Join(reasons, " | "))
}

// classifySingleCommand classifies a single command (no pipes)
func (e *SecurityEngine) classifySingleCommand(command string) (SecurityTier, string, string) {
	// Extract the base command (first word)
	baseCmd := e.extractBaseCommand(command)
	if baseCmd == "" {
		return TierBlocked, "", "empty command"
	}

	// Check classification in order of increasing severity
	if e.readCommands[baseCmd] {
		return TierRead, baseCmd, "classified as read-only command"
	}

	if e.modifyCommands[baseCmd] {
		// Check for dangerous flags/arguments that might upgrade the tier
		if upgraded, reason := e.checkDangerousArgs(baseCmd, command); upgraded {
			return TierDangerous, baseCmd, reason
		}
		return TierModify, baseCmd, "classified as state-modifying command"
	}

	if e.dangerCommands[baseCmd] {
		return TierDangerous, baseCmd, "classified as dangerous command"
	}

	if e.blockedCommands[baseCmd] {
		return TierBlocked, baseCmd, "command is explicitly blocked"
	}

	// Unknown command - use default tier (must be dangerous or blocked)
	defaultTier := SecurityTier(e.config.DefaultTier)
	return defaultTier, baseCmd, fmt.Sprintf("unknown command, using default tier: %s", defaultTier)
}

// extractBaseCommand gets the first word of a command, handling common prefixes
func (e *SecurityEngine) extractBaseCommand(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}

	// Prefixes that don't take arguments (next word is the command)
	simplePrefixes := map[string]bool{
		"sudo":   true,
		"nohup":  true,
		"time":   true,
		"exec":   true,
		"xargs":  true,
		"strace": true,
		"ltrace": true,
	}

	// Prefixes that can take optional arguments (skip arguments starting with -)
	argPrefixes := map[string]bool{
		"nice":    true,
		"timeout": true,
		"watch":   true,
		"env":     true,
	}

	words := strings.Fields(command)
	if len(words) == 0 {
		return ""
	}

	// Skip prefix commands to get to the actual command
	idx := 0
	for idx < len(words) {
		word := words[idx]

		// Handle simple prefixes (no arguments)
		if simplePrefixes[word] {
			idx++
			continue
		}

		// Handle prefixes that take arguments
		if argPrefixes[word] {
			idx++
			// Skip arguments (words starting with - or containing =)
			for idx < len(words) {
				nextWord := words[idx]
				// Skip flags (-n, --timeout, etc.)
				if strings.HasPrefix(nextWord, "-") {
					idx++
					continue
				}
				// Skip VAR=val assignments (for env)
				if strings.Contains(nextWord, "=") && word == "env" {
					idx++
					continue
				}
				// Skip numeric arguments (like "60" for timeout)
				if isNumeric(nextWord) {
					idx++
					continue
				}
				break
			}
			continue
		}

		// Not a prefix, this is the command
		break
	}

	if idx >= len(words) {
		// Command was all prefixes, use the last one
		return words[len(words)-1]
	}

	// Get the command name, handling paths
	cmd := words[idx]
	// Remove path prefix if present
	if lastSlash := strings.LastIndex(cmd, "/"); lastSlash >= 0 {
		cmd = cmd[lastSlash+1:]
	}

	return cmd
}

// isNumeric checks if a string is a numeric value
func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			// Allow decimal point and negative sign
			if c != '.' && c != '-' {
				return false
			}
		}
	}
	return true
}

// checkDangerousArgs checks if a modify-tier command has dangerous arguments
func (e *SecurityEngine) checkDangerousArgs(baseCmd, fullCommand string) (bool, string) {
	switch baseCmd {
	case "rm":
		// rm with -r or -f flags is more dangerous
		if strings.Contains(fullCommand, "-r") || strings.Contains(fullCommand, "-f") ||
			strings.Contains(fullCommand, "--recursive") || strings.Contains(fullCommand, "--force") {
			return true, "rm with recursive or force flags is dangerous"
		}
	case "chmod":
		// chmod with 777 or recursive is dangerous
		if strings.Contains(fullCommand, "777") || strings.Contains(fullCommand, "-R") {
			return true, "chmod with 777 or recursive is dangerous"
		}
	case "chown":
		// chown recursive is dangerous
		if strings.Contains(fullCommand, "-R") || strings.Contains(fullCommand, "--recursive") {
			return true, "chown recursive is dangerous"
		}
	case "mv", "cp":
		// Moving/copying to system directories is dangerous
		systemDirs := []string{"/bin", "/sbin", "/usr/bin", "/usr/sbin", "/etc", "/boot", "/root"}
		for _, dir := range systemDirs {
			if strings.Contains(fullCommand, " "+dir) {
				return true, fmt.Sprintf("operation targeting system directory %s is dangerous", dir)
			}
		}
	case "git":
		// git operations that can lose data
		dangerousGitOps := []string{"reset --hard", "clean -f", "push --force", "push -f"}
		for _, op := range dangerousGitOps {
			if strings.Contains(fullCommand, op) {
				return true, fmt.Sprintf("git %s is a destructive operation", op)
			}
		}
	case "docker":
		// docker operations that affect system
		dangerousDockerOps := []string{"rm -f", "system prune", "volume rm", "network rm"}
		for _, op := range dangerousDockerOps {
			if strings.Contains(fullCommand, op) {
				return true, fmt.Sprintf("docker %s is a destructive operation", op)
			}
		}
	}

	return false, ""
}

// isBlockedCommand checks if the full command matches any blocked command
func (e *SecurityEngine) isBlockedCommand(command string) bool {
	// Normalize whitespace for comparison
	normalized := normalizeWhitespace(command)

	for blocked := range e.blockedCommands {
		// Check exact match and prefix match
		if normalized == blocked || strings.HasPrefix(normalized, blocked+" ") {
			return true
		}
	}

	return false
}

// requiresApproval checks if a tier requires human approval
func (e *SecurityEngine) requiresApproval(tier SecurityTier) bool {
	for _, t := range e.config.RequireApproval {
		if SecurityTier(t) == tier {
			return true
		}
	}
	return false
}

// tierSeverity returns a numeric severity for tier comparison
func tierSeverity(tier SecurityTier) int {
	switch tier {
	case TierRead:
		return 1
	case TierModify:
		return 2
	case TierDangerous:
		return 3
	case TierBlocked:
		return 4
	default:
		return 4 // Unknown tiers are treated as blocked
	}
}

// normalizeWhitespace collapses multiple whitespace characters into single spaces
func normalizeWhitespace(s string) string {
	var builder strings.Builder
	lastWasSpace := true // Start true to trim leading space

	for _, c := range s {
		if unicode.IsSpace(c) {
			if !lastWasSpace {
				builder.WriteRune(' ')
				lastWasSpace = true
			}
		} else {
			builder.WriteRune(c)
			lastWasSpace = false
		}
	}

	return strings.TrimSpace(builder.String())
}

// ValidateCommandForHost checks if a command is allowed for a specific host's security tier
func (e *SecurityEngine) ValidateCommandForHost(command string, hostTier string) *ClassificationResult {
	result := e.ClassifyCommand(command)

	// If host has a security tier restriction, enforce it
	if hostTier != "" {
		hostTierSeverity := tierSeverity(SecurityTier(hostTier))
		commandSeverity := tierSeverity(result.Tier)

		if commandSeverity > hostTierSeverity {
			result.Blocked = true
			result.Reason = fmt.Sprintf("command tier %s exceeds host maximum tier %s", result.Tier, hostTier)
		}
	}

	return result
}

// IsSafeForUnattendedExecution checks if a command can run without human oversight
func (e *SecurityEngine) IsSafeForUnattendedExecution(result *ClassificationResult) bool {
	// Only read-tier commands are safe for unattended execution
	return result.Tier == TierRead && !result.Blocked && len(result.Warnings) == 0
}

// GetSecuritySummary returns a human-readable security summary
func (e *SecurityEngine) GetSecuritySummary() string {
	var builder strings.Builder

	builder.WriteString("SSH Security Configuration:\n")
	builder.WriteString(fmt.Sprintf("  Default tier: %s\n", e.config.DefaultTier))
	builder.WriteString(fmt.Sprintf("  Require approval for: %v\n", e.config.RequireApproval))
	builder.WriteString(fmt.Sprintf("  Allow subshells: %v\n", e.config.AllowSubshells))
	builder.WriteString(fmt.Sprintf("  Allow pipes: %v\n", e.config.AllowPipes))
	builder.WriteString(fmt.Sprintf("  Max command length: %d\n", e.config.GetMaxCommandLength()))
	builder.WriteString(fmt.Sprintf("  Read commands: %d\n", len(e.readCommands)))
	builder.WriteString(fmt.Sprintf("  Modify commands: %d\n", len(e.modifyCommands)))
	builder.WriteString(fmt.Sprintf("  Dangerous commands: %d\n", len(e.dangerCommands)))
	builder.WriteString(fmt.Sprintf("  Blocked patterns: %d\n", len(e.blockedPatterns)))

	return builder.String()
}
