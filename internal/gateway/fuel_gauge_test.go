package gateway

import (
	"testing"

	"conduit/internal/middleware"
	"conduit/internal/monitoring"
)

func TestGetFuelGauge_NilComponentsReturnEmpty(t *testing.T) {
	gw, _ := newTestGatewayWithSessions(t)
	// Neither rateLimitMiddleware nor tokenWindow are wired in the minimal
	// test gateway; GetFuelGauge must not panic and must return a usable
	// zero-value shell.
	gauge := gw.GetFuelGauge(5)

	if gauge.RateLimit.Enabled {
		t.Error("RateLimit.Enabled: expected false with nil middleware")
	}
	if gauge.HasTokenTraffic() {
		t.Error("HasTokenTraffic should be false on fresh gauge")
	}
	if gauge.TakenAt.IsZero() {
		t.Error("TakenAt should be populated")
	}
}

func TestGetFuelGauge_ExposesTokenWindow(t *testing.T) {
	gw, _ := newTestGatewayWithSessions(t)

	tr := monitoring.NewTokenWindowTracker()
	tr.Record(1000, 500, 10, 5)
	tr.Record(250, 125, 0, 0)
	gw.tokenWindow = tr

	gauge := gw.GetFuelGauge(0)
	if !gauge.HasTokenTraffic() {
		t.Error("HasTokenTraffic should be true after two records")
	}
	if gauge.TokenUsage.Hour.Requests != 2 {
		t.Errorf("Hour.Requests: want 2, got %d", gauge.TokenUsage.Hour.Requests)
	}
	if gauge.TokenUsage.Hour.InputTokens != 1250 {
		t.Errorf("Hour.InputTokens: want 1250, got %d", gauge.TokenUsage.Hour.InputTokens)
	}
	if gauge.TokenUsage.Day.OutputTokens != 625 {
		t.Errorf("Day.OutputTokens: want 625, got %d", gauge.TokenUsage.Day.OutputTokens)
	}
}

func TestGetFuelGauge_ExposesRateLimit(t *testing.T) {
	gw, _ := newTestGatewayWithSessions(t)

	cfg := middleware.DefaultRateLimitConfig()
	cfg.Anonymous.MaxRequests = 10
	cfg.Authenticated.MaxRequests = 100
	m := middleware.NewRateLimitMiddleware(middleware.RateLimitMiddlewareConfig{Config: cfg})
	t.Cleanup(m.Stop)
	gw.rateLimitMiddleware = m

	gauge := gw.GetFuelGauge(3)
	if !gauge.RateLimit.Enabled {
		t.Error("RateLimit.Enabled: want true")
	}
	if gauge.RateLimit.Anonymous.Limit != 10 {
		t.Errorf("Anonymous.Limit: want 10, got %d", gauge.RateLimit.Anonymous.Limit)
	}
	if gauge.RateLimit.Authenticated.Limit != 100 {
		t.Errorf("Authenticated.Limit: want 100, got %d", gauge.RateLimit.Authenticated.Limit)
	}
}

func TestGetFuelGauge_IntegratedWithAIRouterObserver(t *testing.T) {
	// Simulate the full wiring: create a gateway with a token window, wire it
	// up through the observer interface, and confirm that an AI recording
	// shows up in the fuel gauge.
	gw, _ := newTestGatewayWithSessions(t)
	tw := monitoring.NewTokenWindowTracker()
	gw.tokenWindow = tw

	// Feed through the observer path that ai.UsageTracker uses. Arguments
	// mirror the RecordUsage signature: inputTokens, outputTokens,
	// cacheWriteTokens, cacheReadTokens.
	tw.OnUsage("anthropic", "claude-sonnet", 500, 250, 50, 25, 100)
	tw.OnUsage("anthropic", "claude-sonnet", 800, 400, 0, 0, 200)

	gauge := gw.GetFuelGauge(0)
	if !gauge.HasTokenTraffic() {
		t.Fatal("HasTokenTraffic should be true after observer calls")
	}
	if gauge.TokenUsage.Hour.InputTokens != 1300 {
		t.Errorf("Hour.InputTokens via observer: want 1300, got %d", gauge.TokenUsage.Hour.InputTokens)
	}
	if gauge.TokenUsage.Hour.OutputTokens != 650 {
		t.Errorf("Hour.OutputTokens via observer: want 650, got %d", gauge.TokenUsage.Hour.OutputTokens)
	}
	// OnUsage reorders cacheWrite/cacheRead into the internal Record signature;
	// expect CacheRead=25 (from cacheReadTokens) and CacheWrite=50 (from cacheWriteTokens).
	if gauge.TokenUsage.Hour.CacheRead != 25 {
		t.Errorf("CacheRead: want 25, got %d", gauge.TokenUsage.Hour.CacheRead)
	}
	if gauge.TokenUsage.Hour.CacheWrite != 50 {
		t.Errorf("CacheWrite: want 50, got %d", gauge.TokenUsage.Hour.CacheWrite)
	}
}
