package heartbeat

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"conduit/internal/config"
)

// Deliverer defines the interface for alert delivery mechanisms.
// Each delivery type (telegram, webhook, mqtt) implements this interface.
type Deliverer interface {
	// Type returns the delivery type identifier (e.g., "telegram", "webhook", "mqtt")
	Type() string

	// Deliver sends an alert to the specified target.
	// Returns an error if delivery fails.
	Deliver(ctx context.Context, alert Alert, target config.AlertTarget) error
}

// DeliveryRegistry manages deliverer instances and routes alerts to the appropriate deliverer.
type DeliveryRegistry struct {
	deliverers map[string]Deliverer
	breaker    *CircuitBreaker
	auditor    *AlertAuditor
	mu         sync.RWMutex
}

// NewDeliveryRegistry creates a new delivery registry with circuit breaker support.
func NewDeliveryRegistry() *DeliveryRegistry {
	return &DeliveryRegistry{
		deliverers: make(map[string]Deliverer),
		breaker:    NewCircuitBreaker(3, 5*time.Minute),
	}
}

// SetAuditor attaches an AlertAuditor that records every alert delivery attempt
// to the alert_history audit trail. Passing nil disables auditing. Auditing is
// best-effort: audit failures are logged elsewhere but do not prevent delivery.
func (r *DeliveryRegistry) SetAuditor(a *AlertAuditor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.auditor = a
}

// Register adds a deliverer to the registry.
// If a deliverer with the same type already exists, it is replaced.
func (r *DeliveryRegistry) Register(d Deliverer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deliverers[d.Type()] = d
}

// Get retrieves a deliverer by type.
func (r *DeliveryRegistry) Get(typeName string) (Deliverer, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.deliverers[typeName]
	return d, ok
}

// Types returns all registered deliverer types.
func (r *DeliveryRegistry) Types() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	types := make([]string, 0, len(r.deliverers))
	for t := range r.deliverers {
		types = append(types, t)
	}
	return types
}

// DeliverAlert routes an alert to the appropriate deliverer based on target type.
// Respects circuit breaker state and records success/failure.
//
// Every attempted delivery is written to the alert_history audit trail (if an
// auditor is attached): this covers the happy path, delivery failures, and
// circuit-breaker short-circuits. Audit write failures are logged but do not
// affect the delivery outcome — the audit trail is best-effort.
func (r *DeliveryRegistry) DeliverAlert(ctx context.Context, alert Alert, target config.AlertTarget) error {
	// Check circuit breaker before attempting delivery
	if r.breaker.IsOpen(target.Name) {
		err := fmt.Errorf("circuit breaker open for target %s: delivery suspended", target.Name)
		r.auditDelivery(ctx, alert, target, "circuit_breaker_open", err)
		return err
	}

	r.mu.RLock()
	deliverer, ok := r.deliverers[target.Type]
	r.mu.RUnlock()

	if !ok {
		err := fmt.Errorf("no deliverer registered for type: %s", target.Type)
		r.auditDelivery(ctx, alert, target, "no_deliverer", err)
		return err
	}

	// Attempt delivery
	err := deliverer.Deliver(ctx, alert, target)

	// Record result with circuit breaker
	if err != nil {
		r.breaker.RecordFailure(target.Name)
		wrapped := fmt.Errorf("delivery failed for target %s: %w", target.Name, err)
		r.auditDelivery(ctx, alert, target, "delivered", wrapped)
		return wrapped
	}

	r.breaker.RecordSuccess(target.Name)
	r.auditDelivery(ctx, alert, target, "delivered", nil)
	return nil
}

// auditDelivery records a delivery attempt to the alert_history table. action
// is the operation taken (e.g. "delivered", "circuit_breaker_open",
// "no_deliverer"). A nil deliveryErr records a successful outcome.
func (r *DeliveryRegistry) auditDelivery(ctx context.Context, alert Alert, target config.AlertTarget, action string, deliveryErr error) {
	r.mu.RLock()
	auditor := r.auditor
	r.mu.RUnlock()

	if auditor == nil {
		return
	}

	result := "success"
	if deliveryErr != nil {
		result = "error: " + deliveryErr.Error()
	}

	// Source defaults to the alert's source, falling back to component/type so
	// downstream queries are never empty. For heartbeat-originated alerts this
	// is the heartbeat job/task name.
	source := alert.Source
	if source == "" {
		source = alert.Component
	}
	if source == "" {
		source = alert.Type
	}

	entry := AlertHistoryEntry{
		AlertType:    alert.Type,
		Severity:     alert.Severity.String(),
		Source:       source,
		Message:      alert.Message,
		ActionTaken:  action + ":" + target.Name + "(" + target.Type + ")",
		ActionResult: result,
		Details: map[string]any{
			"alert_id":    alert.ID,
			"title":       alert.Title,
			"component":   alert.Component,
			"category":    alert.Category,
			"tags":        alert.Tags,
			"target_name": target.Name,
			"target_type": target.Type,
		},
	}
	if entry.AlertType == "" {
		// alert_history.alert_type is NOT NULL; fall back to a sensible value.
		entry.AlertType = "heartbeat"
	}

	if err := auditor.RecordAlert(ctx, entry); err != nil {
		// Non-fatal — log via log package; avoid a hard dependency on a logger
		// here so this function stays self-contained.
		log.Printf("[AlertAudit] failed to record alert history for target %s: %v", target.Name, err)
	}
}

// CircuitBreakerState returns the state of the circuit breaker for a target.
func (r *DeliveryRegistry) CircuitBreakerState(targetName string) (isOpen bool, failures int, cooldownEnds time.Time) {
	return r.breaker.State(targetName)
}

// ResetCircuitBreaker manually resets the circuit breaker for a target.
func (r *DeliveryRegistry) ResetCircuitBreaker(targetName string) {
	r.breaker.Reset(targetName)
}

// CircuitBreaker tracks failures per target and suspends delivery after repeated failures.
type CircuitBreaker struct {
	threshold int           // Number of consecutive failures before opening
	cooldown  time.Duration // How long the circuit stays open

	mu      sync.RWMutex
	targets map[string]*circuitState
}

type circuitState struct {
	failures    int       // Consecutive failure count
	openUntil   time.Time // When the circuit can close again (zero if closed)
	lastFailure time.Time // Time of last failure
}

// NewCircuitBreaker creates a new circuit breaker.
// threshold: number of consecutive failures before opening the circuit.
// cooldown: duration the circuit stays open before allowing retry.
func NewCircuitBreaker(threshold int, cooldown time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		threshold: threshold,
		cooldown:  cooldown,
		targets:   make(map[string]*circuitState),
	}
}

// IsOpen returns true if the circuit is open (delivery should not be attempted).
func (cb *CircuitBreaker) IsOpen(targetName string) bool {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	state, exists := cb.targets[targetName]
	if !exists {
		return false
	}

	// Circuit is open if we haven't passed the cooldown period
	if !state.openUntil.IsZero() && time.Now().Before(state.openUntil) {
		return true
	}

	return false
}

// RecordFailure records a delivery failure for the target.
// Opens the circuit if threshold is reached.
func (cb *CircuitBreaker) RecordFailure(targetName string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	state, exists := cb.targets[targetName]
	if !exists {
		state = &circuitState{}
		cb.targets[targetName] = state
	}

	state.failures++
	state.lastFailure = time.Now()

	// Open circuit if threshold reached
	if state.failures >= cb.threshold {
		state.openUntil = time.Now().Add(cb.cooldown)
	}
}

// RecordSuccess records a successful delivery, resetting the failure count.
func (cb *CircuitBreaker) RecordSuccess(targetName string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	state, exists := cb.targets[targetName]
	if !exists {
		return
	}

	// Reset on success
	state.failures = 0
	state.openUntil = time.Time{}
}

// State returns the current state of the circuit breaker for a target.
func (cb *CircuitBreaker) State(targetName string) (isOpen bool, failures int, cooldownEnds time.Time) {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	state, exists := cb.targets[targetName]
	if !exists {
		return false, 0, time.Time{}
	}

	isOpen = !state.openUntil.IsZero() && time.Now().Before(state.openUntil)
	return isOpen, state.failures, state.openUntil
}

// Reset manually resets the circuit breaker for a target.
func (cb *CircuitBreaker) Reset(targetName string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	delete(cb.targets, targetName)
}
