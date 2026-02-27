package heartbeat

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"conduit/internal/ai"
	"conduit/internal/scheduler"
	"conduit/internal/sessions"
)

// GatewayIntegration provides integration between heartbeat execution and the gateway
type GatewayIntegration struct {
	executor         *JobExecutor
	aiRouter         *ai.Router
	scheduler        scheduler.SchedulerInterface
	channelSender    ChannelSender
	metricsCollector MetricsCollector
}

// ChannelSender interface for sending messages via channels
type ChannelSender interface {
	SendMessage(ctx context.Context, channelID, userID, content string, metadata map[string]string) error
}

// MetricsCollector interface for reporting heartbeat metrics
type MetricsCollector interface {
	MarkHeartbeatSuccess()
	MarkHeartbeatError()
	UpdateHeartbeatJobs(total, enabled int)
}

// NewGatewayIntegration creates a new gateway integration
func NewGatewayIntegration(workspaceDir string, sessionsStore *sessions.Store, aiRouter *ai.Router, scheduler scheduler.SchedulerInterface, channelSender ChannelSender, metricsCollector MetricsCollector) *GatewayIntegration {
	config := DefaultExecutorConfig()
	executor := NewJobExecutor(workspaceDir, sessionsStore, config)

	return &GatewayIntegration{
		executor:         executor,
		aiRouter:         aiRouter,
		scheduler:        scheduler,
		channelSender:    channelSender,
		metricsCollector: metricsCollector,
	}
}

// ExecuteHeartbeat executes a heartbeat job - this is called by the gateway's executeScheduledJob
func (g *GatewayIntegration) ExecuteHeartbeat(ctx context.Context, job *scheduler.Job) error {
	log.Printf("[HeartbeatIntegration] Executing heartbeat job: %s", job.ID)

	// Create AI executor adapter
	aiExecutor := &gatewayAIExecutor{
		aiRouter: g.aiRouter,
	}

	// Execute the heartbeat
	result, err := g.executor.ExecuteHeartbeatJob(ctx, aiExecutor)
	if err != nil {
		log.Printf("[HeartbeatIntegration] Heartbeat execution failed: %v", err)

		// Report error to metrics collector
		if g.metricsCollector != nil {
			g.metricsCollector.MarkHeartbeatError()
		}

		return fmt.Errorf("heartbeat execution failed: %w", err)
	}

	log.Printf("[HeartbeatIntegration] Heartbeat completed: status=%s, actions=%d",
		result.Status, len(result.Actions))

	// Report success to metrics collector (even if result processing fails)
	if g.metricsCollector != nil {
		g.metricsCollector.MarkHeartbeatSuccess()
	}

	// Process the result
	if err := g.processHeartbeatResult(ctx, result, job); err != nil {
		log.Printf("[HeartbeatIntegration] Failed to process heartbeat result: %v", err)
		return fmt.Errorf("failed to process heartbeat result: %w", err)
	}

	return nil
}

// processHeartbeatResult processes the heartbeat execution result and takes appropriate actions
func (g *GatewayIntegration) processHeartbeatResult(ctx context.Context, result *HeartbeatResult, job *scheduler.Job) error {
	switch result.Status {
	case ResultStatusOK:
		// HEARTBEAT_OK - log and optionally send to target if configured for verbose mode
		log.Printf("[HeartbeatIntegration] Heartbeat OK - no action needed")
		if g.shouldSendOKStatus(job) {
			return g.sendToTarget(ctx, job.Target, "HEARTBEAT_OK")
		}
		return nil

	case ResultStatusAction, ResultStatusAlert:
		// Process actions
		return g.executeActions(ctx, result.Actions, job)

	case ResultStatusError:
		// Send error notification
		errorMsg := fmt.Sprintf("❌ Heartbeat error: %s", result.Message)
		return g.sendToTarget(ctx, job.Target, errorMsg)

	case ResultStatusNoAction:
		// No tasks found - this might indicate a configuration issue
		log.Printf("[HeartbeatIntegration] No heartbeat tasks found")
		if g.shouldSendOKStatus(job) {
			return g.sendToTarget(ctx, job.Target, "No heartbeat tasks configured")
		}
		return nil

	default:
		return fmt.Errorf("unknown result status: %s", result.Status)
	}
}

// executeActions processes and executes the actions from a heartbeat result
func (g *GatewayIntegration) executeActions(ctx context.Context, actions []HeartbeatAction, job *scheduler.Job) error {
	if len(actions) == 0 {
		return nil
	}

	log.Printf("[HeartbeatIntegration] Executing %d actions", len(actions))

	// Group actions by priority and type
	immediate, delayed := g.categorizeActions(actions)

	// Execute immediate actions first
	for _, action := range immediate {
		if err := g.executeAction(ctx, action, job); err != nil {
			log.Printf("[HeartbeatIntegration] Failed to execute immediate action: %v", err)
			// Continue with other actions even if one fails
		}
	}

	// Execute delayed actions (could be scheduled for later or executed based on quiet hours)
	for _, action := range delayed {
		if g.shouldExecuteDelayedAction(action) {
			if err := g.executeAction(ctx, action, job); err != nil {
				log.Printf("[HeartbeatIntegration] Failed to execute delayed action: %v", err)
			}
		} else {
			log.Printf("[HeartbeatIntegration] Delaying action due to quiet hours: %s", action.Content)
			// Could schedule for later execution here
		}
	}

	return nil
}

// executeAction executes a single heartbeat action
func (g *GatewayIntegration) executeAction(ctx context.Context, action HeartbeatAction, job *scheduler.Job) error {
	switch action.Type {
	case ActionTypeAlert:
		return g.sendAlert(ctx, action, job)

	case ActionTypeNotification:
		return g.sendNotification(ctx, action, job)

	case ActionTypeDelivery:
		return g.sendDelivery(ctx, action, job)

	case ActionTypeCommand:
		return g.executeCommand(ctx, action, job)

	default:
		return fmt.Errorf("unknown action type: %s", action.Type)
	}
}

// sendAlert sends an alert message
func (g *GatewayIntegration) sendAlert(ctx context.Context, action HeartbeatAction, job *scheduler.Job) error {
	target := g.resolveTarget(action.Target, job.Target)

	// Format alert message with appropriate urgency indicators
	var prefix string
	switch action.Priority {
	case TaskPriorityCritical:
		prefix = "🚨 CRITICAL ALERT"
	case TaskPriorityHigh:
		prefix = "⚠️ ALERT"
	default:
		prefix = "ℹ️ Alert"
	}

	message := fmt.Sprintf("%s: %s", prefix, action.Content)
	return g.sendToTarget(ctx, target, message)
}

// sendNotification sends a regular notification
func (g *GatewayIntegration) sendNotification(ctx context.Context, action HeartbeatAction, job *scheduler.Job) error {
	target := g.resolveTarget(action.Target, job.Target)

	// Add notification emoji based on priority
	var prefix string
	switch action.Priority {
	case TaskPriorityCritical:
		prefix = "🔴"
	case TaskPriorityHigh:
		prefix = "🟡"
	default:
		prefix = "💡"
	}

	message := fmt.Sprintf("%s %s", prefix, action.Content)
	return g.sendToTarget(ctx, target, message)
}

// sendDelivery sends a delivery message (similar to notification but may respect quiet hours)
func (g *GatewayIntegration) sendDelivery(ctx context.Context, action HeartbeatAction, job *scheduler.Job) error {
	target := g.resolveTarget(action.Target, job.Target)
	return g.sendToTarget(ctx, target, action.Content)
}

// executeCommand executes a system command action
func (g *GatewayIntegration) executeCommand(ctx context.Context, action HeartbeatAction, job *scheduler.Job) error {
	// For safety, we'll log the command but not execute it directly
	// In a production system, you might want to have a whitelist of allowed commands
	log.Printf("[HeartbeatIntegration] Command action detected: %s", action.Content)

	// Extract command from metadata if available
	if action.Metadata != nil {
		if command, ok := action.Metadata["command"].(string); ok && command != "" {
			log.Printf("[HeartbeatIntegration] Extracted command: %s", command)
			// Here you could execute the command if it's in an allowlist
			// For now, we'll just log it for security reasons
		}
	}

	// Send a notification about the command that was requested
	target := g.resolveTarget(action.Target, job.Target)
	message := fmt.Sprintf("🔧 Maintenance action: %s", action.Content)
	return g.sendToTarget(ctx, target, message)
}

// categorizeActions splits actions into immediate and delayed based on priority and quiet hours
func (g *GatewayIntegration) categorizeActions(actions []HeartbeatAction) (immediate []HeartbeatAction, delayed []HeartbeatAction) {
	for _, action := range actions {
		// Critical and high priority actions are always immediate
		if action.Priority == TaskPriorityCritical || action.Priority == TaskPriorityHigh || action.Type == ActionTypeAlert {
			immediate = append(immediate, action)
		} else {
			// Check if action should respect quiet hours
			if quietAware, ok := action.Metadata["quiet_aware"].(bool); ok && quietAware {
				delayed = append(delayed, action)
			} else {
				immediate = append(immediate, action)
			}
		}
	}

	return immediate, delayed
}

// shouldExecuteDelayedAction determines if a delayed action should be executed now
func (g *GatewayIntegration) shouldExecuteDelayedAction(action HeartbeatAction) bool {
	// For now, we'll use a simple time-based check
	// In a full implementation, this would integrate with the AlertSeverityRouter's quiet hours logic
	now := time.Now()
	hour := now.Hour()

	// Assume PT timezone quiet hours: 10 PM to 8 AM (22:00 to 08:00)
	// This is a simplified check - real implementation would use proper timezone handling
	isQuietHours := hour >= 22 || hour < 8

	// If it's not quiet hours, execute the action
	if !isQuietHours {
		return true
	}

	// During quiet hours, only execute if not marked as quiet-aware
	if quietAware, ok := action.Metadata["quiet_aware"].(bool); ok && quietAware {
		return false
	}

	return true
}

// resolveTarget determines the final target for message delivery
func (g *GatewayIntegration) resolveTarget(actionTarget, jobTarget string) string {
	// If action specifies a target, use it
	if actionTarget != "" && actionTarget != "telegram" {
		return actionTarget
	}

	// Fall back to job target
	if jobTarget != "" {
		return jobTarget
	}

	// Default fallback
	return "telegram"
}

// sendToTarget sends a message to the specified target
func (g *GatewayIntegration) sendToTarget(ctx context.Context, target, message string) error {
	// Suppress silent response tokens from being delivered to channels
	upper := strings.ToUpper(strings.TrimSpace(message))
	if strings.Contains(upper, "NO_REPLY") || strings.Contains(upper, "HEARTBEAT_OK") {
		log.Printf("[HeartbeatIntegration] Silent token suppressed, not delivering to %s", target)
		return nil
	}

	if g.channelSender == nil {
		log.Printf("[HeartbeatIntegration] No channel sender configured, would send: %s", message)
		return nil
	}

	// Parse target format: "telegram:chatid" or just "chatid"
	parts := strings.SplitN(target, ":", 2)
	var channelID, userID string

	if len(parts) == 2 {
		channelID = parts[0]
		userID = parts[1]
	} else {
		channelID = "telegram" // Default to Telegram
		userID = target
	}

	return g.channelSender.SendMessage(ctx, channelID, userID, message, nil)
}

// shouldSendOKStatus determines if HEARTBEAT_OK status should be sent to target
func (g *GatewayIntegration) shouldSendOKStatus(job *scheduler.Job) bool {
	// Check job metadata for verbose mode
	if job.Metadata != nil {
		if verbose, ok := job.Metadata["verbose"].(bool); ok && verbose {
			return true
		}
	}

	// By default, don't send HEARTBEAT_OK to avoid spam
	return false
}

// ScheduleHeartbeatJob schedules a new heartbeat job in the scheduler
func (g *GatewayIntegration) ScheduleHeartbeatJob(schedule, target, model string, enabled bool) error {
	if g.scheduler == nil {
		return fmt.Errorf("scheduler not available")
	}

	job := &scheduler.Job{
		ID:       fmt.Sprintf("heartbeat_%d", time.Now().UnixNano()),
		Name:     "Heartbeat Task Execution",
		Schedule: schedule,
		Type:     scheduler.JobTypeGo,
		Command:  "heartbeat", // This will be handled specially by gateway
		Model:    model,
		Target:   target,
		Enabled:  enabled,
		Metadata: map[string]interface{}{
			"heartbeat": true,
			"version":   "1.0",
		},
	}

	return g.scheduler.AddJob(job)
}

// gatewayAIExecutor adapts the gateway's AI router to the AIExecutor interface
type gatewayAIExecutor struct {
	aiRouter *ai.Router
}

// ExecutePrompt executes an AI prompt using the gateway's AI router
func (g *gatewayAIExecutor) ExecutePrompt(ctx context.Context, session *sessions.Session, prompt, model string) (AIResponse, error) {
	response, err := g.aiRouter.GenerateResponseWithTools(ctx, session, prompt, "", model)
	if err != nil {
		return nil, err
	}

	return &aiResponseAdapter{response: response}, nil
}

// aiResponseAdapter adapts ai.ConversationResponse to AIResponse interface
type aiResponseAdapter struct {
	response ai.ConversationResponse
}

// GetContent returns the response content
func (a *aiResponseAdapter) GetContent() string {
	return a.response.GetContent()
}

// GetUsage returns the response usage information
func (a *aiResponseAdapter) GetUsage() interface{} {
	return a.response.GetUsage()
}

// IsHeartbeatJob checks if a scheduler job is a heartbeat job
func IsHeartbeatJob(job *scheduler.Job) bool {
	if job == nil || job.Metadata == nil {
		return false
	}

	if isHeartbeat, ok := job.Metadata["heartbeat"].(bool); ok && isHeartbeat {
		return true
	}

	// Also check if command is "heartbeat"
	return job.Command == "heartbeat"
}

// GetHeartbeatJobCount returns the number of heartbeat jobs in the scheduler
func (g *GatewayIntegration) GetHeartbeatJobCount() int {
	if g.scheduler == nil {
		return 0
	}

	jobs := g.scheduler.ListJobs()
	count := 0

	for _, job := range jobs {
		if IsHeartbeatJob(job) {
			count++
		}
	}

	return count
}

// RemoveHeartbeatJobs removes all heartbeat jobs from the scheduler
func (g *GatewayIntegration) RemoveHeartbeatJobs() error {
	if g.scheduler == nil {
		return fmt.Errorf("scheduler not available")
	}

	jobs := g.scheduler.ListJobs()
	var errors []string

	for _, job := range jobs {
		if IsHeartbeatJob(job) {
			if err := g.scheduler.RemoveJob(job.ID); err != nil {
				errors = append(errors, fmt.Sprintf("failed to remove job %s: %v", job.ID, err))
			}
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("errors removing heartbeat jobs: %s", strings.Join(errors, "; "))
	}

	return nil
}
