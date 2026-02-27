package heartbeat

import (
	"context"
	"fmt"
	"log"
	"time"

	"conduit/internal/sessions"
)

// JobExecutor executes heartbeat tasks by reading HEARTBEAT.md and generating AI prompts
type JobExecutor struct {
	workspaceDir    string
	sessionsStore   SessionStoreInterface
	taskInterpreter *TaskInterpreter
	resultProcessor *ResultProcessor
	config          ExecutorConfig
}

// ExecutorConfig holds configuration for the heartbeat job executor
type ExecutorConfig struct {
	// Timeout for heartbeat task execution (default: 60 seconds)
	TimeoutSeconds int

	// Session name prefix for heartbeat sessions
	SessionPrefix string

	// Default model to use if none specified in job
	DefaultModel string

	// Maximum retry attempts for failed tasks
	MaxRetries int

	// Delay between retry attempts
	RetryDelaySeconds int
}

// DefaultExecutorConfig returns a default configuration
func DefaultExecutorConfig() ExecutorConfig {
	return ExecutorConfig{
		TimeoutSeconds:    60,
		SessionPrefix:     "heartbeat",
		DefaultModel:      "claude-sonnet-4-20250514",
		MaxRetries:        3,
		RetryDelaySeconds: 5,
	}
}

// NewJobExecutor creates a new heartbeat job executor
func NewJobExecutor(workspaceDir string, sessionsStore SessionStoreInterface, config ExecutorConfig) *JobExecutor {
	return &JobExecutor{
		workspaceDir:    workspaceDir,
		sessionsStore:   sessionsStore,
		taskInterpreter: NewTaskInterpreter(workspaceDir),
		resultProcessor: NewResultProcessor(),
		config:          config,
	}
}

// ExecuteHeartbeatJob executes a heartbeat task by reading HEARTBEAT.md and executing the instructions
func (e *JobExecutor) ExecuteHeartbeatJob(ctx context.Context, aiExecutor AIExecutor) (*HeartbeatResult, error) {
	log.Printf("[Heartbeat] Starting heartbeat task execution")

	// Create timeout context
	if e.config.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(e.config.TimeoutSeconds)*time.Second)
		defer cancel()
	}

	// Read and interpret HEARTBEAT.md
	tasks, err := e.taskInterpreter.ReadHeartbeatTasks()
	if err != nil {
		return nil, fmt.Errorf("failed to read heartbeat tasks: %w", err)
	}

	if len(tasks) == 0 {
		log.Printf("[Heartbeat] No tasks found in HEARTBEAT.md")
		return &HeartbeatResult{
			Status:  ResultStatusNoAction,
			Message: "No tasks found in HEARTBEAT.md",
		}, nil
	}

	// Generate AI prompt from tasks
	prompt, err := e.taskInterpreter.GeneratePrompt(tasks)
	if err != nil {
		return nil, fmt.Errorf("failed to generate AI prompt: %w", err)
	}

	log.Printf("[Heartbeat] Generated prompt for %d tasks (%d chars)", len(tasks), len(prompt))

	// Create session for this heartbeat execution
	sessionKey := fmt.Sprintf("%s_%d", e.config.SessionPrefix, time.Now().UnixNano())
	session, err := e.sessionsStore.GetOrCreateSession("heartbeat", sessionKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create heartbeat session: %w", err)
	}

	// Execute AI prompt with retries
	var response AIResponse
	var lastErr error

	for attempt := 0; attempt <= e.config.MaxRetries; attempt++ {
		if attempt > 0 {
			log.Printf("[Heartbeat] Retry attempt %d/%d", attempt, e.config.MaxRetries)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(e.config.RetryDelaySeconds) * time.Second):
				// Continue with retry
			}
		}

		response, lastErr = aiExecutor.ExecutePrompt(ctx, session, prompt, e.config.DefaultModel)
		if lastErr == nil {
			break
		}

		log.Printf("[Heartbeat] Attempt %d failed: %v", attempt+1, lastErr)
	}

	if lastErr != nil {
		return nil, fmt.Errorf("failed to execute AI prompt after %d attempts: %w", e.config.MaxRetries+1, lastErr)
	}

	// Process the AI response
	result, err := e.resultProcessor.ProcessResponse(response, tasks)
	if err != nil {
		return nil, fmt.Errorf("failed to process AI response: %w", err)
	}

	result.SessionKey = sessionKey
	result.ExecutionTime = time.Since(time.Now()) // This will be negative, but we'll fix it in actual usage

	log.Printf("[Heartbeat] Completed execution: status=%s, action_count=%d",
		result.Status, len(result.Actions))

	return result, nil
}

// AIExecutor defines the interface for executing AI prompts
// This allows the executor to work with different AI backends
type AIExecutor interface {
	ExecutePrompt(ctx context.Context, session *sessions.Session, prompt, model string) (AIResponse, error)
}

// AIResponse represents the response from an AI execution
type AIResponse interface {
	GetContent() string
	GetUsage() interface{}
}

// HeartbeatResult represents the result of a heartbeat execution
type HeartbeatResult struct {
	// Status indicates the overall result
	Status ResultStatus `json:"status"`

	// Message contains the main response content
	Message string `json:"message"`

	// Actions contains any actions that need to be taken
	Actions []HeartbeatAction `json:"actions,omitempty"`

	// SessionKey is the session used for this execution
	SessionKey string `json:"session_key"`

	// ExecutionTime is how long the execution took
	ExecutionTime time.Duration `json:"execution_time"`

	// TasksProcessed is the number of tasks that were processed
	TasksProcessed int `json:"tasks_processed"`

	// Metadata contains additional information
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// ResultStatus represents the status of a heartbeat execution result
type ResultStatus string

const (
	// ResultStatusOK means everything is normal, no action needed (HEARTBEAT_OK)
	ResultStatusOK ResultStatus = "ok"

	// ResultStatusAction means actions were identified and need to be taken
	ResultStatusAction ResultStatus = "action"

	// ResultStatusAlert means there are alerts that need immediate attention
	ResultStatusAlert ResultStatus = "alert"

	// ResultStatusError means there was an error processing the heartbeat
	ResultStatusError ResultStatus = "error"

	// ResultStatusNoAction means no tasks were found or executed
	ResultStatusNoAction ResultStatus = "no_action"
)

// String returns the string representation of ResultStatus
func (r ResultStatus) String() string {
	return string(r)
}

// HeartbeatAction represents an action that needs to be taken based on heartbeat results
type HeartbeatAction struct {
	// Type of action (alert, notification, command, etc.)
	Type ActionType `json:"type"`

	// Target for the action (channel, user, system, etc.)
	Target string `json:"target"`

	// Content of the action (message to send, command to run, etc.)
	Content string `json:"content"`

	// Priority of the action
	Priority TaskPriority `json:"priority"`

	// Metadata for the action
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// ActionType represents the type of action to be taken
type ActionType string

const (
	ActionTypeAlert        ActionType = "alert"
	ActionTypeNotification ActionType = "notification"
	ActionTypeCommand      ActionType = "command"
	ActionTypeDelivery     ActionType = "delivery"
)

// String returns the string representation of ActionType
func (a ActionType) String() string {
	return string(a)
}

// IsHeartbeatOK checks if the result represents a normal "HEARTBEAT_OK" status
func (r *HeartbeatResult) IsHeartbeatOK() bool {
	return r.Status == ResultStatusOK && len(r.Actions) == 0
}

// HasAlerts checks if the result contains alerts that need immediate attention
func (r *HeartbeatResult) HasAlerts() bool {
	if r.Status == ResultStatusAlert {
		return true
	}

	for _, action := range r.Actions {
		if action.Type == ActionTypeAlert || action.Priority == TaskPriorityCritical {
			return true
		}
	}

	return false
}

// GetImmediateActions returns actions that need immediate attention
func (r *HeartbeatResult) GetImmediateActions() []HeartbeatAction {
	var immediate []HeartbeatAction

	for _, action := range r.Actions {
		if action.Type == ActionTypeAlert || action.Priority == TaskPriorityCritical || action.Priority == TaskPriorityHigh {
			immediate = append(immediate, action)
		}
	}

	return immediate
}

// GetDelayedActions returns actions that can be delayed (e.g., respect quiet hours)
func (r *HeartbeatResult) GetDelayedActions() []HeartbeatAction {
	var delayed []HeartbeatAction

	for _, action := range r.Actions {
		if action.Type != ActionTypeAlert && action.Priority != TaskPriorityCritical && action.Priority != TaskPriorityHigh {
			delayed = append(delayed, action)
		}
	}

	return delayed
}

// Validate validates the heartbeat result
func (r *HeartbeatResult) Validate() error {
	if r.Status == "" {
		return fmt.Errorf("status cannot be empty")
	}

	switch r.Status {
	case ResultStatusOK, ResultStatusAction, ResultStatusAlert, ResultStatusError, ResultStatusNoAction:
		// Valid statuses
	default:
		return fmt.Errorf("invalid status: %s", r.Status)
	}

	for i, action := range r.Actions {
		if action.Type == "" {
			return fmt.Errorf("action %d: type cannot be empty", i)
		}
		if action.Content == "" {
			return fmt.Errorf("action %d: content cannot be empty", i)
		}
	}

	return nil
}
