package heartbeat

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"conduit/internal/config"
)

// conduit-31jg.59: every heartbeat message goes through
// DeliveryRegistry.DeliverAlert. The first attempt is synchronous (callers
// still see the error); live sends that fail are then retried in the
// background per agent_heartbeat.alert_retry_policy, so the heartbeat loop
// never sleeps on a backoff. Deferred-queue flushes are not retried here:
// the durable queue already retries them on the next cycle.

const (
	// maxPendingRetries bounds concurrent background retry goroutines. Past
	// it, further failures are logged and dropped rather than queued.
	maxPendingRetries = 32
	// retryAttemptTimeout bounds a single background retry attempt.
	retryAttemptTimeout = 30 * time.Second
)

// minRetryInterval guards against a zero/absent retry_interval turning the
// retry loop into a tight spin. A var so tests can shorten it.
var minRetryInterval = time.Second

// deliveryMode selects whether a failed first attempt is retried in the
// background.
type deliveryMode int

const (
	deliverWithRetry deliveryMode = iota // live sends
	deliverOnce                          // deferred flush: the queue retries
)

// alertMeta classifies a heartbeat message for the audit trail.
type alertMeta struct {
	Type     string
	Severity AlertSeverity
	Source   string
}

var defaultAlertMeta = alertMeta{Type: "heartbeat", Severity: AlertSeverityInfo, Source: "heartbeat"}

// actionAlertMeta derives audit metadata from a heartbeat action.
func actionAlertMeta(action HeartbeatAction, jobID string) alertMeta {
	sev := AlertSeverityInfo
	switch action.Priority {
	case TaskPriorityCritical:
		sev = AlertSeverityCritical
	case TaskPriorityHigh:
		sev = AlertSeverityWarning
	}
	src := "heartbeat"
	if jobID != "" {
		src = jobID
	}
	return alertMeta{Type: "heartbeat_" + string(action.Type), Severity: sev, Source: src}
}

// retryState tracks background retries so they stay bounded and can be
// stopped on shutdown.
type retryState struct {
	mu      sync.Mutex
	wg      sync.WaitGroup
	pending int
	stop    chan struct{}
	closed  bool
}

// SetDeliveryRegistry routes heartbeat delivery through reg (typically the
// gateway's audited registry) and registers a ChannelSenderDeliverer on it
// that wraps this integration's ChannelSender.
func (g *GatewayIntegration) SetDeliveryRegistry(reg *DeliveryRegistry) {
	if reg == nil {
		return
	}
	reg.Register(NewChannelSenderDeliverer(g.channelSender))
	g.deferMu.Lock()
	g.delivery = reg
	g.deferMu.Unlock()
}

// deliveryRegistry returns the registry in use.
func (g *GatewayIntegration) deliveryRegistry() *DeliveryRegistry {
	g.deferMu.Lock()
	defer g.deferMu.Unlock()
	return g.delivery
}

// Close stops pending background retries and waits for in-flight attempts.
// Safe to call more than once.
func (g *GatewayIntegration) Close() error {
	g.retries.mu.Lock()
	if !g.retries.closed {
		g.retries.closed = true
		close(g.retries.stop)
	}
	g.retries.mu.Unlock()
	g.retries.wg.Wait()
	return nil
}

// dispatch sends an already-sanitized message to target via the registry.
func (g *GatewayIntegration) dispatch(ctx context.Context, target, message string, meta alertMeta, mode deliveryMode) error {
	reg := g.deliveryRegistry()
	now := g.clock()
	alert := Alert{
		ID:        fmt.Sprintf("hb-%d", now.UnixNano()),
		Source:    meta.Source,
		Type:      meta.Type,
		Title:     "Heartbeat " + meta.Type,
		Message:   message,
		Severity:  meta.Severity,
		Status:    AlertStatusPending,
		CreatedAt: now,
	}
	t := channelTarget(target)

	err := reg.DeliverAlert(ctx, alert, t)
	if err == nil || mode != deliverWithRetry || errors.Is(err, ErrCircuitOpen) {
		return err
	}
	g.scheduleRetries(reg, alert, t)
	return err
}

// scheduleRetries retries a failed live delivery in the background per the
// configured retry policy. Each attempt goes through DeliverAlert, so it is
// audited and counted by the circuit breaker; retries stop early on success,
// on an open breaker, or on Close.
func (g *GatewayIntegration) scheduleRetries(reg *DeliveryRegistry, alert Alert, target config.AlertTarget) {
	policy := g.quietConfig().AlertRetryPolicy
	if policy.MaxRetries <= 0 {
		return
	}

	g.retries.mu.Lock()
	if g.retries.closed || g.retries.pending >= maxPendingRetries {
		closed := g.retries.closed
		g.retries.mu.Unlock()
		if !closed {
			log.Printf("[HeartbeatIntegration] Retry backlog full (%d); not retrying delivery to %s", maxPendingRetries, target.Name)
		}
		return
	}
	g.retries.pending++
	g.retries.wg.Add(1)
	stop := g.retries.stop
	g.retries.mu.Unlock()

	go func() {
		defer func() {
			g.retries.mu.Lock()
			g.retries.pending--
			g.retries.mu.Unlock()
			g.retries.wg.Done()
		}()

		delay := policy.RetryInterval
		if delay < minRetryInterval {
			delay = minRetryInterval
		}
		backoff := policy.BackoffFactor
		if backoff < 1 {
			backoff = 1
		}
		for attempt := 1; attempt <= policy.MaxRetries; attempt++ {
			timer := time.NewTimer(delay)
			select {
			case <-stop:
				timer.Stop()
				return
			case <-timer.C:
			}

			ctx, cancel := context.WithTimeout(context.Background(), retryAttemptTimeout)
			err := reg.DeliverAlert(ctx, alert, target)
			cancel()
			if err == nil {
				log.Printf("[HeartbeatIntegration] Delivery to %s succeeded on retry %d", target.Name, attempt)
				return
			}
			if errors.Is(err, ErrCircuitOpen) {
				log.Printf("[HeartbeatIntegration] Circuit open for %s; abandoning retries after %d attempt(s)", target.Name, attempt)
				return
			}
			log.Printf("[HeartbeatIntegration] Delivery retry %d/%d to %s failed: %v", attempt, policy.MaxRetries, target.Name, err)
			delay = time.Duration(float64(delay) * backoff)
		}
		log.Printf("[HeartbeatIntegration] Delivery to %s failed after %d retries; giving up", target.Name, policy.MaxRetries)
	}()
}
