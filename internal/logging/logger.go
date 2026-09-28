// Package logging provides structured logging using Go's slog package.
// It supports JSON and text output formats, log levels, and request ID correlation.
package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"

	"conduit/internal/redact"
)

// Default logger instance, initialized to a text handler writing to stderr
var (
	defaultLogger *slog.Logger
	defaultMu     sync.RWMutex
)

func init() {
	// Initialize with a default text logger
	defaultLogger = slog.New(slog.NewTextHandler(redact.NewWriter(os.Stderr), &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
}

// ParseLevel converts a string level to slog.Level
func ParseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// New creates a new slog.Logger with the specified configuration.
// It writes to os.Stderr by default.
func New(level string, format string) *slog.Logger {
	return NewWithWriter(level, format, redact.NewWriter(os.Stderr)) // conduit-31jg.83
}

// NewWithWriter creates a new slog.Logger with the specified configuration
// and custom writer.
func NewWithWriter(level string, format string, w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level: ParseLevel(level),
	}

	var handler slog.Handler
	switch strings.ToLower(format) {
	case "json":
		handler = slog.NewJSONHandler(w, opts)
	default:
		handler = slog.NewTextHandler(w, opts)
	}

	return slog.New(handler)
}

// SetDefault sets the default logger used by the package-level functions
func SetDefault(logger *slog.Logger) {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	defaultLogger = logger
}

// Default returns the default logger
func Default() *slog.Logger {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultLogger
}

// FromContext returns the default logger, annotated with the request ID
// when ctx carries one.
func FromContext(ctx context.Context) *slog.Logger {
	if ctx == nil {
		return Default()
	}

	// If there's a request ID in the context, add it to the default logger
	if reqID := RequestIDFromContext(ctx); reqID != "" {
		return Default().With("request_id", reqID)
	}

	return Default()
}

// Debug logs a debug message using the logger from context
func Debug(ctx context.Context, msg string, args ...any) {
	FromContext(ctx).Debug(msg, args...)
}

// Info logs an info message using the logger from context
func Info(ctx context.Context, msg string, args ...any) {
	FromContext(ctx).Info(msg, args...)
}

// Warn logs a warning message using the logger from context
func Warn(ctx context.Context, msg string, args ...any) {
	FromContext(ctx).Warn(msg, args...)
}

// Error logs an error message using the logger from context
func Error(ctx context.Context, msg string, args ...any) {
	FromContext(ctx).Error(msg, args...)
}
