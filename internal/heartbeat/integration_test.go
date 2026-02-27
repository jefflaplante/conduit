package heartbeat

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"conduit/internal/sessions"
)

// Mock AI executor for integration testing
type mockAIExecutor struct {
	responses     map[string]string // prompt -> response mapping
	fixedResponse string            // fixed response to return (if set)
}

func (m *mockAIExecutor) ExecutePrompt(ctx context.Context, session *sessions.Session, prompt, model string) (AIResponse, error) {
	// If fixed response is set, use it
	if m.fixedResponse != "" {
		return &mockAIResponse{content: m.fixedResponse}, nil
	}

	// Simple mock that looks for keywords in the prompt to determine response
	response := ""

	if contains(prompt, "Check shared alert queue") {
		if contains(prompt, "critical") {
			response = "Found 2 critical alerts in queue. Delivering to Jeff immediately via Telegram."
		} else {
			response = "HEARTBEAT_OK"
		}
	} else if contains(prompt, "system status") {
		response = "All systems operational. Database: OK, API: OK, Disk usage: 67% (normal)."
	} else {
		response = "HEARTBEAT_OK"
	}

	// Check if there's a specific response for this prompt
	if specificResponse, exists := m.responses[prompt]; exists {
		response = specificResponse
	}

	return &mockAIResponse{content: response}, nil
}

func newMockAIExecutor() *mockAIExecutor {
	return &mockAIExecutor{
		responses: make(map[string]string),
	}
}

func newMockAIExecutorWithResponse(response string) *mockAIExecutor {
	return &mockAIExecutor{
		responses:     make(map[string]string),
		fixedResponse: response,
	}
}

func (m *mockAIExecutor) setResponse(prompt, response string) {
	m.responses[prompt] = response
}

// funcMockAIExecutor is a mock that uses a function for execution
type funcMockAIExecutor struct {
	execFunc func(ctx context.Context, session *sessions.Session, prompt, model string) (AIResponse, error)
}

func (f *funcMockAIExecutor) ExecutePrompt(ctx context.Context, session *sessions.Session, prompt, model string) (AIResponse, error) {
	return f.execFunc(ctx, session, prompt, model)
}

// Mock session store for integration testing
type mockSessionStore struct {
	sessions map[string]*sessions.Session
}

func newMockSessionStore() *mockSessionStore {
	return &mockSessionStore{
		sessions: make(map[string]*sessions.Session),
	}
}

func (m *mockSessionStore) GetOrCreateSession(userID, channelID string) (*sessions.Session, error) {
	key := userID + "_" + channelID
	if session, exists := m.sessions[key]; exists {
		return session, nil
	}

	session := &sessions.Session{
		Key:       key,
		UserID:    userID,
		ChannelID: channelID,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Context:   make(map[string]string),
	}

	m.sessions[key] = session
	return session, nil
}

func TestJobExecutor_ExecuteHeartbeatJob_Integration(t *testing.T) {
	// Create temporary workspace
	tempDir, err := os.MkdirTemp("", "heartbeat_integration_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create test HEARTBEAT.md
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
Monitor critical systems:
- Database connectivity
- API endpoints
- Disk space usage

Report any issues immediately.
`

	heartbeatPath := filepath.Join(tempDir, "HEARTBEAT.md")
	if err := os.WriteFile(heartbeatPath, []byte(heartbeatContent), 0644); err != nil {
		t.Fatalf("Failed to write HEARTBEAT.md: %v", err)
	}

	// Set up mock dependencies
	mockSessions := newMockSessionStore()

	// Create executor
	config := DefaultExecutorConfig()
	config.TimeoutSeconds = 5 // Shorter timeout for tests
	executor := NewJobExecutor(tempDir, mockSessions, config)

	tests := []struct {
		name             string
		aiResponse       string
		expectedStatus   ResultStatus
		expectedActions  int
		shouldBeOK       bool
		shouldHaveAlerts bool
	}{
		{
			name:             "heartbeat_ok_response",
			aiResponse:       "HEARTBEAT_OK",
			expectedStatus:   ResultStatusOK,
			expectedActions:  0,
			shouldBeOK:       true,
			shouldHaveAlerts: false,
		},
		{
			name:             "no_alerts_found",
			aiResponse:       "Checked alert queue and system status. No alerts found, all systems operational. HEARTBEAT_OK.",
			expectedStatus:   ResultStatusOK,
			expectedActions:  0,
			shouldBeOK:       true,
			shouldHaveAlerts: false,
		},
		{
			name:             "critical_alert",
			aiResponse:       "CRITICAL: Database connection failed! Deliver to Jeff immediately via Telegram.",
			expectedStatus:   ResultStatusAlert,
			expectedActions:  2, // "CRITICAL" triggers alert action, "Deliver to Jeff" triggers delivery action
			shouldBeOK:       false,
			shouldHaveAlerts: true,
		},
		{
			name:             "warning_alert_quiet_aware",
			aiResponse:       "Warning: High CPU usage detected. Deliver to Jeff if he's likely awake (8 AM - 10 PM PT).",
			expectedStatus:   ResultStatusAlert,
			expectedActions:  2, // "Warning" triggers alert action, "Deliver to Jeff" triggers delivery action
			shouldBeOK:       false,
			shouldHaveAlerts: true,
		},
		{
			name:             "maintenance_action",
			aiResponse:       "Processed 3 info-level alerts. Clear the queue: python3 -c \"import os; open('alerts.json', 'w').write('[]')\"",
			expectedStatus:   ResultStatusAction,
			expectedActions:  1,
			shouldBeOK:       false,
			shouldHaveAlerts: false,
		},
		{
			name:             "system_status_info",
			aiResponse:       "System status: All services running normally. CPU: 45%, Memory: 62%, Disk: 78%. No action required.",
			expectedStatus:   ResultStatusAction,
			expectedActions:  1,
			shouldBeOK:       false,
			shouldHaveAlerts: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up AI response for this test
			ctx := context.Background()

			// Create a custom AI executor that returns our test response
			testAI := newMockAIExecutorWithResponse(tt.aiResponse)

			// Execute the heartbeat job
			result, err := executor.ExecuteHeartbeatJob(ctx, testAI)
			if err != nil {
				t.Fatalf("ExecuteHeartbeatJob() error = %v", err)
			}

			// Verify result status
			if result.Status != tt.expectedStatus {
				t.Errorf("Expected status %s, got %s", tt.expectedStatus, result.Status)
			}

			// Verify action count
			if len(result.Actions) != tt.expectedActions {
				t.Errorf("Expected %d actions, got %d", tt.expectedActions, len(result.Actions))
			}

			// Verify HEARTBEAT_OK status
			if result.IsHeartbeatOK() != tt.shouldBeOK {
				t.Errorf("Expected IsHeartbeatOK() to be %v, got %v", tt.shouldBeOK, result.IsHeartbeatOK())
			}

			// Verify alerts status
			if result.HasAlerts() != tt.shouldHaveAlerts {
				t.Errorf("Expected HasAlerts() to be %v, got %v", tt.shouldHaveAlerts, result.HasAlerts())
			}

			// Verify result has session key
			if result.SessionKey == "" {
				t.Error("Expected session key to be set")
			}

			// Verify tasks were processed
			if result.TasksProcessed == 0 {
				t.Error("Expected tasks to be processed")
			}

			// Validate the result
			if err := result.Validate(); err != nil {
				t.Errorf("Result validation failed: %v", err)
			}

			t.Logf("Test '%s' completed: status=%s, actions=%d, message=%s",
				tt.name, result.Status, len(result.Actions), result.Message)
		})
	}
}

func TestJobExecutor_ExecuteHeartbeatJob_MissingFile(t *testing.T) {
	// Test with non-existent workspace directory
	nonExistentDir := "/tmp/nonexistent_heartbeat_test"

	mockSessions := newMockSessionStore()
	mockAI := newMockAIExecutor()

	config := DefaultExecutorConfig()
	executor := NewJobExecutor(nonExistentDir, mockSessions, config)

	ctx := context.Background()
	_, err := executor.ExecuteHeartbeatJob(ctx, mockAI)

	if err == nil {
		t.Fatal("Expected error for missing HEARTBEAT.md file")
	}

	if !contains(err.Error(), "failed to read heartbeat tasks") {
		t.Errorf("Expected 'failed to read heartbeat tasks' error, got: %v", err)
	}
}

func TestJobExecutor_ExecuteHeartbeatJob_EmptyFile(t *testing.T) {
	// Create temporary workspace with empty HEARTBEAT.md
	tempDir, err := os.MkdirTemp("", "heartbeat_empty_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create empty HEARTBEAT.md
	heartbeatPath := filepath.Join(tempDir, "HEARTBEAT.md")
	if err := os.WriteFile(heartbeatPath, []byte("# HEARTBEAT.md\n\n"), 0644); err != nil {
		t.Fatalf("Failed to write empty HEARTBEAT.md: %v", err)
	}

	mockSessions := newMockSessionStore()
	mockAI := newMockAIExecutor()

	config := DefaultExecutorConfig()
	executor := NewJobExecutor(tempDir, mockSessions, config)

	ctx := context.Background()
	result, err := executor.ExecuteHeartbeatJob(ctx, mockAI)

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Should return no action status
	if result.Status != ResultStatusNoAction {
		t.Errorf("Expected status %s for empty file, got %s", ResultStatusNoAction, result.Status)
	}
}

func TestJobExecutor_ExecuteHeartbeatJob_Timeout(t *testing.T) {
	// Create temporary workspace
	tempDir, err := os.MkdirTemp("", "heartbeat_timeout_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create simple HEARTBEAT.md
	heartbeatContent := `# HEARTBEAT.md

## Quick check
Just a quick check.
`

	heartbeatPath := filepath.Join(tempDir, "HEARTBEAT.md")
	if err := os.WriteFile(heartbeatPath, []byte(heartbeatContent), 0644); err != nil {
		t.Fatalf("Failed to write HEARTBEAT.md: %v", err)
	}

	// Create AI executor that hangs
	hangingAI := &funcMockAIExecutor{
		execFunc: func(ctx context.Context, session *sessions.Session, prompt, model string) (AIResponse, error) {
			select {
			case <-time.After(10 * time.Second): // Longer than our timeout
				return &mockAIResponse{content: "Should not reach here"}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}

	mockSessions := newMockSessionStore()

	config := DefaultExecutorConfig()
	config.TimeoutSeconds = 1 // Very short timeout
	executor := NewJobExecutor(tempDir, mockSessions, config)

	ctx := context.Background()
	_, err = executor.ExecuteHeartbeatJob(ctx, hangingAI)

	if err == nil {
		t.Fatal("Expected timeout error")
	}

	if !contains(err.Error(), "context deadline exceeded") {
		t.Errorf("Expected timeout error, got: %v", err)
	}
}

func TestJobExecutor_ExecuteHeartbeatJob_Retries(t *testing.T) {
	// Create temporary workspace
	tempDir, err := os.MkdirTemp("", "heartbeat_retry_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create HEARTBEAT.md
	heartbeatContent := `# HEARTBEAT.md

## Test task
A test task for retry logic.
`

	heartbeatPath := filepath.Join(tempDir, "HEARTBEAT.md")
	if err := os.WriteFile(heartbeatPath, []byte(heartbeatContent), 0644); err != nil {
		t.Fatalf("Failed to write HEARTBEAT.md: %v", err)
	}

	// Create AI executor that fails a few times then succeeds
	attemptCount := 0
	retryAI := &funcMockAIExecutor{
		execFunc: func(ctx context.Context, session *sessions.Session, prompt, model string) (AIResponse, error) {
			attemptCount++
			if attemptCount < 3 {
				return nil, &tempError{message: "Temporary AI failure"}
			}
			return &mockAIResponse{content: "HEARTBEAT_OK"}, nil
		},
	}

	mockSessions := newMockSessionStore()

	config := DefaultExecutorConfig()
	config.MaxRetries = 3
	config.RetryDelaySeconds = 0 // No delay for faster tests
	executor := NewJobExecutor(tempDir, mockSessions, config)

	ctx := context.Background()
	result, err := executor.ExecuteHeartbeatJob(ctx, retryAI)

	if err != nil {
		t.Fatalf("Unexpected error after retries: %v", err)
	}

	if attemptCount != 3 {
		t.Errorf("Expected 3 attempts, got %d", attemptCount)
	}

	if result.Status != ResultStatusOK {
		t.Errorf("Expected OK status after retry success, got %s", result.Status)
	}
}

func TestJobExecutor_ExecuteHeartbeatJob_RetryExhaustion(t *testing.T) {
	// Create temporary workspace
	tempDir, err := os.MkdirTemp("", "heartbeat_retry_exhaustion_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create HEARTBEAT.md
	heartbeatContent := `# HEARTBEAT.md

## Test task
A test task for retry exhaustion.
`

	heartbeatPath := filepath.Join(tempDir, "HEARTBEAT.md")
	if err := os.WriteFile(heartbeatPath, []byte(heartbeatContent), 0644); err != nil {
		t.Fatalf("Failed to write HEARTBEAT.md: %v", err)
	}

	// Create AI executor that always fails
	attemptCount := 0
	failingAI := &funcMockAIExecutor{
		execFunc: func(ctx context.Context, session *sessions.Session, prompt, model string) (AIResponse, error) {
			attemptCount++
			return nil, &tempError{message: "Persistent AI failure"}
		},
	}

	mockSessions := newMockSessionStore()

	config := DefaultExecutorConfig()
	config.MaxRetries = 2
	config.RetryDelaySeconds = 0 // No delay for faster tests
	executor := NewJobExecutor(tempDir, mockSessions, config)

	ctx := context.Background()
	_, err = executor.ExecuteHeartbeatJob(ctx, failingAI)

	if err == nil {
		t.Fatal("Expected error after retry exhaustion")
	}

	expectedAttempts := config.MaxRetries + 1 // Initial attempt + retries
	if attemptCount != expectedAttempts {
		t.Errorf("Expected %d attempts, got %d", expectedAttempts, attemptCount)
	}

	if !contains(err.Error(), "failed to execute AI prompt after") {
		t.Errorf("Expected retry exhaustion error, got: %v", err)
	}
}

// tempError is a mock error type for testing
type tempError struct {
	message string
}

func (e *tempError) Error() string {
	return e.message
}

func TestHeartbeatResult_GetImmediateActions(t *testing.T) {
	result := &HeartbeatResult{
		Actions: []HeartbeatAction{
			{Type: ActionTypeAlert, Priority: TaskPriorityCritical, Content: "Critical alert"},
			{Type: ActionTypeNotification, Priority: TaskPriorityNormal, Content: "Normal notification"},
			{Type: ActionTypeDelivery, Priority: TaskPriorityHigh, Content: "High priority delivery"},
			{Type: ActionTypeCommand, Priority: TaskPriorityLow, Content: "Low priority command"},
		},
	}

	immediate := result.GetImmediateActions()

	// Should include critical and high priority actions
	if len(immediate) != 2 {
		t.Errorf("Expected 2 immediate actions, got %d", len(immediate))
	}

	// Check that we got the right actions
	foundCritical := false
	foundHigh := false

	for _, action := range immediate {
		if action.Priority == TaskPriorityCritical {
			foundCritical = true
		}
		if action.Priority == TaskPriorityHigh {
			foundHigh = true
		}
	}

	if !foundCritical {
		t.Error("Expected to find critical priority action in immediate actions")
	}

	if !foundHigh {
		t.Error("Expected to find high priority action in immediate actions")
	}
}

func TestHeartbeatResult_GetDelayedActions(t *testing.T) {
	result := &HeartbeatResult{
		Actions: []HeartbeatAction{
			{Type: ActionTypeAlert, Priority: TaskPriorityCritical, Content: "Critical alert"},
			{Type: ActionTypeNotification, Priority: TaskPriorityNormal, Content: "Normal notification"},
			{Type: ActionTypeDelivery, Priority: TaskPriorityHigh, Content: "High priority delivery"},
			{Type: ActionTypeCommand, Priority: TaskPriorityLow, Content: "Low priority command"},
		},
	}

	delayed := result.GetDelayedActions()

	// Should include normal and low priority actions
	if len(delayed) != 2 {
		t.Errorf("Expected 2 delayed actions, got %d", len(delayed))
	}

	// Check that we got the right actions
	foundNormal := false
	foundLow := false

	for _, action := range delayed {
		if action.Priority == TaskPriorityNormal {
			foundNormal = true
		}
		if action.Priority == TaskPriorityLow {
			foundLow = true
		}
	}

	if !foundNormal {
		t.Error("Expected to find normal priority action in delayed actions")
	}

	if !foundLow {
		t.Error("Expected to find low priority action in delayed actions")
	}
}
