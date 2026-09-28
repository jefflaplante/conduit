package monitoring

import (
	"encoding/json"
	"fmt"
	"sync"
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

// MemoryEventStore is an in-memory implementation of EventStore
type MemoryEventStore struct {
	mu        sync.RWMutex
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

	m.mu.Lock()
	defer m.mu.Unlock()

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
	m.mu.RLock()
	defer m.mu.RUnlock()

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
	m.mu.RLock()
	defer m.mu.RUnlock()

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
	m.mu.Lock()
	defer m.mu.Unlock()

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
