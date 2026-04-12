package reflection

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReflectionMiddleware_SuccessfulExecution(t *testing.T) {
	store := newTestStore(t)
	cfg := DefaultConfig() // capture_level = "all"
	mw := NewReflectionMiddleware(store, cfg)
	hook := mw.Hook()

	ctx := context.Background()
	hook(ctx, ToolOutcomeInfo{
		ToolName:   "ReadFile",
		SessionKey: "sess-1",
		Success:    true,
		Duration:   150 * time.Millisecond,
	})

	entries, err := store.QueryBySession(ctx, "sess-1")
	require.NoError(t, err)
	require.Len(t, entries, 1)

	e := entries[0]
	assert.Equal(t, "ReadFile", e.Tool)
	assert.Equal(t, OutcomeSuccess, e.Outcome)
	assert.Equal(t, TypeToolOutcome, e.Type)
	assert.Equal(t, "system", e.Source)
	assert.Equal(t, 150*time.Millisecond, e.Duration)
	assert.Empty(t, e.Insight, "success should not set insight")
}

func TestReflectionMiddleware_FailedExecution(t *testing.T) {
	store := newTestStore(t)
	cfg := DefaultConfig()
	mw := NewReflectionMiddleware(store, cfg)
	hook := mw.Hook()

	ctx := context.Background()
	hook(ctx, ToolOutcomeInfo{
		ToolName:   "Bash",
		SessionKey: "sess-2",
		Success:    false,
		Error:      "permission denied: /etc/shadow",
		Duration:   25 * time.Millisecond,
		RetryCount: 2,
	})

	entries, err := store.QueryBySession(ctx, "sess-2")
	require.NoError(t, err)
	require.Len(t, entries, 1)

	e := entries[0]
	assert.Equal(t, "Bash", e.Tool)
	assert.Equal(t, OutcomeFailure, e.Outcome)
	assert.Equal(t, "permission denied: /etc/shadow", e.Insight)
	assert.Equal(t, 2, e.RetryCount)
}

func TestReflectionMiddleware_TimeoutExecution(t *testing.T) {
	store := newTestStore(t)
	cfg := DefaultConfig()
	mw := NewReflectionMiddleware(store, cfg)
	hook := mw.Hook()

	ctx := context.Background()
	hook(ctx, ToolOutcomeInfo{
		ToolName:   "WebFetch",
		SessionKey: "sess-3",
		Success:    false,
		Error:      "context deadline exceeded",
		IsTimeout:  true,
		Duration:   30 * time.Second,
	})

	entries, err := store.QueryBySession(ctx, "sess-3")
	require.NoError(t, err)
	require.Len(t, entries, 1)

	e := entries[0]
	assert.Equal(t, OutcomeTimeout, e.Outcome)
	assert.Equal(t, "context deadline exceeded", e.Insight)
}

func TestReflectionMiddleware_CaptureLevel_Failures(t *testing.T) {
	store := newTestStore(t)
	cfg := &ReflectionConfig{
		Enabled:       true,
		CaptureLevel:  "failures",
		RetentionDays: 30,
	}
	mw := NewReflectionMiddleware(store, cfg)
	hook := mw.Hook()

	ctx := context.Background()

	// Success should NOT be captured.
	hook(ctx, ToolOutcomeInfo{
		ToolName:   "ReadFile",
		SessionKey: "sess-4",
		Success:    true,
		Duration:   10 * time.Millisecond,
	})

	entries, err := store.QueryBySession(ctx, "sess-4")
	require.NoError(t, err)
	assert.Empty(t, entries, "success should not be captured at 'failures' level")

	// Failure SHOULD be captured.
	hook(ctx, ToolOutcomeInfo{
		ToolName:   "Bash",
		SessionKey: "sess-4",
		Success:    false,
		Error:      "command not found",
		Duration:   5 * time.Millisecond,
	})

	entries, err = store.QueryBySession(ctx, "sess-4")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, OutcomeFailure, entries[0].Outcome)

	// Timeout SHOULD be captured.
	hook(ctx, ToolOutcomeInfo{
		ToolName:   "WebFetch",
		SessionKey: "sess-4",
		Success:    false,
		Error:      "context deadline exceeded",
		IsTimeout:  true,
		Duration:   30 * time.Second,
	})

	entries, err = store.QueryBySession(ctx, "sess-4")
	require.NoError(t, err)
	require.Len(t, entries, 2)
}

func TestReflectionMiddleware_CaptureLevel_Anomalies(t *testing.T) {
	store := newTestStore(t)
	cfg := &ReflectionConfig{
		Enabled:       true,
		CaptureLevel:  "anomalies",
		RetentionDays: 30,
	}
	mw := NewReflectionMiddleware(store, cfg)
	hook := mw.Hook()

	ctx := context.Background()

	// Normal success should NOT be captured.
	hook(ctx, ToolOutcomeInfo{
		ToolName:   "ReadFile",
		SessionKey: "sess-5",
		Success:    true,
		Duration:   10 * time.Millisecond,
	})

	entries, err := store.QueryBySession(ctx, "sess-5")
	require.NoError(t, err)
	assert.Empty(t, entries, "success should not be captured at 'anomalies' level")

	// Failure SHOULD be captured.
	hook(ctx, ToolOutcomeInfo{
		ToolName:   "Bash",
		SessionKey: "sess-5",
		Success:    false,
		Error:      "exit code 1",
		Duration:   50 * time.Millisecond,
	})

	entries, err = store.QueryBySession(ctx, "sess-5")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, OutcomeFailure, entries[0].Outcome)

	// Timeout SHOULD be captured.
	hook(ctx, ToolOutcomeInfo{
		ToolName:   "WebFetch",
		SessionKey: "sess-5",
		Success:    false,
		Error:      "context deadline exceeded",
		IsTimeout:  true,
		Duration:   30 * time.Second,
	})

	entries, err = store.QueryBySession(ctx, "sess-5")
	require.NoError(t, err)
	require.Len(t, entries, 2)
}

func TestReflectionMiddleware_Disabled(t *testing.T) {
	store := newTestStore(t)
	cfg := &ReflectionConfig{
		Enabled:       false,
		CaptureLevel:  "all",
		RetentionDays: 30,
	}
	mw := NewReflectionMiddleware(store, cfg)
	hook := mw.Hook()

	ctx := context.Background()
	hook(ctx, ToolOutcomeInfo{
		ToolName:   "ReadFile",
		SessionKey: "sess-disabled",
		Success:    true,
		Duration:   10 * time.Millisecond,
	})

	entries, err := store.QueryBySession(ctx, "sess-disabled")
	require.NoError(t, err)
	assert.Empty(t, entries, "nothing should be captured when disabled")
}

func TestReflectionMiddleware_NilStore(t *testing.T) {
	cfg := DefaultConfig()
	mw := NewReflectionMiddleware(nil, cfg)
	hook := mw.Hook()

	// Should not panic.
	hook(context.Background(), ToolOutcomeInfo{
		ToolName: "ReadFile",
		Success:  true,
		Duration: 10 * time.Millisecond,
	})
}

func TestClassifyOutcome(t *testing.T) {
	tests := []struct {
		name     string
		info     ToolOutcomeInfo
		expected Outcome
	}{
		{
			name:     "success",
			info:     ToolOutcomeInfo{Success: true},
			expected: OutcomeSuccess,
		},
		{
			name:     "failure from error",
			info:     ToolOutcomeInfo{Success: false, Error: "boom"},
			expected: OutcomeFailure,
		},
		{
			name:     "failure from success=false without error",
			info:     ToolOutcomeInfo{Success: false},
			expected: OutcomeFailure,
		},
		{
			name:     "timeout overrides failure",
			info:     ToolOutcomeInfo{Success: false, Error: "context deadline exceeded", IsTimeout: true},
			expected: OutcomeTimeout,
		},
		{
			name:     "success=true but has error string → failure",
			info:     ToolOutcomeInfo{Success: true, Error: "partial error"},
			expected: OutcomeFailure,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, classifyOutcome(tt.info))
		})
	}
}

func TestIsTimeoutError(t *testing.T) {
	assert.True(t, IsTimeoutError("context deadline exceeded"))
	assert.True(t, IsTimeoutError("operation timeout after 30s"))
	assert.True(t, IsTimeoutError("Context Deadline Exceeded"))
	assert.False(t, IsTimeoutError("permission denied"))
	assert.False(t, IsTimeoutError(""))
}
