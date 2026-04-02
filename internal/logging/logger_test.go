package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		input    string
		expected slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"INFO", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"WARN", slog.LevelWarn},
		{"error", slog.LevelError},
		{"ERROR", slog.LevelError},
		{"unknown", slog.LevelInfo}, // defaults to info
		{"", slog.LevelInfo},        // defaults to info
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			result := ParseLevel(tc.input)
			if result != tc.expected {
				t.Errorf("ParseLevel(%q) = %v, want %v", tc.input, result, tc.expected)
			}
		})
	}
}

func TestNewWithWriter_TextFormat(t *testing.T) {
	var buf bytes.Buffer
	logger := NewWithWriter("info", "text", &buf)

	logger.Info("test message", "key", "value")

	output := buf.String()
	if !strings.Contains(output, "test message") {
		t.Errorf("expected output to contain 'test message', got: %s", output)
	}
	if !strings.Contains(output, "key=value") {
		t.Errorf("expected output to contain 'key=value', got: %s", output)
	}
}

func TestNewWithWriter_JSONFormat(t *testing.T) {
	var buf bytes.Buffer
	logger := NewWithWriter("info", "json", &buf)

	logger.Info("test message", "key", "value")

	output := buf.String()

	// Parse as JSON to verify structure
	var logEntry map[string]interface{}
	if err := json.Unmarshal([]byte(output), &logEntry); err != nil {
		t.Fatalf("failed to parse JSON output: %v, output: %s", err, output)
	}

	if logEntry["msg"] != "test message" {
		t.Errorf("expected msg='test message', got: %v", logEntry["msg"])
	}
	if logEntry["key"] != "value" {
		t.Errorf("expected key='value', got: %v", logEntry["key"])
	}
	if logEntry["level"] != "INFO" {
		t.Errorf("expected level='INFO', got: %v", logEntry["level"])
	}
}

func TestNewWithWriter_LevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	logger := NewWithWriter("warn", "text", &buf)

	// This should be filtered out
	logger.Info("info message")
	logger.Debug("debug message")

	// This should appear
	logger.Warn("warn message")
	logger.Error("error message")

	output := buf.String()
	if strings.Contains(output, "info message") {
		t.Error("info message should have been filtered out")
	}
	if strings.Contains(output, "debug message") {
		t.Error("debug message should have been filtered out")
	}
	if !strings.Contains(output, "warn message") {
		t.Error("warn message should appear in output")
	}
	if !strings.Contains(output, "error message") {
		t.Error("error message should appear in output")
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Level != "info" {
		t.Errorf("expected default level 'info', got: %s", cfg.Level)
	}
	if cfg.Format != "text" {
		t.Errorf("expected default format 'text', got: %s", cfg.Format)
	}
}

func TestSetDefault(t *testing.T) {
	var buf bytes.Buffer
	logger := NewWithWriter("debug", "text", &buf)

	// Save original default
	original := Default()
	defer SetDefault(original)

	SetDefault(logger)

	if Default() != logger {
		t.Error("SetDefault did not update the default logger")
	}
}

func TestFromContext_WithLogger(t *testing.T) {
	var buf bytes.Buffer
	logger := NewWithWriter("info", "text", &buf)

	ctx := WithLogger(context.Background(), logger)
	retrieved := FromContext(ctx)

	retrieved.Info("test")

	if !strings.Contains(buf.String(), "test") {
		t.Error("logger from context should have written to the buffer")
	}
}

func TestFromContext_WithNilContext(t *testing.T) {
	logger := FromContext(nil)
	if logger == nil {
		t.Error("FromContext(nil) should return default logger, not nil")
	}
}

func TestFromContext_WithoutLogger(t *testing.T) {
	logger := FromContext(context.Background())
	if logger == nil {
		t.Error("FromContext should return default logger when no logger in context")
	}
}

func TestFromContext_WithRequestID(t *testing.T) {
	var buf bytes.Buffer
	logger := NewWithWriter("info", "json", &buf)
	SetDefault(logger)
	defer SetDefault(slog.Default()) // restore

	ctx := WithRequestID(context.Background(), "test-req-123")
	ctxLogger := FromContext(ctx)

	ctxLogger.Info("test message")

	output := buf.String()
	var logEntry map[string]interface{}
	if err := json.Unmarshal([]byte(output), &logEntry); err != nil {
		t.Fatalf("failed to parse JSON output: %v", err)
	}

	if logEntry["request_id"] != "test-req-123" {
		t.Errorf("expected request_id='test-req-123', got: %v", logEntry["request_id"])
	}
}

func TestPackageLevelFunctions(t *testing.T) {
	var buf bytes.Buffer
	logger := NewWithWriter("debug", "text", &buf)
	SetDefault(logger)
	defer SetDefault(slog.Default()) // restore

	ctx := context.Background()

	Debug(ctx, "debug msg")
	Info(ctx, "info msg")
	Warn(ctx, "warn msg")
	Error(ctx, "error msg")

	output := buf.String()
	if !strings.Contains(output, "debug msg") {
		t.Error("Debug should have logged")
	}
	if !strings.Contains(output, "info msg") {
		t.Error("Info should have logged")
	}
	if !strings.Contains(output, "warn msg") {
		t.Error("Warn should have logged")
	}
	if !strings.Contains(output, "error msg") {
		t.Error("Error should have logged")
	}
}

func TestWith(t *testing.T) {
	var buf bytes.Buffer
	logger := NewWithWriter("info", "text", &buf)
	SetDefault(logger)
	defer SetDefault(slog.Default()) // restore

	childLogger := With("component", "test")
	childLogger.Info("message")

	output := buf.String()
	if !strings.Contains(output, "component=test") {
		t.Errorf("expected output to contain 'component=test', got: %s", output)
	}
}

func TestWithContext(t *testing.T) {
	var buf bytes.Buffer
	logger := NewWithWriter("info", "json", &buf)
	SetDefault(logger)
	defer SetDefault(slog.Default()) // restore

	ctx := WithRequestID(context.Background(), "req-456")
	childLogger := WithContext(ctx, "component", "gateway")

	childLogger.Info("test")

	output := buf.String()
	var logEntry map[string]interface{}
	if err := json.Unmarshal([]byte(output), &logEntry); err != nil {
		t.Fatalf("failed to parse JSON output: %v", err)
	}

	if logEntry["request_id"] != "req-456" {
		t.Errorf("expected request_id='req-456', got: %v", logEntry["request_id"])
	}
	if logEntry["component"] != "gateway" {
		t.Errorf("expected component='gateway', got: %v", logEntry["component"])
	}
}
