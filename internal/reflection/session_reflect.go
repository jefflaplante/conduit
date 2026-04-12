package reflection

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// SessionInfo holds session-level data that comes from the session store
// rather than the reflection store (e.g. duration, message count).
type SessionInfo struct {
	Duration      time.Duration
	MessageCount  int
	MaxChainDepth int
}

// SessionMetrics summarizes tool usage and outcomes for a single session.
type SessionMetrics struct {
	SessionKey     string        `json:"session_key"`
	TotalToolCalls int           `json:"total_tool_calls"`
	UniqueTools    int           `json:"unique_tools"`
	FailureCount   int           `json:"failure_count"`
	FailureRate    float64       `json:"failure_rate"`
	MostUsedTool   string        `json:"most_used_tool"`
	MostFailedTool string        `json:"most_failed_tool"`
	MaxChainDepth  int           `json:"max_chain_depth"`
	Duration       time.Duration `json:"duration"`
	MessageCount   int           `json:"message_count"`
	CircularCount  int           `json:"circular_count"`
}

// SessionReflector computes session metrics from reflection data and writes
// session summary entries.
type SessionReflector struct {
	store *ReflectionStore
}

// NewSessionReflector creates a SessionReflector backed by the given store.
func NewSessionReflector(store *ReflectionStore) *SessionReflector {
	return &SessionReflector{store: store}
}

// ComputeMetrics queries the reflection store for all entries in the given
// session and computes aggregate metrics. SessionInfo supplies data from the
// session store that is not available in the reflection store.
func (r *SessionReflector) ComputeMetrics(ctx context.Context, sessionKey string, info *SessionInfo) (*SessionMetrics, error) {
	entries, err := r.store.QueryBySession(ctx, sessionKey)
	if err != nil {
		return nil, fmt.Errorf("query session entries: %w", err)
	}

	m := &SessionMetrics{
		SessionKey: sessionKey,
	}

	// Apply session-level info if provided.
	if info != nil {
		m.Duration = info.Duration
		m.MessageCount = info.MessageCount
		m.MaxChainDepth = info.MaxChainDepth
	}

	// Track per-tool usage and failures.
	toolUsage := make(map[string]int)
	toolFailures := make(map[string]int)

	for _, e := range entries {
		switch e.Type {
		case TypeToolOutcome:
			m.TotalToolCalls++
			if e.Tool != "" {
				toolUsage[e.Tool]++
			}
			if e.Outcome == OutcomeFailure || e.Outcome == OutcomeTimeout {
				m.FailureCount++
				if e.Tool != "" {
					toolFailures[e.Tool]++
				}
			}
		case TypePattern:
			m.CircularCount++
		}
	}

	// Unique tool count.
	m.UniqueTools = len(toolUsage)

	// Failure rate.
	if m.TotalToolCalls > 0 {
		m.FailureRate = float64(m.FailureCount) / float64(m.TotalToolCalls)
	}

	// Most-used tool.
	m.MostUsedTool = maxKey(toolUsage)

	// Most-failed tool.
	m.MostFailedTool = maxKey(toolFailures)

	return m, nil
}

// WriteSessionSummary creates a TypeSessionSummary entry with the metrics
// JSON-encoded in the Insight field and writes it via the store.
//
// Score semantics: 0 means Go-computed (unscored), 1-5 means model-scored.
func (r *SessionReflector) WriteSessionSummary(ctx context.Context, metrics *SessionMetrics, score int) error {
	insight, err := json.Marshal(metrics)
	if err != nil {
		return fmt.Errorf("marshal session metrics: %w", err)
	}

	entry := NewEntry("system", TypeSessionSummary, OutcomeSuccess)
	entry.SessionKey = metrics.SessionKey
	entry.Insight = string(insight)
	entry.Score = score

	if err := r.store.Insert(ctx, entry); err != nil {
		return fmt.Errorf("write session summary: %w", err)
	}
	return nil
}

// BuildReflectionPrompt returns the Diff B session reflection prompt text
// for injection into a conversation at session-end.
func (r *SessionReflector) BuildReflectionPrompt() string {
	return `[Session Reflection] Before wrapping up, briefly assess:
1. What was the primary ask? Was it satisfied?
2. Any tool failures, unexpected results, or things that took multiple tries?
3. Any patterns worth remembering for future sessions?
4. Rate this session's outcome 1-5 (1=failed, 3=partial, 5=fully satisfied).
Store findings: Brain(action="store", key="reflect.session.<topic>", value="<insight>", tier="working")
Then: Brain(action="consolidate") to promote important items.`
}

// maxKey returns the key with the highest value in the map, or "" if empty.
// When there is a tie the result is deterministic but unspecified.
func maxKey(m map[string]int) string {
	var best string
	var bestCount int
	for k, v := range m {
		if v > bestCount || (v == bestCount && k < best) {
			best = k
			bestCount = v
		}
	}
	return best
}
