package heartbeat

import (
	"context"
	"fmt"
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
	mu         sync.RWMutex
}

// NewDeliveryRegistry creates a new delivery registry with circuit breaker support.
func NewDeliveryRegistry() *DeliveryRegistry {
	return &DeliveryRegistry{
		deliverers: make(map[string]Deliverer),
		breaker:    NewCircuitBreaker(3, 5*time.Minute),
	}
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
func (r *DeliveryRegistry) DeliverAlert(ctx context.Context, alert Alert, target config.AlertTarget) error {
	// Check circuit breaker before attempting delivery
	if r.breaker.IsOpen(target.Name) {
		return fmt.Errorf("circuit breaker open for target %s: delivery suspended", target.Name)
	}

	r.mu.RLock()
	deliverer, ok := r.deliverers[target.Type]
	r.mu.RUnlock()

	if !ok {
		return fmt.Errorf("no deliverer registered for type: %s", target.Type)
	}

	// Attempt delivery
	err := deliverer.Deliver(ctx, alert, target)

	// Record result with circuit breaker
	if err != nil {
		r.breaker.RecordFailure(target.Name)
		return fmt.Errorf("delivery failed for target %s: %w", target.Name, err)
	}

	r.breaker.RecordSuccess(target.Name)
	return nil
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
