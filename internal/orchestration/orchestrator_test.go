package orchestration

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockRunner is a configurable mock AgentRunner for testing.
type mockRunner struct {
	mu        sync.Mutex
	calls     []mockRunCall
	runFunc   func(ctx context.Context, agentID string, config AgentConfig, message string, shared *SharedContext) (AgentResult, error)
	callCount int32 // atomic
}

type mockRunCall struct {
	AgentID string
	Config  AgentConfig
	Message string
}

func (m *mockRunner) Run(ctx context.Context, agentID string, config AgentConfig, message string, shared *SharedContext) (AgentResult, error) {
	atomic.AddInt32(&m.callCount, 1)
	m.mu.Lock()
	m.calls = append(m.calls, mockRunCall{AgentID: agentID, Config: config, Message: message})
	m.mu.Unlock()

	if m.runFunc != nil {
		return m.runFunc(ctx, agentID, config, message, shared)
	}
	return AgentResult{
		AgentID:    agentID,
		Response:   fmt.Sprintf("response from %s", config.Role),
		TokensUsed: 100,
	}, nil
}

func (m *mockRunner) getCalls() []mockRunCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]mockRunCall, len(m.calls))
	copy(result, m.calls)
	return result
}

// --- Orchestrator tests ---

func TestCreateAgent(t *testing.T) {
	runner := &mockRunner{}
	orch := NewOrchestrator(runner)

	id, err := orch.CreateAgent(AgentConfig{Role: "researcher"})
	require.NoError(t, err)
	assert.Contains(t, id, "agent_")
	assert.Equal(t, 1, orch.AgentCount())

	status, err := orch.GetAgentStatus(id)
	require.NoError(t, err)
	assert.Equal(t, AgentStatusIdle, status)
}

func TestCreateAgentAfterShutdown(t *testing.T) {
	runner := &mockRunner{}
	orch := NewOrchestrator(runner)
	require.NoError(t, orch.Shutdown())

	_, err := orch.CreateAgent(AgentConfig{Role: "test"})
	assert.ErrorIs(t, err, ErrOrchestratorShutdown)
}

func TestSendToAgent(t *testing.T) {
	runner := &mockRunner{}
	orch := NewOrchestrator(runner)
	ctx := context.Background()

	id, err := orch.CreateAgent(AgentConfig{Role: "worker"})
	require.NoError(t, err)

	err = orch.SendToAgent(ctx, id, "do the thing")
	require.NoError(t, err)

	// Wait for completion
	results := orch.CollectResults(5 * time.Second)
	require.Len(t, results, 1)
	assert.Equal(t, AgentStatusCompleted, results[0].Status)
	assert.Equal(t, "response from worker", results[0].Response)
}

func TestSendToNonexistentAgent(t *testing.T) {
	runner := &mockRunner{}
	orch := NewOrchestrator(runner)
	ctx := context.Background()

	err := orch.SendToAgent(ctx, "nonexistent", "hello")
	assert.ErrorIs(t, err, ErrAgentNotFound)
}

func TestSendToAgentBusy(t *testing.T) {
	blockCh := make(chan struct{})
	runner := &mockRunner{
		runFunc: func(ctx context.Context, agentID string, config AgentConfig, message string, shared *SharedContext) (AgentResult, error) {
			<-blockCh // block until released
			return AgentResult{Response: "done"}, nil
		},
	}
	orch := NewOrchestrator(runner)
	ctx := context.Background()

	id, err := orch.CreateAgent(AgentConfig{Role: "slow"})
	require.NoError(t, err)

	err = orch.SendToAgent(ctx, id, "first")
	require.NoError(t, err)

	// Small delay to let goroutine start
	time.Sleep(20 * time.Millisecond)

	err = orch.SendToAgent(ctx, id, "second")
	assert.ErrorIs(t, err, ErrAgentBusy)

	close(blockCh)
	orch.CollectResults(2 * time.Second) // wait for clean shutdown
}

func TestAgentCancellation(t *testing.T) {
	runner := &mockRunner{
		runFunc: func(ctx context.Context, agentID string, config AgentConfig, message string, shared *SharedContext) (AgentResult, error) {
			<-ctx.Done()
			return AgentResult{}, ctx.Err()
		},
	}
	orch := NewOrchestrator(runner)

	id, err := orch.CreateAgent(AgentConfig{Role: "cancellable"})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	err = orch.SendToAgent(ctx, id, "work")
	require.NoError(t, err)

	// Cancel after a brief delay
	time.Sleep(20 * time.Millisecond)
	cancel()

	results := orch.CollectResults(2 * time.Second)
	require.Len(t, results, 1)
	assert.Equal(t, AgentStatusCancelled, results[0].Status)
}

func TestAgentFailure(t *testing.T) {
	runner := &mockRunner{
		runFunc: func(ctx context.Context, agentID string, config AgentConfig, message string, shared *SharedContext) (AgentResult, error) {
			return AgentResult{}, errors.New("something went wrong")
		},
	}
	orch := NewOrchestrator(runner)
	ctx := context.Background()

	id, err := orch.CreateAgent(AgentConfig{Role: "failer"})
	require.NoError(t, err)

	err = orch.SendToAgent(ctx, id, "fail please")
	require.NoError(t, err)

	results := orch.CollectResults(5 * time.Second)
	require.Len(t, results, 1)
	assert.Equal(t, AgentStatusFailed, results[0].Status)
	assert.Error(t, results[0].Error)
}

func TestBroadcastContext(t *testing.T) {
	runner := &mockRunner{
		runFunc: func(ctx context.Context, agentID string, config AgentConfig, message string, shared *SharedContext) (AgentResult, error) {
			val := shared.GetString("broadcast_key")
			return AgentResult{Response: val}, nil
		},
	}
	orch := NewOrchestrator(runner)
	ctx := context.Background()

	err := orch.BroadcastContext("broadcast_key", "shared_value")
	require.NoError(t, err)

	id, err := orch.CreateAgent(AgentConfig{Role: "reader"})
	require.NoError(t, err)

	err = orch.SendToAgent(ctx, id, "read context")
	require.NoError(t, err)

	results := orch.CollectResults(5 * time.Second)
	require.Len(t, results, 1)
	assert.Equal(t, "shared_value", results[0].Response)
}

func TestCollectResultsTimeout(t *testing.T) {
	runner := &mockRunner{
		runFunc: func(ctx context.Context, agentID string, config AgentConfig, message string, shared *SharedContext) (AgentResult, error) {
			time.Sleep(5 * time.Second)
			return AgentResult{Response: "late"}, nil
		},
	}
	orch := NewOrchestrator(runner)
	ctx := context.Background()

	id, err := orch.CreateAgent(AgentConfig{Role: "slow"})
	require.NoError(t, err)

	err = orch.SendToAgent(ctx, id, "slow work")
	require.NoError(t, err)

	// Short timeout should return before the agent completes
	results := orch.CollectResults(50 * time.Millisecond)
	require.Len(t, results, 1)
	assert.Equal(t, AgentStatusRunning, results[0].Status)

	// Clean up
	require.NoError(t, orch.Shutdown())
}

func TestShutdownCancelsAgents(t *testing.T) {
	cancelledCh := make(chan struct{})
	runner := &mockRunner{
		runFunc: func(ctx context.Context, agentID string, config AgentConfig, message string, shared *SharedContext) (AgentResult, error) {
			<-ctx.Done()
			close(cancelledCh)
			return AgentResult{}, ctx.Err()
		},
	}
	orch := NewOrchestrator(runner)
	ctx := context.Background()

	_, err := orch.CreateAgent(AgentConfig{Role: "long-running"})
	require.NoError(t, err)

	err = orch.SendToAgent(ctx, "agent_1", "work forever")
	require.NoError(t, err)

	time.Sleep(20 * time.Millisecond)

	err = orch.Shutdown()
	require.NoError(t, err)

	// Agent should have been cancelled
	select {
	case <-cancelledCh:
		// Good, agent was cancelled
	case <-time.After(2 * time.Second):
		t.Fatal("agent was not cancelled during shutdown")
	}
}

func TestDoubleShutdown(t *testing.T) {
	runner := &mockRunner{}
	orch := NewOrchestrator(runner)

	err := orch.Shutdown()
	require.NoError(t, err)

	err = orch.Shutdown()
	assert.ErrorIs(t, err, ErrOrchestratorShutdown)
}

func TestGetAgentResult(t *testing.T) {
	runner := &mockRunner{}
	orch := NewOrchestrator(runner)
	ctx := context.Background()

	id, err := orch.CreateAgent(AgentConfig{Role: "worker"})
	require.NoError(t, err)

	// Before sending, result should be nil
	result, err := orch.GetAgentResult(id)
	require.NoError(t, err)
	assert.Nil(t, result)

	err = orch.SendToAgent(ctx, id, "work")
	require.NoError(t, err)

	orch.CollectResults(5 * time.Second)

	result, err = orch.GetAgentResult(id)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, AgentStatusCompleted, result.Status)
}

func TestGroupContext(t *testing.T) {
	runner := &mockRunner{}
	orch := NewOrchestrator(runner)

	grpA := orch.GroupContext("team-a")
	grpB := orch.GroupContext("team-b")

	grpA.Set("key", "value-a")
	grpB.Set("key", "value-b")

	assert.Equal(t, "value-a", grpA.GetString("key"))
	assert.Equal(t, "value-b", grpB.GetString("key"))

	// Same group returns same context
	grpA2 := orch.GroupContext("team-a")
	assert.Equal(t, "value-a", grpA2.GetString("key"))
}

func TestMultipleAgentsConcurrent(t *testing.T) {
	var counter int32
	runner := &mockRunner{
		runFunc: func(ctx context.Context, agentID string, config AgentConfig, message string, shared *SharedContext) (AgentResult, error) {
			atomic.AddInt32(&counter, 1)
			time.Sleep(10 * time.Millisecond)
			return AgentResult{Response: fmt.Sprintf("done-%s", agentID)}, nil
		},
	}
	orch := NewOrchestrator(runner)
	ctx := context.Background()

	const n = 10
	for i := 0; i < n; i++ {
		id, err := orch.CreateAgent(AgentConfig{Role: fmt.Sprintf("worker-%d", i)})
		require.NoError(t, err)
		err = orch.SendToAgent(ctx, id, "go")
		require.NoError(t, err)
	}

	results := orch.CollectResults(10 * time.Second)
	assert.Len(t, results, n)
	assert.Equal(t, int32(n), atomic.LoadInt32(&counter))

	for _, r := range results {
		assert.Equal(t, AgentStatusCompleted, r.Status)
	}
}
