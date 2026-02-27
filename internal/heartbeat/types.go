package heartbeat

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
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

// TaskStatus represents the execution status of a heartbeat task
type TaskStatus string

const (
	TaskStatusPending   TaskStatus = "pending"
	TaskStatusRunning   TaskStatus = "running"
	TaskStatusCompleted TaskStatus = "completed"
	TaskStatusFailed    TaskStatus = "failed"
	TaskStatusSkipped   TaskStatus = "skipped"
)

// String returns the string representation of TaskStatus
func (s TaskStatus) String() string {
	return string(s)
}

// IsValid checks if the TaskStatus is valid
func (s TaskStatus) IsValid() bool {
	switch s {
	case TaskStatusPending, TaskStatusRunning, TaskStatusCompleted, TaskStatusFailed, TaskStatusSkipped:
		return true
	default:
		return false
	}
}

// MarshalJSON implements json.Marshaler
func (s TaskStatus) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(s))
}

// UnmarshalJSON implements json.Unmarshaler
func (s *TaskStatus) UnmarshalJSON(data []byte) error {
	var str string
	if err := json.Unmarshal(data, &str); err != nil {
		return err
	}

	status := TaskStatus(str)
	if !status.IsValid() {
		return fmt.Errorf("invalid task status: %s", str)
	}

	*s = status
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

// HeartbeatTask represents a single task to be processed during heartbeat
type HeartbeatTask struct {
	// Identification
	ID          string   `json:"id"`
	Type        TaskType `json:"type"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`

	// Scheduling
	CreatedAt   time.Time      `json:"created_at"`
	ScheduledAt time.Time      `json:"scheduled_at"`
	Deadline    *time.Time     `json:"deadline,omitempty"`
	Interval    *time.Duration `json:"interval,omitempty"` // For recurring tasks

	// Execution
	Status     TaskStatus   `json:"status"`
	Priority   TaskPriority `json:"priority"`
	RetryCount int          `json:"retry_count"`
	MaxRetries int          `json:"max_retries"`
	LastError  string       `json:"last_error,omitempty"`

	// Timing
	StartedAt   *time.Time     `json:"started_at,omitempty"`
	CompletedAt *time.Time     `json:"completed_at,omitempty"`
	Duration    *time.Duration `json:"duration,omitempty"`

	// Task-specific data
	Payload map[string]interface{} `json:"payload,omitempty"`
	Tags    []string               `json:"tags,omitempty"`

	// Dependencies and relationships
	DependsOn  []string `json:"depends_on,omitempty"`
	Dependents []string `json:"dependents,omitempty"`

	// Configuration
	TimeoutDuration *time.Duration `json:"timeout_duration,omitempty"`
	SkipQuietHours  bool           `json:"skip_quiet_hours,omitempty"`
}

// Validate validates the heartbeat task
func (h HeartbeatTask) Validate() error {
	if h.ID == "" {
		return fmt.Errorf("task ID cannot be empty")
	}

	if !h.Type.IsValid() {
		return fmt.Errorf("invalid task type: %s", h.Type)
	}

	if h.Name == "" {
		return fmt.Errorf("task name cannot be empty")
	}

	if !h.Status.IsValid() {
		return fmt.Errorf("invalid task status: %s", h.Status)
	}

	if !h.Priority.IsValid() {
		return fmt.Errorf("invalid task priority: %d", h.Priority)
	}

	if h.RetryCount < 0 {
		return fmt.Errorf("retry count cannot be negative (got %d)", h.RetryCount)
	}

	if h.MaxRetries < 0 {
		return fmt.Errorf("max retries cannot be negative (got %d)", h.MaxRetries)
	}

	if h.RetryCount > h.MaxRetries {
		return fmt.Errorf("retry count (%d) cannot exceed max retries (%d)", h.RetryCount, h.MaxRetries)
	}

	if h.CreatedAt.IsZero() {
		return fmt.Errorf("created_at cannot be zero")
	}

	if h.ScheduledAt.IsZero() {
		return fmt.Errorf("scheduled_at cannot be zero")
	}

	// Validate deadline is after scheduled time
	if h.Deadline != nil && h.Deadline.Before(h.ScheduledAt) {
		return fmt.Errorf("deadline cannot be before scheduled time")
	}

	// Validate timing consistency
	if h.StartedAt != nil && h.StartedAt.Before(h.CreatedAt) {
		return fmt.Errorf("started_at cannot be before created_at")
	}

	if h.CompletedAt != nil && h.StartedAt != nil && h.CompletedAt.Before(*h.StartedAt) {
		return fmt.Errorf("completed_at cannot be before started_at")
	}

	return nil
}

// IsReady checks if the task is ready to be executed
func (h HeartbeatTask) IsReady() bool {
	// Task must be pending
	if h.Status != TaskStatusPending {
		return false
	}

	// Check if it's time to execute
	if time.Now().Before(h.ScheduledAt) {
		return false
	}

	// Check deadline
	if h.Deadline != nil && time.Now().After(*h.Deadline) {
		return false // Past deadline
	}

	return true
}

// IsOverdue checks if the task is past its deadline
func (h HeartbeatTask) IsOverdue() bool {
	if h.Deadline == nil {
		return false
	}

	return time.Now().After(*h.Deadline)
}

// CanRetry checks if the task can be retried
func (h HeartbeatTask) CanRetry() bool {
	return h.Status == TaskStatusFailed && h.RetryCount < h.MaxRetries
}

// HasTimedOut checks if the task has exceeded its timeout duration
func (h HeartbeatTask) HasTimedOut() bool {
	if h.TimeoutDuration == nil || h.StartedAt == nil {
		return false
	}

	return time.Since(*h.StartedAt) > *h.TimeoutDuration
}

// ElapsedTime returns the time elapsed since the task started
func (h HeartbeatTask) ElapsedTime() *time.Duration {
	if h.StartedAt == nil {
		return nil
	}

	var endTime time.Time
	if h.CompletedAt != nil {
		endTime = *h.CompletedAt
	} else {
		endTime = time.Now()
	}

	duration := endTime.Sub(*h.StartedAt)
	return &duration
}

// NextScheduledTime calculates the next scheduled time for recurring tasks
func (h HeartbeatTask) NextScheduledTime() *time.Time {
	if h.Interval == nil {
		return nil // Not a recurring task
	}

	var baseTime time.Time
	if h.CompletedAt != nil {
		baseTime = *h.CompletedAt
	} else {
		baseTime = h.ScheduledAt
	}

	nextTime := baseTime.Add(*h.Interval)
	return &nextTime
}

// TaskQueue represents a collection of heartbeat tasks
type TaskQueue struct {
	Tasks    []HeartbeatTask `json:"tasks"`
	LastSync time.Time       `json:"last_sync"`
	Version  int             `json:"version"`
}

// Validate validates the task queue
func (q TaskQueue) Validate() error {
	taskIDs := make(map[string]bool)

	for i, task := range q.Tasks {
		if err := task.Validate(); err != nil {
			return fmt.Errorf("task %d (%s) validation failed: %w", i, task.ID, err)
		}

		// Check for duplicate IDs
		if taskIDs[task.ID] {
			return fmt.Errorf("duplicate task ID: %s", task.ID)
		}
		taskIDs[task.ID] = true
	}

	return nil
}

// GetReadyTasks returns tasks that are ready for execution
func (q TaskQueue) GetReadyTasks() []HeartbeatTask {
	var ready []HeartbeatTask

	for _, task := range q.Tasks {
		if task.IsReady() {
			ready = append(ready, task)
		}
	}

	return ready
}

// GetTaskByID finds a task by its ID
func (q TaskQueue) GetTaskByID(id string) (*HeartbeatTask, bool) {
	for i, task := range q.Tasks {
		if task.ID == id {
			return &q.Tasks[i], true
		}
	}

	return nil, false
}

// RemoveCompletedTasks removes tasks that are completed or failed beyond retry limit
func (q *TaskQueue) RemoveCompletedTasks() {
	var activeTasks []HeartbeatTask

	for _, task := range q.Tasks {
		// Keep task if it's not in a final state
		if task.Status != TaskStatusCompleted &&
			(task.Status != TaskStatusFailed || task.CanRetry()) {
			activeTasks = append(activeTasks, task)
		}
	}

	q.Tasks = activeTasks
}

// AddTask adds a new task to the queue
func (q *TaskQueue) AddTask(task HeartbeatTask) error {
	if err := task.Validate(); err != nil {
		return fmt.Errorf("invalid task: %w", err)
	}

	// Check for duplicate ID
	if _, exists := q.GetTaskByID(task.ID); exists {
		return fmt.Errorf("task with ID %s already exists", task.ID)
	}

	q.Tasks = append(q.Tasks, task)
	q.Version++

	return nil
}

// UpdateTaskStatus updates the status of a task
func (q *TaskQueue) UpdateTaskStatus(taskID string, status TaskStatus) error {
	for i, task := range q.Tasks {
		if task.ID == taskID {
			q.Tasks[i].Status = status

			now := time.Now()
			switch status {
			case TaskStatusRunning:
				q.Tasks[i].StartedAt = &now
			case TaskStatusCompleted, TaskStatusFailed:
				q.Tasks[i].CompletedAt = &now
				if q.Tasks[i].StartedAt != nil {
					duration := now.Sub(*q.Tasks[i].StartedAt)
					q.Tasks[i].Duration = &duration
				}
			}

			q.Version++
			return nil
		}
	}

	return fmt.Errorf("task not found: %s", taskID)
}
