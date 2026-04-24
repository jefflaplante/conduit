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

// TestReflectionMiddleware_SkipsInsertWhenCallerNearlyExpired verifies that
// when the caller's context has less than reflectionInsertMinRemaining time
// left, the middleware skips the insert entirely (conduit-3fwk).
func TestReflectionMiddleware_SkipsInsertWhenCallerNearlyExpired(t *testing.T) {
	store := newTestStore(t)
	mw := NewReflectionMiddleware(store, DefaultConfig())

	var called bool
	mw.insertFn = func(_ context.Context, _ *ReflectionEntry) error {
		called = true
		return nil
	}

	// Caller deadline well below the 1s minimum — insert must be skipped.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	mw.recordOutcome(ctx, ToolOutcomeInfo{
		ToolName:   "ReadFile",
		SessionKey: "sess-skip",
		Success:    true,
		Duration:   10 * time.Millisecond,
	})

	assert.False(t, called, "insert must be skipped when caller's deadline < 1s")
}

// TestReflectionMiddleware_CapsInsertTimeoutToCallerDeadline verifies that
// when the caller has a deadline shorter than the default 5s, the detached
// insert context's deadline is capped near (and below) the caller's deadline
// rather than running for the full 5s (conduit-3fwk).
func TestReflectionMiddleware_CapsInsertTimeoutToCallerDeadline(t *testing.T) {
	store := newTestStore(t)
	mw := NewReflectionMiddleware(store, DefaultConfig())

	var insertCtx context.Context
	mw.insertFn = func(ctx context.Context, _ *ReflectionEntry) error {
		insertCtx = ctx
		return nil
	}

	callerCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	callerDeadline, _ := callerCtx.Deadline()

	mw.recordOutcome(callerCtx, ToolOutcomeInfo{
		ToolName:   "ReadFile",
		SessionKey: "sess-cap",
		Success:    true,
		Duration:   10 * time.Millisecond,
	})

	require.NotNil(t, insertCtx, "insert should have been called")
	insertDeadline, ok := insertCtx.Deadline()
	require.True(t, ok, "insert context must have a deadline")

	// Insert deadline must NOT exceed caller's deadline (with a small slack
	// for clock movement during the call).
	assert.LessOrEqual(t, insertDeadline.UnixNano(), callerDeadline.Add(100*time.Millisecond).UnixNano(),
		"insert deadline must not exceed caller's deadline + 100ms slack")

	// Insert deadline must be well below the default 5s (i.e. capped).
	assert.Less(t, time.Until(insertDeadline), reflectionInsertTimeout,
		"insert deadline must be capped below the default 5s timeout")
}

// TestReflectionMiddleware_NoCallerDeadlineUsesDefaultTimeout verifies that
// when the caller has no deadline (e.g. context.Background), the middleware
// uses the full default 5s insert timeout (conduit-3fwk).
func TestReflectionMiddleware_NoCallerDeadlineUsesDefaultTimeout(t *testing.T) {
	store := newTestStore(t)
	mw := NewReflectionMiddleware(store, DefaultConfig())

	var insertCtx context.Context
	mw.insertFn = func(ctx context.Context, _ *ReflectionEntry) error {
		insertCtx = ctx
		return nil
	}

	mw.recordOutcome(context.Background(), ToolOutcomeInfo{
		ToolName:   "ReadFile",
		SessionKey: "sess-bg",
		Success:    true,
		Duration:   10 * time.Millisecond,
	})

	require.NotNil(t, insertCtx, "insert should have been called")
	deadline, ok := insertCtx.Deadline()
	require.True(t, ok, "insert context must always carry a deadline")

	remaining := time.Until(deadline)
	// Should be near 5s — allow generous lower bound to avoid flake.
	assert.Greater(t, remaining, 4*time.Second,
		"with no caller deadline, insert should use the default ~5s timeout")
	assert.LessOrEqual(t, remaining, reflectionInsertTimeout+100*time.Millisecond,
		"insert deadline should not exceed the default timeout")
}
