package heartbeat

import (
	"context"
	"errors"
	"fmt"
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

// conduit-31jg.66: a heartbeat turn stopped by /stop or shutdown (the
// gateway's TurnRunner executor reports context.Canceled) is not retried.
func TestExecuteHeartbeatJob_StoppedTurnNotRetried(t *testing.T) {
	tempDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tempDir, "HEARTBEAT.md"), []byte("# HEARTBEAT.md\n\n## Check status\nCheck the system status.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	config := DefaultExecutorConfig()
	config.TimeoutSeconds = 10
	config.RetryDelaySeconds = 0
	executor := NewJobExecutor(tempDir, newMockSessionStore(), config)

	calls := 0
	aiExec := &funcMockAIExecutor{
		execFunc: func(ctx context.Context, session *sessions.Session, prompt, model string) (AIResponse, error) {
			calls++
			return nil, fmt.Errorf("heartbeat turn stopped: %w", context.Canceled)
		},
	}
	if _, err := executor.ExecuteHeartbeatJob(context.Background(), aiExec); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Fatalf("stopped turn attempted %d times, want 1 (no retry)", calls)
	}
}
