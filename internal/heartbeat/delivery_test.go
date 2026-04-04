package heartbeat

import (
	"context"
	"errors"
	"testing"
	"time"

	"conduit/internal/config"
)

// mockDeliverer is a test deliverer that can be configured to succeed or fail.
type mockDeliverer struct {
	deliveryType string
	shouldFail   bool
	deliveries   []mockDelivery
}

type mockDelivery struct {
	Alert  Alert
	Target config.AlertTarget
}

func (m *mockDeliverer) Type() string {
	return m.deliveryType
}

func (m *mockDeliverer) Deliver(ctx context.Context, alert Alert, target config.AlertTarget) error {
	m.deliveries = append(m.deliveries, mockDelivery{Alert: alert, Target: target})
	if m.shouldFail {
		return errors.New("mock delivery failure")
	}
	return nil
}

func TestDeliveryRegistry_Register(t *testing.T) {
	registry := NewDeliveryRegistry()

	mock := &mockDeliverer{deliveryType: "test"}
	registry.Register(mock)

	got, ok := registry.Get("test")
	if !ok {
		t.Fatal("expected to find registered deliverer")
	}
	if got.Type() != "test" {
		t.Errorf("expected type 'test', got %q", got.Type())
	}
}

func TestDeliveryRegistry_GetUnregistered(t *testing.T) {
	registry := NewDeliveryRegistry()

	_, ok := registry.Get("nonexistent")
	if ok {
		t.Error("expected Get to return false for unregistered type")
	}
}

func TestDeliveryRegistry_Types(t *testing.T) {
	registry := NewDeliveryRegistry()
	registry.Register(&mockDeliverer{deliveryType: "telegram"})
	registry.Register(&mockDeliverer{deliveryType: "webhook"})
	registry.Register(&mockDeliverer{deliveryType: "mqtt"})

	types := registry.Types()
	if len(types) != 3 {
		t.Errorf("expected 3 types, got %d", len(types))
	}

	typeSet := make(map[string]bool)
	for _, typ := range types {
		typeSet[typ] = true
	}

	for _, expected := range []string{"telegram", "webhook", "mqtt"} {
		if !typeSet[expected] {
			t.Errorf("expected type %q in registry", expected)
		}
	}
}

func TestDeliveryRegistry_DeliverAlert_Success(t *testing.T) {
	registry := NewDeliveryRegistry()
	mock := &mockDeliverer{deliveryType: "telegram", shouldFail: false}
	registry.Register(mock)

	alert := Alert{
		ID:       "test-1",
		Source:   "test",
		Title:    "Test Alert",
		Message:  "Test message",
		Severity: AlertSeverityWarning,
	}

	target := config.AlertTarget{
		Name:   "telegram-test",
		Type:   "telegram",
		Config: map[string]string{"chat_id": "123"},
	}

	err := registry.DeliverAlert(context.Background(), alert, target)
	if err != nil {
		t.Errorf("expected successful delivery, got error: %v", err)
	}

	if len(mock.deliveries) != 1 {
		t.Errorf("expected 1 delivery, got %d", len(mock.deliveries))
	}

	if mock.deliveries[0].Alert.ID != "test-1" {
		t.Errorf("expected alert ID 'test-1', got %q", mock.deliveries[0].Alert.ID)
	}
}

func TestDeliveryRegistry_DeliverAlert_UnknownType(t *testing.T) {
	registry := NewDeliveryRegistry()

	alert := Alert{ID: "test-1", Source: "test", Title: "Test", Message: "Test", Severity: AlertSeverityInfo}
	target := config.AlertTarget{Name: "unknown", Type: "unknown"}

	err := registry.DeliverAlert(context.Background(), alert, target)
	if err == nil {
		t.Error("expected error for unknown deliverer type")
	}
}

func TestDeliveryRegistry_DeliverAlert_Failure(t *testing.T) {
	registry := NewDeliveryRegistry()
	mock := &mockDeliverer{deliveryType: "telegram", shouldFail: true}
	registry.Register(mock)

	alert := Alert{ID: "test-1", Source: "test", Title: "Test", Message: "Test", Severity: AlertSeverityWarning}
	target := config.AlertTarget{Name: "telegram-test", Type: "telegram"}

	err := registry.DeliverAlert(context.Background(), alert, target)
	if err == nil {
		t.Error("expected error for failed delivery")
	}
}

func TestCircuitBreaker_StartsClosedC(t *testing.T) {
	cb := NewCircuitBreaker(3, time.Minute)

	if cb.IsOpen("target1") {
		t.Error("circuit should be closed for new target")
	}
}

func TestCircuitBreaker_OpensAfterThreshold(t *testing.T) {
	cb := NewCircuitBreaker(3, time.Minute)

	cb.RecordFailure("target1")
	cb.RecordFailure("target1")
	if cb.IsOpen("target1") {
		t.Error("circuit should still be closed after 2 failures (threshold is 3)")
	}

	cb.RecordFailure("target1")
	if !cb.IsOpen("target1") {
		t.Error("circuit should be open after 3 failures")
	}
}

func TestCircuitBreaker_SuccessResets(t *testing.T) {
	cb := NewCircuitBreaker(3, time.Minute)

	cb.RecordFailure("target1")
	cb.RecordFailure("target1")
	cb.RecordSuccess("target1")

	// Should need 3 more failures to open
	cb.RecordFailure("target1")
	cb.RecordFailure("target1")
	if cb.IsOpen("target1") {
		t.Error("circuit should be closed after success reset")
	}
}

func TestCircuitBreaker_CooldownExpires(t *testing.T) {
	cb := NewCircuitBreaker(2, 10*time.Millisecond)

	cb.RecordFailure("target1")
	cb.RecordFailure("target1")

	if !cb.IsOpen("target1") {
		t.Error("circuit should be open")
	}

	// Wait for cooldown
	time.Sleep(15 * time.Millisecond)

	if cb.IsOpen("target1") {
		t.Error("circuit should be closed after cooldown")
	}
}

func TestCircuitBreaker_State(t *testing.T) {
	cb := NewCircuitBreaker(2, time.Minute)

	cb.RecordFailure("target1")
	cb.RecordFailure("target1")

	isOpen, failures, cooldownEnds := cb.State("target1")

	if !isOpen {
		t.Error("expected circuit to be open")
	}
	if failures != 2 {
		t.Errorf("expected 2 failures, got %d", failures)
	}
	if cooldownEnds.IsZero() {
		t.Error("expected non-zero cooldown end time")
	}
}

func TestCircuitBreaker_Reset(t *testing.T) {
	cb := NewCircuitBreaker(2, time.Minute)

	cb.RecordFailure("target1")
	cb.RecordFailure("target1")

	if !cb.IsOpen("target1") {
		t.Error("circuit should be open")
	}

	cb.Reset("target1")

	if cb.IsOpen("target1") {
		t.Error("circuit should be closed after reset")
	}
}

func TestCircuitBreaker_IndependentTargets(t *testing.T) {
	cb := NewCircuitBreaker(2, time.Minute)

	cb.RecordFailure("target1")
	cb.RecordFailure("target1")

	if !cb.IsOpen("target1") {
		t.Error("target1 circuit should be open")
	}
	if cb.IsOpen("target2") {
		t.Error("target2 circuit should be closed (independent)")
	}
}

func TestDeliveryRegistry_CircuitBreakerIntegration(t *testing.T) {
	registry := NewDeliveryRegistry()
	mock := &mockDeliverer{deliveryType: "telegram", shouldFail: true}
	registry.Register(mock)

	alert := Alert{ID: "test-1", Source: "test", Title: "Test", Message: "Test", Severity: AlertSeverityWarning}
	target := config.AlertTarget{Name: "failing-target", Type: "telegram"}

	// Fail 3 times to open circuit
	for i := 0; i < 3; i++ {
		_ = registry.DeliverAlert(context.Background(), alert, target)
	}

	// Next attempt should fail immediately due to open circuit
	err := registry.DeliverAlert(context.Background(), alert, target)
	if err == nil {
		t.Error("expected circuit breaker to block delivery")
	}

	// Verify deliverer wasn't called for the blocked attempt
	if len(mock.deliveries) != 3 {
		t.Errorf("expected 3 deliveries (circuit should block 4th), got %d", len(mock.deliveries))
	}
}

func TestAlertProcessorWithRegistry(t *testing.T) {
	// Create a temp directory for the queue
	tmpDir := t.TempDir()
	queuePath := tmpDir + "/alerts.json"

	// Create config with a telegram target
	cfg := &config.AgentHeartbeatConfig{
		Enabled: true,
		AlertTargets: []config.AlertTarget{
			{
				Name:     "test-telegram",
				Type:     "telegram",
				Severity: []string{"critical", "warning"},
				Config:   map[string]string{"chat_id": "123", "bot_token": "test-token"},
			},
		},
		AlertRetryPolicy: config.AlertRetryPolicy{
			MaxRetries:    3,
			RetryInterval: time.Minute,
			BackoffFactor: 2.0,
		},
	}

	// Create registry with mock deliverer
	registry := NewDeliveryRegistry()
	mock := &mockDeliverer{deliveryType: "telegram", shouldFail: false}
	registry.Register(mock)

	// Create processor with registry
	processor := NewAlertProcessorWithRegistry(queuePath, cfg, registry)

	// Deliver an alert
	alert := Alert{
		ID:        "integration-test-1",
		Source:    "test",
		Title:     "Integration Test",
		Message:   "Testing processor with registry",
		Severity:  AlertSeverityWarning,
		Status:    AlertStatusPending,
		CreatedAt: time.Now(),
	}

	err := processor.DeliverAlert(alert, "test-telegram")
	if err != nil {
		t.Errorf("expected successful delivery, got error: %v", err)
	}

	// Verify mock received the alert
	if len(mock.deliveries) != 1 {
		t.Errorf("expected 1 delivery, got %d", len(mock.deliveries))
	}

	if mock.deliveries[0].Alert.ID != "integration-test-1" {
		t.Errorf("expected alert ID 'integration-test-1', got %q", mock.deliveries[0].Alert.ID)
	}

	if mock.deliveries[0].Target.Name != "test-telegram" {
		t.Errorf("expected target name 'test-telegram', got %q", mock.deliveries[0].Target.Name)
	}
}
