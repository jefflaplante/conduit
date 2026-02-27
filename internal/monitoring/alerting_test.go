package monitoring

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- test helpers ---

// staticMetricsProvider returns fixed metric values for testing.
type staticMetricsProvider struct {
	mu     sync.RWMutex
	values map[string]float64
}

func newStaticProvider(values map[string]float64) *staticMetricsProvider {
	return &staticMetricsProvider{values: values}
}

func (p *staticMetricsProvider) GetMetricValue(metric string) (float64, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	v, ok := p.values[metric]
	return v, ok
}

func (p *staticMetricsProvider) set(metric string, value float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.values[metric] = value
}

// collectingHandler accumulates all received alerts for assertion.
type collectingHandler struct {
	mu     sync.Mutex
	alerts []Alert
}

func (h *collectingHandler) HandleAlert(alert Alert) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.alerts = append(h.alerts, alert)
}

func (h *collectingHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.alerts)
}

func (h *collectingHandler) getAlerts() []Alert {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Alert, len(h.alerts))
	copy(out, h.alerts)
	return out
}

// --- AlertRule tests ---

func TestAlertRule_Validate(t *testing.T) {
	tests := []struct {
		name    string
		rule    AlertRule
		wantErr bool
	}{
		{
			name: "valid rule",
			rule: AlertRule{
				Name:      "high_error_rate",
				Metric:    "error_rate",
				Condition: ConditionGreaterThan,
				Threshold: 5.0,
				Severity:  AlertSeverityWarning,
			},
			wantErr: false,
		},
		{
			name: "empty name",
			rule: AlertRule{
				Metric:    "error_rate",
				Condition: ConditionGreaterThan,
				Threshold: 5.0,
				Severity:  AlertSeverityWarning,
			},
			wantErr: true,
		},
		{
			name: "empty metric",
			rule: AlertRule{
				Name:      "test",
				Condition: ConditionGreaterThan,
				Threshold: 5.0,
				Severity:  AlertSeverityWarning,
			},
			wantErr: true,
		},
		{
			name: "invalid condition",
			rule: AlertRule{
				Name:      "test",
				Metric:    "error_rate",
				Condition: "gte",
				Threshold: 5.0,
				Severity:  AlertSeverityWarning,
			},
			wantErr: true,
		},
		{
			name: "invalid severity",
			rule: AlertRule{
				Name:      "test",
				Metric:    "error_rate",
				Condition: ConditionGreaterThan,
				Threshold: 5.0,
				Severity:  "fatal",
			},
			wantErr: true,
		},
		{
			name: "lt condition",
			rule: AlertRule{
				Name:      "low_sessions",
				Metric:    "active_sessions",
				Condition: ConditionLessThan,
				Threshold: 1.0,
				Severity:  AlertSeverityInfo,
			},
			wantErr: false,
		},
		{
			name: "eq condition",
			rule: AlertRule{
				Name:      "zero_goroutines",
				Metric:    "goroutine_count",
				Condition: ConditionEqual,
				Threshold: 0,
				Severity:  AlertSeverityCritical,
			},
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.rule.Validate()
			if tc.wantErr && err == nil {
				t.Error("expected validation error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected validation error: %v", err)
			}
		})
	}
}

// --- AlertManager construction ---

func TestNewAlertManager(t *testing.T) {
	provider := newStaticProvider(map[string]float64{})
	am := NewAlertManager(provider)
	if am == nil {
		t.Fatal("NewAlertManager returned nil")
	}
	if am.IsRunning() {
		t.Error("new manager should not be running")
	}
}

func TestNewAlertManager_NilProvider(t *testing.T) {
	am := NewAlertManager(nil)
	if am == nil {
		t.Fatal("NewAlertManager with nil provider should still return a manager")
	}
	// Evaluate with nil provider should be safe.
	fired := am.Evaluate()
	if len(fired) != 0 {
		t.Errorf("expected no alerts with nil provider, got %d", len(fired))
	}
}

// --- AddRule / RemoveRule ---

func TestAlertManager_AddRule(t *testing.T) {
	am := NewAlertManager(newStaticProvider(nil))

	rule := AlertRule{
		Name:      "test_rule",
		Metric:    "error_rate",
		Condition: ConditionGreaterThan,
		Threshold: 10,
		Severity:  AlertSeverityWarning,
	}

	err := am.AddRule(rule)
	if err != nil {
		t.Fatalf("AddRule failed: %v", err)
	}

	// Duplicate should fail.
	err = am.AddRule(rule)
	if err == nil {
		t.Error("expected error adding duplicate rule")
	}

	// Invalid rule should fail.
	err = am.AddRule(AlertRule{})
	if err == nil {
		t.Error("expected error adding invalid rule")
	}
}

func TestAlertManager_RemoveRule(t *testing.T) {
	provider := newStaticProvider(map[string]float64{"error_rate": 50})
	am := NewAlertManager(provider)

	rule := AlertRule{
		Name:      "to_remove",
		Metric:    "error_rate",
		Condition: ConditionGreaterThan,
		Threshold: 10,
		Severity:  AlertSeverityWarning,
	}

	am.AddRule(rule)

	// Fire the rule to create an active alert.
	am.Evaluate()
	if len(am.GetActiveAlerts()) != 1 {
		t.Fatal("expected 1 active alert before removal")
	}

	// Remove should resolve the active alert.
	am.RemoveRule("to_remove")
	if len(am.GetActiveAlerts()) != 0 {
		t.Error("active alert should be resolved after rule removal")
	}

	// Removing a non-existent rule should not panic.
	am.RemoveRule("nonexistent")
}

func TestAlertManager_GetRule(t *testing.T) {
	am := NewAlertManager(newStaticProvider(nil))

	rule := AlertRule{
		Name:      "my_rule",
		Metric:    "failed_requests",
		Condition: ConditionGreaterThan,
		Threshold: 100,
		Severity:  AlertSeverityCritical,
	}
	am.AddRule(rule)

	got, ok := am.GetRule("my_rule")
	if !ok {
		t.Fatal("expected to find rule")
	}
	if got.Name != rule.Name || got.Threshold != rule.Threshold {
		t.Errorf("GetRule returned unexpected values: %+v", got)
	}

	_, ok = am.GetRule("missing")
	if ok {
		t.Error("expected false for missing rule")
	}
}

func TestAlertManager_GetRules(t *testing.T) {
	am := NewAlertManager(newStaticProvider(nil))

	am.AddRule(AlertRule{Name: "r1", Metric: "m1", Condition: ConditionGreaterThan, Threshold: 1, Severity: AlertSeverityInfo})
	am.AddRule(AlertRule{Name: "r2", Metric: "m2", Condition: ConditionLessThan, Threshold: 2, Severity: AlertSeverityWarning})

	rules := am.GetRules()
	if len(rules) != 2 {
		t.Errorf("expected 2 rules, got %d", len(rules))
	}
}

// --- Evaluate ---

func TestAlertManager_Evaluate_GreaterThan(t *testing.T) {
	provider := newStaticProvider(map[string]float64{"error_rate": 15})
	am := NewAlertManager(provider)

	am.AddRule(AlertRule{
		Name:      "high_error_rate",
		Metric:    "error_rate",
		Condition: ConditionGreaterThan,
		Threshold: 10,
		Severity:  AlertSeverityWarning,
	})

	fired := am.Evaluate()
	if len(fired) != 1 {
		t.Fatalf("expected 1 fired alert, got %d", len(fired))
	}

	alert := fired[0]
	if alert.RuleName != "high_error_rate" {
		t.Errorf("unexpected rule name: %s", alert.RuleName)
	}
	if alert.CurrentValue != 15 {
		t.Errorf("unexpected current value: %f", alert.CurrentValue)
	}
	if alert.Threshold != 10 {
		t.Errorf("unexpected threshold: %f", alert.Threshold)
	}
	if alert.Severity != AlertSeverityWarning {
		t.Errorf("unexpected severity: %s", alert.Severity)
	}
	if alert.Resolved {
		t.Error("newly fired alert should not be resolved")
	}
	if alert.FiredAt.IsZero() {
		t.Error("FiredAt should be set")
	}
	if alert.Message == "" {
		t.Error("Message should not be empty")
	}
}

func TestAlertManager_Evaluate_LessThan(t *testing.T) {
	provider := newStaticProvider(map[string]float64{"active_sessions": 0})
	am := NewAlertManager(provider)

	am.AddRule(AlertRule{
		Name:      "no_sessions",
		Metric:    "active_sessions",
		Condition: ConditionLessThan,
		Threshold: 1,
		Severity:  AlertSeverityCritical,
	})

	fired := am.Evaluate()
	if len(fired) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(fired))
	}
	if fired[0].Severity != AlertSeverityCritical {
		t.Errorf("expected critical severity, got %s", fired[0].Severity)
	}
}

func TestAlertManager_Evaluate_Equal(t *testing.T) {
	provider := newStaticProvider(map[string]float64{"goroutine_count": 42})
	am := NewAlertManager(provider)

	am.AddRule(AlertRule{
		Name:      "exact_goroutines",
		Metric:    "goroutine_count",
		Condition: ConditionEqual,
		Threshold: 42,
		Severity:  AlertSeverityInfo,
	})

	fired := am.Evaluate()
	if len(fired) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(fired))
	}
}

func TestAlertManager_Evaluate_NoBreachNoAlert(t *testing.T) {
	provider := newStaticProvider(map[string]float64{"error_rate": 2})
	am := NewAlertManager(provider)

	am.AddRule(AlertRule{
		Name:      "high_error_rate",
		Metric:    "error_rate",
		Condition: ConditionGreaterThan,
		Threshold: 10,
		Severity:  AlertSeverityWarning,
	})

	fired := am.Evaluate()
	if len(fired) != 0 {
		t.Errorf("expected 0 alerts when threshold not breached, got %d", len(fired))
	}
}

func TestAlertManager_Evaluate_UnknownMetric(t *testing.T) {
	provider := newStaticProvider(map[string]float64{})
	am := NewAlertManager(provider)

	am.AddRule(AlertRule{
		Name:      "unknown",
		Metric:    "nonexistent_metric",
		Condition: ConditionGreaterThan,
		Threshold: 0,
		Severity:  AlertSeverityInfo,
	})

	fired := am.Evaluate()
	if len(fired) != 0 {
		t.Errorf("expected 0 alerts for unknown metric, got %d", len(fired))
	}
}

func TestAlertManager_Evaluate_Cooldown(t *testing.T) {
	provider := newStaticProvider(map[string]float64{"error_rate": 50})
	am := NewAlertManager(provider)

	am.AddRule(AlertRule{
		Name:      "rate_check",
		Metric:    "error_rate",
		Condition: ConditionGreaterThan,
		Threshold: 10,
		Severity:  AlertSeverityWarning,
		Cooldown:  1 * time.Hour, // very long cooldown
	})

	// First evaluation should fire.
	fired1 := am.Evaluate()
	if len(fired1) != 1 {
		t.Fatalf("expected 1 alert on first eval, got %d", len(fired1))
	}

	// Second evaluation should be suppressed by cooldown.
	fired2 := am.Evaluate()
	if len(fired2) != 0 {
		t.Errorf("expected 0 alerts during cooldown, got %d", len(fired2))
	}
}

func TestAlertManager_Evaluate_CooldownExpired(t *testing.T) {
	provider := newStaticProvider(map[string]float64{"error_rate": 50})
	am := NewAlertManager(provider)

	am.AddRule(AlertRule{
		Name:      "rate_check",
		Metric:    "error_rate",
		Condition: ConditionGreaterThan,
		Threshold: 10,
		Severity:  AlertSeverityWarning,
		Cooldown:  10 * time.Millisecond,
	})

	// First evaluation fires.
	fired1 := am.Evaluate()
	if len(fired1) != 1 {
		t.Fatalf("expected 1 alert on first eval, got %d", len(fired1))
	}

	// Wait for cooldown to expire.
	time.Sleep(20 * time.Millisecond)

	// Second evaluation should fire again.
	fired2 := am.Evaluate()
	if len(fired2) != 1 {
		t.Errorf("expected 1 alert after cooldown expired, got %d", len(fired2))
	}
}

func TestAlertManager_Evaluate_ZeroCooldown(t *testing.T) {
	provider := newStaticProvider(map[string]float64{"error_rate": 50})
	am := NewAlertManager(provider)

	am.AddRule(AlertRule{
		Name:      "no_cooldown",
		Metric:    "error_rate",
		Condition: ConditionGreaterThan,
		Threshold: 10,
		Severity:  AlertSeverityWarning,
		Cooldown:  0,
	})

	// With zero cooldown, every evaluation should fire if breached.
	for i := 0; i < 5; i++ {
		fired := am.Evaluate()
		if len(fired) != 1 {
			t.Errorf("eval %d: expected 1 alert with zero cooldown, got %d", i, len(fired))
		}
	}
}

func TestAlertManager_Evaluate_Resolution(t *testing.T) {
	provider := newStaticProvider(map[string]float64{"error_rate": 50})
	am := NewAlertManager(provider)

	am.AddRule(AlertRule{
		Name:      "rate_check",
		Metric:    "error_rate",
		Condition: ConditionGreaterThan,
		Threshold: 10,
		Severity:  AlertSeverityWarning,
	})

	// Fire the alert.
	am.Evaluate()
	active := am.GetActiveAlerts()
	if len(active) != 1 {
		t.Fatalf("expected 1 active alert, got %d", len(active))
	}

	// Fix the metric.
	provider.set("error_rate", 5)

	// Evaluate again -- should resolve.
	am.Evaluate()
	active = am.GetActiveAlerts()
	if len(active) != 0 {
		t.Errorf("expected 0 active alerts after resolution, got %d", len(active))
	}
}

func TestAlertManager_Evaluate_MultipleRules(t *testing.T) {
	provider := newStaticProvider(map[string]float64{
		"error_rate":      50,
		"memory_usage_mb": 500,
		"goroutine_count": 5,
		"active_sessions": 0,
	})
	am := NewAlertManager(provider)

	am.AddRule(AlertRule{
		Name:      "high_errors",
		Metric:    "error_rate",
		Condition: ConditionGreaterThan,
		Threshold: 10,
		Severity:  AlertSeverityWarning,
	})
	am.AddRule(AlertRule{
		Name:      "high_memory",
		Metric:    "memory_usage_mb",
		Condition: ConditionGreaterThan,
		Threshold: 256,
		Severity:  AlertSeverityCritical,
	})
	am.AddRule(AlertRule{
		Name:      "low_goroutines",
		Metric:    "goroutine_count",
		Condition: ConditionLessThan,
		Threshold: 10,
		Severity:  AlertSeverityInfo,
	})
	am.AddRule(AlertRule{
		Name:      "healthy_sessions",
		Metric:    "active_sessions",
		Condition: ConditionGreaterThan,
		Threshold: 100,
		Severity:  AlertSeverityInfo,
	})

	fired := am.Evaluate()

	// Should fire: high_errors, high_memory, low_goroutines
	// Should NOT fire: healthy_sessions (0 is not > 100)
	if len(fired) != 3 {
		t.Errorf("expected 3 fired alerts, got %d", len(fired))
		for _, a := range fired {
			t.Logf("  fired: %s", a.RuleName)
		}
	}

	active := am.GetActiveAlerts()
	if len(active) != 3 {
		t.Errorf("expected 3 active alerts, got %d", len(active))
	}
}

// --- Active alerts and history ---

func TestAlertManager_GetActiveAlerts_Empty(t *testing.T) {
	am := NewAlertManager(newStaticProvider(nil))
	alerts := am.GetActiveAlerts()
	if alerts == nil {
		t.Error("GetActiveAlerts should return non-nil empty slice")
	}
	if len(alerts) != 0 {
		t.Errorf("expected 0 active alerts, got %d", len(alerts))
	}
}

func TestAlertManager_GetAlertHistory(t *testing.T) {
	provider := newStaticProvider(map[string]float64{"error_rate": 50})
	am := NewAlertManager(provider)

	am.AddRule(AlertRule{
		Name:      "rate_check",
		Metric:    "error_rate",
		Condition: ConditionGreaterThan,
		Threshold: 10,
		Severity:  AlertSeverityWarning,
		Cooldown:  0, // no cooldown
	})

	// Generate multiple alerts.
	for i := 0; i < 5; i++ {
		am.Evaluate()
	}

	history := am.GetAlertHistory(0) // all history
	if len(history) != 5 {
		t.Errorf("expected 5 history entries, got %d", len(history))
	}

	// Filter by recent time.
	recent := am.GetAlertHistory(1 * time.Hour)
	if len(recent) != 5 {
		t.Errorf("expected 5 recent entries, got %d", len(recent))
	}
}

func TestAlertManager_HistoryTrimming(t *testing.T) {
	provider := newStaticProvider(map[string]float64{"error_rate": 50})
	am := NewAlertManager(provider)
	am.maxHistory = 10 // small cap for testing

	am.AddRule(AlertRule{
		Name:      "rate_check",
		Metric:    "error_rate",
		Condition: ConditionGreaterThan,
		Threshold: 10,
		Severity:  AlertSeverityWarning,
		Cooldown:  0,
	})

	// Generate more alerts than the max.
	for i := 0; i < 20; i++ {
		am.Evaluate()
	}

	history := am.GetAlertHistory(0)
	if len(history) > 10 {
		t.Errorf("history should be trimmed to 10, got %d", len(history))
	}
}

// --- Subscribe / Handlers ---

func TestAlertManager_Subscribe(t *testing.T) {
	provider := newStaticProvider(map[string]float64{"error_rate": 50})
	am := NewAlertManager(provider)

	handler := &collectingHandler{}
	am.Subscribe(handler)

	am.AddRule(AlertRule{
		Name:      "test",
		Metric:    "error_rate",
		Condition: ConditionGreaterThan,
		Threshold: 10,
		Severity:  AlertSeverityWarning,
	})

	am.Evaluate()

	if handler.count() != 1 {
		t.Errorf("handler expected 1 alert, got %d", handler.count())
	}

	alerts := handler.getAlerts()
	if alerts[0].RuleName != "test" {
		t.Errorf("unexpected rule name in handler: %s", alerts[0].RuleName)
	}
}

func TestAlertManager_Subscribe_MultipleHandlers(t *testing.T) {
	provider := newStaticProvider(map[string]float64{"error_rate": 50})
	am := NewAlertManager(provider)

	h1 := &collectingHandler{}
	h2 := &collectingHandler{}
	am.Subscribe(h1)
	am.Subscribe(h2)

	am.AddRule(AlertRule{
		Name:      "test",
		Metric:    "error_rate",
		Condition: ConditionGreaterThan,
		Threshold: 10,
		Severity:  AlertSeverityWarning,
	})

	am.Evaluate()

	if h1.count() != 1 {
		t.Errorf("handler1 expected 1 alert, got %d", h1.count())
	}
	if h2.count() != 1 {
		t.Errorf("handler2 expected 1 alert, got %d", h2.count())
	}
}

func TestAlertManager_Subscribe_NilHandler(t *testing.T) {
	am := NewAlertManager(newStaticProvider(nil))
	// Should not panic.
	am.Subscribe(nil)
}

func TestAlertManager_HandlerPanicRecovery(t *testing.T) {
	provider := newStaticProvider(map[string]float64{"error_rate": 50})
	am := NewAlertManager(provider)

	panicHandler := &FuncHandler{
		Fn: func(a Alert) {
			panic("handler exploded")
		},
	}
	goodHandler := &collectingHandler{}

	am.Subscribe(panicHandler)
	am.Subscribe(goodHandler)

	am.AddRule(AlertRule{
		Name:      "test",
		Metric:    "error_rate",
		Condition: ConditionGreaterThan,
		Threshold: 10,
		Severity:  AlertSeverityWarning,
	})

	// Should not panic even though one handler panics.
	am.Evaluate()

	// The good handler should still receive the alert.
	if goodHandler.count() != 1 {
		t.Errorf("good handler expected 1 alert despite panic, got %d", goodHandler.count())
	}
}

// --- FuncHandler ---

func TestFuncHandler(t *testing.T) {
	var received Alert
	h := &FuncHandler{
		Fn: func(a Alert) {
			received = a
		},
	}

	alert := Alert{RuleName: "test_func", Message: "hello"}
	h.HandleAlert(alert)

	if received.RuleName != "test_func" {
		t.Errorf("expected rule name 'test_func', got %s", received.RuleName)
	}
}

func TestFuncHandler_NilFn(t *testing.T) {
	h := &FuncHandler{}
	// Should not panic.
	h.HandleAlert(Alert{})
}

// --- LogHandler ---

func TestLogHandler(t *testing.T) {
	h := &LogHandler{}
	// Should not panic.
	h.HandleAlert(Alert{
		RuleName:     "test",
		Severity:     AlertSeverityWarning,
		Message:      "test message",
		Metric:       "error_rate",
		CurrentValue: 50,
		Threshold:    10,
	})
}

// --- ChannelHandler ---

func TestChannelHandler(t *testing.T) {
	h := &ChannelHandler{TargetChannel: "general"}
	// Should not panic.
	h.HandleAlert(Alert{
		RuleName: "test",
		Message:  "test message",
	})
}

// --- Start / Stop ---

func TestAlertManager_StartStop(t *testing.T) {
	provider := newStaticProvider(map[string]float64{"error_rate": 50})
	am := NewAlertManager(provider)

	handler := &collectingHandler{}
	am.Subscribe(handler)

	am.AddRule(AlertRule{
		Name:      "test",
		Metric:    "error_rate",
		Condition: ConditionGreaterThan,
		Threshold: 10,
		Severity:  AlertSeverityWarning,
		Cooldown:  0,
	})

	am.Start(50 * time.Millisecond)
	if !am.IsRunning() {
		t.Error("expected manager to be running")
	}

	// Let it run a few cycles.
	time.Sleep(200 * time.Millisecond)

	am.Stop()
	if am.IsRunning() {
		t.Error("expected manager to be stopped")
	}

	// Should have received some alerts.
	if handler.count() < 1 {
		t.Errorf("expected at least 1 alert from background loop, got %d", handler.count())
	}
}

func TestAlertManager_DoubleStart(t *testing.T) {
	am := NewAlertManager(newStaticProvider(nil))

	am.Start(100 * time.Millisecond)
	defer am.Stop()

	// Second start should be a no-op.
	am.Start(100 * time.Millisecond)
	if !am.IsRunning() {
		t.Error("should still be running")
	}
}

func TestAlertManager_StopNotRunning(t *testing.T) {
	am := NewAlertManager(newStaticProvider(nil))
	// Should not panic.
	am.Stop()
}

func TestAlertManager_StartDefaultInterval(t *testing.T) {
	am := NewAlertManager(newStaticProvider(nil))
	// Zero interval should use default.
	am.Start(0)
	defer am.Stop()

	if !am.IsRunning() {
		t.Error("should be running with default interval")
	}
}

// --- SnapshotMetricsProvider ---

func TestSnapshotMetricsProvider(t *testing.T) {
	metrics := NewGatewayMetrics()
	metrics.UpdateSessionCount(5, 3, 1, 1, 10)
	metrics.UpdateQueueMetrics(20, 8)
	metrics.IncrementCompleted()
	metrics.IncrementFailed()

	provider := NewSnapshotMetricsProvider(metrics)

	tests := []struct {
		metric   string
		expected float64
		known    bool
	}{
		{"active_sessions", 5, true},
		{"processing_sessions", 3, true},
		{"waiting_sessions", 1, true},
		{"idle_sessions", 1, true},
		{"total_sessions", 10, true},
		{"queue_depth", 20, true},
		{"pending_requests", 8, true},
		{"completed_requests", 1, true},
		{"failed_requests", 1, true},
		{"nonexistent", 0, false},
	}

	for _, tc := range tests {
		t.Run(tc.metric, func(t *testing.T) {
			val, ok := provider.GetMetricValue(tc.metric)
			if ok != tc.known {
				t.Errorf("GetMetricValue(%s) known=%v, want %v", tc.metric, ok, tc.known)
			}
			if ok && val != tc.expected {
				t.Errorf("GetMetricValue(%s) = %f, want %f", tc.metric, val, tc.expected)
			}
		})
	}
}

func TestSnapshotMetricsProvider_ErrorRate(t *testing.T) {
	metrics := NewGatewayMetrics()
	// 2 completed, 1 failed = 33.33% error rate
	metrics.IncrementCompleted()
	metrics.IncrementCompleted()
	metrics.IncrementFailed()

	provider := NewSnapshotMetricsProvider(metrics)

	val, ok := provider.GetMetricValue("error_rate")
	if !ok {
		t.Fatal("error_rate should be a known metric")
	}
	// 1 failed / (2 completed + 1 failed) * 100 = 33.33...
	expected := 100.0 / 3.0
	if val < expected-0.01 || val > expected+0.01 {
		t.Errorf("error_rate = %f, expected ~%f", val, expected)
	}
}

func TestSnapshotMetricsProvider_ErrorRateZeroRequests(t *testing.T) {
	metrics := NewGatewayMetrics()
	provider := NewSnapshotMetricsProvider(metrics)

	val, ok := provider.GetMetricValue("error_rate")
	if !ok {
		t.Fatal("error_rate should be known")
	}
	if val != 0 {
		t.Errorf("error_rate with zero requests should be 0, got %f", val)
	}
}

func TestSnapshotMetricsProvider_NilMetrics(t *testing.T) {
	provider := NewSnapshotMetricsProvider(nil)
	val, ok := provider.GetMetricValue("active_sessions")
	if ok {
		t.Error("expected unknown for nil metrics")
	}
	if val != 0 {
		t.Errorf("expected 0, got %f", val)
	}
}

func TestSnapshotMetricsProvider_SystemMetrics(t *testing.T) {
	metrics := NewGatewayMetrics()
	metrics.UpdateSystemMetrics()

	provider := NewSnapshotMetricsProvider(metrics)

	// memory_usage_mb should be positive after system metrics update.
	val, ok := provider.GetMetricValue("memory_usage_mb")
	if !ok {
		t.Fatal("memory_usage_mb should be known")
	}
	if val <= 0 {
		t.Errorf("memory_usage_mb should be positive, got %f", val)
	}

	// goroutine_count should be positive.
	val, ok = provider.GetMetricValue("goroutine_count")
	if !ok {
		t.Fatal("goroutine_count should be known")
	}
	if val <= 0 {
		t.Errorf("goroutine_count should be positive, got %f", val)
	}
}

// --- Thread safety ---

func TestAlertManager_ConcurrentAccess(t *testing.T) {
	provider := newStaticProvider(map[string]float64{
		"error_rate":      50,
		"memory_usage_mb": 300,
	})
	am := NewAlertManager(provider)

	am.AddRule(AlertRule{
		Name:      "errors",
		Metric:    "error_rate",
		Condition: ConditionGreaterThan,
		Threshold: 10,
		Severity:  AlertSeverityWarning,
		Cooldown:  0,
	})
	am.AddRule(AlertRule{
		Name:      "memory",
		Metric:    "memory_usage_mb",
		Condition: ConditionGreaterThan,
		Threshold: 256,
		Severity:  AlertSeverityCritical,
		Cooldown:  0,
	})

	handler := &collectingHandler{}
	am.Subscribe(handler)

	var wg sync.WaitGroup
	workers := 20
	opsPerWorker := 50

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < opsPerWorker; j++ {
				switch j % 6 {
				case 0:
					am.Evaluate()
				case 1:
					am.GetActiveAlerts()
				case 2:
					am.GetAlertHistory(1 * time.Hour)
				case 3:
					am.GetRules()
				case 4:
					am.IsRunning()
				case 5:
					provider.set("error_rate", float64(50+j%10))
				}
			}
		}(i)
	}

	wg.Wait()

	// Should not have panicked; verify some state.
	if handler.count() < 1 {
		t.Error("expected at least 1 alert from concurrent evaluations")
	}
}

func TestAlertManager_ConcurrentStartStop(t *testing.T) {
	am := NewAlertManager(newStaticProvider(map[string]float64{"m": 1}))
	am.AddRule(AlertRule{
		Name:      "r",
		Metric:    "m",
		Condition: ConditionGreaterThan,
		Threshold: 0,
		Severity:  AlertSeverityInfo,
		Cooldown:  0,
	})

	for i := 0; i < 10; i++ {
		am.Start(20 * time.Millisecond)
		time.Sleep(30 * time.Millisecond)
		am.Stop()
	}

	// Final state should be stopped.
	if am.IsRunning() {
		t.Error("should be stopped after all cycles")
	}
}

// --- Integration-style test combining all features ---

func TestAlertManager_FullLifecycle(t *testing.T) {
	// Set up a realistic scenario with GatewayMetrics.
	metrics := NewGatewayMetrics()
	metrics.UpdateSessionCount(5, 3, 1, 1, 10)
	metrics.UpdateQueueMetrics(20, 8)
	// Create 10 completed, 5 failed -> 33% error rate.
	for i := 0; i < 10; i++ {
		metrics.IncrementCompleted()
	}
	for i := 0; i < 5; i++ {
		metrics.IncrementFailed()
	}

	provider := NewSnapshotMetricsProvider(metrics)
	am := NewAlertManager(provider)

	var alertCount atomic.Int32
	am.Subscribe(&FuncHandler{Fn: func(a Alert) {
		alertCount.Add(1)
	}})
	am.Subscribe(&LogHandler{})

	// Add rules
	am.AddRule(AlertRule{
		Name:        "high_error_rate",
		Metric:      "error_rate",
		Condition:   ConditionGreaterThan,
		Threshold:   10,
		Severity:    AlertSeverityWarning,
		Cooldown:    50 * time.Millisecond,
		Description: "Error rate exceeds 10%",
	})
	am.AddRule(AlertRule{
		Name:        "high_queue_depth",
		Metric:      "queue_depth",
		Condition:   ConditionGreaterThan,
		Threshold:   50,
		Severity:    AlertSeverityCritical,
		Description: "Queue depth too high",
	})

	// Start background evaluation.
	am.Start(30 * time.Millisecond)
	defer am.Stop()

	// Let it run.
	time.Sleep(150 * time.Millisecond)

	// Verify alerts fired for error_rate (which is ~33%) but NOT queue_depth (20 < 50).
	active := am.GetActiveAlerts()
	foundErrorRate := false
	for _, a := range active {
		if a.RuleName == "high_error_rate" {
			foundErrorRate = true
		}
		if a.RuleName == "high_queue_depth" {
			t.Error("high_queue_depth should not have fired")
		}
	}
	if !foundErrorRate {
		t.Error("high_error_rate should be active")
	}

	// Fix the error rate.
	for i := 0; i < 100; i++ {
		metrics.IncrementCompleted()
	}
	// Now error rate is 5 / (10 + 100 + 5) * 100 ~ 4.3%

	// Wait for a couple more evaluation cycles.
	time.Sleep(100 * time.Millisecond)

	active = am.GetActiveAlerts()
	for _, a := range active {
		if a.RuleName == "high_error_rate" {
			t.Error("high_error_rate should have been resolved after fixing metrics")
		}
	}

	// Check history.
	history := am.GetAlertHistory(1 * time.Minute)
	if len(history) < 1 {
		t.Error("expected at least 1 entry in alert history")
	}

	// Verify handler was called.
	if alertCount.Load() < 1 {
		t.Error("expected handler to receive at least 1 alert")
	}
}

// --- Alert struct tests ---

func TestAlert_MessageContent(t *testing.T) {
	provider := newStaticProvider(map[string]float64{"error_rate": 15.5})
	am := NewAlertManager(provider)

	am.AddRule(AlertRule{
		Name:      "err",
		Metric:    "error_rate",
		Condition: ConditionGreaterThan,
		Threshold: 10,
		Severity:  AlertSeverityWarning,
	})

	fired := am.Evaluate()
	if len(fired) != 1 {
		t.Fatal("expected 1 alert")
	}

	msg := fired[0].Message
	if msg == "" {
		t.Error("alert message should not be empty")
	}
	// Message should contain the metric name and values.
	if !containsSubstring(msg, "error_rate") {
		t.Errorf("message should contain metric name: %s", msg)
	}
	if !containsSubstring(msg, "15.50") {
		t.Errorf("message should contain current value: %s", msg)
	}
	if !containsSubstring(msg, "10.00") {
		t.Errorf("message should contain threshold: %s", msg)
	}
}

func TestAlertSeverity_Level(t *testing.T) {
	if AlertSeverityInfo.severityLevel() >= AlertSeverityWarning.severityLevel() {
		t.Error("info should be lower than warning")
	}
	if AlertSeverityWarning.severityLevel() >= AlertSeverityCritical.severityLevel() {
		t.Error("warning should be lower than critical")
	}
	if AlertSeverity("unknown").severityLevel() != 0 {
		t.Error("unknown severity should have level 0")
	}
}

// containsSubstring checks if s contains substr.
func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// --- metricFromSnapshot coverage ---

func TestMetricFromSnapshot_AllFields(t *testing.T) {
	snap := MetricsSnapshot{
		ActiveSessions:     1,
		ProcessingSessions: 2,
		WaitingSessions:    3,
		IdleSessions:       4,
		TotalSessions:      10,
		QueueDepth:         5,
		PendingRequests:    6,
		CompletedRequests:  100,
		FailedRequests:     7,
		WebhookConnections: 8,
		ActiveWebhooks:     9,
		UptimeSeconds:      3600,
		MemoryUsageBytes:   1048576,
		MemoryUsageMB:      1.0,
		GoroutineCount:     42,
	}

	allMetrics := []struct {
		name     string
		expected float64
	}{
		{"active_sessions", 1},
		{"processing_sessions", 2},
		{"waiting_sessions", 3},
		{"idle_sessions", 4},
		{"total_sessions", 10},
		{"queue_depth", 5},
		{"pending_requests", 6},
		{"completed_requests", 100},
		{"failed_requests", 7},
		{"webhook_connections", 8},
		{"active_webhooks", 9},
		{"uptime_seconds", 3600},
		{"memory_usage_bytes", 1048576},
		{"memory_usage_mb", 1.0},
		{"goroutine_count", 42},
	}

	for _, tc := range allMetrics {
		t.Run(tc.name, func(t *testing.T) {
			val, ok := metricFromSnapshot(snap, tc.name)
			if !ok {
				t.Errorf("metric %s should be known", tc.name)
			}
			if val != tc.expected {
				t.Errorf("metric %s = %f, want %f", tc.name, val, tc.expected)
			}
		})
	}

	// Unknown metric.
	_, ok := metricFromSnapshot(snap, "totally_unknown")
	if ok {
		t.Error("unknown metric should return false")
	}
}

func TestMetricFromSnapshot_ErrorRateCalculation(t *testing.T) {
	tests := []struct {
		name      string
		completed int64
		failed    int64
		expected  float64
	}{
		{"no requests", 0, 0, 0},
		{"all success", 100, 0, 0},
		{"all failures", 0, 10, 100},
		{"mixed", 90, 10, 10},
		{"high failure", 20, 80, 80},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			snap := MetricsSnapshot{
				CompletedRequests: tc.completed,
				FailedRequests:    tc.failed,
			}
			val, ok := metricFromSnapshot(snap, "error_rate")
			if !ok {
				t.Fatal("error_rate should be known")
			}
			if val < tc.expected-0.01 || val > tc.expected+0.01 {
				t.Errorf("error_rate = %f, want %f", val, tc.expected)
			}
		})
	}
}
