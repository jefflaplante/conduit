package heartbeat

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTaskInterpreter_ReadHeartbeatTasks(t *testing.T) {
	// Create a temporary directory and HEARTBEAT.md file for testing
	tempDir, err := os.MkdirTemp("", "heartbeat_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create test HEARTBEAT.md content
	heartbeatContent := `# HEARTBEAT.md

## Check shared alert queue
Read ` + "`memory/alerts/pending.json`" + `. If it contains any alerts:
- **critical** severity: Deliver to Jeff immediately via Telegram
- **warning** severity: Deliver to Jeff if he's likely awake (8 AM - 10 PM PT)
- **info** severity: Skip — save for the next briefing

After delivering, clear the queue:
` + "```bash\n" + `python3 -c "import os; open(os.path.expanduser('~/conduit/alerts/pending.json'), 'w').write('[]')"
` + "```\n" + `
If no alerts (or only info-level), reply HEARTBEAT_OK.

## Check system status
Monitor critical systems and services:
- Database connectivity
- API endpoints health
- Disk space usage

Report any issues immediately.
`

	heartbeatPath := filepath.Join(tempDir, "HEARTBEAT.md")
	if err := os.WriteFile(heartbeatPath, []byte(heartbeatContent), 0644); err != nil {
		t.Fatalf("Failed to write HEARTBEAT.md: %v", err)
	}

	// Test the task interpreter
	interpreter := NewTaskInterpreter(tempDir)
	tasks, err := interpreter.ReadHeartbeatTasks()
	if err != nil {
		t.Fatalf("Failed to read heartbeat tasks: %v", err)
	}

	// Verify we got the expected number of tasks
	expectedTasks := 2
	if len(tasks) != expectedTasks {
		t.Errorf("Expected %d tasks, got %d", expectedTasks, len(tasks))
	}

	// Check first task (alert queue)
	if len(tasks) > 0 {
		alertTask := tasks[0]
		if alertTask.Title != "Check shared alert queue" {
			t.Errorf("Expected first task title 'Check shared alert queue', got '%s'", alertTask.Title)
		}

		if alertTask.Type != TaskTypeAlerts {
			t.Errorf("Expected first task type %s, got %s", TaskTypeAlerts, alertTask.Type)
		}

		if len(alertTask.Instructions) < 3 {
			t.Errorf("Expected at least 3 instructions in first task, got %d", len(alertTask.Instructions))
		}

		if len(alertTask.CodeBlocks) != 1 {
			t.Errorf("Expected 1 code block in first task, got %d", len(alertTask.CodeBlocks))
		}

		if alertTask.CodeBlocks[0].Language != "bash" {
			t.Errorf("Expected bash code block, got %s", alertTask.CodeBlocks[0].Language)
		}

		if !alertTask.HasConditional {
			t.Error("Expected first task to have conditional logic")
		}

		if !alertTask.IsQuietAware {
			t.Error("Expected first task to be quiet-aware")
		}
	}

	// Check second task (system status)
	if len(tasks) > 1 {
		statusTask := tasks[1]
		if statusTask.Title != "Check system status" {
			t.Errorf("Expected second task title 'Check system status', got '%s'", statusTask.Title)
		}

		if statusTask.Type != TaskTypeChecks {
			t.Errorf("Expected second task type %s, got %s", TaskTypeChecks, statusTask.Type)
		}

		if !statusTask.IsImmediate {
			t.Error("Expected second task to be immediate (due to 'immediately' keyword)")
		}
	}
}

func TestTaskInterpreter_GeneratePrompt(t *testing.T) {
	interpreter := NewTaskInterpreter("/tmp")

	// Create test tasks
	tasks := []ParsedHeartbeatTask{
		{
			Title:       "Test Alert Task",
			Description: "Check for test alerts",
			Instructions: []string{
				"Check alert queue",
				"Send critical alerts immediately",
				"Defer warning alerts during quiet hours",
			},
			CodeBlocks: []CodeBlock{
				{
					Language: "bash",
					Content:  "echo 'test command'",
				},
			},
			Type:     TaskTypeAlerts,
			Priority: TaskPriorityHigh,
		},
	}

	prompt, err := interpreter.GeneratePrompt(tasks)
	if err != nil {
		t.Fatalf("Failed to generate prompt: %v", err)
	}

	// Check that prompt contains expected elements
	expectedElements := []string{
		"Read HEARTBEAT.md",
		"HEARTBEAT_OK",
		"Test Alert Task",
		"Check alert queue",
		"```bash",
		"echo 'test command'",
	}

	for _, element := range expectedElements {
		if !contains(prompt, element) {
			t.Errorf("Expected prompt to contain '%s', but it didn't", element)
		}
	}
}

func TestTaskInterpreter_parseHeartbeatContent(t *testing.T) {
	interpreter := NewTaskInterpreter("/tmp")

	content := `# HEARTBEAT.md

## Critical Alert Check
This is a critical task that needs immediate attention.
- Check for critical alerts
- **critical** severity: Send immediately
- If nothing found, reply HEARTBEAT_OK

## Maintenance Task
Routine maintenance operations:
- Clean up old logs
- Update system status
- Run scheduled backups

` + "```python\n" + `
import os
print("maintenance complete")
` + "```\n"

	tasks, err := interpreter.parseHeartbeatContent(content)
	if err != nil {
		t.Fatalf("Failed to parse content: %v", err)
	}

	if len(tasks) != 2 {
		t.Fatalf("Expected 2 tasks, got %d", len(tasks))
	}

	// Test first task
	criticalTask := tasks[0]
	if criticalTask.Title != "Critical Alert Check" {
		t.Errorf("Expected title 'Critical Alert Check', got '%s'", criticalTask.Title)
	}

	if criticalTask.Priority != TaskPriorityCritical {
		t.Errorf("Expected priority %s, got %s", TaskPriorityCritical, criticalTask.Priority)
	}

	if !criticalTask.IsImmediate {
		t.Error("Expected critical task to be immediate")
	}

	if criticalTask.Type != TaskTypeAlerts {
		t.Errorf("Expected type %s, got %s", TaskTypeAlerts, criticalTask.Type)
	}

	// Test second task
	maintenanceTask := tasks[1]
	if maintenanceTask.Title != "Maintenance Task" {
		t.Errorf("Expected title 'Maintenance Task', got '%s'", maintenanceTask.Title)
	}

	if len(maintenanceTask.CodeBlocks) != 1 {
		t.Errorf("Expected 1 code block, got %d", len(maintenanceTask.CodeBlocks))
	}

	if maintenanceTask.CodeBlocks[0].Language != "python" {
		t.Errorf("Expected python code block, got %s", maintenanceTask.CodeBlocks[0].Language)
	}
}

func TestTaskInterpreter_inferTaskType(t *testing.T) {
	interpreter := NewTaskInterpreter("/tmp")

	tests := []struct {
		title    string
		expected TaskType
	}{
		{"Check Alert Queue", TaskTypeAlerts},
		{"System Health Monitor", TaskTypeChecks},
		{"Daily Status Report", TaskTypeReports},
		{"Cleanup Old Files", TaskTypeMaintenance},
		{"Random Task", TaskTypeChecks}, // Default fallback
	}

	for _, test := range tests {
		result := interpreter.inferTaskType(test.title)
		if result != test.expected {
			t.Errorf("For title '%s', expected %s, got %s", test.title, test.expected, result)
		}
	}
}

func TestTaskInterpreter_inferTaskPriority(t *testing.T) {
	interpreter := NewTaskInterpreter("/tmp")

	tests := []struct {
		title    string
		expected TaskPriority
	}{
		{"Critical System Alert", TaskPriorityCritical},
		{"Urgent Maintenance", TaskPriorityCritical},
		{"Warning Alert Check", TaskPriorityHigh},
		{"Info Level Report", TaskPriorityLow},
		{"Routine Maintenance", TaskPriorityLow},
		{"Regular Task", TaskPriorityNormal}, // Default
	}

	for _, test := range tests {
		result := interpreter.inferTaskPriority(test.title)
		if result != test.expected {
			t.Errorf("For title '%s', expected %s, got %s", test.title, test.expected, result)
		}
	}
}

func TestParsedHeartbeatTask_ToHeartbeatTask(t *testing.T) {
	parsed := ParsedHeartbeatTask{
		Title:        "Test Task",
		Description:  "A test task",
		Instructions: []string{"Do something", "Do something else"},
		CodeBlocks: []CodeBlock{
			{Language: "bash", Content: "echo test"},
		},
		Type:           TaskTypeAlerts,
		Priority:       TaskPriorityHigh,
		Conditions:     []string{"if alerts found"},
		Actions:        []string{"send alert"},
		IsImmediate:    true,
		IsQuietAware:   true,
		HasConditional: true,
		LineStart:      5,
		LineEnd:        10,
	}

	task := parsed.ToHeartbeatTask()

	// Check basic fields
	if task.Name != "Test Task" {
		t.Errorf("Expected name 'Test Task', got '%s'", task.Name)
	}

	if task.Description != "A test task" {
		t.Errorf("Expected description 'A test task', got '%s'", task.Description)
	}

	if task.Type != TaskTypeAlerts {
		t.Errorf("Expected type %s, got %s", TaskTypeAlerts, task.Type)
	}

	if task.Priority != TaskPriorityHigh {
		t.Errorf("Expected priority %s, got %s", TaskPriorityHigh, task.Priority)
	}

	if task.Status != TaskStatusPending {
		t.Errorf("Expected status %s, got %s", TaskStatusPending, task.Status)
	}

	if task.MaxRetries != 3 {
		t.Errorf("Expected max retries 3, got %d", task.MaxRetries)
	}

	// Check payload
	if task.Payload == nil {
		t.Fatal("Expected payload to be set")
	}

	if instructions, ok := task.Payload["instructions"].([]string); !ok {
		t.Error("Expected payload to contain instructions")
	} else if len(instructions) != 2 {
		t.Errorf("Expected 2 instructions in payload, got %d", len(instructions))
	}

	// Check timeout
	if task.TimeoutDuration == nil {
		t.Fatal("Expected timeout duration to be set")
	}

	if *task.TimeoutDuration != 60*time.Second {
		t.Errorf("Expected 60s timeout for high priority, got %v", *task.TimeoutDuration)
	}

	// Check tags
	expectedTags := []string{"immediate", "quiet_aware", "conditional"}
	if len(task.Tags) != len(expectedTags) {
		t.Errorf("Expected %d tags, got %d", len(expectedTags), len(task.Tags))
	}

	for _, expectedTag := range expectedTags {
		found := false
		for _, tag := range task.Tags {
			if tag == expectedTag {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected tag '%s' not found in task tags", expectedTag)
		}
	}

	// Check quiet hours behavior
	if !task.SkipQuietHours {
		t.Error("Expected immediate/critical task to skip quiet hours")
	}
}

func TestTaskInterpreter_ReadHeartbeatTasks_FileNotFound(t *testing.T) {
	interpreter := NewTaskInterpreter("/nonexistent/path")

	_, err := interpreter.ReadHeartbeatTasks()
	if err == nil {
		t.Fatal("Expected error for non-existent HEARTBEAT.md file")
	}

	if !contains(err.Error(), "HEARTBEAT.md not found") {
		t.Errorf("Expected 'not found' error, got: %v", err)
	}
}

func TestTaskInterpreter_GeneratePrompt_EmptyTasks(t *testing.T) {
	interpreter := NewTaskInterpreter("/tmp")

	_, err := interpreter.GeneratePrompt([]ParsedHeartbeatTask{})
	if err == nil {
		t.Fatal("Expected error for empty tasks")
	}

	if !contains(err.Error(), "no tasks") {
		t.Errorf("Expected 'no tasks' error, got: %v", err)
	}
}

func TestParsedHeartbeatTask_Validate(t *testing.T) {
	tests := []struct {
		name    string
		task    ParsedHeartbeatTask
		wantErr bool
	}{
		{
			name: "valid task",
			task: ParsedHeartbeatTask{
				Title:     "Valid Task",
				Type:      TaskTypeAlerts,
				Priority:  TaskPriorityNormal,
				LineStart: 0,
				LineEnd:   5,
			},
			wantErr: false,
		},
		{
			name: "empty title",
			task: ParsedHeartbeatTask{
				Title:     "",
				Type:      TaskTypeAlerts,
				Priority:  TaskPriorityNormal,
				LineStart: 0,
				LineEnd:   5,
			},
			wantErr: true,
		},
		{
			name: "invalid type",
			task: ParsedHeartbeatTask{
				Title:     "Test",
				Type:      "invalid",
				Priority:  TaskPriorityNormal,
				LineStart: 0,
				LineEnd:   5,
			},
			wantErr: true,
		},
		{
			name: "invalid priority",
			task: ParsedHeartbeatTask{
				Title:     "Test",
				Type:      TaskTypeAlerts,
				Priority:  999,
				LineStart: 0,
				LineEnd:   5,
			},
			wantErr: true,
		},
		{
			name: "invalid line numbers",
			task: ParsedHeartbeatTask{
				Title:     "Test",
				Type:      TaskTypeAlerts,
				Priority:  TaskPriorityNormal,
				LineStart: 5,
				LineEnd:   2,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.task.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// contains helper moved to result_processor_test.go to avoid redeclaration
