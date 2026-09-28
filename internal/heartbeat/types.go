package heartbeat

import (
	"encoding/json"
	"fmt"
	"strings"
)

// TaskType represents the type of heartbeat task
type TaskType string

const (
	TaskTypeAlerts      TaskType = "alerts"
	TaskTypeChecks      TaskType = "checks"
	TaskTypeReports     TaskType = "reports"
	TaskTypeMaintenance TaskType = "maintenance"
)

// String returns the string representation of TaskType
func (t TaskType) String() string {
	return string(t)
}

// IsValid checks if the TaskType is valid
func (t TaskType) IsValid() bool {
	switch t {
	case TaskTypeAlerts, TaskTypeChecks, TaskTypeReports, TaskTypeMaintenance:
		return true
	default:
		return false
	}
}

// MarshalJSON implements json.Marshaler
func (t TaskType) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(t))
}

// UnmarshalJSON implements json.Unmarshaler
func (t *TaskType) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}

	taskType := TaskType(s)
	if !taskType.IsValid() {
		return fmt.Errorf("invalid task type: %s", s)
	}

	*t = taskType
	return nil
}

// TaskPriority represents the priority of a heartbeat task
type TaskPriority int

const (
	TaskPriorityLow      TaskPriority = 1
	TaskPriorityNormal   TaskPriority = 5
	TaskPriorityHigh     TaskPriority = 10
	TaskPriorityCritical TaskPriority = 15
)

// String returns the string representation of TaskPriority
func (p TaskPriority) String() string {
	switch p {
	case TaskPriorityLow:
		return "low"
	case TaskPriorityNormal:
		return "normal"
	case TaskPriorityHigh:
		return "high"
	case TaskPriorityCritical:
		return "critical"
	default:
		return fmt.Sprintf("unknown(%d)", int(p))
	}
}

// IsValid checks if the TaskPriority is valid
func (p TaskPriority) IsValid() bool {
	switch p {
	case TaskPriorityLow, TaskPriorityNormal, TaskPriorityHigh, TaskPriorityCritical:
		return true
	default:
		return false
	}
}

// MarshalJSON implements json.Marshaler
func (p TaskPriority) MarshalJSON() ([]byte, error) {
	return json.Marshal(p.String())
}

// UnmarshalJSON implements json.Unmarshaler
func (p *TaskPriority) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}

	switch strings.ToLower(s) {
	case "low":
		*p = TaskPriorityLow
	case "normal":
		*p = TaskPriorityNormal
	case "high":
		*p = TaskPriorityHigh
	case "critical":
		*p = TaskPriorityCritical
	default:
		return fmt.Errorf("invalid task priority: %s", s)
	}

	return nil
}
