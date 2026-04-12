package heartbeat

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"conduit/internal/scheduler"
)

// mockBrainWriter implements BrainWriter for testing
type mockBrainWriter struct {
	mu      sync.Mutex
	stored  map[string]string // key -> value
	deleted []string          // keys that were deleted
	listErr error             // if set, ListAlertKeys returns this error
}

func newMockBrainWriter() *mockBrainWriter {
	return &mockBrainWriter{
		stored: make(map[string]string),
	}
}

func (m *mockBrainWriter) StoreAlert(ctx context.Context, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stored[key] = value
	return nil
}

func (m *mockBrainWriter) DeleteAlert(ctx context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.stored, key)
	m.deleted = append(m.deleted, key)
	return nil
}

func (m *mockBrainWriter) ListAlertKeys(ctx context.Context, prefix string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.listErr != nil {
		return nil, m.listErr
	}
	var keys []string
	for k := range m.stored {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (m *mockBrainWriter) getStored() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make(map[string]string, len(m.stored))
	for k, v := range m.stored {
		cp[k] = v
	}
	return cp
}

func (m *mockBrainWriter) getDeleted() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]string, len(m.deleted))
	copy(cp, m.deleted)
	return cp
}

func TestSanitizeKeyComponent(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Heartbeat Task Execution", "heartbeat_task_execution"},
		{"Check shared alert queue", "check_shared_alert_queue"},
		{"agent_heartbeat_main", "agent_heartbeat_main"},
		{"CRITICAL: Database down!", "critical_database_down"},
		{"  spaces  around  ", "spaces_around"},
		{"MiXeD CaSe", "mixed_case"},
		{"dots.and-dashes", "dots_and_dashes"},
		{"multiple   spaces", "multiple_spaces"},
		{"123numeric456", "123numeric456"},
		{"", ""},
		{"special!@#$%chars", "special_chars"},
		{"already_clean", "already_clean"},
		{"trailing_special!!!", "trailing_special"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := SanitizeKeyComponent(tt.input)
			if result != tt.expected {
				t.Errorf("SanitizeKeyComponent(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestSyncAlertsToBrain_NilBrainWriter(t *testing.T) {
	// When BrainWriter is nil, syncAlertsToBrain should be a no-op
	g := &GatewayIntegration{
		brainWriter: nil,
	}

	result := &HeartbeatResult{
		Status:  ResultStatusAlert,
		Actions: []HeartbeatAction{{Type: ActionTypeAlert, Content: "test"}},
	}
	job := &scheduler.Job{ID: "test_job", Name: "Test Job"}

	// Should not panic
	g.syncAlertsToBrain(context.Background(), result, job)
}

func TestSyncAlertsToBrain_OKClearsAlerts(t *testing.T) {
	bw := newMockBrainWriter()
	g := &GatewayIntegration{brainWriter: bw}

	// Pre-populate some alerts
	ctx := context.Background()
	bw.StoreAlert(ctx, "sense.alerts.test_job.0_high_cpu", "severity=high")
	bw.StoreAlert(ctx, "sense.alerts.test_job.1_disk_full", "severity=critical")
	// Also add an unrelated alert that should NOT be deleted
	bw.StoreAlert(ctx, "sense.alerts.other_job.0_memory", "severity=warning")

	result := &HeartbeatResult{Status: ResultStatusOK}
	job := &scheduler.Job{ID: "irrelevant", Name: "Test Job"}

	g.syncAlertsToBrain(ctx, result, job)

	stored := bw.getStored()
	// The test_job alerts should be cleared
	if _, ok := stored["sense.alerts.test_job.0_high_cpu"]; ok {
		t.Error("Expected test_job alert 0 to be deleted")
	}
	if _, ok := stored["sense.alerts.test_job.1_disk_full"]; ok {
		t.Error("Expected test_job alert 1 to be deleted")
	}
	// The other_job alert should remain
	if _, ok := stored["sense.alerts.other_job.0_memory"]; !ok {
		t.Error("Expected other_job alert to remain")
	}
}

func TestSyncAlertsToBrain_NoActionClearsAlerts(t *testing.T) {
	bw := newMockBrainWriter()
	g := &GatewayIntegration{brainWriter: bw}

	ctx := context.Background()
	bw.StoreAlert(ctx, "sense.alerts.my_job.0_stale", "old alert")

	result := &HeartbeatResult{Status: ResultStatusNoAction}
	job := &scheduler.Job{ID: "irrelevant", Name: "My Job"}

	g.syncAlertsToBrain(ctx, result, job)

	stored := bw.getStored()
	if _, ok := stored["sense.alerts.my_job.0_stale"]; ok {
		t.Error("Expected alert to be cleared on NoAction status")
	}
}

func TestSyncAlertsToBrain_AlertWritesEntries(t *testing.T) {
	bw := newMockBrainWriter()
	g := &GatewayIntegration{brainWriter: bw}

	result := &HeartbeatResult{
		Status: ResultStatusAlert,
		Actions: []HeartbeatAction{
			{
				Type:     ActionTypeAlert,
				Content:  "CRITICAL: Database connection failed",
				Priority: TaskPriorityCritical,
				Target:   "telegram",
			},
			{
				Type:     ActionTypeNotification,
				Content:  "High CPU usage detected",
				Priority: TaskPriorityHigh,
				Target:   "telegram",
			},
			{
				// Command actions should NOT be written to Brain
				Type:     ActionTypeCommand,
				Content:  "cleanup something",
				Priority: TaskPriorityLow,
				Target:   "system",
			},
		},
	}
	job := &scheduler.Job{ID: "agent_heartbeat_main", Name: "Heartbeat Task Execution"}

	g.syncAlertsToBrain(context.Background(), result, job)

	stored := bw.getStored()

	// Should have exactly 2 entries (alert + notification, not command)
	if len(stored) != 2 {
		t.Errorf("Expected 2 stored entries, got %d: %v", len(stored), stored)
	}

	// Check keys follow the expected pattern
	foundAlert := false
	foundNotification := false
	for key, val := range stored {
		if contains(key, "sense.alerts.heartbeat_task_execution.0_") {
			foundAlert = true
			if !contains(val, "severity=critical") {
				t.Errorf("Expected critical severity in value, got: %s", val)
			}
			if !contains(val, "Database connection failed") {
				t.Errorf("Expected alert content in value, got: %s", val)
			}
		}
		if contains(key, "sense.alerts.heartbeat_task_execution.1_") {
			foundNotification = true
			if !contains(val, "severity=high") {
				t.Errorf("Expected high severity in value, got: %s", val)
			}
		}
	}

	if !foundAlert {
		t.Error("Expected to find alert entry in stored keys")
	}
	if !foundNotification {
		t.Error("Expected to find notification entry in stored keys")
	}
}

func TestSyncAlertsToBrain_ActionStatusWritesEntries(t *testing.T) {
	bw := newMockBrainWriter()
	g := &GatewayIntegration{brainWriter: bw}

	result := &HeartbeatResult{
		Status: ResultStatusAction,
		Actions: []HeartbeatAction{
			{
				Type:     ActionTypeNotification,
				Content:  "System report: disk space at 85%",
				Priority: TaskPriorityNormal,
				Target:   "telegram",
			},
		},
	}
	job := &scheduler.Job{ID: "hb_main", Name: "Heartbeat"}

	g.syncAlertsToBrain(context.Background(), result, job)

	stored := bw.getStored()
	if len(stored) != 1 {
		t.Errorf("Expected 1 stored entry, got %d", len(stored))
	}
	for _, val := range stored {
		if !contains(val, "severity=normal") {
			t.Errorf("Expected normal severity, got: %s", val)
		}
	}
}

func TestSyncAlertsToBrain_ErrorWritesEntry(t *testing.T) {
	bw := newMockBrainWriter()
	g := &GatewayIntegration{brainWriter: bw}

	result := &HeartbeatResult{
		Status:  ResultStatusError,
		Message: "AI provider returned 500 Internal Server Error",
	}
	job := &scheduler.Job{ID: "agent_heartbeat_main", Name: "Heartbeat Task Execution"}

	g.syncAlertsToBrain(context.Background(), result, job)

	stored := bw.getStored()
	if len(stored) != 1 {
		t.Errorf("Expected 1 stored entry for error, got %d", len(stored))
	}

	for key, val := range stored {
		if !contains(key, "sense.alerts.heartbeat_task_execution.error") {
			t.Errorf("Expected error key, got: %s", key)
		}
		if !contains(val, "severity=critical") {
			t.Errorf("Expected critical severity for errors, got: %s", val)
		}
		if !contains(val, "500 Internal Server Error") {
			t.Errorf("Expected error message in value, got: %s", val)
		}
	}
}

func TestSyncAlertsToBrain_FallsBackToJobID(t *testing.T) {
	bw := newMockBrainWriter()
	g := &GatewayIntegration{brainWriter: bw}

	result := &HeartbeatResult{
		Status:  ResultStatusAlert,
		Actions: []HeartbeatAction{{Type: ActionTypeAlert, Content: "test alert", Priority: TaskPriorityHigh}},
	}
	// Job with empty Name — should use ID as fallback
	job := &scheduler.Job{ID: "custom_job_123", Name: ""}

	g.syncAlertsToBrain(context.Background(), result, job)

	stored := bw.getStored()
	for key := range stored {
		if !contains(key, "custom_job_123") {
			t.Errorf("Expected key to contain job ID, got: %s", key)
		}
	}
}

func TestSyncAlertsToBrain_ListErrorDoesNotPanic(t *testing.T) {
	bw := newMockBrainWriter()
	bw.listErr = fmt.Errorf("database locked")
	g := &GatewayIntegration{brainWriter: bw}

	result := &HeartbeatResult{Status: ResultStatusOK}
	job := &scheduler.Job{ID: "test", Name: "Test"}

	// Should not panic despite list error
	g.syncAlertsToBrain(context.Background(), result, job)
}

func TestSyncAlertsToBrain_LongContentTruncated(t *testing.T) {
	bw := newMockBrainWriter()
	g := &GatewayIntegration{brainWriter: bw}

	// Create a very long alert content
	longContent := ""
	for i := 0; i < 50; i++ {
		longContent += "very long alert content "
	}

	result := &HeartbeatResult{
		Status: ResultStatusAlert,
		Actions: []HeartbeatAction{
			{Type: ActionTypeAlert, Content: longContent, Priority: TaskPriorityHigh},
		},
	}
	job := &scheduler.Job{ID: "test", Name: "Test Job"}

	g.syncAlertsToBrain(context.Background(), result, job)

	stored := bw.getStored()
	for key, val := range stored {
		// Key should have truncated alert type (max 60 chars for the alert_type portion)
		if len(key) > 200 {
			t.Errorf("Key too long: %d chars", len(key))
		}
		// Value message should be truncated to ~200 chars
		if !contains(val, "...") {
			t.Errorf("Expected truncated value to contain '...', got length %d", len(val))
		}
	}
}

func TestTruncateString(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is longer than ten", 10, "this is lo..."},
		{"", 5, ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := truncateString(tt.input, tt.maxLen)
			if result != tt.expected {
				t.Errorf("truncateString(%q, %d) = %q, want %q", tt.input, tt.maxLen, result, tt.expected)
			}
		})
	}
}

func TestSetBrainWriter(t *testing.T) {
	g := &GatewayIntegration{}

	if g.brainWriter != nil {
		t.Error("Expected nil BrainWriter initially")
	}

	bw := newMockBrainWriter()
	g.SetBrainWriter(bw)

	if g.brainWriter == nil {
		t.Error("Expected BrainWriter to be set after SetBrainWriter")
	}

	// Can also set to nil to disable
	g.SetBrainWriter(nil)
	if g.brainWriter != nil {
		t.Error("Expected BrainWriter to be nil after SetBrainWriter(nil)")
	}
}
