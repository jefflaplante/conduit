package monitoring

import (
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// AlertSeverity represents the severity level of an alerting rule alert.
type AlertSeverity string

const (
	AlertSeverityInfo     AlertSeverity = "info"
	AlertSeverityWarning  AlertSeverity = "warning"
	AlertSeverityCritical AlertSeverity = "critical"
)

// severityLevel returns a numeric severity for ordering comparisons.
func (s AlertSeverity) severityLevel() int {
	switch s {
	case AlertSeverityInfo:
		return 1
	case AlertSeverityWarning:
		return 2
	case AlertSeverityCritical:
		return 3
	default:
		return 0
	}
}

// AlertCondition defines the comparison operator for a rule threshold.
type AlertCondition string

const (
	ConditionGreaterThan AlertCondition = "gt"
	ConditionLessThan    AlertCondition = "lt"
	ConditionEqual       AlertCondition = "eq"
)

// AlertRule defines a configurable threshold-based alert rule.
type AlertRule struct {
	// Name is a unique identifier for this rule.
	Name string `json:"name"`
	// Metric is the name of the metric to evaluate (e.g. "error_rate", "failed_requests").
	Metric string `json:"metric"`
	// Condition is the comparison operator applied to the metric value against the threshold.
	Condition AlertCondition `json:"condition"`
	// Threshold is the boundary value that triggers the alert when breached.
	Threshold float64 `json:"threshold"`
	// Window is the time window over which the metric is evaluated.
	Window time.Duration `json:"window"`
	// Severity is the alert severity when this rule fires.
	Severity AlertSeverity `json:"severity"`
	// Cooldown is the minimum duration between consecutive firings of this rule.
	Cooldown time.Duration `json:"cooldown"`
	// Description provides optional human-readable context for the rule.
	Description string `json:"description,omitempty"`
}

// Validate checks that the rule has the minimum required fields.
func (r AlertRule) Validate() error {
	if r.Name == "" {
		return fmt.Errorf("rule name cannot be empty")
	}
	if r.Metric == "" {
		return fmt.Errorf("rule metric cannot be empty")
	}
	switch r.Condition {
	case ConditionGreaterThan, ConditionLessThan, ConditionEqual:
		// valid
	default:
		return fmt.Errorf("invalid condition %q: must be gt, lt, or eq", r.Condition)
	}
	if r.Severity.severityLevel() == 0 {
		return fmt.Errorf("invalid severity %q: must be info, warning, or critical", r.Severity)
	}
	return nil
}

// Alert represents a fired alert produced by evaluating a rule against current metrics.
type Alert struct {
	// RuleName is the name of the rule that fired.
	RuleName string `json:"rule_name"`
	// Metric is the metric that was evaluated.
	Metric string `json:"metric"`
	// CurrentValue is the metric's current value at fire time.
	CurrentValue float64 `json:"current_value"`
	// Threshold is the rule's configured threshold.
	Threshold float64 `json:"threshold"`
	// Condition is the comparison that was breached.
	Condition AlertCondition `json:"condition"`
	// Severity is the severity inherited from the rule.
	Severity AlertSeverity `json:"severity"`
	// Message is a human-readable description of the alert.
	Message string `json:"message"`
	// FiredAt is the timestamp when the alert fired.
	FiredAt time.Time `json:"fired_at"`
	// Resolved indicates whether the condition has since cleared.
	Resolved bool `json:"resolved"`
	// ResolvedAt is the timestamp when the alert was resolved, if applicable.
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
}

// AlertHandler is a pluggable callback interface for alert delivery.
type AlertHandler interface {
	HandleAlert(alert Alert)
}

// MetricsProvider is the interface the AlertManager uses to obtain current metric values.
// Implementations may pull data from GatewayMetrics, UsageTracker, or any other source.
type MetricsProvider interface {
	// GetMetricValue returns the current value of the named metric.
	// Returns the value and true if the metric is known, or 0 and false otherwise.
	GetMetricValue(metric string) (float64, bool)
}

// SnapshotMetricsProvider adapts a *GatewayMetrics (via Snapshot) into a MetricsProvider.
type SnapshotMetricsProvider struct {
	metrics *GatewayMetrics
}

// NewSnapshotMetricsProvider creates a MetricsProvider backed by GatewayMetrics.
func NewSnapshotMetricsProvider(metrics *GatewayMetrics) *SnapshotMetricsProvider {
	return &SnapshotMetricsProvider{metrics: metrics}
}

// GetMetricValue returns the value of the named metric from the current snapshot.
func (p *SnapshotMetricsProvider) GetMetricValue(metric string) (float64, bool) {
	if p.metrics == nil {
		return 0, false
	}
	snap := p.metrics.Snapshot()
	return metricFromSnapshot(snap, metric)
}

// metricFromSnapshot extracts a named metric value from a MetricsSnapshot.
func metricFromSnapshot(snap MetricsSnapshot, metric string) (float64, bool) {
	switch metric {
	case "active_sessions":
		return float64(snap.ActiveSessions), true
	case "processing_sessions":
		return float64(snap.ProcessingSessions), true
	case "waiting_sessions":
		return float64(snap.WaitingSessions), true
	case "idle_sessions":
		return float64(snap.IdleSessions), true
	case "total_sessions":
		return float64(snap.TotalSessions), true
	case "queue_depth":
		return float64(snap.QueueDepth), true
	case "pending_requests":
		return float64(snap.PendingRequests), true
	case "completed_requests":
		return float64(snap.CompletedRequests), true
	case "failed_requests":
		return float64(snap.FailedRequests), true
	case "webhook_connections":
		return float64(snap.WebhookConnections), true
	case "active_webhooks":
		return float64(snap.ActiveWebhooks), true
	case "uptime_seconds":
		return float64(snap.UptimeSeconds), true
	case "memory_usage_mb":
		return snap.MemoryUsageMB, true
	case "memory_usage_bytes":
		return float64(snap.MemoryUsageBytes), true
	case "goroutine_count":
		return float64(snap.GoroutineCount), true
	case "error_rate":
		total := snap.CompletedRequests + snap.FailedRequests
		if total == 0 {
			return 0, true
		}
		return float64(snap.FailedRequests) / float64(total) * 100.0, true
	default:
		return 0, false
	}
}

// AlertManager is the centralized alert management system. It evaluates configurable rules
// against live metrics and dispatches fired alerts to registered handlers.
type AlertManager struct {
	mu sync.RWMutex

	// rules stores registered alert rules keyed by name.
	rules map[string]AlertRule

	// provider supplies current metric values for evaluation.
	provider MetricsProvider

	// handlers are callbacks invoked when an alert fires.
	handlers []AlertHandler

	// activeAlerts holds alerts that are currently firing (not yet resolved).
	activeAlerts map[string]*Alert

	// alertHistory stores all fired alerts for historical querying.
	alertHistory []Alert
	maxHistory   int

	// lastFired tracks the last time each rule fired, for cooldown enforcement.
	lastFired map[string]time.Time

	// background evaluation loop
	running atomic.Bool
	cancel  func()
	done    chan struct{}
}

// NewAlertManager creates a new AlertManager with the given MetricsProvider.
// The manager starts in a stopped state; call Start to begin background evaluation.
func NewAlertManager(provider MetricsProvider) *AlertManager {
	return &AlertManager{
		rules:        make(map[string]AlertRule),
		provider:     provider,
		activeAlerts: make(map[string]*Alert),
		alertHistory: make([]Alert, 0, 256),
		maxHistory:   1000,
		lastFired:    make(map[string]time.Time),
		done:         make(chan struct{}),
	}
}

// AddRule registers an alert rule. Returns an error if the rule is invalid or
// a rule with the same name already exists.
func (am *AlertManager) AddRule(rule AlertRule) error {
	if err := rule.Validate(); err != nil {
		return fmt.Errorf("invalid rule: %w", err)
	}

	am.mu.Lock()
	defer am.mu.Unlock()

	if _, exists := am.rules[rule.Name]; exists {
		return fmt.Errorf("rule %q already exists", rule.Name)
	}

	am.rules[rule.Name] = rule
	return nil
}

// RemoveRule unregisters a rule by name and resolves any active alert for that rule.
func (am *AlertManager) RemoveRule(name string) {
	am.mu.Lock()
	defer am.mu.Unlock()

	delete(am.rules, name)
	// Resolve any active alert for the removed rule.
	if alert, exists := am.activeAlerts[name]; exists {
		now := time.Now()
		alert.Resolved = true
		alert.ResolvedAt = &now
		delete(am.activeAlerts, name)
	}
	delete(am.lastFired, name)
}

// GetRule returns a copy of the named rule and true, or a zero value and false if not found.
func (am *AlertManager) GetRule(name string) (AlertRule, bool) {
	am.mu.RLock()
	defer am.mu.RUnlock()

	r, ok := am.rules[name]
	return r, ok
}

// GetRules returns a copy of all registered rules.
func (am *AlertManager) GetRules() []AlertRule {
	am.mu.RLock()
	defer am.mu.RUnlock()

	rules := make([]AlertRule, 0, len(am.rules))
	for _, r := range am.rules {
		rules = append(rules, r)
	}
	return rules
}

// Subscribe registers an AlertHandler that will be called for every fired alert.
func (am *AlertManager) Subscribe(handler AlertHandler) {
	if handler == nil {
		return
	}

	am.mu.Lock()
	defer am.mu.Unlock()

	am.handlers = append(am.handlers, handler)
}

// Evaluate checks all registered rules against current metrics. Rules whose condition
// is breached produce an alert (subject to cooldown). Rules whose condition has cleared
// resolve any previously active alert. Returns the list of newly fired alerts.
func (am *AlertManager) Evaluate() []Alert {
	am.mu.Lock()
	defer am.mu.Unlock()

	if am.provider == nil {
		return nil
	}

	now := time.Now()
	var fired []Alert

	for _, rule := range am.rules {
		value, known := am.provider.GetMetricValue(rule.Metric)
		if !known {
			// Unknown metric -- skip silently for graceful degradation.
			continue
		}

		breached := am.isBreached(rule.Condition, value, rule.Threshold)

		if breached {
			// Enforce cooldown.
			if last, ok := am.lastFired[rule.Name]; ok {
				if rule.Cooldown > 0 && now.Sub(last) < rule.Cooldown {
					continue
				}
			}

			alert := Alert{
				RuleName:     rule.Name,
				Metric:       rule.Metric,
				CurrentValue: value,
				Threshold:    rule.Threshold,
				Condition:    rule.Condition,
				Severity:     rule.Severity,
				Message:      am.formatMessage(rule, value),
				FiredAt:      now,
			}

			am.activeAlerts[rule.Name] = &alert
			am.lastFired[rule.Name] = now
			am.appendHistory(alert)
			fired = append(fired, alert)
		} else {
			// Condition cleared -- resolve active alert if present.
			if active, exists := am.activeAlerts[rule.Name]; exists {
				resolvedAt := now
				active.Resolved = true
				active.ResolvedAt = &resolvedAt
				delete(am.activeAlerts, rule.Name)
			}
		}
	}

	// Deliver alerts to handlers outside of the critical path but still under
	// the same lock to guarantee ordering. Handlers should be lightweight;
	// heavy work should be done asynchronously inside the handler.
	handlers := make([]AlertHandler, len(am.handlers))
	copy(handlers, am.handlers)

	// Release lock before calling handlers to avoid deadlocks in handler code.
	am.mu.Unlock()
	for _, alert := range fired {
		for _, h := range handlers {
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("[AlertManager] handler panic for alert %q: %v", alert.RuleName, r)
					}
				}()
				h.HandleAlert(alert)
			}()
		}
	}
	am.mu.Lock()

	return fired
}

// GetActiveAlerts returns a copy of all currently active (unresolved) alerts.
func (am *AlertManager) GetActiveAlerts() []Alert {
	am.mu.RLock()
	defer am.mu.RUnlock()

	alerts := make([]Alert, 0, len(am.activeAlerts))
	for _, a := range am.activeAlerts {
		alerts = append(alerts, *a)
	}
	return alerts
}

// GetAlertHistory returns historical alerts that fired within the given duration.
// A zero or negative duration returns all stored history.
func (am *AlertManager) GetAlertHistory(since time.Duration) []Alert {
	am.mu.RLock()
	defer am.mu.RUnlock()

	if since <= 0 {
		out := make([]Alert, len(am.alertHistory))
		copy(out, am.alertHistory)
		return out
	}

	cutoff := time.Now().Add(-since)
	var out []Alert
	for _, a := range am.alertHistory {
		if !a.FiredAt.Before(cutoff) {
			out = append(out, a)
		}
	}
	return out
}

// Start begins a background goroutine that calls Evaluate at the given interval.
// It is safe to call Start multiple times; subsequent calls are no-ops while running.
func (am *AlertManager) Start(interval time.Duration) {
	if am.running.Load() {
		return
	}

	if interval <= 0 {
		interval = 30 * time.Second
	}

	am.mu.Lock()
	am.done = make(chan struct{})
	am.mu.Unlock()

	am.running.Store(true)

	// Build a cancellation mechanism.
	stopCh := make(chan struct{})
	am.mu.Lock()
	am.cancel = func() { close(stopCh) }
	am.mu.Unlock()

	go am.evaluationLoop(interval, stopCh)
}

// Stop halts the background evaluation loop. It blocks until the loop exits or
// a reasonable timeout elapses.
func (am *AlertManager) Stop() {
	if !am.running.Load() {
		return
	}

	am.mu.RLock()
	cancelFn := am.cancel
	done := am.done
	am.mu.RUnlock()

	if cancelFn != nil {
		cancelFn()
	}

	// Wait for the loop to exit.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		log.Println("[AlertManager] stop timed out waiting for evaluation loop")
	}

	am.running.Store(false)
}

// IsRunning returns whether the background evaluation loop is active.
func (am *AlertManager) IsRunning() bool {
	return am.running.Load()
}

// --- internal helpers ---

func (am *AlertManager) evaluationLoop(interval time.Duration, stop <-chan struct{}) {
	defer func() {
		am.mu.RLock()
		done := am.done
		am.mu.RUnlock()
		close(done)
	}()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Immediate first evaluation.
	am.Evaluate()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			am.Evaluate()
		}
	}
}

func (am *AlertManager) isBreached(cond AlertCondition, value, threshold float64) bool {
	switch cond {
	case ConditionGreaterThan:
		return value > threshold
	case ConditionLessThan:
		return value < threshold
	case ConditionEqual:
		return value == threshold
	default:
		return false
	}
}

func (am *AlertManager) formatMessage(rule AlertRule, value float64) string {
	var condStr string
	switch rule.Condition {
	case ConditionGreaterThan:
		condStr = "exceeded"
	case ConditionLessThan:
		condStr = "dropped below"
	case ConditionEqual:
		condStr = "equals"
	}
	return fmt.Sprintf("[%s] %s %s threshold: %.2f (threshold: %.2f)",
		rule.Severity, rule.Metric, condStr, value, rule.Threshold)
}

// appendHistory adds an alert to the history ring, evicting the oldest entry when full.
func (am *AlertManager) appendHistory(a Alert) {
	am.alertHistory = append(am.alertHistory, a)
	if len(am.alertHistory) > am.maxHistory {
		// Trim oldest entries.
		excess := len(am.alertHistory) - am.maxHistory
		copy(am.alertHistory, am.alertHistory[excess:])
		am.alertHistory = am.alertHistory[:am.maxHistory]
	}
}

// --- Built-in handlers ---

// LogHandler logs fired alerts to stderr via the standard log package.
type LogHandler struct{}

// HandleAlert logs the alert.
func (h *LogHandler) HandleAlert(alert Alert) {
	log.Printf("[Alert][%s] %s (rule=%s metric=%s value=%.2f threshold=%.2f)",
		alert.Severity, alert.Message, alert.RuleName, alert.Metric,
		alert.CurrentValue, alert.Threshold)
}

// ChannelHandler is a placeholder for future channel-based alert delivery.
// When a ChannelSender implementation is available it can be injected here.
type ChannelHandler struct {
	// TargetChannel is the channel name or ID to send alerts to.
	TargetChannel string
}

// HandleAlert is a no-op placeholder. Integrate with ChannelSender when available.
func (h *ChannelHandler) HandleAlert(alert Alert) {
	// Placeholder: in a future integration, this would call
	// channelSender.SendMessage(h.TargetChannel, alert.Message)
	log.Printf("[Alert][ChannelHandler] would send to %s: %s", h.TargetChannel, alert.Message)
}

// FuncHandler adapts a plain function into an AlertHandler. Useful for tests and
// lightweight integrations that don't warrant a full struct.
type FuncHandler struct {
	Fn func(Alert)
}

// HandleAlert calls the wrapped function.
func (h *FuncHandler) HandleAlert(alert Alert) {
	if h.Fn != nil {
		h.Fn(alert)
	}
}
