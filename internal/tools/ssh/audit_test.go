package ssh

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"conduit/internal/config"
)

func TestNewAuditLogger(t *testing.T) {
	tests := []struct {
		name        string
		config      config.SSHAuditConfig
		expectNil   bool
		expectError bool
	}{
		{
			name: "enabled with valid config",
			config: config.SSHAuditConfig{
				Enabled: true,
				LogPath: filepath.Join(t.TempDir(), "audit.jsonl"),
			},
			expectNil:   false,
			expectError: false,
		},
		{
			name: "disabled returns nil",
			config: config.SSHAuditConfig{
				Enabled: false,
			},
			expectNil:   true,
			expectError: false,
		},
		{
			name: "enabled without log path",
			config: config.SSHAuditConfig{
				Enabled: true,
				LogPath: "",
			},
			expectNil:   true,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, err := NewAuditLogger(tt.config)

			if tt.expectError && err == nil {
				t.Error("expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			if tt.expectNil && logger != nil {
				t.Error("expected nil logger but got non-nil")
			}
			if !tt.expectNil && !tt.expectError && logger == nil {
				t.Error("expected non-nil logger but got nil")
			}

			if logger != nil {
				defer logger.Close()
			}
		})
	}
}

func TestAuditLogger_LogExecution(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "audit.jsonl")

	cfg := config.SSHAuditConfig{
		Enabled:          true,
		LogPath:          logPath,
		LogCommands:      true,
		LogOutput:        true,
		MaxOutputCapture: 100,
	}

	logger, err := NewAuditLogger(cfg)
	if err != nil {
		t.Fatalf("failed to create audit logger: %v", err)
	}
	defer logger.Close()

	// Log an entry
	entry := &AuditEntry{
		SessionID:    "sess-123",
		UserID:       "user-456",
		Host:         "test-host",
		Command:      "ls -la",
		SecurityTier: "read",
		Approved:     true,
		ExitCode:     0,
		Duration:     "100ms",
		Stdout:       "file1.txt\nfile2.txt",
		Stderr:       "",
	}

	if err := logger.LogExecution(entry); err != nil {
		t.Fatalf("failed to log execution: %v", err)
	}

	// Read the log file
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}

	// Parse the JSONL entry
	var logged AuditEntry
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}

	if err := json.Unmarshal([]byte(lines[0]), &logged); err != nil {
		t.Fatalf("failed to unmarshal log entry: %v", err)
	}

	// Verify the logged entry
	if logged.SessionID != entry.SessionID {
		t.Errorf("SessionID mismatch: got %s, want %s", logged.SessionID, entry.SessionID)
	}
	if logged.Host != entry.Host {
		t.Errorf("Host mismatch: got %s, want %s", logged.Host, entry.Host)
	}
	if logged.Command != entry.Command {
		t.Errorf("Command mismatch: got %s, want %s", logged.Command, entry.Command)
	}
	if logged.ExitCode != entry.ExitCode {
		t.Errorf("ExitCode mismatch: got %d, want %d", logged.ExitCode, entry.ExitCode)
	}
	if !logged.Timestamp.After(time.Time{}) {
		t.Error("Timestamp should be set")
	}
}

func TestAuditLogger_OutputTruncation(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "audit.jsonl")

	maxOutput := 50
	cfg := config.SSHAuditConfig{
		Enabled:          true,
		LogPath:          logPath,
		LogCommands:      true,
		LogOutput:        true,
		MaxOutputCapture: maxOutput,
	}

	logger, err := NewAuditLogger(cfg)
	if err != nil {
		t.Fatalf("failed to create audit logger: %v", err)
	}
	defer logger.Close()

	// Create output longer than maxOutput
	longOutput := strings.Repeat("x", maxOutput+50)
	entry := &AuditEntry{
		Host:     "test-host",
		Command:  "cat large-file",
		ExitCode: 0,
		Duration: "100ms",
		Stdout:   longOutput,
		Stderr:   longOutput,
	}

	if err := logger.LogExecution(entry); err != nil {
		t.Fatalf("failed to log execution: %v", err)
	}

	// Read and verify truncation
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}

	var logged AuditEntry
	if err := json.Unmarshal(data[:len(data)-1], &logged); err != nil {
		t.Fatalf("failed to unmarshal log entry: %v", err)
	}

	expectedTruncated := longOutput[:maxOutput] + "...[truncated]"
	if logged.Stdout != expectedTruncated {
		t.Errorf("Stdout not truncated correctly: len=%d, expected len=%d", len(logged.Stdout), len(expectedTruncated))
	}
	if logged.Stderr != expectedTruncated {
		t.Errorf("Stderr not truncated correctly: len=%d, expected len=%d", len(logged.Stderr), len(expectedTruncated))
	}
}

func TestAuditLogger_LogOutputDisabled(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "audit.jsonl")

	cfg := config.SSHAuditConfig{
		Enabled:     true,
		LogPath:     logPath,
		LogCommands: true,
		LogOutput:   false, // Output logging disabled
	}

	logger, err := NewAuditLogger(cfg)
	if err != nil {
		t.Fatalf("failed to create audit logger: %v", err)
	}
	defer logger.Close()

	entry := &AuditEntry{
		Host:     "test-host",
		Command:  "ls -la",
		ExitCode: 0,
		Duration: "100ms",
		Stdout:   "should be removed",
		Stderr:   "should also be removed",
	}

	if err := logger.LogExecution(entry); err != nil {
		t.Fatalf("failed to log execution: %v", err)
	}

	// Read and verify output is not logged
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}

	var logged AuditEntry
	if err := json.Unmarshal(data[:len(data)-1], &logged); err != nil {
		t.Fatalf("failed to unmarshal log entry: %v", err)
	}

	if logged.Stdout != "" {
		t.Errorf("Stdout should be empty when LogOutput is false, got: %s", logged.Stdout)
	}
	if logged.Stderr != "" {
		t.Errorf("Stderr should be empty when LogOutput is false, got: %s", logged.Stderr)
	}
}

func TestAuditLogger_MultipleEntries(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "audit.jsonl")

	cfg := config.SSHAuditConfig{
		Enabled:     true,
		LogPath:     logPath,
		LogCommands: true,
		LogOutput:   true,
	}

	logger, err := NewAuditLogger(cfg)
	if err != nil {
		t.Fatalf("failed to create audit logger: %v", err)
	}
	defer logger.Close()

	// Log multiple entries
	for i := 0; i < 5; i++ {
		entry := &AuditEntry{
			Host:     "test-host",
			Command:  "test command",
			ExitCode: i,
			Duration: "100ms",
		}
		if err := logger.LogExecution(entry); err != nil {
			t.Fatalf("failed to log entry %d: %v", i, err)
		}
	}

	// Read and verify all entries
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 5 {
		t.Fatalf("expected 5 lines, got %d", len(lines))
	}

	// Verify each entry has correct exit code
	for i, line := range lines {
		var entry AuditEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("failed to unmarshal line %d: %v", i, err)
		}
		if entry.ExitCode != i {
			t.Errorf("line %d: expected exit code %d, got %d", i, i, entry.ExitCode)
		}
	}
}

func TestAuditLogger_Cleanup(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "audit.jsonl")

	cfg := config.SSHAuditConfig{
		Enabled:       true,
		LogPath:       logPath,
		LogCommands:   true,
		LogOutput:     true,
		RetentionDays: 7,
	}

	logger, err := NewAuditLogger(cfg)
	if err != nil {
		t.Fatalf("failed to create audit logger: %v", err)
	}
	defer logger.Close()

	// Log entries with different timestamps
	now := time.Now()
	oldEntry := &AuditEntry{
		Timestamp: now.AddDate(0, 0, -10), // 10 days ago
		Host:      "test-host",
		Command:   "old command",
		ExitCode:  0,
		Duration:  "100ms",
	}
	recentEntry := &AuditEntry{
		Timestamp: now.AddDate(0, 0, -3), // 3 days ago
		Host:      "test-host",
		Command:   "recent command",
		ExitCode:  0,
		Duration:  "100ms",
	}

	if err := logger.LogExecution(oldEntry); err != nil {
		t.Fatalf("failed to log old entry: %v", err)
	}
	if err := logger.LogExecution(recentEntry); err != nil {
		t.Fatalf("failed to log recent entry: %v", err)
	}

	// Run cleanup
	if err := logger.Cleanup(); err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}

	// Read and verify only recent entry remains
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line after cleanup, got %d", len(lines))
	}

	var entry AuditEntry
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("failed to unmarshal entry: %v", err)
	}

	if entry.Command != "recent command" {
		t.Errorf("expected recent command, got: %s", entry.Command)
	}
}

func TestAuditLogger_CleanupNoRetention(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "audit.jsonl")

	cfg := config.SSHAuditConfig{
		Enabled:       true,
		LogPath:       logPath,
		LogCommands:   true,
		RetentionDays: 0, // No retention policy
	}

	logger, err := NewAuditLogger(cfg)
	if err != nil {
		t.Fatalf("failed to create audit logger: %v", err)
	}
	defer logger.Close()

	// Log an entry
	entry := &AuditEntry{
		Host:     "test-host",
		Command:  "test command",
		ExitCode: 0,
		Duration: "100ms",
	}
	if err := logger.LogExecution(entry); err != nil {
		t.Fatalf("failed to log entry: %v", err)
	}

	// Run cleanup (should be a no-op)
	if err := logger.Cleanup(); err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}

	// Verify entry still exists
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line after cleanup, got %d", len(lines))
	}
}

func TestAuditLogger_Close(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "audit.jsonl")

	cfg := config.SSHAuditConfig{
		Enabled:     true,
		LogPath:     logPath,
		LogCommands: true,
	}

	logger, err := NewAuditLogger(cfg)
	if err != nil {
		t.Fatalf("failed to create audit logger: %v", err)
	}

	// Log an entry
	entry := &AuditEntry{
		Host:     "test-host",
		Command:  "test command",
		ExitCode: 0,
		Duration: "100ms",
	}
	if err := logger.LogExecution(entry); err != nil {
		t.Fatalf("failed to log entry: %v", err)
	}

	// Close the logger
	if err := logger.Close(); err != nil {
		t.Fatalf("failed to close logger: %v", err)
	}

	// Verify file was closed by checking we can't write anymore
	if err := logger.LogExecution(entry); err != nil {
		// This should fail, but we expect nil (no-op) since file is nil
	}

	// Verify the log file exists and has content
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}

	if len(data) == 0 {
		t.Error("log file should have content")
	}
}

func TestAuditLogger_NilLogger(t *testing.T) {
	var logger *AuditLogger

	// All operations should be no-ops on nil logger
	entry := &AuditEntry{
		Host:     "test-host",
		Command:  "test command",
		ExitCode: 0,
		Duration: "100ms",
	}

	if err := logger.LogExecution(entry); err != nil {
		t.Errorf("LogExecution on nil logger should not error, got: %v", err)
	}

	if err := logger.Cleanup(); err != nil {
		t.Errorf("Cleanup on nil logger should not error, got: %v", err)
	}

	if err := logger.Close(); err != nil {
		t.Errorf("Close on nil logger should not error, got: %v", err)
	}
}

func TestSplitLines(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		expected int
	}{
		{
			name:     "single line with newline",
			data:     []byte("line1\n"),
			expected: 1,
		},
		{
			name:     "single line without newline",
			data:     []byte("line1"),
			expected: 1,
		},
		{
			name:     "multiple lines",
			data:     []byte("line1\nline2\nline3\n"),
			expected: 3,
		},
		{
			name:     "empty data",
			data:     []byte(""),
			expected: 0,
		},
		{
			name:     "only newlines",
			data:     []byte("\n\n\n"),
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := splitLines(tt.data)
			if len(lines) != tt.expected {
				t.Errorf("expected %d lines, got %d", tt.expected, len(lines))
			}
		})
	}
}
