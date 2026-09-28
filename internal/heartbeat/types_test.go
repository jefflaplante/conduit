package heartbeat

import (
	"encoding/json"
	"testing"
)

func TestTaskTypeValidation(t *testing.T) {
	tests := []struct {
		name     string
		taskType TaskType
		isValid  bool
	}{
		{"valid alerts", TaskTypeAlerts, true},
		{"valid checks", TaskTypeChecks, true},
		{"valid reports", TaskTypeReports, true},
		{"valid maintenance", TaskTypeMaintenance, true},
		{"invalid type", TaskType("invalid"), false},
		{"empty type", TaskType(""), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.taskType.IsValid() != tt.isValid {
				t.Errorf("expected IsValid() = %v, got %v", tt.isValid, tt.taskType.IsValid())
			}
		})
	}
}

func TestTaskTypeJSONSerialization(t *testing.T) {
	tests := []struct {
		name     string
		taskType TaskType
	}{
		{"alerts", TaskTypeAlerts},
		{"checks", TaskTypeChecks},
		{"reports", TaskTypeReports},
		{"maintenance", TaskTypeMaintenance},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Marshal
			data, err := json.Marshal(tt.taskType)
			if err != nil {
				t.Fatalf("failed to marshal TaskType: %v", err)
			}

			// Unmarshal
			var unmarshaled TaskType
			if err := json.Unmarshal(data, &unmarshaled); err != nil {
				t.Fatalf("failed to unmarshal TaskType: %v", err)
			}

			if unmarshaled != tt.taskType {
				t.Errorf("expected %s, got %s", tt.taskType, unmarshaled)
			}
		})
	}

	// Test invalid JSON
	invalidJSON := `"invalid_type"`
	var taskType TaskType
	if err := json.Unmarshal([]byte(invalidJSON), &taskType); err == nil {
		t.Error("expected error when unmarshaling invalid task type")
	}
}

func TestTaskPriorityValidation(t *testing.T) {
	tests := []struct {
		name     string
		priority TaskPriority
		isValid  bool
		expected string
	}{
		{"low priority", TaskPriorityLow, true, "low"},
		{"normal priority", TaskPriorityNormal, true, "normal"},
		{"high priority", TaskPriorityHigh, true, "high"},
		{"critical priority", TaskPriorityCritical, true, "critical"},
		{"invalid priority", TaskPriority(99), false, "unknown(99)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.priority.IsValid() != tt.isValid {
				t.Errorf("expected IsValid() = %v, got %v", tt.isValid, tt.priority.IsValid())
			}

			if tt.priority.String() != tt.expected {
				t.Errorf("expected String() = %s, got %s", tt.expected, tt.priority.String())
			}
		})
	}
}

func TestTaskPriorityJSONSerialization(t *testing.T) {
	tests := []struct {
		name     string
		priority TaskPriority
		expected string
	}{
		{"low", TaskPriorityLow, "low"},
		{"normal", TaskPriorityNormal, "normal"},
		{"high", TaskPriorityHigh, "high"},
		{"critical", TaskPriorityCritical, "critical"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Marshal
			data, err := json.Marshal(tt.priority)
			if err != nil {
				t.Fatalf("failed to marshal TaskPriority: %v", err)
			}

			expected := `"` + tt.expected + `"`
			if string(data) != expected {
				t.Errorf("expected JSON %s, got %s", expected, string(data))
			}

			// Unmarshal
			var unmarshaled TaskPriority
			if err := json.Unmarshal(data, &unmarshaled); err != nil {
				t.Fatalf("failed to unmarshal TaskPriority: %v", err)
			}

			if unmarshaled != tt.priority {
				t.Errorf("expected %s, got %s", tt.priority, unmarshaled)
			}
		})
	}
}
