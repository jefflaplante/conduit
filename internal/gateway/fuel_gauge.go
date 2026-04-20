package gateway

import (
	"time"

	"conduit/internal/middleware"
	"conduit/internal/monitoring"
)

// FuelGauge is the agent-queryable view of how close the gateway is to its
// external ceilings. It intentionally stays small: rate-limit headroom per
// tier and rolling-window token consumption. Agents can use it to self-throttle
// non-critical work, defer batch jobs, or warn the user before hard limits.
//
// Values are always a point-in-time view; nothing is persisted. There is no
// HTTP endpoint for this data — it is exposed only via Gateway.GetFuelGauge()
// for tools/agents that already hold a reference to the gateway.
type FuelGauge struct {
	// RateLimit is the snapshot of the HTTP rate-limit middleware (per-tier
	// active buckets and tightest identifiers). Always populated; when rate
	// limiting is globally disabled Enabled=false and limits still reflect
	// the configured values.
	RateLimit middleware.RateLimitSnapshot `json:"rate_limit"`

	// TokenUsage is the rolling hour / rolling day AI token counter. Zero
	// values mean no traffic has been recorded in that window.
	TokenUsage monitoring.TokenUsageSnapshot `json:"token_usage"`

	// TakenAt is the time at which the snapshot was assembled.
	TakenAt time.Time `json:"taken_at"`
}

// HasTokenTraffic reports whether any successful AI call has been recorded
// in either window. Convenience for callers wanting a quick "is this data
// meaningful yet?" check.
func (f FuelGauge) HasTokenTraffic() bool {
	return f.TokenUsage.Hour.Requests > 0 || f.TokenUsage.Day.Requests > 0
}

// GetFuelGauge returns a point-in-time snapshot of rate-limit headroom and
// rolling-window AI token consumption. topN caps the number of per-tier
// identifiers returned (most-pressured first); pass 0 or a negative value
// to include all.
func (g *Gateway) GetFuelGauge(topN int) FuelGauge {
	now := time.Now()
	gauge := FuelGauge{TakenAt: now}

	if g.rateLimitMiddleware != nil {
		gauge.RateLimit = g.rateLimitMiddleware.Snapshot(topN)
	} else {
		// Middleware not initialised (e.g. tests). Surface a disabled shell
		// so callers can still rely on field presence.
		gauge.RateLimit = middleware.RateLimitSnapshot{
			Enabled: false,
			TakenAt: now,
		}
	}

	if g.tokenWindow != nil {
		gauge.TokenUsage = g.tokenWindow.Snapshot()
	} else {
		gauge.TokenUsage = monitoring.TokenUsageSnapshot{TakenAt: now}
	}

	return gauge
}
