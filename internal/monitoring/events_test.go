package monitoring

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNewHeartbeatEvent(t *testing.T) {
	eventType := EventTypeHeartbeat
	severity := SeverityInfo
	message := "Test message"
	source := "test-source"

	event := NewHeartbeatEvent(eventType, severity, message, source)

	if event == nil {
		t.Fatal("NewHeartbeatEvent() returned nil")
	}

	if event.Type != eventType {
		t.Errorf("Type = %s, expected %s", event.Type, eventType)
	}

	if event.Severity != severity {
		t.Errorf("Severity = %s, expected %s", event.Severity, severity)
	}

	if event.Message != message {
		t.Errorf("Message = %s, expected %s", event.Message, message)
	}

	if event.Source != source {
		t.Errorf("Source = %s, expected %s", event.Source, source)
	}

	if event.ID == "" {
		t.Error("ID should be set")
	}

	if event.Timestamp.IsZero() {
		t.Error("Timestamp should be set")
	}

	if event.Metadata == nil {
		t.Error("Metadata should be initialized")
	}

	if event.Context == nil {
		t.Error("Context should be initialized")
	}
}

func TestNewHeartbeatEventWithMetrics(t *testing.T) {
	metrics := NewGatewayMetrics()
	metrics.UpdateSessionCount(5, 2, 1, 2, 10)
	metrics.SetStatus("healthy")

	message := "Heartbeat with metrics"
	source := "test-heartbeat"

	event := NewHeartbeatEventWithMetrics(metrics, message, source)

	if event == nil {
		t.Fatal("NewHeartbeatEventWithMetrics() returned nil")
	}

	if event.Type != EventTypeHeartbeat {
		t.Errorf("Type = %s, expected %s", event.Type, EventTypeHeartbeat)
	}

	if event.Severity != SeverityInfo {
		t.Errorf("Severity = %s, expected %s", event.Severity, SeverityInfo)
	}

	if event.Metrics == nil {
		t.Fatal("Metrics should be set")
	}

	if event.Metrics.ActiveSessions != 5 {
		t.Errorf("Metrics.ActiveSessions = %d, expected 5", event.Metrics.ActiveSessions)
	}

	if event.Metrics.Status != "healthy" {
		t.Errorf("Metrics.Status = %s, expected 'healthy'", event.Metrics.Status)
	}
}

func TestNewHeartbeatEventWithMetrics_NilMetrics(t *testing.T) {
	event := NewHeartbeatEventWithMetrics(nil, "test", "test-source")

	if event == nil {
		t.Fatal("NewHeartbeatEventWithMetrics() returned nil")
	}

	if event.Metrics != nil {
		t.Error("Metrics should be nil when passed nil")
	}
}

func TestNewStatusChangeEvent(t *testing.T) {
	oldStatus := "healthy"
	newStatus := "degraded"
	source := "gateway"

	event := NewStatusChangeEvent(oldStatus, newStatus, source)

	if event.Type != EventTypeStatusChange {
		t.Errorf("Type = %s, expected %s", event.Type, EventTypeStatusChange)
	}

	if event.Severity != SeverityWarning {
		t.Errorf("Severity = %s, expected %s", event.Severity, SeverityWarning)
	}

	expectedMessage := "Status changed from healthy to degraded"
	if event.Message != expectedMessage {
		t.Errorf("Message = %s, expected %s", event.Message, expectedMessage)
	}

	// Check metadata
	if event.Metadata["old_status"] != oldStatus {
		t.Errorf("Metadata old_status = %v, expected %s", event.Metadata["old_status"], oldStatus)
	}

	if event.Metadata["new_status"] != newStatus {
		t.Errorf("Metadata new_status = %v, expected %s", event.Metadata["new_status"], newStatus)
	}
}

func TestNewMetricAlertEvent(t *testing.T) {
	metric := "memory_usage_mb"
	value := 512.5
	threshold := 500.0
	source := "monitor"

	event := NewMetricAlertEvent(metric, value, threshold, source)

	if event.Type != EventTypeMetricAlert {
		t.Errorf("Type = %s, expected %s", event.Type, EventTypeMetricAlert)
	}

	if event.Severity != SeverityWarning {
		t.Errorf("Severity = %s, expected %s", event.Severity, SeverityWarning)
	}

	expectedMessage := "Metric memory_usage_mb exceeded threshold: 512.5 > 500"
	if event.Message != expectedMessage {
		t.Errorf("Message = %s, expected %s", event.Message, expectedMessage)
	}

	// Check metadata
	if event.Metadata["metric"] != metric {
		t.Errorf("Metadata metric = %v, expected %s", event.Metadata["metric"], metric)
	}

	if event.Metadata["value"] != value {
		t.Errorf("Metadata value = %v, expected %v", event.Metadata["value"], value)
	}

	if event.Metadata["threshold"] != threshold {
		t.Errorf("Metadata threshold = %v, expected %v", event.Metadata["threshold"], threshold)
	}
}

func TestNewSystemEvent(t *testing.T) {
	severity := SeverityError
	message := "System error occurred"
	source := "system"

	event := NewSystemEvent(severity, message, source)

	if event.Type != EventTypeSystemEvent {
		t.Errorf("Type = %s, expected %s", event.Type, EventTypeSystemEvent)
	}

	if event.Severity != severity {
		t.Errorf("Severity = %s, expected %s", event.Severity, severity)
	}

	if event.Message != message {
		t.Errorf("Message = %s, expected %s", event.Message, message)
	}

	if event.Source != source {
		t.Errorf("Source = %s, expected %s", event.Source, source)
	}
}

func TestHeartbeatEvent_AddMetadata(t *testing.T) {
	event := NewHeartbeatEvent(EventTypeHeartbeat, SeverityInfo, "test", "test")

	// Add various types of metadata
	event.AddMetadata("string_key", "string_value")
	event.AddMetadata("int_key", 42)
	event.AddMetadata("float_key", 3.14)
	event.AddMetadata("bool_key", true)

	if event.Metadata["string_key"] != "string_value" {
		t.Errorf("string_key = %v, expected 'string_value'", event.Metadata["string_key"])
	}

	if event.Metadata["int_key"] != 42 {
		t.Errorf("int_key = %v, expected 42", event.Metadata["int_key"])
	}

	if event.Metadata["float_key"] != 3.14 {
		t.Errorf("float_key = %v, expected 3.14", event.Metadata["float_key"])
	}

	if event.Metadata["bool_key"] != true {
		t.Errorf("bool_key = %v, expected true", event.Metadata["bool_key"])
	}
}

func TestHeartbeatEvent_AddContext(t *testing.T) {
	event := NewHeartbeatEvent(EventTypeHeartbeat, SeverityInfo, "test", "test")

	event.AddContext("session_id", "sess_123")
	event.AddContext("user_id", "user_456")

	if event.Context["session_id"] != "sess_123" {
		t.Errorf("Context session_id = %s, expected 'sess_123'", event.Context["session_id"])
	}

	if event.Context["user_id"] != "user_456" {
		t.Errorf("Context user_id = %s, expected 'user_456'", event.Context["user_id"])
	}
}

func TestHeartbeatEvent_SetMetrics(t *testing.T) {
	event := NewHeartbeatEvent(EventTypeHeartbeat, SeverityInfo, "test", "test")

	metrics := NewGatewayMetrics()
	metrics.UpdateSessionCount(3, 1, 1, 1, 5)

	event.SetMetrics(metrics)

	if event.Metrics == nil {
		t.Fatal("Metrics should be set")
	}

	if event.Metrics.ActiveSessions != 3 {
		t.Errorf("Metrics.ActiveSessions = %d, expected 3", event.Metrics.ActiveSessions)
	}

	// Test with nil metrics - should not change existing metrics
	originalMetrics := event.Metrics
	event.SetMetrics(nil)
	if event.Metrics != originalMetrics {
		t.Error("Metrics should remain unchanged when SetMetrics(nil) is called")
	}
}

func TestHeartbeatEvent_ToJSON(t *testing.T) {
	event := NewHeartbeatEvent(EventTypeHeartbeat, SeverityInfo, "test message", "test-source")
	event.AddMetadata("test_key", "test_value")
	event.AddContext("context_key", "context_value")

	jsonData, err := event.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON() failed: %v", err)
	}

	// Parse back to verify structure
	var parsed map[string]interface{}
	err = json.Unmarshal(jsonData, &parsed)
	if err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}

	if parsed["type"] != string(EventTypeHeartbeat) {
		t.Errorf("Parsed type = %v, expected %s", parsed["type"], EventTypeHeartbeat)
	}

	if parsed["message"] != "test message" {
		t.Errorf("Parsed message = %v, expected 'test message'", parsed["message"])
	}
}

func TestHeartbeatEvent_String(t *testing.T) {
	timestamp := time.Date(2023, 10, 15, 14, 30, 45, 0, time.UTC)

	event := &HeartbeatEvent{
		ID:        "test-id",
		Type:      EventTypeHeartbeat,
		Severity:  SeverityInfo,
		Timestamp: timestamp,
		Message:   "Test heartbeat",
		Source:    "test-source",
	}

	str := event.String()
	expected := "[2023-10-15T14:30:45Z] heartbeat: info (test-source) - Test heartbeat"

	if str != expected {
		t.Errorf("String() = %s, expected %s", str, expected)
	}
}

func TestHeartbeatEvent_IsAlert(t *testing.T) {
	tests := []struct {
		name     string
		severity HeartbeatEventSeverity
		isAlert  bool
	}{
		{"info is not alert", SeverityInfo, false},
		{"warning is alert", SeverityWarning, true},
		{"error is alert", SeverityError, true},
		{"critical is alert", SeverityCritical, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := NewHeartbeatEvent(EventTypeHeartbeat, tt.severity, "test", "test")
			if event.IsAlert() != tt.isAlert {
				t.Errorf("IsAlert() = %v, expected %v", event.IsAlert(), tt.isAlert)
			}
		})
	}
}

func TestHeartbeatEvent_MatchesFilter(t *testing.T) {
	timestamp := time.Now()
	event := &HeartbeatEvent{
		Type:      EventTypeHeartbeat,
		Severity:  SeverityInfo,
		Source:    "test-source",
		Timestamp: timestamp,
	}

	tests := []struct {
		name    string
		filter  EventFilter
		matches bool
	}{
		{
			name:    "empty filter matches all",
			filter:  EventFilter{},
			matches: true,
		},
		{
			name:    "matching type",
			filter:  EventFilter{Type: EventTypeHeartbeat},
			matches: true,
		},
		{
			name:    "non-matching type",
			filter:  EventFilter{Type: EventTypeSystemEvent},
			matches: false,
		},
		{
			name:    "matching severity",
			filter:  EventFilter{Severity: SeverityInfo},
			matches: true,
		},
		{
			name:    "non-matching severity",
			filter:  EventFilter{Severity: SeverityError},
			matches: false,
		},
		{
			name:    "matching source",
			filter:  EventFilter{Source: "test-source"},
			matches: true,
		},
		{
			name:    "non-matching source",
			filter:  EventFilter{Source: "other-source"},
			matches: false,
		},
		{
			name:    "since filter (match)",
			filter:  EventFilter{Since: &[]time.Time{timestamp.Add(-1 * time.Hour)}[0]},
			matches: true,
		},
		{
			name:    "since filter (no match)",
			filter:  EventFilter{Since: &[]time.Time{timestamp.Add(1 * time.Hour)}[0]},
			matches: false,
		},
		{
			name:    "until filter (match)",
			filter:  EventFilter{Until: &[]time.Time{timestamp.Add(1 * time.Hour)}[0]},
			matches: true,
		},
		{
			name:    "until filter (no match)",
			filter:  EventFilter{Until: &[]time.Time{timestamp.Add(-1 * time.Hour)}[0]},
			matches: false,
		},
		{
			name: "multiple criteria (match)",
			filter: EventFilter{
				Type:     EventTypeHeartbeat,
				Severity: SeverityInfo,
				Source:   "test-source",
			},
			matches: true,
		},
		{
			name: "multiple criteria (no match)",
			filter: EventFilter{
				Type:     EventTypeHeartbeat,
				Severity: SeverityError,
				Source:   "test-source",
			},
			matches: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if event.MatchesFilter(tt.filter) != tt.matches {
				t.Errorf("MatchesFilter() = %v, expected %v", event.MatchesFilter(tt.filter), tt.matches)
			}
		})
	}
}

func TestMemoryEventStore_NewMemoryEventStore(t *testing.T) {
	// Test with specified max events
	store := NewMemoryEventStore(500)
	if store.maxEvents != 500 {
		t.Errorf("maxEvents = %d, expected 500", store.maxEvents)
	}

	// Test with zero (should default)
	store = NewMemoryEventStore(0)
	if store.maxEvents != 1000 {
		t.Errorf("maxEvents = %d, expected 1000 (default)", store.maxEvents)
	}

	// Test with negative (should default)
	store = NewMemoryEventStore(-10)
	if store.maxEvents != 1000 {
		t.Errorf("maxEvents = %d, expected 1000 (default)", store.maxEvents)
	}
}

func TestMemoryEventStore_Store(t *testing.T) {
	store := NewMemoryEventStore(3) // Small size for testing

	// Test storing nil event
	err := store.Store(nil)
	if err == nil {
		t.Error("Store(nil) should return error")
	}

	// Store some events
	event1 := NewHeartbeatEvent(EventTypeHeartbeat, SeverityInfo, "event 1", "source1")
	event2 := NewHeartbeatEvent(EventTypeHeartbeat, SeverityInfo, "event 2", "source2")
	event3 := NewHeartbeatEvent(EventTypeHeartbeat, SeverityInfo, "event 3", "source3")
	event4 := NewHeartbeatEvent(EventTypeHeartbeat, SeverityInfo, "event 4", "source4")

	err = store.Store(event1)
	if err != nil {
		t.Errorf("Store() failed: %v", err)
	}

	err = store.Store(event2)
	if err != nil {
		t.Errorf("Store() failed: %v", err)
	}

	err = store.Store(event3)
	if err != nil {
		t.Errorf("Store() failed: %v", err)
	}

	// This should trigger trimming
	err = store.Store(event4)
	if err != nil {
		t.Errorf("Store() failed: %v", err)
	}

	// Should only have 3 events (oldest should be removed)
	if len(store.events) != 3 {
		t.Errorf("len(events) = %d, expected 3", len(store.events))
	}

	// First event should be event2 (event1 was trimmed)
	if store.events[0].Message != "event 2" {
		t.Errorf("First event message = %s, expected 'event 2'", store.events[0].Message)
	}
}

func TestMemoryEventStore_Query(t *testing.T) {
	store := NewMemoryEventStore(100)

	// Store some test events
	event1 := NewHeartbeatEvent(EventTypeHeartbeat, SeverityInfo, "heartbeat", "source1")
	event2 := NewHeartbeatEvent(EventTypeStatusChange, SeverityWarning, "status change", "source1")
	event3 := NewHeartbeatEvent(EventTypeSystemEvent, SeverityError, "system error", "source2")

	store.Store(event1)
	store.Store(event2)
	store.Store(event3)

	// Query all events
	results, err := store.Query(EventFilter{})
	if err != nil {
		t.Errorf("Query() failed: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("Query all: len(results) = %d, expected 3", len(results))
	}

	// Query by type
	results, err = store.Query(EventFilter{Type: EventTypeHeartbeat})
	if err != nil {
		t.Errorf("Query() failed: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("Query by type: len(results) = %d, expected 1", len(results))
	}

	// Query by source
	results, err = store.Query(EventFilter{Source: "source1"})
	if err != nil {
		t.Errorf("Query() failed: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("Query by source: len(results) = %d, expected 2", len(results))
	}

	// Query with limit
	results, err = store.Query(EventFilter{MaxResults: 2})
	if err != nil {
		t.Errorf("Query() failed: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("Query with limit: len(results) = %d, expected 2", len(results))
	}
}

func TestMemoryEventStore_Count(t *testing.T) {
	store := NewMemoryEventStore(100)

	// Initial count should be 0
	count, err := store.Count(EventFilter{})
	if err != nil {
		t.Errorf("Count() failed: %v", err)
	}
	if count != 0 {
		t.Errorf("Initial count = %d, expected 0", count)
	}

	// Store some events
	event1 := NewHeartbeatEvent(EventTypeHeartbeat, SeverityInfo, "heartbeat", "source1")
	event2 := NewHeartbeatEvent(EventTypeStatusChange, SeverityWarning, "status change", "source1")

	store.Store(event1)
	store.Store(event2)

	// Count all
	count, err = store.Count(EventFilter{})
	if err != nil {
		t.Errorf("Count() failed: %v", err)
	}
	if count != 2 {
		t.Errorf("Count all = %d, expected 2", count)
	}

	// Count by type
	count, err = store.Count(EventFilter{Type: EventTypeHeartbeat})
	if err != nil {
		t.Errorf("Count() failed: %v", err)
	}
	if count != 1 {
		t.Errorf("Count by type = %d, expected 1", count)
	}
}

func TestMemoryEventStore_Clear(t *testing.T) {
	store := NewMemoryEventStore(100)

	// Store some events
	event := NewHeartbeatEvent(EventTypeHeartbeat, SeverityInfo, "test", "test")
	store.Store(event)

	// Clear
	err := store.Clear()
	if err != nil {
		t.Errorf("Clear() failed: %v", err)
	}

	// Verify empty
	count, err := store.Count(EventFilter{})
	if err != nil {
		t.Errorf("Count() failed: %v", err)
	}
	if count != 0 {
		t.Errorf("Count after clear = %d, expected 0", count)
	}
}

func TestGenerateEventID(t *testing.T) {
	id1 := generateEventID()
	id2 := generateEventID()

	if id1 == "" {
		t.Error("generateEventID() should not return empty string")
	}

	if id1 == id2 {
		t.Error("generateEventID() should return unique IDs")
	}

	// Should start with expected prefix
	if len(id1) < 4 || id1[:4] != "evt_" {
		t.Errorf("generateEventID() should start with 'evt_', got %s", id1)
	}
}
