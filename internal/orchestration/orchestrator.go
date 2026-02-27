package orchestration

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// AgentStatus represents the current state of an orchestrated agent.
type AgentStatus string

const (
	AgentStatusIdle      AgentStatus = "idle"
	AgentStatusRunning   AgentStatus = "running"
	AgentStatusCompleted AgentStatus = "completed"
	AgentStatusFailed    AgentStatus = "failed"
	AgentStatusCancelled AgentStatus = "cancelled"
)

// AgentConfig holds the configuration for spawning a new agent.
type AgentConfig struct {
	Role         string   `json:"role"`
	SystemPrompt string   `json:"system_prompt"`
	Model        string   `json:"model,omitempty"`
	ToolsAllowed []string `json:"tools_allowed,omitempty"`
	MaxTurns     int      `json:"max_turns,omitempty"`
}

// AgentResult holds the outcome of an agent's work.
type AgentResult struct {
	AgentID    string        `json:"agent_id"`
	Response   string        `json:"response"`
	Status     AgentStatus   `json:"status"`
	TokensUsed int           `json:"tokens_used"`
	Duration   time.Duration `json:"duration"`
	Error      error         `json:"-"`
}

// AgentRunner is the interface that the orchestrator uses to execute agent work.
// Callers provide an implementation that connects to the actual AI provider or
// uses a mock for testing.
type AgentRunner interface {
	// Run executes the agent's task and returns the response.
	// The context carries cancellation signals from the orchestrator.
	// The SharedContext provides read/write access to shared state.
	Run(ctx context.Context, agentID string, config AgentConfig, message string, shared *SharedContext) (AgentResult, error)
}

// agentEntry tracks a single orchestrated agent.
type agentEntry struct {
	mu        sync.Mutex
	id        string
	config    AgentConfig
	status    AgentStatus
	result    *AgentResult
	cancel    context.CancelFunc
	startTime time.Time
	doneCh    chan struct{} // closed when agent finishes
}

// Orchestrator manages multiple agent sessions with shared context.
type Orchestrator struct {
	mu            sync.RWMutex
	agents        map[string]*agentEntry
	globalContext *SharedContext
	groupContexts map[string]*SharedContext
	runner        AgentRunner
	idCounter     uint64
	closed        int32 // atomic; 1 when Shutdown has been called
}

// NewOrchestrator creates a new multi-agent orchestrator.
func NewOrchestrator(runner AgentRunner) *Orchestrator {
	return &Orchestrator{
		agents:        make(map[string]*agentEntry),
		globalContext: NewSharedContext(ScopeGlobal),
		groupContexts: make(map[string]*SharedContext),
		runner:        runner,
	}
}

// GlobalContext returns the orchestrator's global shared context.
func (o *Orchestrator) GlobalContext() *SharedContext {
	return o.globalContext
}

// GroupContext returns (or creates) a scoped context for the given group name.
func (o *Orchestrator) GroupContext(group string) *SharedContext {
	o.mu.Lock()
	defer o.mu.Unlock()
	ctx, ok := o.groupContexts[group]
	if !ok {
		ctx = NewSharedContext(ScopeGroup)
		o.groupContexts[group] = ctx
	}
	return ctx
}

// CreateAgent spawns a new agent with the given configuration.
// Returns the agent ID that can be used to interact with the agent.
func (o *Orchestrator) CreateAgent(config AgentConfig) (string, error) {
	if atomic.LoadInt32(&o.closed) == 1 {
		return "", ErrOrchestratorShutdown
	}

	id := fmt.Sprintf("agent_%d", atomic.AddUint64(&o.idCounter, 1))

	entry := &agentEntry{
		id:     id,
		config: config,
		status: AgentStatusIdle,
		doneCh: make(chan struct{}),
	}

	o.mu.Lock()
	o.agents[id] = entry
	o.mu.Unlock()

	return id, nil
}

// SendToAgent sends a message to a specific agent, triggering its execution.
// The agent runs asynchronously; use CollectResults or GetAgentStatus to
// check on progress.
func (o *Orchestrator) SendToAgent(ctx context.Context, agentID string, message string) error {
	if atomic.LoadInt32(&o.closed) == 1 {
		return ErrOrchestratorShutdown
	}

	o.mu.RLock()
	entry, ok := o.agents[agentID]
	o.mu.RUnlock()
	if !ok {
		return fmt.Errorf("%w: %s", ErrAgentNotFound, agentID)
	}

	entry.mu.Lock()
	if entry.status == AgentStatusRunning {
		entry.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrAgentBusy, agentID)
	}
	agentCtx, cancel := context.WithCancel(ctx)
	entry.status = AgentStatusRunning
	entry.cancel = cancel
	entry.startTime = time.Now()
	// Reset doneCh if agent is being re-run
	select {
	case <-entry.doneCh:
		entry.doneCh = make(chan struct{})
	default:
	}
	entry.mu.Unlock()

	go o.runAgent(agentCtx, entry, message)

	return nil
}

// runAgent executes the agent runner in a goroutine.
func (o *Orchestrator) runAgent(ctx context.Context, entry *agentEntry, message string) {
	defer func() {
		entry.mu.Lock()
		if entry.cancel != nil {
			entry.cancel()
			entry.cancel = nil
		}
		close(entry.doneCh)
		entry.mu.Unlock()
	}()

	result, err := o.runner.Run(ctx, entry.id, entry.config, message, o.globalContext)

	entry.mu.Lock()
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			entry.status = AgentStatusCancelled
		} else {
			entry.status = AgentStatusFailed
		}
		result.Error = err
		result.Status = entry.status
	} else {
		entry.status = AgentStatusCompleted
		result.Status = AgentStatusCompleted
	}
	result.AgentID = entry.id
	result.Duration = time.Since(entry.startTime)
	entry.result = &result
	entry.mu.Unlock()
}

// BroadcastContext sets a key-value pair in the global shared context,
// making it available to all agents.
func (o *Orchestrator) BroadcastContext(key string, value interface{}) error {
	if atomic.LoadInt32(&o.closed) == 1 {
		return ErrOrchestratorShutdown
	}
	o.globalContext.Set(key, value)
	return nil
}

// GetAgentStatus returns the current status of a specific agent.
func (o *Orchestrator) GetAgentStatus(agentID string) (AgentStatus, error) {
	o.mu.RLock()
	entry, ok := o.agents[agentID]
	o.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrAgentNotFound, agentID)
	}

	entry.mu.Lock()
	defer entry.mu.Unlock()
	return entry.status, nil
}

// GetAgentResult returns the result of a completed agent, or nil if not yet finished.
func (o *Orchestrator) GetAgentResult(agentID string) (*AgentResult, error) {
	o.mu.RLock()
	entry, ok := o.agents[agentID]
	o.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrAgentNotFound, agentID)
	}

	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.result == nil {
		return nil, nil
	}
	result := *entry.result
	return &result, nil
}

// CollectResults waits for all agents to complete (or the timeout to expire)
// and returns their results.
func (o *Orchestrator) CollectResults(timeout time.Duration) []AgentResult {
	deadline := time.After(timeout)

	o.mu.RLock()
	entries := make([]*agentEntry, 0, len(o.agents))
	for _, entry := range o.agents {
		entries = append(entries, entry)
	}
	o.mu.RUnlock()

	// Wait for each agent to finish or timeout
	for _, entry := range entries {
		entry.mu.Lock()
		doneCh := entry.doneCh
		status := entry.status
		entry.mu.Unlock()

		// Only wait if agent is running
		if status == AgentStatusRunning {
			select {
			case <-doneCh:
			case <-deadline:
				// Timeout reached; collect whatever is available
				goto collect
			}
		}
	}

collect:
	results := make([]AgentResult, 0, len(entries))
	for _, entry := range entries {
		entry.mu.Lock()
		if entry.result != nil {
			results = append(results, *entry.result)
		} else {
			// Agent hasn't finished yet; report current status
			results = append(results, AgentResult{
				AgentID:  entry.id,
				Status:   entry.status,
				Duration: time.Since(entry.startTime),
			})
		}
		entry.mu.Unlock()
	}
	return results
}

// Shutdown gracefully shuts down all running agents and prevents new ones from starting.
func (o *Orchestrator) Shutdown() error {
	if !atomic.CompareAndSwapInt32(&o.closed, 0, 1) {
		return ErrOrchestratorShutdown // already shut down
	}

	o.mu.RLock()
	entries := make([]*agentEntry, 0, len(o.agents))
	for _, entry := range o.agents {
		entries = append(entries, entry)
	}
	o.mu.RUnlock()

	for _, entry := range entries {
		entry.mu.Lock()
		if entry.cancel != nil {
			entry.cancel()
		}
		entry.mu.Unlock()
	}

	// Wait briefly for agents to wind down
	for _, entry := range entries {
		entry.mu.Lock()
		doneCh := entry.doneCh
		entry.mu.Unlock()

		select {
		case <-doneCh:
		case <-time.After(5 * time.Second):
		}
	}

	return nil
}

// AgentCount returns the total number of agents (any status).
func (o *Orchestrator) AgentCount() int {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return len(o.agents)
}

// Sentinel errors for the orchestration package.
var (
	ErrAgentNotFound        = errors.New("agent not found")
	ErrAgentBusy            = errors.New("agent is already running")
	ErrOrchestratorShutdown = errors.New("orchestrator has been shut down")
)
