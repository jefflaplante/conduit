package ai

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"conduit/internal/config"
	"conduit/internal/sessions"
	"conduit/internal/tools/types"
)

// ClaudeCodeProvider routes LLM calls through the `claude` CLI in print mode.
// It implements both Provider and StreamingProvider interfaces.
type ClaudeCodeProvider struct {
	name          string
	config        config.ClaudeCodeConfig
	model         string
	sessionMapper *sessions.ClaudeCodeSessionMapper
}

// Compile-time interface checks.
var _ Provider = (*ClaudeCodeProvider)(nil)
var _ StreamingProvider = (*ClaudeCodeProvider)(nil)

// NewClaudeCodeProvider creates a new claude-code provider.
// The sessionMapper may be nil; session resume is skipped when nil.
func NewClaudeCodeProvider(cfg config.ProviderConfig, sessionMapper *sessions.ClaudeCodeSessionMapper) (*ClaudeCodeProvider, error) {
	ccCfg := cfg.ClaudeCodeOrDefault()

	// Validate that the claude binary is reachable.
	if _, err := exec.LookPath(ccCfg.ClaudePath); err != nil {
		return nil, fmt.Errorf("claude binary not found at %q: %w", ccCfg.ClaudePath, err)
	}

	return &ClaudeCodeProvider{
		name:          cfg.Name,
		config:        ccCfg,
		model:         cfg.Model,
		sessionMapper: sessionMapper,
	}, nil
}

// Name returns the provider name.
func (p *ClaudeCodeProvider) Name() string {
	return p.name
}

// SetSessionMapper sets or replaces the session mapper (called by gateway during wiring).
func (p *ClaudeCodeProvider) SetSessionMapper(mapper *sessions.ClaudeCodeSessionMapper) {
	p.sessionMapper = mapper
}

// GenerateResponse executes `claude -p` synchronously and returns the full response.
func (p *ClaudeCodeProvider) GenerateResponse(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error) {
	userMessage := extractLastUserMessage(req.Messages)
	if userMessage == "" {
		return nil, fmt.Errorf("no user message found in request")
	}

	conduitSessionID := extractSessionID(ctx)

	// Create a timeout context.
	timeout := time.Duration(p.config.TimeoutSeconds) * time.Second
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := p.buildCommand(execCtx, userMessage, conduitSessionID, false)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	log.Printf("[ClaudeCode] Executing: %s (session=%s)", p.config.ClaudePath, conduitSessionID)

	if err := cmd.Run(); err != nil {
		return nil, classifyClaudeCodeError(stderr.String(), cmd.ProcessState.ExitCode(), err)
	}

	result, err := ParseClaudeCodeJSON(&stdout)
	if err != nil {
		return nil, fmt.Errorf("failed to parse claude response: %w", err)
	}

	// Save session mapping for future resume.
	p.saveSessionMapping(conduitSessionID, result.SessionID)

	return &GenerateResponse{
		Content:   result.Content,
		ToolCalls: nil, // Claude Code handles tool execution internally.
		Usage:     result.Usage,
		Partial:   result.Partial,
	}, nil
}

// GenerateResponseStreaming executes `claude -p` with stream-json output and
// delivers text deltas to the callback as they arrive.
func (p *ClaudeCodeProvider) GenerateResponseStreaming(ctx context.Context, req *GenerateRequest, onDelta StreamCallback) (*GenerateResponse, error) {
	userMessage := extractLastUserMessage(req.Messages)
	if userMessage == "" {
		return nil, fmt.Errorf("no user message found in request")
	}

	conduitSessionID := extractSessionID(ctx)

	timeout := time.Duration(p.config.TimeoutSeconds) * time.Second
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := p.buildCommand(execCtx, userMessage, conduitSessionID, true)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	log.Printf("[ClaudeCode] Executing (streaming): %s (session=%s)", p.config.ClaudePath, conduitSessionID)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start claude: %w", err)
	}

	result, parseErr := ParseClaudeCodeStream(stdoutPipe, onDelta)

	// Wait for the process to exit.
	waitErr := cmd.Wait()

	// Prefer parse error if we got one; otherwise check process exit.
	if parseErr != nil {
		return &GenerateResponse{
			Content: result.Content,
			Usage:   result.Usage,
			Partial: true,
		}, parseErr
	}
	if waitErr != nil {
		return nil, classifyClaudeCodeError(stderr.String(), cmd.ProcessState.ExitCode(), waitErr)
	}

	// Save session mapping for future resume.
	p.saveSessionMapping(conduitSessionID, result.SessionID)

	return &GenerateResponse{
		Content:   result.Content,
		ToolCalls: nil,
		Usage:     result.Usage,
		Partial:   result.Partial,
	}, nil
}

// buildCommand constructs the `claude -p` exec.Cmd with all configured flags.
func (p *ClaudeCodeProvider) buildCommand(ctx context.Context, userMessage string, conduitSessionID string, streaming bool) *exec.Cmd {
	args := []string{"-p"}

	// Output format.
	if streaming {
		args = append(args, "--output-format", "stream-json")
	} else {
		args = append(args, "--output-format", "json")
	}

	// Model override.
	if p.model != "" {
		args = append(args, "--model", p.model)
	}

	// Session resume.
	if p.sessionMapper != nil && conduitSessionID != "" {
		ccSessionID, _ := p.sessionMapper.GetClaudeCodeSession(conduitSessionID)
		if ccSessionID != "" {
			args = append(args, "--resume", ccSessionID)
		}
	}

	// Allowed tools.
	if len(p.config.AllowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(p.config.AllowedTools, ","))
	}

	// Permission mode.
	if p.config.PermissionMode != "" {
		args = append(args, "--permission-mode", p.config.PermissionMode)
	}

	// Max turns.
	if p.config.MaxTurns > 0 {
		args = append(args, "--max-turns", strconv.Itoa(p.config.MaxTurns))
	}

	// The prompt is the final argument.
	args = append(args, userMessage)

	cmd := exec.CommandContext(ctx, p.config.ClaudePath, args...)
	if p.config.WorkingDir != "" {
		cmd.Dir = p.config.WorkingDir
	}
	return cmd
}

// saveSessionMapping persists the Conduit→CC session mapping if possible.
func (p *ClaudeCodeProvider) saveSessionMapping(conduitSessionID, ccSessionID string) {
	if p.sessionMapper == nil || conduitSessionID == "" || ccSessionID == "" {
		return
	}
	if err := p.sessionMapper.SaveMapping(conduitSessionID, ccSessionID); err != nil {
		log.Printf("[ClaudeCode] Warning: failed to save session mapping: %v", err)
	}
}

// extractLastUserMessage returns the content of the last message with Role="user".
func extractLastUserMessage(messages []ChatMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			return messages[i].Content
		}
	}
	return ""
}

// extractSessionID retrieves the Conduit session key from the request context.
// The gateway sets this via types.WithRequestContext.
func extractSessionID(ctx context.Context) string {
	return types.RequestSessionKey(ctx)
}

// classifyClaudeCodeError maps stderr output and exit codes from the Claude CLI
// to Conduit's error categories, returning an appropriately classified error.
func classifyClaudeCodeError(stderr string, exitCode int, originalErr error) error {
	lower := strings.ToLower(stderr)

	// Build a descriptive error message.
	msg := fmt.Sprintf("claude process exited with code %d", exitCode)
	if stderr != "" {
		// Trim to a reasonable length for error messages.
		trimmed := stderr
		if len(trimmed) > 500 {
			trimmed = trimmed[:500] + "..."
		}
		msg = fmt.Sprintf("%s: %s", msg, strings.TrimSpace(trimmed))
	}

	// Classify based on stderr content.
	switch {
	case strings.Contains(lower, "authentication") ||
		strings.Contains(lower, "unauthorized") ||
		strings.Contains(lower, "api key") ||
		strings.Contains(lower, "401"):
		return fmt.Errorf("authentication error: %s", msg)

	case strings.Contains(lower, "rate_limit") ||
		strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "429") ||
		strings.Contains(lower, "too many requests"):
		return fmt.Errorf("rate limit: %s", msg)

	case strings.Contains(lower, "overloaded") ||
		strings.Contains(lower, "503") ||
		strings.Contains(lower, "service unavailable"):
		return fmt.Errorf("service unavailable: %s", msg)

	case strings.Contains(lower, "timeout") ||
		strings.Contains(lower, "timed out") ||
		strings.Contains(lower, "deadline exceeded"):
		return fmt.Errorf("timeout: %s", msg)
	}

	// Check if the original error itself is a context timeout/cancellation.
	if originalErr != nil {
		if ctx := originalErr.Error(); strings.Contains(ctx, "context deadline exceeded") ||
			strings.Contains(ctx, "signal: killed") {
			return fmt.Errorf("timeout: %s", msg)
		}
	}

	return fmt.Errorf("claude-code error: %s", msg)
}
