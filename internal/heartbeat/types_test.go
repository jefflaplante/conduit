package heartbeat

import (
	"encoding/json"
	"testing"
	"time"
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

func TestTaskStatusValidation(t *testing.T) {
	tests := []struct {
		name       string
		taskStatus TaskStatus
		isValid    bool
	}{
		{"valid pending", TaskStatusPending, true},
		{"valid running", TaskStatusRunning, true},
		{"valid completed", TaskStatusCompleted, true},
		{"valid failed", TaskStatusFailed, true},
		{"valid skipped", TaskStatusSkipped, true},
		{"invalid status", TaskStatus("invalid"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.taskStatus.IsValid() != tt.isValid {
				t.Errorf("expected IsValid() = %v, got %v", tt.isValid, tt.taskStatus.IsValid())
			}
		})
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

func TestHeartbeatTaskValidation(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour)

	tests := []struct {
		name    string
		task    HeartbeatTask
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid task",
			task: HeartbeatTask{
				ID:          "test-task-1",
				Type:        TaskTypeAlerts,
				Name:        "Test Task",
				CreatedAt:   now,
				ScheduledAt: future,
				Status:      TaskStatusPending,
				Priority:    TaskPriorityNormal,
				RetryCount:  0,
				MaxRetries:  3,
			},
			wantErr: false,
		},
		{
			name: "empty ID",
			task: HeartbeatTask{
				Type:        TaskTypeAlerts,
				Name:        "Test Task",
				CreatedAt:   now,
				ScheduledAt: future,
				Status:      TaskStatusPending,
				Priority:    TaskPriorityNormal,
			},
			wantErr: true,
			errMsg:  "task ID cannot be empty",
		},
		{
			name: "invalid type",
			task: HeartbeatTask{
				ID:          "test-task-1",
				Type:        TaskType("invalid"),
				Name:        "Test Task",
				CreatedAt:   now,
				ScheduledAt: future,
				Status:      TaskStatusPending,
				Priority:    TaskPriorityNormal,
			},
			wantErr: true,
			errMsg:  "invalid task type",
		},
		{
			name: "empty name",
			task: HeartbeatTask{
				ID:          "test-task-1",
				Type:        TaskTypeAlerts,
				CreatedAt:   now,
				ScheduledAt: future,
				Status:      TaskStatusPending,
				Priority:    TaskPriorityNormal,
			},
			wantErr: true,
			errMsg:  "task name cannot be empty",
		},
		{
			name: "negative retry count",
			task: HeartbeatTask{
				ID:          "test-task-1",
				Type:        TaskTypeAlerts,
				Name:        "Test Task",
				CreatedAt:   now,
				ScheduledAt: future,
				Status:      TaskStatusPending,
				Priority:    TaskPriorityNormal,
				RetryCount:  -1,
				MaxRetries:  3,
			},
			wantErr: true,
			errMsg:  "retry count cannot be negative",
		},
		{
			name: "retry count exceeds max",
			task: HeartbeatTask{
				ID:          "test-task-1",
				Type:        TaskTypeAlerts,
				Name:        "Test Task",
				CreatedAt:   now,
				ScheduledAt: future,
				Status:      TaskStatusPending,
				Priority:    TaskPriorityNormal,
				RetryCount:  5,
				MaxRetries:  3,
			},
			wantErr: true,
			errMsg:  "retry count (5) cannot exceed max retries (3)",
		},
		{
			name: "deadline before scheduled time",
			task: HeartbeatTask{
				ID:          "test-task-1",
				Type:        TaskTypeAlerts,
				Name:        "Test Task",
				CreatedAt:   now,
				ScheduledAt: future,
				Deadline:    &now, // Before scheduled time
				Status:      TaskStatusPending,
				Priority:    TaskPriorityNormal,
			},
			wantErr: true,
			errMsg:  "deadline cannot be before scheduled time",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.task.Validate()

			if tt.wantErr {
				if err == nil {
					t.Error("expected validation error, got nil")
					return
				}
				if tt.errMsg != "" && !containsString(err.Error(), tt.errMsg) {
					t.Errorf("expected error containing %q, got %q", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected validation error: %v", err)
				}
			}
		})
	}
}

func TestHeartbeatTaskBehaviors(t *testing.T) {
	now := time.Now()
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)
	deadline := now.Add(2 * time.Hour)

	t.Run("IsReady", func(t *testing.T) {
		tests := []struct {
			name     string
			task     HeartbeatTask
			expected bool
		}{
			{
				name: "ready task",
				task: HeartbeatTask{
					Status:      TaskStatusPending,
					ScheduledAt: past, // In the past, ready to run
				},
				expected: true,
			},
			{
				name: "not ready - wrong status",
				task: HeartbeatTask{
					Status:      TaskStatusRunning,
					ScheduledAt: past,
				},
				expected: false,
			},
			{
				name: "not ready - future scheduled time",
				task: HeartbeatTask{
					Status:      TaskStatusPending,
					ScheduledAt: future,
				},
				expected: false,
			},
			{
				name: "not ready - past deadline",
				task: HeartbeatTask{
					Status:      TaskStatusPending,
					ScheduledAt: past,
					Deadline:    &past, // Past deadline
				},
				expected: false,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if tt.task.IsReady() != tt.expected {
					t.Errorf("expected IsReady() = %v, got %v", tt.expected, tt.task.IsReady())
				}
			})
		}
	})

	t.Run("IsOverdue", func(t *testing.T) {
		tests := []struct {
			name     string
			task     HeartbeatTask
			expected bool
		}{
			{
				name: "overdue task",
				task: HeartbeatTask{
					Deadline: &past,
				},
				expected: true,
			},
			{
				name: "not overdue",
				task: HeartbeatTask{
					Deadline: &deadline,
				},
				expected: false,
			},
			{
				name:     "no deadline",
				task:     HeartbeatTask{},
				expected: false,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if tt.task.IsOverdue() != tt.expected {
					t.Errorf("expected IsOverdue() = %v, got %v", tt.expected, tt.task.IsOverdue())
				}
			})
		}
	})

	t.Run("CanRetry", func(t *testing.T) {
		tests := []struct {
			name     string
			task     HeartbeatTask
			expected bool
		}{
			{
				name: "can retry",
				task: HeartbeatTask{
					Status:     TaskStatusFailed,
					RetryCount: 1,
					MaxRetries: 3,
				},
				expected: true,
			},
			{
				name: "cannot retry - wrong status",
				task: HeartbeatTask{
					Status:     TaskStatusCompleted,
					RetryCount: 1,
					MaxRetries: 3,
				},
				expected: false,
			},
			{
				name: "cannot retry - max retries reached",
				task: HeartbeatTask{
					Status:     TaskStatusFailed,
					RetryCount: 3,
					MaxRetries: 3,
				},
				expected: false,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if tt.task.CanRetry() != tt.expected {
					t.Errorf("expected CanRetry() = %v, got %v", tt.expected, tt.task.CanRetry())
				}
			})
		}
	})

	t.Run("NextScheduledTime", func(t *testing.T) {
		interval := time.Hour
		task := HeartbeatTask{
			ScheduledAt: past,
			CompletedAt: &now,
			Interval:    &interval,
		}

		nextTime := task.NextScheduledTime()
		if nextTime == nil {
			t.Fatal("expected next scheduled time, got nil")
		}

		expected := now.Add(interval)
		if !nextTime.Equal(expected) {
			t.Errorf("expected next scheduled time %v, got %v", expected, *nextTime)
		}

		// Test non-recurring task
		taskNoInterval := HeartbeatTask{}
		if taskNoInterval.NextScheduledTime() != nil {
			t.Error("expected nil for non-recurring task")
		}
	})
}

func TestTaskQueue(t *testing.T) {
	now := time.Now()

	task1 := HeartbeatTask{
		ID:          "task-1",
		Type:        TaskTypeAlerts,
		Name:        "Task 1",
		CreatedAt:   now,
		ScheduledAt: now.Add(-time.Hour), // Ready
		Status:      TaskStatusPending,
		Priority:    TaskPriorityNormal,
	}

	task2 := HeartbeatTask{
		ID:          "task-2",
		Type:        TaskTypeChecks,
		Name:        "Task 2",
		CreatedAt:   now,
		ScheduledAt: now.Add(time.Hour), // Not ready yet
		Status:      TaskStatusPending,
		Priority:    TaskPriorityHigh,
	}

	task3 := HeartbeatTask{
		ID:          "task-3",
		Type:        TaskTypeReports,
		Name:        "Task 3",
		CreatedAt:   now,
		ScheduledAt: now,
		Status:      TaskStatusCompleted,
		Priority:    TaskPriorityLow,
	}

	t.Run("AddTask", func(t *testing.T) {
		queue := &TaskQueue{}

		// Add valid task
		if err := queue.AddTask(task1); err != nil {
			t.Errorf("unexpected error adding task: %v", err)
		}

		if len(queue.Tasks) != 1 {
			t.Errorf("expected 1 task in queue, got %d", len(queue.Tasks))
		}

		// Add duplicate ID
		if err := queue.AddTask(task1); err == nil {
			t.Error("expected error adding duplicate task")
		}
	})

	t.Run("GetReadyTasks", func(t *testing.T) {
		queue := TaskQueue{
			Tasks: []HeartbeatTask{task1, task2, task3},
		}

		ready := queue.GetReadyTasks()
		if len(ready) != 1 {
			t.Errorf("expected 1 ready task, got %d", len(ready))
		}

		if ready[0].ID != "task-1" {
			t.Errorf("expected ready task ID 'task-1', got %s", ready[0].ID)
		}
	})

	t.Run("GetTaskByID", func(t *testing.T) {
		queue := TaskQueue{
			Tasks: []HeartbeatTask{task1, task2},
		}

		// Existing task
		if task, found := queue.GetTaskByID("task-1"); !found {
			t.Error("expected to find task-1")
		} else if task.ID != "task-1" {
			t.Errorf("expected task ID 'task-1', got %s", task.ID)
		}

		// Non-existing task
		if _, found := queue.GetTaskByID("non-existent"); found {
			t.Error("expected not to find non-existent task")
		}
	})

	t.Run("UpdateTaskStatus", func(t *testing.T) {
		queue := TaskQueue{
			Tasks:   []HeartbeatTask{task1},
			Version: 1,
		}

		// Update existing task
		if err := queue.UpdateTaskStatus("task-1", TaskStatusRunning); err != nil {
			t.Errorf("unexpected error updating task status: %v", err)
		}

		if queue.Tasks[0].Status != TaskStatusRunning {
			t.Errorf("expected status Running, got %s", queue.Tasks[0].Status)
		}

		if queue.Tasks[0].StartedAt == nil {
			t.Error("expected StartedAt to be set")
		}

		if queue.Version != 2 {
			t.Errorf("expected version 2, got %d", queue.Version)
		}

		// Update non-existing task
		if err := queue.UpdateTaskStatus("non-existent", TaskStatusCompleted); err == nil {
			t.Error("expected error updating non-existent task")
		}
	})

	t.Run("RemoveCompletedTasks", func(t *testing.T) {
		queue := TaskQueue{
			Tasks: []HeartbeatTask{task1, task2, task3},
		}

		queue.RemoveCompletedTasks()

		// Should keep pending tasks
		if len(queue.Tasks) != 2 {
			t.Errorf("expected 2 tasks after cleanup, got %d", len(queue.Tasks))
		}

		// Check that completed task was removed
		for _, task := range queue.Tasks {
			if task.ID == "task-3" {
				t.Error("expected completed task to be removed")
			}
		}
	})
}

func TestHeartbeatTaskJSONSerialization(t *testing.T) {
	now := time.Now()
	deadline := now.Add(time.Hour)
	interval := 30 * time.Minute
	timeout := 10 * time.Minute

	task := HeartbeatTask{
		ID:              "test-task",
		Type:            TaskTypeAlerts,
		Name:            "Test Task",
		Description:     "Test Description",
		CreatedAt:       now,
		ScheduledAt:     now,
		Deadline:        &deadline,
		Interval:        &interval,
		Status:          TaskStatusPending,
		Priority:        TaskPriorityHigh,
		RetryCount:      1,
		MaxRetries:      3,
		LastError:       "test error",
		Payload:         map[string]interface{}{"key": "value"},
		Tags:            []string{"tag1", "tag2"},
		DependsOn:       []string{"dep1", "dep2"},
		TimeoutDuration: &timeout,
		SkipQuietHours:  true,
	}

	// Marshal
	data, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("failed to marshal task: %v", err)
	}

	// Unmarshal
	var unmarshaled HeartbeatTask
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("failed to unmarshal task: %v", err)
	}

	// Validate round trip
	if err := unmarshaled.Validate(); err != nil {
		t.Errorf("unmarshaled task validation failed: %v", err)
	}

	// Check critical fields
	if unmarshaled.ID != task.ID {
		t.Errorf("ID mismatch: expected %s, got %s", task.ID, unmarshaled.ID)
	}

	if unmarshaled.Type != task.Type {
		t.Errorf("Type mismatch: expected %s, got %s", task.Type, unmarshaled.Type)
	}

	if unmarshaled.Priority != task.Priority {
		t.Errorf("Priority mismatch: expected %s, got %s", task.Priority, unmarshaled.Priority)
	}
}
