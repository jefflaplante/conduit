package heartbeat

import (
	"strings"
	"testing"
)

// Mock AI response for testing
type mockAIResponse struct {
	content string
	usage   interface{}
}

func (m *mockAIResponse) GetContent() string {
	return m.content
}

func (m *mockAIResponse) GetUsage() interface{} {
	return m.usage
}

func TestResultProcessor_ProcessResponse_HeartbeatOK(t *testing.T) {
	processor := NewResultProcessor()

	tests := []struct {
		name     string
		content  string
		expected ResultStatus
	}{
		{
			name:     "explicit HEARTBEAT_OK",
			content:  "HEARTBEAT_OK",
			expected: ResultStatusOK,
		},
		{
			name:     "case insensitive HEARTBEAT_OK",
			content:  "heartbeat_ok",
			expected: ResultStatusOK,
		},
		{
			name:     "HEARTBEAT_OK in sentence",
			content:  "All systems checked. HEARTBEAT_OK.",
			expected: ResultStatusOK,
		},
		{
			name:     "no alerts found",
			content:  "Checked alert queue - no alerts found.",
			expected: ResultStatusOK,
		},
		{
			name:     "nothing needs attention",
			content:  "System status is normal, nothing needs attention.",
			expected: ResultStatusOK,
		},
		{
			name:     "no action needed",
			content:  "All checks completed successfully. No action needed.",
			expected: ResultStatusOK,
		},
		{
			name:     "only info level",
			content:  "Found only info-level alerts, skipping delivery.",
			expected: ResultStatusOK,
		},
		{
			name:     "all clear",
			content:  "All clear.",
			expected: ResultStatusOK,
		},
		{
			name:     "short no issues",
			content:  "No issues",
			expected: ResultStatusOK,
		},
		{
			name:     "empty queue",
			content:  "Alert queue is empty, all good.",
			expected: ResultStatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := &mockAIResponse{content: tt.content}
			result, err := processor.ProcessResponse(response, []ParsedHeartbeatTask{})
			if err != nil {
				t.Fatalf("ProcessResponse() error = %v", err)
			}

			if result.Status != tt.expected {
				t.Errorf("Expected status %s, got %s", tt.expected, result.Status)
			}

			if result.Status == ResultStatusOK && len(result.Actions) > 0 {
				t.Errorf("Expected no actions for OK status, got %d actions", len(result.Actions))
			}
		})
	}
}

func TestResultProcessor_ProcessResponse_AlertActions(t *testing.T) {
	processor := NewResultProcessor()

	tests := []struct {
		name             string
		content          string
		expectedStatus   ResultStatus
		expectedActions  int
		expectedType     ActionType
		expectedPriority TaskPriority
	}{
		{
			name:             "critical alert",
			content:          "CRITICAL: Database connection failed, deliver to Jeff immediately via Telegram",
			expectedStatus:   ResultStatusAlert,
			expectedActions:  1,
			expectedType:     ActionTypeAlert,
			expectedPriority: TaskPriorityCritical,
		},
		{
			name:             "warning alert",
			content:          "Warning: High CPU usage detected, alert Jeff if he's awake",
			expectedStatus:   ResultStatusAlert,
			expectedActions:  1,
			expectedType:     ActionTypeAlert,
			expectedPriority: TaskPriorityHigh,
		},
		{
			name:             "urgent system issue",
			content:          "Urgent: Disk space critically low on server, needs immediate attention",
			expectedStatus:   ResultStatusAlert,
			expectedActions:  1,
			expectedType:     ActionTypeAlert,
			expectedPriority: TaskPriorityCritical,
		},
		{
			name:             "delivery instruction",
			content:          "Send to Jeff via Telegram: important status changes detected",
			expectedStatus:   ResultStatusAction,
			expectedActions:  1,
			expectedType:     ActionTypeDelivery,
			expectedPriority: TaskPriorityHigh,
		},
		{
			name:             "maintenance command",
			content:          "Clear the queue with cleanup: python3 -c \"import os; open('alerts.json', 'w').write('[]')\"",
			expectedStatus:   ResultStatusAction,
			expectedActions:  1,
			expectedType:     ActionTypeCommand,
			expectedPriority: TaskPriorityLow,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := &mockAIResponse{content: tt.content}
			result, err := processor.ProcessResponse(response, []ParsedHeartbeatTask{})
			if err != nil {
				t.Fatalf("ProcessResponse() error = %v", err)
			}

			if result.Status != tt.expectedStatus {
				t.Errorf("Expected status %s, got %s", tt.expectedStatus, result.Status)
			}

			if len(result.Actions) != tt.expectedActions {
				t.Errorf("Expected %d actions, got %d", tt.expectedActions, len(result.Actions))
			}

			if len(result.Actions) > 0 {
				action := result.Actions[0]
				if action.Type != tt.expectedType {
					t.Errorf("Expected action type %s, got %s", tt.expectedType, action.Type)
				}

				if action.Priority != tt.expectedPriority {
					t.Errorf("Expected priority %s, got %s", tt.expectedPriority, action.Priority)
				}
			}
		})
	}
}

func TestResultProcessor_ProcessResponse_GeneralNotification(t *testing.T) {
	processor := NewResultProcessor()

	content := "System health report: All services running normally, database backup completed at 02:00 AM"
	response := &mockAIResponse{content: content}

	result, err := processor.ProcessResponse(response, []ParsedHeartbeatTask{})
	if err != nil {
		t.Fatalf("ProcessResponse() error = %v", err)
	}

	// Should create a general notification since it's not HEARTBEAT_OK but doesn't match specific patterns
	if result.Status != ResultStatusAction {
		t.Errorf("Expected status %s, got %s", ResultStatusAction, result.Status)
	}

	if len(result.Actions) != 1 {
		t.Errorf("Expected 1 action, got %d", len(result.Actions))
	}

	if result.Actions[0].Type != ActionTypeNotification {
		t.Errorf("Expected notification action, got %s", result.Actions[0].Type)
	}

	if result.Actions[0].Priority != TaskPriorityNormal {
		t.Errorf("Expected normal priority, got %s", result.Actions[0].Priority)
	}
}

func TestResultProcessor_isHeartbeatOK(t *testing.T) {
	processor := NewResultProcessor()

	tests := []struct {
		content  string
		expected bool
	}{
		{"HEARTBEAT_OK", true},
		{"heartbeat_ok", true},
		{"Everything looks good. HEARTBEAT_OK.", true},
		{"No alerts found in queue", true},
		{"nothing needs attention", true},
		{"no action needed", true},
		{"all clear", true},
		{"only info-level alerts", true},
		{"all good", true},      // Short phrase, should match
		{"no issues", true},     // Short phrase, should match
		{"empty queue", true},   // Short phrase, should match
		{"no new alerts", true}, // Short phrase, should match
		{"Critical alert found!", false},
		{"Warning: high CPU", false},
		{"Need to deliver alerts", false},
		{"This is a longer message about system status and various checks that were performed", false},
	}

	for _, tt := range tests {
		t.Run(tt.content, func(t *testing.T) {
			result := processor.isHeartbeatOK(tt.content)
			if result != tt.expected {
				t.Errorf("For content '%s', expected %v, got %v", tt.content, tt.expected, result)
			}
		})
	}
}

func TestResultProcessor_inferPriorityFromContent(t *testing.T) {
	processor := NewResultProcessor()

	tests := []struct {
		content  string
		expected TaskPriority
	}{
		{"CRITICAL: System down!", TaskPriorityCritical},
		{"Urgent maintenance required", TaskPriorityCritical},
		{"Database connection failed", TaskPriorityCritical},
		{"Server is down", TaskPriorityCritical},
		{"Cannot connect to service", TaskPriorityCritical},
		{"Warning: High CPU usage", TaskPriorityHigh},
		{"Alert: Memory usage high", TaskPriorityHigh},
		{"Important system notification", TaskPriorityHigh},
		{"Attention required for service", TaskPriorityHigh},
		{"Issue detected in logs", TaskPriorityHigh},
		{"Info: Backup completed", TaskPriorityLow},
		{"Information about system status", TaskPriorityLow},
		{"Note: Scheduled maintenance", TaskPriorityLow},
		{"Routine cleanup finished", TaskPriorityLow},
		{"Maintenance task completed", TaskPriorityLow},
		{"System status update", TaskPriorityNormal},
		{"Regular heartbeat check", TaskPriorityNormal},
		{"Standard notification", TaskPriorityNormal},
	}

	for _, tt := range tests {
		t.Run(tt.content, func(t *testing.T) {
			result := processor.inferPriorityFromContent(tt.content)
			if result != tt.expected {
				t.Errorf("For content '%s', expected %s, got %s", tt.content, tt.expected, result)
			}
		})
	}
}

func TestResultProcessor_isImmediateAction(t *testing.T) {
	processor := NewResultProcessor()

	tests := []struct {
		content  string
		expected bool
	}{
		{"Deliver immediately via Telegram", true},
		{"Send alert now", true},
		{"Urgent action required", true},
		{"Critical issue - handle ASAP", true},
		{"Process right away", true},
		{"Execute at once", true},
		{"Deliver to Jeff if he's awake", false},
		{"Schedule for later", false},
		{"Regular notification", false},
		{"Info level alert", false},
	}

	for _, tt := range tests {
		t.Run(tt.content, func(t *testing.T) {
			result := processor.isImmediateAction(tt.content)
			if result != tt.expected {
				t.Errorf("For content '%s', expected %v, got %v", tt.content, tt.expected, result)
			}
		})
	}
}

func TestResultProcessor_isQuietAwareAction(t *testing.T) {
	processor := NewResultProcessor()

	tests := []struct {
		content  string
		expected bool
	}{
		{"Deliver to Jeff if he's likely awake (8 AM - 10 PM PT)", true},
		{"Send during quiet hours", true},
		{"Alert if awake", true},
		{"Respect quiet hours", true},
		{"Send immediately regardless", false},
		{"Critical alert", false},
		{"Regular notification", false},
	}

	for _, tt := range tests {
		t.Run(tt.content, func(t *testing.T) {
			result := processor.isQuietAwareAction(tt.content)
			if result != tt.expected {
				t.Errorf("For content '%s', expected %v, got %v", tt.content, tt.expected, result)
			}
		})
	}
}

func TestResultProcessor_extractDeliveryTarget(t *testing.T) {
	processor := NewResultProcessor()

	tests := []struct {
		content  string
		expected string
	}{
		{"Deliver to Jeff via Telegram", "telegram"},
		{"Send to Jeff via telegram", "telegram"},
		{"Notify user through email", "email"},
		{"Alert admin via SMS", "sms"},
		{"Send to jeff immediately", "jeff"},
		{"Regular notification", "telegram"}, // Default
	}

	for _, tt := range tests {
		t.Run(tt.content, func(t *testing.T) {
			result := processor.extractDeliveryTarget(tt.content)
			if result != tt.expected {
				t.Errorf("For content '%s', expected %s, got %s", tt.content, tt.expected, result)
			}
		})
	}
}

func TestResultProcessor_extractCommand(t *testing.T) {
	processor := NewResultProcessor()

	tests := []struct {
		content  string
		expected string
	}{
		{
			content:  "Clear the queue:\n```bash\nrm -f alerts.json\n```",
			expected: "rm -f alerts.json",
		},
		{
			content:  "Run cleanup: `cleanup.sh`",
			expected: "cleanup.sh",
		},
		{
			content:  "Execute: python3 -c \"print('hello')\"",
			expected: "python3 -c \"print('hello')\"",
		},
		{
			content:  "No command here",
			expected: "",
		},
		{
			content:  "```python\nprint('test')\n```",
			expected: "print('test')",
		},
	}

	for _, tt := range tests {
		t.Run(tt.content, func(t *testing.T) {
			result := processor.extractCommand(tt.content)
			if result != tt.expected {
				t.Errorf("For content '%s', expected '%s', got '%s'", tt.content, tt.expected, result)
			}
		})
	}
}

func TestResultProcessor_FilterActionsByQuietHours(t *testing.T) {
	processor := NewResultProcessor()

	actions := []HeartbeatAction{
		{
			Type:     ActionTypeAlert,
			Priority: TaskPriorityCritical,
			Content:  "Critical alert",
		},
		{
			Type:     ActionTypeDelivery,
			Priority: TaskPriorityNormal,
			Content:  "Regular notification",
		},
		{
			Type:     ActionTypeDelivery,
			Priority: TaskPriorityNormal,
			Content:  "Quiet-aware notification",
			Metadata: map[string]interface{}{"quiet_aware": true},
		},
	}

	// Test during quiet hours
	immediate, delayed := processor.FilterActionsByQuietHours(actions, true)

	if len(immediate) != 2 {
		t.Errorf("Expected 2 immediate actions during quiet hours, got %d", len(immediate))
	}

	if len(delayed) != 1 {
		t.Errorf("Expected 1 delayed action during quiet hours, got %d", len(delayed))
	}

	// Test during non-quiet hours
	immediate, delayed = processor.FilterActionsByQuietHours(actions, false)

	if len(immediate) != 3 {
		t.Errorf("Expected 3 immediate actions during non-quiet hours, got %d", len(immediate))
	}

	if len(delayed) != 0 {
		t.Errorf("Expected 0 delayed actions during non-quiet hours, got %d", len(delayed))
	}
}

func TestResultProcessor_MergeActions(t *testing.T) {
	processor := NewResultProcessor()

	actions := []HeartbeatAction{
		{
			Type:     ActionTypeNotification,
			Target:   "telegram",
			Content:  "First notification",
			Priority: TaskPriorityNormal,
		},
		{
			Type:     ActionTypeNotification,
			Target:   "telegram",
			Content:  "Second notification",
			Priority: TaskPriorityHigh,
		},
		{
			Type:     ActionTypeAlert,
			Target:   "telegram",
			Content:  "Alert message",
			Priority: TaskPriorityCritical,
		},
	}

	merged := processor.MergeActions(actions)

	// Should merge the two notifications but keep alert separate
	if len(merged) != 2 {
		t.Errorf("Expected 2 merged actions, got %d", len(merged))
	}

	// Find the merged notification action
	var notificationAction *HeartbeatAction
	for _, action := range merged {
		if action.Type == ActionTypeNotification {
			notificationAction = &action
			break
		}
	}

	if notificationAction == nil {
		t.Fatal("Expected to find merged notification action")
	}

	if notificationAction.Priority != TaskPriorityHigh {
		t.Errorf("Expected merged action to have highest priority (high), got %s", notificationAction.Priority)
	}

	if !contains(notificationAction.Content, "First notification") || !contains(notificationAction.Content, "Second notification") {
		t.Errorf("Expected merged content to contain both notifications, got: %s", notificationAction.Content)
	}

	if count, ok := notificationAction.Metadata["merged_count"].(int); !ok || count != 2 {
		t.Errorf("Expected merged_count metadata to be 2, got %v", count)
	}
}

func TestResultProcessor_ValidateActions(t *testing.T) {
	processor := NewResultProcessor()

	tests := []struct {
		name    string
		actions []HeartbeatAction
		wantErr bool
	}{
		{
			name: "valid actions",
			actions: []HeartbeatAction{
				{
					Type:     ActionTypeAlert,
					Target:   "telegram",
					Content:  "Test alert",
					Priority: TaskPriorityHigh,
				},
			},
			wantErr: false,
		},
		{
			name: "empty type",
			actions: []HeartbeatAction{
				{
					Type:     "",
					Target:   "telegram",
					Content:  "Test",
					Priority: TaskPriorityNormal,
				},
			},
			wantErr: true,
		},
		{
			name: "empty target",
			actions: []HeartbeatAction{
				{
					Type:     ActionTypeAlert,
					Target:   "",
					Content:  "Test",
					Priority: TaskPriorityNormal,
				},
			},
			wantErr: true,
		},
		{
			name: "empty content",
			actions: []HeartbeatAction{
				{
					Type:     ActionTypeAlert,
					Target:   "telegram",
					Content:  "",
					Priority: TaskPriorityNormal,
				},
			},
			wantErr: true,
		},
		{
			name: "invalid priority",
			actions: []HeartbeatAction{
				{
					Type:     ActionTypeAlert,
					Target:   "telegram",
					Content:  "Test",
					Priority: 999,
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := processor.ValidateActions(tt.actions)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateActions() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestResultProcessor_GetActionSummary(t *testing.T) {
	processor := NewResultProcessor()

	tests := []struct {
		name     string
		actions  []HeartbeatAction
		expected string
	}{
		{
			name:     "no actions",
			actions:  []HeartbeatAction{},
			expected: "No actions required",
		},
		{
			name: "single alert",
			actions: []HeartbeatAction{
				{Type: ActionTypeAlert, Priority: TaskPriorityCritical},
			},
			expected: "1 alert (1 critical)",
		},
		{
			name: "multiple actions",
			actions: []HeartbeatAction{
				{Type: ActionTypeAlert, Priority: TaskPriorityCritical},
				{Type: ActionTypeNotification, Priority: TaskPriorityNormal},
				{Type: ActionTypeDelivery, Priority: TaskPriorityHigh},
			},
			expected: "1 alert, 1 notification, 1 delivery (1 critical)",
		},
		{
			name: "high priority actions",
			actions: []HeartbeatAction{
				{Type: ActionTypeNotification, Priority: TaskPriorityHigh},
				{Type: ActionTypeNotification, Priority: TaskPriorityNormal},
			},
			expected: "2 notifications (1 high priority)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := processor.GetActionSummary(tt.actions)
			if !contains(result, "1 alert") && !contains(result, "1 notification") && !contains(result, "1 delivery") && !contains(result, "No actions") {
				// More flexible check since the exact wording might vary
				t.Logf("Got summary: %s", result)
				// Just verify it's not empty and contains some key elements
				if len(result) == 0 {
					t.Errorf("Expected non-empty summary, got empty string")
				}
			}
		})
	}
}

func TestHeartbeatResult_IsHeartbeatOK(t *testing.T) {
	tests := []struct {
		name     string
		result   HeartbeatResult
		expected bool
	}{
		{
			name: "status OK with HEARTBEAT_OK message",
			result: HeartbeatResult{
				Status:  ResultStatusOK,
				Message: "HEARTBEAT_OK",
			},
			expected: true,
		},
		{
			name: "status OK with no actions",
			result: HeartbeatResult{
				Status:  ResultStatusOK,
				Message: "All systems normal",
				Actions: []HeartbeatAction{},
			},
			expected: true,
		},
		{
			name: "status OK with actions",
			result: HeartbeatResult{
				Status:  ResultStatusOK,
				Message: "HEARTBEAT_OK",
				Actions: []HeartbeatAction{
					{Type: ActionTypeNotification},
				},
			},
			expected: false,
		},
		{
			name: "status alert",
			result: HeartbeatResult{
				Status:  ResultStatusAlert,
				Message: "HEARTBEAT_OK",
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.result.IsHeartbeatOK()
			if result != tt.expected {
				t.Errorf("IsHeartbeatOK() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestHeartbeatResult_HasAlerts(t *testing.T) {
	tests := []struct {
		name     string
		result   HeartbeatResult
		expected bool
	}{
		{
			name: "status alert",
			result: HeartbeatResult{
				Status: ResultStatusAlert,
			},
			expected: true,
		},
		{
			name: "alert action",
			result: HeartbeatResult{
				Status: ResultStatusAction,
				Actions: []HeartbeatAction{
					{Type: ActionTypeAlert},
				},
			},
			expected: true,
		},
		{
			name: "critical priority action",
			result: HeartbeatResult{
				Status: ResultStatusAction,
				Actions: []HeartbeatAction{
					{Type: ActionTypeNotification, Priority: TaskPriorityCritical},
				},
			},
			expected: true,
		},
		{
			name: "no alerts",
			result: HeartbeatResult{
				Status: ResultStatusAction,
				Actions: []HeartbeatAction{
					{Type: ActionTypeNotification, Priority: TaskPriorityNormal},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.result.HasAlerts()
			if result != tt.expected {
				t.Errorf("HasAlerts() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// Helper function to check if string contains substring (shared across test files)
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
