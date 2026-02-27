package monitoring

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// HeartbeatEventType represents the type of heartbeat event
type HeartbeatEventType string

const (
	EventTypeHeartbeat    HeartbeatEventType = "heartbeat"
	EventTypeStatusChange HeartbeatEventType = "status_change"
	EventTypeMetricAlert  HeartbeatEventType = "metric_alert"
	EventTypeSystemEvent  HeartbeatEventType = "system_event"
)

// HeartbeatEventSeverity represents the severity of an event
type HeartbeatEventSeverity string

const (
	SeverityInfo     HeartbeatEventSeverity = "info"
	SeverityWarning  HeartbeatEventSeverity = "warning"
	SeverityError    HeartbeatEventSeverity = "error"
	SeverityCritical HeartbeatEventSeverity = "critical"
)

// HeartbeatEvent represents a diagnostic event emitted by the heartbeat system
type HeartbeatEvent struct {
	ID        string                 `json:"id"`
	Type      HeartbeatEventType     `json:"type"`
	Severity  HeartbeatEventSeverity `json:"severity"`
	Timestamp time.Time              `json:"timestamp"`
	Message   string                 `json:"message"`
	Source    string                 `json:"source"`
	Metrics   *MetricsSnapshot       `json:"metrics,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	Context   map[string]string      `json:"context,omitempty"`
}

// EventFilter represents criteria for filtering events
type EventFilter struct {
	Type       HeartbeatEventType     `json:"type,omitempty"`
	Severity   HeartbeatEventSeverity `json:"severity,omitempty"`
	Source     string                 `json:"source,omitempty"`
	Since      *time.Time             `json:"since,omitempty"`
	Until      *time.Time             `json:"until,omitempty"`
	MaxResults int                    `json:"max_results,omitempty"`
}

// NewHeartbeatEvent creates a new heartbeat event
func NewHeartbeatEvent(eventType HeartbeatEventType, severity HeartbeatEventSeverity, message, source string) *HeartbeatEvent {
	return &HeartbeatEvent{
		ID:        generateEventID(),
		Type:      eventType,
		Severity:  severity,
		Timestamp: time.Now(),
		Message:   message,
		Source:    source,
		Metadata:  make(map[string]interface{}),
		Context:   make(map[string]string),
	}
}

// NewHeartbeatEventWithMetrics creates a heartbeat event that includes metrics snapshot
func NewHeartbeatEventWithMetrics(metrics *GatewayMetrics, message, source string) *HeartbeatEvent {
	event := NewHeartbeatEvent(EventTypeHeartbeat, SeverityInfo, message, source)
	if metrics != nil {
		snapshot := metrics.Snapshot()
		event.Metrics = &snapshot
	}
	return event
}

// NewStatusChangeEvent creates a status change event
func NewStatusChangeEvent(oldStatus, newStatus, source string) *HeartbeatEvent {
	event := NewHeartbeatEvent(EventTypeStatusChange, SeverityWarning,
		fmt.Sprintf("Status changed from %s to %s", oldStatus, newStatus), source)
	event.AddMetadata("old_status", oldStatus)
	event.AddMetadata("new_status", newStatus)
	return event
}

// NewMetricAlertEvent creates a metric alert event
func NewMetricAlertEvent(metric string, value interface{}, threshold interface{}, source string) *HeartbeatEvent {
	event := NewHeartbeatEvent(EventTypeMetricAlert, SeverityWarning,
		fmt.Sprintf("Metric %s exceeded threshold: %v > %v", metric, value, threshold), source)
	event.AddMetadata("metric", metric)
	event.AddMetadata("value", value)
	event.AddMetadata("threshold", threshold)
	return event
}

// NewSystemEvent creates a system event
func NewSystemEvent(severity HeartbeatEventSeverity, message, source string) *HeartbeatEvent {
	return NewHeartbeatEvent(EventTypeSystemEvent, severity, message, source)
}

// AddMetadata adds metadata to the event
func (e *HeartbeatEvent) AddMetadata(key string, value interface{}) {
	if e.Metadata == nil {
		e.Metadata = make(map[string]interface{})
	}
	e.Metadata[key] = value
}

// AddContext adds context information to the event
func (e *HeartbeatEvent) AddContext(key, value string) {
	if e.Context == nil {
		e.Context = make(map[string]string)
	}
	e.Context[key] = value
}

// SetMetrics sets the metrics snapshot for the event
func (e *HeartbeatEvent) SetMetrics(metrics *GatewayMetrics) {
	if metrics != nil {
		snapshot := metrics.Snapshot()
		e.Metrics = &snapshot
	}
}

// ToJSON serializes the event to JSON
func (e *HeartbeatEvent) ToJSON() ([]byte, error) {
	return json.Marshal(e)
}

// String returns a human-readable string representation
func (e *HeartbeatEvent) String() string {
	return fmt.Sprintf("[%s] %s: %s (%s) - %s",
		e.Timestamp.Format(time.RFC3339),
		e.Type,
		e.Severity,
		e.Source,
		e.Message)
}

// IsAlert returns true if the event represents an alert (warning or higher severity)
func (e *HeartbeatEvent) IsAlert() bool {
	return e.Severity == SeverityWarning || e.Severity == SeverityError || e.Severity == SeverityCritical
}

// MatchesFilter returns true if the event matches the given filter
func (e *HeartbeatEvent) MatchesFilter(filter EventFilter) bool {
	// Check type filter
	if filter.Type != "" && e.Type != filter.Type {
		return false
	}

	// Check severity filter
	if filter.Severity != "" && e.Severity != filter.Severity {
		return false
	}

	// Check source filter
	if filter.Source != "" && e.Source != filter.Source {
		return false
	}

	// Check time filters
	if filter.Since != nil && e.Timestamp.Before(*filter.Since) {
		return false
	}

	if filter.Until != nil && e.Timestamp.After(*filter.Until) {
		return false
	}

	return true
}

// EventStore represents a store for heartbeat events
type EventStore interface {
	Store(event *HeartbeatEvent) error
	Query(filter EventFilter) ([]*HeartbeatEvent, error)
	Count(filter EventFilter) (int, error)
	Clear() error
}

// EventEmitter represents an interface for emitting events to external systems
type EventEmitter interface {
	EmitEvent(event *HeartbeatEvent) error
	EmitBatch(events []*HeartbeatEvent) error
	Configure(config EventEmitterConfig) error
	IsEnabled() bool
	Close() error
}

// EventEmitterConfig configures external event emission
type EventEmitterConfig struct {
	Enabled   bool                `json:"enabled"`
	Type      string              `json:"type"`       // "webhook", "log", "syslog"
	Endpoint  string              `json:"endpoint"`   // Webhook URL or log file path
	Headers   map[string]string   `json:"headers"`    // Additional headers for webhooks
	Format    string              `json:"format"`     // "json", "text"
	BatchSize int                 `json:"batch_size"` // For batch operations
	Timeout   int                 `json:"timeout_ms"` // Timeout for external calls
	Filters   EventEmitterFilters `json:"filters"`    // What events to emit
}

// EventEmitterFilters defines which events to emit
type EventEmitterFilters struct {
	MinSeverity HeartbeatEventSeverity `json:"min_severity"` // Only emit events at or above this severity
	Types       []HeartbeatEventType   `json:"types"`        // Only emit these event types (empty = all)
	Sources     []string               `json:"sources"`      // Only emit from these sources (empty = all)
}

// MemoryEventStore is an in-memory implementation of EventStore
type MemoryEventStore struct {
	events    []*HeartbeatEvent
	maxEvents int
}

// NewMemoryEventStore creates a new in-memory event store
func NewMemoryEventStore(maxEvents int) *MemoryEventStore {
	if maxEvents <= 0 {
		maxEvents = 1000 // Default to 1000 events
	}
	return &MemoryEventStore{
		events:    make([]*HeartbeatEvent, 0, maxEvents),
		maxEvents: maxEvents,
	}
}

// Store stores an event in memory
func (m *MemoryEventStore) Store(event *HeartbeatEvent) error {
	if event == nil {
		return fmt.Errorf("event cannot be nil")
	}

	// Add the event
	m.events = append(m.events, event)

	// Trim to max size if necessary
	if len(m.events) > m.maxEvents {
		// Remove oldest events
		copy(m.events, m.events[len(m.events)-m.maxEvents:])
		m.events = m.events[:m.maxEvents]
	}

	return nil
}

// Query queries events from memory based on filter
func (m *MemoryEventStore) Query(filter EventFilter) ([]*HeartbeatEvent, error) {
	var results []*HeartbeatEvent

	for _, event := range m.events {
		if event.MatchesFilter(filter) {
			results = append(results, event)
		}
	}

	// Limit results if specified
	if filter.MaxResults > 0 && len(results) > filter.MaxResults {
		results = results[:filter.MaxResults]
	}

	return results, nil
}

// Count counts events matching the filter
func (m *MemoryEventStore) Count(filter EventFilter) (int, error) {
	count := 0
	for _, event := range m.events {
		if event.MatchesFilter(filter) {
			count++
		}
	}
	return count, nil
}

// Clear removes all events from memory
func (m *MemoryEventStore) Clear() error {
	m.events = m.events[:0]
	return nil
}

// eventIDCounter ensures unique event IDs even when called in rapid succession
var eventIDCounter atomic.Int64

// generateEventID generates a unique event ID
func generateEventID() string {
	seq := eventIDCounter.Add(1)
	return fmt.Sprintf("evt_%d_%d", time.Now().UnixNano(), seq)
}

// WebhookEventEmitter emits events to external webhooks
type WebhookEventEmitter struct {
	config     EventEmitterConfig
	httpClient *http.Client
	enabled    bool
}

// NewWebhookEventEmitter creates a new webhook event emitter
func NewWebhookEventEmitter(config EventEmitterConfig) *WebhookEventEmitter {
	return &WebhookEventEmitter{
		config: config,
		httpClient: &http.Client{
			Timeout: time.Duration(config.Timeout) * time.Millisecond,
		},
		enabled: config.Enabled,
	}
}

// EmitEvent emits a single event to the configured webhook
func (w *WebhookEventEmitter) EmitEvent(event *HeartbeatEvent) error {
	if !w.enabled || w.config.Endpoint == "" {
		return nil // Silently skip if not configured
	}

	// Check if event matches filters
	if !w.shouldEmitEvent(event) {
		return nil // Skip filtered events
	}

	var payload []byte
	var err error
	var contentType string

	switch w.config.Format {
	case "text":
		payload = []byte(event.String())
		contentType = "text/plain"
	default: // json
		payload, err = event.ToJSON()
		if err != nil {
			return fmt.Errorf("failed to serialize event: %w", err)
		}
		contentType = "application/json"
	}

	// Create HTTP request
	req, err := http.NewRequest("POST", w.config.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "Conduit-Gateway/0.1.0")

	// Add custom headers
	for key, value := range w.config.Headers {
		req.Header.Set(key, value)
	}

	// Send request
	resp, err := w.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook returned error status: %d", resp.StatusCode)
	}

	return nil
}

// EmitBatch emits multiple events in a single request
func (w *WebhookEventEmitter) EmitBatch(events []*HeartbeatEvent) error {
	if !w.enabled || w.config.Endpoint == "" {
		return nil
	}

	// Filter events
	var filteredEvents []*HeartbeatEvent
	for _, event := range events {
		if w.shouldEmitEvent(event) {
			filteredEvents = append(filteredEvents, event)
		}
	}

	if len(filteredEvents) == 0 {
		return nil // Nothing to emit
	}

	var payload []byte
	var err error
	var contentType string

	switch w.config.Format {
	case "text":
		var lines []string
		for _, event := range filteredEvents {
			lines = append(lines, event.String())
		}
		payload = []byte(strings.Join(lines, "\n"))
		contentType = "text/plain"
	default: // json
		payload, err = json.Marshal(map[string]interface{}{
			"events":    filteredEvents,
			"count":     len(filteredEvents),
			"timestamp": time.Now(),
		})
		if err != nil {
			return fmt.Errorf("failed to serialize events: %w", err)
		}
		contentType = "application/json"
	}

	// Create and send request
	req, err := http.NewRequest("POST", w.config.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "Conduit-Gateway/0.1.0")

	for key, value := range w.config.Headers {
		req.Header.Set(key, value)
	}

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook returned error status: %d", resp.StatusCode)
	}

	return nil
}

// Configure updates the emitter configuration
func (w *WebhookEventEmitter) Configure(config EventEmitterConfig) error {
	w.config = config
	w.enabled = config.Enabled

	// Update HTTP client timeout
	w.httpClient.Timeout = time.Duration(config.Timeout) * time.Millisecond

	return nil
}

// IsEnabled returns whether the emitter is enabled
func (w *WebhookEventEmitter) IsEnabled() bool {
	return w.enabled
}

// Close closes the emitter (cleanup)
func (w *WebhookEventEmitter) Close() error {
	w.enabled = false
	return nil
}

// shouldEmitEvent checks if an event should be emitted based on filters
func (w *WebhookEventEmitter) shouldEmitEvent(event *HeartbeatEvent) bool {
	// Check minimum severity
	if w.config.Filters.MinSeverity != "" {
		if !w.meetsSeverityThreshold(event.Severity, w.config.Filters.MinSeverity) {
			return false
		}
	}

	// Check event types filter
	if len(w.config.Filters.Types) > 0 {
		found := false
		for _, allowedType := range w.config.Filters.Types {
			if event.Type == allowedType {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// Check sources filter
	if len(w.config.Filters.Sources) > 0 {
		found := false
		for _, allowedSource := range w.config.Filters.Sources {
			if event.Source == allowedSource {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	return true
}

// meetsSeverityThreshold checks if event severity meets the minimum threshold
func (w *WebhookEventEmitter) meetsSeverityThreshold(eventSeverity, minSeverity HeartbeatEventSeverity) bool {
	severityLevels := map[HeartbeatEventSeverity]int{
		SeverityInfo:     1,
		SeverityWarning:  2,
		SeverityError:    3,
		SeverityCritical: 4,
	}

	eventLevel, eventOk := severityLevels[eventSeverity]
	minLevel, minOk := severityLevels[minSeverity]

	if !eventOk || !minOk {
		return true // Default to allowing if unknown severity
	}

	return eventLevel >= minLevel
}
