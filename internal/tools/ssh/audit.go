//go:build with_ssh

// Package ssh implements the SSH remote execution tool with security controls.
package ssh

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"conduit/internal/config"
)

// AuditEntry represents a single audit log entry
type AuditEntry struct {
	Timestamp    time.Time `json:"timestamp"`
	SessionID    string    `json:"session_id,omitempty"`
	UserID       string    `json:"user_id,omitempty"`
	Host         string    `json:"host"`
	Command      string    `json:"command"`
	SecurityTier string    `json:"security_tier,omitempty"`
	Approved     bool      `json:"approved"`
	ApprovedBy   string    `json:"approved_by,omitempty"`
	ExitCode     int       `json:"exit_code"`
	Duration     string    `json:"duration"`
	Stdout       string    `json:"stdout,omitempty"`
	Stderr       string    `json:"stderr,omitempty"`
	Error        string    `json:"error,omitempty"`
	TimedOut     bool      `json:"timed_out,omitempty"`
}

// AuditLogger handles audit logging for SSH operations
type AuditLogger struct {
	config config.SSHAuditConfig
	file   *os.File
	mu     sync.Mutex
}

// NewAuditLogger creates a new audit logger with the given configuration
func NewAuditLogger(cfg config.SSHAuditConfig) (*AuditLogger, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	if cfg.LogPath == "" {
		return nil, fmt.Errorf("audit log path cannot be empty when audit is enabled")
	}

	// Create directory if it doesn't exist
	dir := filepath.Dir(cfg.LogPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create audit log directory: %w", err)
	}

	// Open the log file in append mode
	file, err := os.OpenFile(cfg.LogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open audit log file: %w", err)
	}

	return &AuditLogger{
		config: cfg,
		file:   file,
	}, nil
}

// LogExecution appends an audit entry to the JSONL log file
func (l *AuditLogger) LogExecution(entry *AuditEntry) error {
	if l == nil || l.file == nil {
		return nil // Audit logging is disabled
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// Set timestamp if not already set
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}

	// Truncate output if necessary
	maxOutput := l.config.GetMaxOutputCapture()
	if l.config.LogOutput {
		if len(entry.Stdout) > maxOutput {
			entry.Stdout = entry.Stdout[:maxOutput] + "...[truncated]"
		}
		if len(entry.Stderr) > maxOutput {
			entry.Stderr = entry.Stderr[:maxOutput] + "...[truncated]"
		}
	} else {
		// Don't log output if LogOutput is false
		entry.Stdout = ""
		entry.Stderr = ""
	}

	// Redact secrets if enabled
	if l.config.RedactSecrets {
		entry.Command = redactSecrets(entry.Command)
		entry.Stdout = redactSecrets(entry.Stdout)
		entry.Stderr = redactSecrets(entry.Stderr)
	}

	// Marshal to JSON
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("failed to marshal audit entry: %w", err)
	}

	// Append newline for JSONL format
	data = append(data, '\n')

	// Write to file
	if _, err := l.file.Write(data); err != nil {
		return fmt.Errorf("failed to write audit entry: %w", err)
	}

	return nil
}

// Cleanup removes old audit log entries based on retention policy
func (l *AuditLogger) Cleanup() error {
	if l == nil || l.config.RetentionDays <= 0 {
		return nil // No cleanup needed
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// Read the current log file
	if err := l.file.Sync(); err != nil {
		return fmt.Errorf("failed to sync audit log: %w", err)
	}

	// Reopen file for reading
	data, err := os.ReadFile(l.config.LogPath)
	if err != nil {
		return fmt.Errorf("failed to read audit log: %w", err)
	}

	if len(data) == 0 {
		return nil // Empty file, nothing to clean
	}

	// Calculate cutoff time
	cutoff := time.Now().AddDate(0, 0, -l.config.RetentionDays)

	// Parse and filter entries
	lines := splitLines(data)
	var kept [][]byte

	for _, line := range lines {
		if len(line) == 0 {
			continue
		}

		var entry AuditEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			// Keep malformed entries to avoid data loss
			kept = append(kept, line)
			continue
		}

		// Keep entries newer than cutoff
		if entry.Timestamp.After(cutoff) {
			kept = append(kept, line)
		}
	}

	// If nothing changed, don't rewrite the file
	if len(kept) == len(lines) {
		return nil
	}

	// Write filtered entries back
	// Close the current file handle
	if err := l.file.Close(); err != nil {
		return fmt.Errorf("failed to close audit log: %w", err)
	}

	// Reopen in truncate mode
	file, err := os.OpenFile(l.config.LogPath, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to reopen audit log: %w", err)
	}
	l.file = file

	// Write kept entries
	for _, line := range kept {
		if _, err := l.file.Write(append(line, '\n')); err != nil {
			return fmt.Errorf("failed to write filtered audit entry: %w", err)
		}
	}

	return l.file.Sync()
}

// Close closes the audit log file
func (l *AuditLogger) Close() error {
	if l == nil || l.file == nil {
		return nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if err := l.file.Sync(); err != nil {
		return fmt.Errorf("failed to sync audit log: %w", err)
	}

	if err := l.file.Close(); err != nil {
		return fmt.Errorf("failed to close audit log: %w", err)
	}

	l.file = nil
	return nil
}

// splitLines splits data by newlines, preserving the line content without newlines
func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0

	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			if i > start {
				lines = append(lines, data[start:i])
			}
			start = i + 1
		}
	}

	// Add the last line if there's no trailing newline
	if start < len(data) {
		lines = append(lines, data[start:])
	}

	return lines
}

// secretPatterns are compiled regex patterns for detecting sensitive values in strings.
// Each pattern uses a named capture group "secret" to identify the portion to redact.
// Order matters: more specific patterns first.
var secretPatterns = []*regexp.Regexp{
	// URL credentials: https://user:password@host, postgres://user:pass@host
	regexp.MustCompile(`(?i)(://[^:\s]+:)(?P<secret>[^@\s]+)(@)`),
	// "Bearer <token>" — captures the token after Bearer
	regexp.MustCompile(`(?i)\bBearer\s+(?P<secret>\S+)`),
	// Common CLI flags: --password=foo, --token=bar, --api-key=val
	regexp.MustCompile(`(?i)(?:-{1,2})(?:password|passwd|token|secret|api[_-]?key|key|auth)\s*[=\s]\s*["']?(?P<secret>[^\s"']+)["']?`),
	// Environment variable assignments: API_KEY=abc123, SECRET="value"
	regexp.MustCompile(`(?i)\b[A-Z][A-Z0-9_]*(?:KEY|SECRET|TOKEN|PASSWORD|PASSWD|AUTH|CREDENTIAL)\b\s*=\s*["']?(?P<secret>[^\s"']+)["']?`),
	// JSON/structured data: "token": "value", "password":"val"
	regexp.MustCompile(`(?i)["']?(?:password|passwd|token|secret|api[_-]?key|apikey|credential)["']?\s*[:=]\s*["'](?P<secret>[^"']+)["']`),
	// Standalone keyword=value (not after --): password=value, token = value (requires leading whitespace/start)
	regexp.MustCompile(`(?m)(?:^|\s)(?:password|passwd|token|secret|api[_-]?key|apikey)\s*[:=]\s*["']?(?P<secret>[^\s"']+)["']?`),
	// Long opaque strings (64+ chars) that look like API keys/tokens
	regexp.MustCompile(`(?P<secret>[A-Za-z0-9+/=_-]{64,})`),
}

// redactSecrets replaces detected sensitive values in s with [REDACTED].
// It targets common patterns: URL credentials, auth headers/flags, env vars,
// and opaque token-like strings.
func redactSecrets(s string) string {
	for _, pat := range secretPatterns {
		s = pat.ReplaceAllStringFunc(s, func(match string) string {
			// Find the named group "secret" within the match
			groups := pat.FindStringSubmatch(match)
			secretIdx := pat.SubexpIndex("secret")
			if secretIdx < 0 || secretIdx >= len(groups) {
				return match
			}
			secret := groups[secretIdx]
			if secret == "" {
				return match
			}
			// Replace only the secret portion within the full match
			return stringsReplace(match, secret, "[REDACTED]")
		})
	}
	return s
}

// stringsReplace replaces the first occurrence of old with new in s.
func stringsReplace(s, old, new string) string {
	for i := 0; i <= len(s)-len(old); i++ {
		if s[i:i+len(old)] == old {
			return s[:i] + new + s[i+len(old):]
		}
	}
	return s
}
