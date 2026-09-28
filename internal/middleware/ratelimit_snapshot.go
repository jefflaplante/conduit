package middleware

import (
	"time"

	"conduit/internal/ratelimit"
)

// RateLimitTierSnapshot describes the current state of one rate-limit tier.
type RateLimitTierSnapshot struct {
	Enabled        bool                     `json:"enabled"`
	WindowDuration time.Duration            `json:"window_duration"`
	Limit          int                      `json:"limit"`
	ActiveBuckets  int                      `json:"active_buckets"` // Total tracked identifiers
	TopIdentifiers []TierIdentifierSnapshot `json:"top_identifiers,omitempty"`
}

// TierIdentifierSnapshot mirrors ratelimit.IdentifierSnapshot with a stable
// JSON-friendly shape for tool consumers.
type TierIdentifierSnapshot struct {
	Identifier string    `json:"identifier"`
	Used       int       `json:"used"`
	Remaining  int       `json:"remaining"`
	ResetAt    time.Time `json:"reset_at"`
}

// RateLimitSnapshot is a point-in-time view of rate-limit headroom used by
// the fuel-gauge API. It intentionally exposes only what a querying agent
// needs: per-tier configuration plus active-bucket counts and the top N
// most-consumed identifiers per tier.
type RateLimitSnapshot struct {
	Enabled       bool                  `json:"enabled"`
	Anonymous     RateLimitTierSnapshot `json:"anonymous"`
	Authenticated RateLimitTierSnapshot `json:"authenticated"`
	TakenAt       time.Time             `json:"taken_at"`
}

// Snapshot returns the current rate-limit state without mutating it.
// Each tier reports its configuration, the number of active buckets, and
// up to topN identifiers sorted by fewest remaining requests (i.e. closest
// to being rate-limited). Pass topN <= 0 for "all identifiers".
func (m *RateLimitMiddleware) Snapshot(topN int) RateLimitSnapshot {
	out := RateLimitSnapshot{
		Enabled: m.config.Enabled,
		TakenAt: time.Now(),
	}

	out.Anonymous = snapshotTier(m.anonymousLimiter, m.config.Anonymous.WindowSeconds, m.config.Anonymous.MaxRequests, m.config.Enabled, topN)
	out.Authenticated = snapshotTier(m.authenticatedLimiter, m.config.Authenticated.WindowSeconds, m.config.Authenticated.MaxRequests, m.config.Enabled, topN)

	return out
}

// snapshotTier builds a tier snapshot from a sliding-window limiter.
// If the limiter is nil (rate limiting disabled) it reports the tier as
// disabled but still includes the configured limit/window for callers.
func snapshotTier(limiter *ratelimit.SlidingWindow, windowSeconds, maxRequests int, globalEnabled bool, topN int) RateLimitTierSnapshot {
	tier := RateLimitTierSnapshot{
		Enabled:        globalEnabled && limiter != nil,
		WindowDuration: time.Duration(windowSeconds) * time.Second,
		Limit:          maxRequests,
	}
	if limiter == nil {
		return tier
	}

	snap := limiter.Snapshot()
	tier.ActiveBuckets = snap.ActiveBuckets

	if len(snap.Identifiers) == 0 {
		return tier
	}

	// Sort by Remaining ascending (smallest first) so the tightest identifiers
	// surface first. Ties broken by most-recent access.
	ids := make([]TierIdentifierSnapshot, 0, len(snap.Identifiers))
	for _, s := range snap.Identifiers {
		ids = append(ids, TierIdentifierSnapshot{
			Identifier: s.Identifier,
			Used:       s.Used,
			Remaining:  s.Remaining,
			ResetAt:    s.ResetAt,
		})
	}
	sortIdentifiersByPressure(ids)
	if topN > 0 && len(ids) > topN {
		ids = ids[:topN]
	}
	tier.TopIdentifiers = ids
	return tier
}

// sortIdentifiersByPressure sorts identifiers with smallest Remaining first.
func sortIdentifiersByPressure(ids []TierIdentifierSnapshot) {
	// Simple in-place selection sort; identifier count is bounded by active
	// buckets (usually <1000). Avoids pulling in sort just for this.
	for i := 0; i < len(ids); i++ {
		minIdx := i
		for j := i + 1; j < len(ids); j++ {
			if ids[j].Remaining < ids[minIdx].Remaining {
				minIdx = j
			}
		}
		if minIdx != i {
			ids[i], ids[minIdx] = ids[minIdx], ids[i]
		}
	}
}

// GetStats returns statistics about rate limiting
func (m *RateLimitMiddleware) GetStats() map[string]interface{} {
	if !m.config.Enabled {
		return map[string]interface{}{
			"enabled": false,
		}
	}

	stats := map[string]interface{}{
		"enabled": true,
		"config": map[string]interface{}{
			"anonymous": map[string]interface{}{
				"window_seconds": m.config.Anonymous.WindowSeconds,
				"max_requests":   m.config.Anonymous.MaxRequests,
			},
			"authenticated": map[string]interface{}{
				"window_seconds": m.config.Authenticated.WindowSeconds,
				"max_requests":   m.config.Authenticated.MaxRequests,
			},
			"cleanup_interval_seconds": m.config.CleanupIntervalSeconds,
		},
	}

	if m.anonymousLimiter != nil {
		anonymousStats := m.anonymousLimiter.GetStats()
		stats["anonymous_stats"] = map[string]interface{}{
			"active_buckets":   anonymousStats.ActiveBuckets,
			"total_timestamps": anonymousStats.TotalTimestamps,
		}
	}

	if m.authenticatedLimiter != nil {
		authenticatedStats := m.authenticatedLimiter.GetStats()
		stats["authenticated_stats"] = map[string]interface{}{
			"active_buckets":   authenticatedStats.ActiveBuckets,
			"total_timestamps": authenticatedStats.TotalTimestamps,
		}
	}

	return stats
}
