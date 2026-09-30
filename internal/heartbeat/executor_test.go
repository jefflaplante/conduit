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

// finalizingAIExecutor records FinalizeSession calls (conduit-385r).
type finalizingAIExecutor struct {
	funcMockAIExecutor
	finalized []string
}

func (f *finalizingAIExecutor) FinalizeSession(s *sessions.Session) {
	f.finalized = append(f.finalized, s.Key)
}

// conduit-385r: the executor finalizes the run's session exactly once after
// the last attempt, on success, failure and panic.
func TestExecuteHeartbeatJob_FinalizesSessionOnce(t *testing.T) {
	tempDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tempDir, "HEARTBEAT.md"), []byte("# HEARTBEAT.md\n\n## Check status\nCheck the system status.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	config := DefaultExecutorConfig()
	config.TimeoutSeconds = 10
	config.RetryDelaySeconds = 0
	config.MaxRetries = 2

	run := func(t *testing.T, exec func(n int) (AIResponse, error)) (*finalizingAIExecutor, int) {
		t.Helper()
		calls := 0
		f := &finalizingAIExecutor{}
		f.execFunc = func(ctx context.Context, session *sessions.Session, prompt, model string) (AIResponse, error) {
			calls++
			if len(f.finalized) != 0 {
				t.Errorf("session finalized before attempt %d", calls)
			}
			return exec(calls)
		}
		func() {
			defer func() { _ = recover() }()
			_, _ = NewJobExecutor(tempDir, newMockSessionStore(), config).ExecuteHeartbeatJob(context.Background(), f)
		}()
		return f, calls
	}

	t.Run("success", func(t *testing.T) {
		f, _ := run(t, func(int) (AIResponse, error) { return &mockAIResponse{content: "HEARTBEAT_OK"}, nil })
		if len(f.finalized) != 1 {
			t.Fatalf("finalized %v, want once", f.finalized)
		}
	})
	t.Run("failure after retries", func(t *testing.T) {
		f, calls := run(t, func(int) (AIResponse, error) { return nil, errors.New("down") })
		if calls != 3 || len(f.finalized) != 1 {
			t.Fatalf("calls=%d finalized=%v, want 3 attempts then one finalize", calls, f.finalized)
		}
	})
	t.Run("panic", func(t *testing.T) {
		f, _ := run(t, func(int) (AIResponse, error) { panic("boom") })
		if len(f.finalized) != 1 {
			t.Fatalf("finalized %v, want once despite the panic", f.finalized)
		}
	})
}

// The result reports the key the store gave the session, not the channel ID
// the executor built it from (conduit-27cz).
func TestExecuteHeartbeatJob_ReportsStoredSessionKey(t *testing.T) {
	tempDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tempDir, "HEARTBEAT.md"), []byte("# HEARTBEAT.md\n\n## Check status\nCheck the system status.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	config := DefaultExecutorConfig()
	config.TimeoutSeconds = 10
	store := newMockSessionStore()

	var used string
	aiExec := &funcMockAIExecutor{
		execFunc: func(ctx context.Context, session *sessions.Session, prompt, model string) (AIResponse, error) {
			used = session.Key
			return &mockAIResponse{content: "HEARTBEAT_OK"}, nil
		},
	}
	result, err := NewJobExecutor(tempDir, store, config).ExecuteHeartbeatJob(context.Background(), aiExec)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.sessions[result.SessionKey]; !ok || result.SessionKey != used {
		t.Fatalf("SessionKey = %q, want the stored key %q", result.SessionKey, used)
	}
}
