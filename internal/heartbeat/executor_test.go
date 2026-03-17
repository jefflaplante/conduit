package heartbeat

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"conduit/internal/sessions"
)

func TestExecuteHeartbeatJob_ExecutionTime(t *testing.T) {
	// Create temp workspace with HEARTBEAT.md
	tempDir := t.TempDir()
	heartbeatContent := `# HEARTBEAT.md

## Check status
Check the system status.
Reply HEARTBEAT_OK if everything is fine.
`
	err := os.WriteFile(filepath.Join(tempDir, "HEARTBEAT.md"), []byte(heartbeatContent), 0644)
	if err != nil {
		t.Fatalf("Failed to write HEARTBEAT.md: %v", err)
	}

	config := DefaultExecutorConfig()
	config.TimeoutSeconds = 10

	sessionStore := newMockSessionStore()
	executor := NewJobExecutor(tempDir, sessionStore, config)

	// Create mock AI executor with a small delay to ensure measurable time
	aiExec := &funcMockAIExecutor{
		execFunc: func(ctx context.Context, session *sessions.Session, prompt, model string) (AIResponse, error) {
			time.Sleep(50 * time.Millisecond)
			return &mockAIResponse{content: "HEARTBEAT_OK - All systems normal"}, nil
		},
	}

	result, err := executor.ExecuteHeartbeatJob(context.Background(), aiExec)
	if err != nil {
		t.Fatalf("ExecuteHeartbeatJob failed: %v", err)
	}

	// The execution time should be positive and at least as long as the mock delay
	if result.ExecutionTime <= 0 {
		t.Errorf("Expected positive ExecutionTime, got %v", result.ExecutionTime)
	}

	if result.ExecutionTime < 50*time.Millisecond {
		t.Errorf("Expected ExecutionTime >= 50ms (mock delay), got %v", result.ExecutionTime)
	}
}
