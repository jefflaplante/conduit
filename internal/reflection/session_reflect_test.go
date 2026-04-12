package reflection

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComputeMetrics_MixedEntries(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	session := "sess-metrics"

	// 3 BashTool successes, 2 BashTool failures, 1 ReadFile success, 1 ReadFile failure, 1 pattern entry
	entries := []*ReflectionEntry{
		makeEntry("m-1", session, "BashTool", OutcomeSuccess, now),
		makeEntry("m-2", session, "BashTool", OutcomeSuccess, now.Add(time.Second)),
		makeEntry("m-3", session, "BashTool", OutcomeSuccess, now.Add(2*time.Second)),
		makeEntry("m-4", session, "BashTool", OutcomeFailure, now.Add(3*time.Second)),
		makeEntry("m-5", session, "BashTool", OutcomeFailure, now.Add(4*time.Second)),
		makeEntry("m-6", session, "ReadFile", OutcomeSuccess, now.Add(5*time.Second)),
		makeEntry("m-7", session, "ReadFile", OutcomeFailure, now.Add(6*time.Second)),
		// Timeout counts as a failure
		{
			ID: "m-8", SessionKey: session, Timestamp: now.Add(7 * time.Second),
			Source: "system", Type: TypeToolOutcome, Tool: "WebFetch",
			Outcome: OutcomeTimeout, Duration: 5 * time.Second,
		},
		// Pattern entry (circular detection)
		{
			ID: "m-9", SessionKey: session, Timestamp: now.Add(8 * time.Second),
			Source: "system", Type: TypePattern,
			Outcome: OutcomeFailure, Insight: "Read -> Bash -> Read loop detected",
		},
		// Another circular pattern
		{
			ID: "m-10", SessionKey: session, Timestamp: now.Add(9 * time.Second),
			Source: "system", Type: TypePattern,
			Outcome: OutcomeFailure, Insight: "Bash -> Bash -> Bash loop detected",
		},
	}

	require.NoError(t, store.InsertBatch(ctx, entries))

	reflector := NewSessionReflector(store)
	info := &SessionInfo{
		Duration:      5 * time.Minute,
		MessageCount:  20,
		MaxChainDepth: 4,
	}

	metrics, err := reflector.ComputeMetrics(ctx, session, info)
	require.NoError(t, err)

	assert.Equal(t, session, metrics.SessionKey)
	assert.Equal(t, 8, metrics.TotalToolCalls, "8 tool_outcome entries")
	assert.Equal(t, 3, metrics.UniqueTools, "BashTool, ReadFile, WebFetch")
	// m-4: BashTool failure, m-5: BashTool failure, m-7: ReadFile failure, m-8: WebFetch timeout = 4
	assert.Equal(t, 4, metrics.FailureCount, "2 BashTool + 1 ReadFile failures + 1 WebFetch timeout")
	assert.InDelta(t, 4.0/8.0, metrics.FailureRate, 0.001)

	assert.Equal(t, "BashTool", metrics.MostUsedTool, "BashTool has 5 calls")
	assert.Equal(t, "BashTool", metrics.MostFailedTool, "BashTool has 2 failures")

	assert.Equal(t, 2, metrics.CircularCount, "2 pattern entries")

	// SessionInfo passthrough
	assert.Equal(t, 5*time.Minute, metrics.Duration)
	assert.Equal(t, 20, metrics.MessageCount)
	assert.Equal(t, 4, metrics.MaxChainDepth)
}

func TestComputeMetrics_NoEntries(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	reflector := NewSessionReflector(store)
	metrics, err := reflector.ComputeMetrics(ctx, "empty-session", nil)
	require.NoError(t, err)

	assert.Equal(t, "empty-session", metrics.SessionKey)
	assert.Equal(t, 0, metrics.TotalToolCalls)
	assert.Equal(t, 0, metrics.UniqueTools)
	assert.Equal(t, 0, metrics.FailureCount)
	assert.Equal(t, 0.0, metrics.FailureRate)
	assert.Equal(t, "", metrics.MostUsedTool)
	assert.Equal(t, "", metrics.MostFailedTool)
	assert.Equal(t, 0, metrics.MaxChainDepth)
	assert.Equal(t, time.Duration(0), metrics.Duration)
	assert.Equal(t, 0, metrics.MessageCount)
	assert.Equal(t, 0, metrics.CircularCount)
}

func TestComputeMetrics_NilSessionInfo(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	session := "sess-nil-info"

	entry := makeEntry("ni-1", session, "BashTool", OutcomeSuccess, now)
	require.NoError(t, store.Insert(ctx, entry))

	reflector := NewSessionReflector(store)
	metrics, err := reflector.ComputeMetrics(ctx, session, nil)
	require.NoError(t, err)

	assert.Equal(t, 1, metrics.TotalToolCalls)
	assert.Equal(t, time.Duration(0), metrics.Duration)
	assert.Equal(t, 0, metrics.MessageCount)
	assert.Equal(t, 0, metrics.MaxChainDepth)
}

func TestWriteSessionSummary(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	reflector := NewSessionReflector(store)
	metrics := &SessionMetrics{
		SessionKey:     "sess-summary",
		TotalToolCalls: 10,
		UniqueTools:    3,
		FailureCount:   2,
		FailureRate:    0.2,
		MostUsedTool:   "BashTool",
		MostFailedTool: "WebFetch",
		MaxChainDepth:  3,
		Duration:       10 * time.Minute,
		MessageCount:   15,
		CircularCount:  1,
	}

	err := reflector.WriteSessionSummary(ctx, metrics, 4)
	require.NoError(t, err)

	// Verify entry was written
	entries, err := store.QueryBySession(ctx, "sess-summary")
	require.NoError(t, err)
	require.Len(t, entries, 1)

	entry := entries[0]
	assert.Equal(t, "sess-summary", entry.SessionKey)
	assert.Equal(t, "system", entry.Source)
	assert.Equal(t, TypeSessionSummary, entry.Type)
	assert.Equal(t, OutcomeSuccess, entry.Outcome)
	assert.Equal(t, 4, entry.Score)
	assert.NotEmpty(t, entry.ID, "should have a UUID")
	assert.False(t, entry.Timestamp.IsZero(), "should have a timestamp")

	// Verify insight contains JSON-encoded metrics
	var decoded SessionMetrics
	err = json.Unmarshal([]byte(entry.Insight), &decoded)
	require.NoError(t, err)
	assert.Equal(t, "sess-summary", decoded.SessionKey)
	assert.Equal(t, 10, decoded.TotalToolCalls)
	assert.Equal(t, 3, decoded.UniqueTools)
	assert.Equal(t, 2, decoded.FailureCount)
	assert.InDelta(t, 0.2, decoded.FailureRate, 0.001)
	assert.Equal(t, "BashTool", decoded.MostUsedTool)
	assert.Equal(t, "WebFetch", decoded.MostFailedTool)
}

func TestWriteSessionSummary_ZeroScore(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	reflector := NewSessionReflector(store)
	metrics := &SessionMetrics{
		SessionKey:     "sess-unscored",
		TotalToolCalls: 5,
	}

	err := reflector.WriteSessionSummary(ctx, metrics, 0)
	require.NoError(t, err)

	entries, err := store.QueryBySession(ctx, "sess-unscored")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, 0, entries[0].Score)
}

func TestBuildReflectionPrompt(t *testing.T) {
	reflector := NewSessionReflector(nil)
	prompt := reflector.BuildReflectionPrompt()

	assert.NotEmpty(t, prompt)

	// Verify key phrases from the Diff B spec are present.
	assert.True(t, strings.Contains(prompt, "[Session Reflection]"),
		"should contain section header")
	assert.True(t, strings.Contains(prompt, "primary ask"),
		"should ask about primary ask")
	assert.True(t, strings.Contains(prompt, "tool failures"),
		"should ask about tool failures")
	assert.True(t, strings.Contains(prompt, "patterns worth remembering"),
		"should ask about patterns")
	assert.True(t, strings.Contains(prompt, "1-5"),
		"should include rating scale")
	assert.True(t, strings.Contains(prompt, "Brain(action=\"store\""),
		"should include Brain store instruction")
	assert.True(t, strings.Contains(prompt, "Brain(action=\"consolidate\")"),
		"should include Brain consolidate instruction")
	assert.True(t, strings.Contains(prompt, "reflect.session."),
		"should include reflect.session namespace")
}

func TestMaxKey(t *testing.T) {
	tests := []struct {
		name string
		m    map[string]int
		want string
	}{
		{"empty map", map[string]int{}, ""},
		{"single entry", map[string]int{"a": 1}, "a"},
		{"clear winner", map[string]int{"a": 1, "b": 5, "c": 3}, "b"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, maxKey(tt.m))
		})
	}
}
