package gateway

import (
	"context"
	"testing"

	"conduit/internal/middleware"
	"conduit/internal/monitoring"
)

// TestFuelGaugeToMap_Shape verifies that ToMap returns the expected nested
// structure and that all mandatory keys are present (conduit-zojv).
func TestFuelGaugeToMap_Shape(t *testing.T) {
	gw, _ := newTestGatewayWithSessions(t)
	tw := monitoring.NewTokenWindowTracker()
	tw.Record(100, 50, 5, 2)
	gw.tokenWindow = tw

	m := gw.GetFuelGauge(0).ToMap()

	// top-level keys
	for _, k := range []string{"taken_at", "rate_limit", "token_usage"} {
		if _, ok := m[k]; !ok {
			t.Errorf("ToMap missing top-level key %q", k)
		}
	}

	// token_usage sub-keys
	tu, ok := m["token_usage"].(map[string]interface{})
	if !ok {
		t.Fatal("token_usage should be map[string]interface{}")
	}
	for _, k := range []string{"hour", "day", "taken_at"} {
		if _, ok := tu[k]; !ok {
			t.Errorf("token_usage missing key %q", k)
		}
	}

	hour, ok := tu["hour"].(map[string]interface{})
	if !ok {
		t.Fatal("token_usage.hour should be map[string]interface{}")
	}
	// Fields from monitoring.TokenWindowSnapshot are int64.
	if v, _ := hour["requests"].(int64); v != 1 {
		t.Errorf("hour.requests: want 1, got %v (type %T)", hour["requests"], hour["requests"])
	}
	if v, _ := hour["input_tokens"].(int64); v != 100 {
		t.Errorf("hour.input_tokens: want 100, got %v", hour["input_tokens"])
	}

	// rate_limit sub-keys
	rl, ok := m["rate_limit"].(map[string]interface{})
	if !ok {
		t.Fatal("rate_limit should be map[string]interface{}")
	}
	for _, k := range []string{"enabled", "anonymous", "authenticated"} {
		if _, ok := rl[k]; !ok {
			t.Errorf("rate_limit missing key %q", k)
		}
	}
}

// TestGetFuelGaugeMap_ViaInterface verifies that GetFuelGaugeMap on *Gateway
// (satisfying types.GatewayService) returns non-nil data (conduit-zojv).
func TestGetFuelGaugeMap_ViaInterface(t *testing.T) {
	gw, _ := newTestGatewayWithSessions(t)
	tw := monitoring.NewTokenWindowTracker()
	tw.Record(200, 100, 0, 0)
	gw.tokenWindow = tw

	m := gw.GetFuelGaugeMap(0)
	if m == nil {
		t.Fatal("GetFuelGaugeMap returned nil")
	}
	if _, ok := m["token_usage"]; !ok {
		t.Error("GetFuelGaugeMap result missing token_usage key")
	}
}

// TestGetSessionStatus_IncludesFuelGauge verifies that GetSessionStatus
// embeds a fuel_gauge field in its result (conduit-zojv).
func TestGetSessionStatus_IncludesFuelGauge(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	sess, err := store.GetOrCreateSession("u1", "ch1")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	status, err := gw.GetSessionStatus(context.Background(), sess.Key)
	if err != nil {
		t.Fatalf("GetSessionStatus: %v", err)
	}
	fg, ok := status["fuel_gauge"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected fuel_gauge in status, got %T", status["fuel_gauge"])
	}
	if _, ok := fg["token_usage"]; !ok {
		t.Error("fuel_gauge missing token_usage key")
	}
}

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
