package gateway

import (
	"context"
	"time"

	"conduit/internal/reflection"
	"conduit/internal/sessions"
)

// reflectOnIdleSessions runs periodically to write Go-only reflection metrics
// for substantive sessions that have gone idle without a clean WS disconnect
// (e.g., network drops where the close frame is never received).
//
// A session is considered "substantive" when it has >5 messages. This is a
// heuristic from the SPAR spec to avoid writing summaries for trivial sessions
// (e.g. a single /status check). Already-reflected sessions are tracked in
// memory to avoid duplicate writes across ticks.
func (g *Gateway) reflectOnIdleSessions(ctx context.Context, idleTimeout time.Duration, interval time.Duration) {
	if g.sessionReflector == nil {
		return
	}

	reflected := make(map[string]struct{})
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-idleTimeout)
			keys, err := g.sessions.GetIdleSessions(cutoff, 5)
			if err != nil {
				g.logger.Warn("reflection: failed to query idle sessions", "error", err)
				continue
			}
			for _, key := range keys {
				if _, done := reflected[key]; done {
					continue
				}
				reflCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				g.reflectOnSessionEnd(reflCtx, key)
				cancel()
				reflected[key] = struct{}{}
			}
		}
	}
}

// reflectOnSessionEnd performs low-confidence (Go-only) reflection for a session.
// This is called when the model is unavailable (idle timeout, WS disconnect).
// It writes a session summary with score=0 if the session was substantive.
func (g *Gateway) reflectOnSessionEnd(ctx context.Context, sessionKey string) {
	if g.sessionReflector == nil || g.reflectionStore == nil {
		return
	}

	// Look up the session to check if it's substantive
	session, err := g.sessions.GetSession(sessionKey)
	if err != nil {
		g.logger.Debug("reflection: could not look up session for end-of-session metrics",
			"session_key", sessionKey, "error", err)
		return
	}

	// Substantive check: >5 messages (spec threshold)
	if session.MessageCount <= 5 {
		g.logger.Debug("reflection: skipping non-substantive session",
			"session_key", sessionKey, "message_count", session.MessageCount)
		return
	}

	// Compute session duration
	duration := time.Since(session.CreatedAt)

	info := &reflection.SessionInfo{
		Duration:     duration,
		MessageCount: session.MessageCount,
	}

	metrics, err := g.sessionReflector.ComputeMetrics(ctx, sessionKey, info)
	if err != nil {
		g.logger.Warn("reflection: failed to compute session metrics",
			"session_key", sessionKey, "error", err)
		return
	}

	// Write session summary with score=0 (Go-computed, unscored)
	if err := g.sessionReflector.WriteSessionSummary(ctx, metrics, 0); err != nil {
		g.logger.Warn("reflection: failed to write session summary",
			"session_key", sessionKey, "error", err)
		return
	}

	g.logger.Info("reflection: wrote low-confidence session summary",
		"session_key", sessionKey,
		"message_count", session.MessageCount,
		"tool_calls", metrics.TotalToolCalls,
		"failure_rate", metrics.FailureRate)
}

// reflectHighConfidencePre returns the reflection prompt text to inject into
// the conversation at session-end. The caller appends this to the user's
// message so the model reflects before signing off.
//
// Returns empty string if reflection is not initialized.
func (g *Gateway) reflectHighConfidencePre() string {
	if g.sessionReflector == nil {
		return ""
	}
	return g.sessionReflector.BuildReflectionPrompt()
}

// reflectHighConfidencePost computes metrics and writes the session summary
// after the model has responded to the reflection prompt.
func (g *Gateway) reflectHighConfidencePost(ctx context.Context, session *sessions.Session) {
	if g.sessionReflector == nil {
		return
	}

	duration := time.Since(session.CreatedAt)
	info := &reflection.SessionInfo{
		Duration:     duration,
		MessageCount: session.MessageCount,
	}

	metrics, err := g.sessionReflector.ComputeMetrics(ctx, session.Key, info)
	if err != nil {
		g.logger.Warn("reflection: failed to compute post-reflection metrics",
			"session_key", session.Key, "error", err)
		return
	}

	// Score is not set here — the model's response should have stored a score
	// via Brain. We write the Go-computed summary with score=0; the model's
	// self-assessment is stored separately in Brain under reflect.session.*.
	if err := g.sessionReflector.WriteSessionSummary(ctx, metrics, 0); err != nil {
		g.logger.Warn("reflection: failed to write post-reflection summary",
			"session_key", session.Key, "error", err)
		return
	}

	g.logger.Info("reflection: wrote high-confidence session summary",
		"session_key", session.Key,
		"message_count", session.MessageCount,
		"tool_calls", metrics.TotalToolCalls)
}

// shouldTriggerReflection checks if a message should trigger session-end reflection.
// Returns (shouldTrigger, triggerType). Safe to call even if reflection is disabled
// (returns false).
func (g *Gateway) shouldTriggerReflection(message string) (bool, reflection.TriggerType) {
	if g.farewellDetector == nil {
		return false, reflection.TriggerNone
	}
	return g.farewellDetector.ShouldTriggerReflection(message)
}
