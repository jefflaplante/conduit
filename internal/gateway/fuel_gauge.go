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

// ToMap renders the FuelGauge as a JSON-serializable map suitable for tool
// responses. The structure is intentionally flat / shallow so callers can
// forward it over the GatewayService interface without a direct import of
// the middleware or monitoring packages.
func (f FuelGauge) ToMap() map[string]interface{} {
	return map[string]interface{}{
		"taken_at": f.TakenAt,
		"rate_limit": map[string]interface{}{
			"enabled":  f.RateLimit.Enabled,
			"taken_at": f.RateLimit.TakenAt,
			"anonymous": map[string]interface{}{
				"enabled":        f.RateLimit.Anonymous.Enabled,
				"limit":          f.RateLimit.Anonymous.Limit,
				"window_seconds": int(f.RateLimit.Anonymous.WindowDuration.Seconds()),
				"active_buckets": f.RateLimit.Anonymous.ActiveBuckets,
			},
			"authenticated": map[string]interface{}{
				"enabled":        f.RateLimit.Authenticated.Enabled,
				"limit":          f.RateLimit.Authenticated.Limit,
				"window_seconds": int(f.RateLimit.Authenticated.WindowDuration.Seconds()),
				"active_buckets": f.RateLimit.Authenticated.ActiveBuckets,
			},
		},
		"token_usage": map[string]interface{}{
			"taken_at": f.TokenUsage.TakenAt,
			"hour": map[string]interface{}{
				"requests":      f.TokenUsage.Hour.Requests,
				"input_tokens":  f.TokenUsage.Hour.InputTokens,
				"output_tokens": f.TokenUsage.Hour.OutputTokens,
				"cache_read":    f.TokenUsage.Hour.CacheRead,
				"cache_write":   f.TokenUsage.Hour.CacheWrite,
				"errors":        f.TokenUsage.Hour.Errors,
			},
			"day": map[string]interface{}{
				"requests":      f.TokenUsage.Day.Requests,
				"input_tokens":  f.TokenUsage.Day.InputTokens,
				"output_tokens": f.TokenUsage.Day.OutputTokens,
				"cache_read":    f.TokenUsage.Day.CacheRead,
				"cache_write":   f.TokenUsage.Day.CacheWrite,
				"errors":        f.TokenUsage.Day.Errors,
			},
		},
	}
}

// GetFuelGaugeMap is the GatewayService interface method: returns the fuel-gauge
// snapshot as a map so tool-layer consumers don't take a direct dependency on
// the gateway.FuelGauge struct (avoids circular imports). topN caps the number
// of per-tier rate-limit identifiers returned.
func (g *Gateway) GetFuelGaugeMap(topN int) map[string]interface{} {
	return g.GetFuelGauge(topN).ToMap()
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

	if g.monitoring != nil && g.monitoring.TokenWindow != nil {
		gauge.TokenUsage = g.monitoring.TokenWindow.Snapshot()
	} else {
		gauge.TokenUsage = monitoring.TokenUsageSnapshot{TakenAt: now}
	}

	return gauge
}
