package heartbeat

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"conduit/internal/ai"
	"conduit/internal/channels"
	"conduit/internal/config"
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
	brainWriter      BrainWriter

	// conduit-31jg.33: quiet hours come from cfg.AgentHeartbeat (see
	// SetAgentHeartbeatConfig); deferred actions are persisted, not dropped.
	workspaceDir string
	deferMu      sync.Mutex // guards hbCfg, deferred
	flushMu      sync.Mutex // serializes FlushDeferred
	hbCfg        *config.AgentHeartbeatConfig
	deferred     *SharedAlertQueue
	now          func() time.Time // test hook; nil means time.Now
	// flushTimer fires FlushDeferred when quiet hours end, so deferred
	// actions don't wait up to one heartbeat interval (conduit-31jg.87).
	// Guarded by deferMu; flushTimerAt is its deadline.
	flushTimer   *time.Timer
	flushTimerAt time.Time

	// conduit-31jg.59: all delivery goes through a DeliveryRegistry
	// (circuit breaker + alert_history audit). Guarded by deferMu.
	delivery *DeliveryRegistry
	retries  retryState

	// aiExecutor, when set, runs the heartbeat prompt (the gateway installs
	// one backed by its TurnRunner, conduit-31jg.66); nil calls aiRouter
	// directly.
	aiExecutor AIExecutor
}

// BrainWriter is an optional callback interface for writing heartbeat alerts into
// Brain's sense.alerts.* namespace. This avoids importing brain directly and follows
// the existing DI patterns in the heartbeat package.
type BrainWriter interface {
	// StoreAlert writes an alert entry to Brain working memory.
	// key is the full dotted key (e.g. "sense.alerts.high_cpu").
	StoreAlert(ctx context.Context, key, value string) error
	// DeleteAlert removes a resolved alert from Brain.
	DeleteAlert(ctx context.Context, key string) error
	// ListAlertKeys returns all Brain keys under the given prefix.
	ListAlertKeys(ctx context.Context, prefix string) ([]string, error)
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

// NewGatewayIntegration creates a new gateway integration.
// model overrides the default AI model; timeoutSeconds overrides the per-execution timeout.
// Pass "" / 0 for built-in defaults.
func NewGatewayIntegration(workspaceDir string, sessionsStore *sessions.Store, aiRouter *ai.Router, scheduler scheduler.SchedulerInterface, channelSender ChannelSender, metricsCollector MetricsCollector, model string, timeoutSeconds int) *GatewayIntegration {
	config := DefaultExecutorConfig()
	if model != "" {
		config.DefaultModel = model
	}
	if timeoutSeconds > 0 {
		config.TimeoutSeconds = timeoutSeconds
	}
	executor := NewJobExecutor(workspaceDir, sessionsStore, config)

	// Default registry (no auditor) so delivery always has breaker
	// semantics; the gateway swaps in its audited registry via
	// SetDeliveryRegistry.
	delivery := NewDeliveryRegistry()
	delivery.Register(NewChannelSenderDeliverer(channelSender))

	g := &GatewayIntegration{
		executor:         executor,
		aiRouter:         aiRouter,
		scheduler:        scheduler,
		channelSender:    channelSender,
		metricsCollector: metricsCollector,
		workspaceDir:     workspaceDir,
		deferred:         NewSharedAlertQueue(deferredQueuePath(workspaceDir, "")),
		delivery:         delivery,
		retries:          newRetryState(),
	}
	g.deferred.SetClock(g.clock) // expiry judged by the same clock as deferAction
	return g
}

// SetBrainWriter sets the optional BrainWriter for persisting alerts to Brain's
// sense.alerts.* namespace. If nil, heartbeat results are processed normally without
// Brain integration.
func (g *GatewayIntegration) SetBrainWriter(bw BrainWriter) {
	g.brainWriter = bw
}

// SetAIExecutor overrides how the heartbeat prompt is executed. The gateway
// uses it to run heartbeat turns on its shared TurnRunner (transcript in the
// turn lock, ActiveRequests registration, usage/cost; conduit-31jg.66).
// Call before the scheduler starts.
func (g *GatewayIntegration) SetAIExecutor(e AIExecutor) {
	g.aiExecutor = e
}

// ExecuteHeartbeat executes a heartbeat job - this is called by the gateway's executeScheduledJob
func (g *GatewayIntegration) ExecuteHeartbeat(ctx context.Context, job *scheduler.Job) error {
	log.Printf("[HeartbeatIntegration] Executing heartbeat job: %s", job.ID)

	// conduit-31jg.33: deliver anything deferred during quiet hours first, so
	// a slow or failing AI call cannot hold it back.
	if n, err := g.FlushDeferred(ctx); err != nil {
		log.Printf("[HeartbeatIntegration] Deferred flush failed: %v", err)
	} else if n > 0 {
		log.Printf("[HeartbeatIntegration] Delivered %d deferred action(s)", n)
	}

	// Create AI executor adapter
	var aiExecutor AIExecutor = &gatewayAIExecutor{
		aiRouter: g.aiRouter,
	}
	if g.aiExecutor != nil {
		aiExecutor = g.aiExecutor
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
	// Write alerts to Brain's sense.alerts.* namespace (if Brain is available)
	g.syncAlertsToBrain(ctx, result, job)

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

// syncAlertsToBrain writes non-OK heartbeat results to Brain's sense.alerts.* namespace
// and clears resolved alerts when the heartbeat result is OK.
func (g *GatewayIntegration) syncAlertsToBrain(ctx context.Context, result *HeartbeatResult, job *scheduler.Job) {
	if g.brainWriter == nil {
		return
	}

	jobKey := SanitizeKeyComponent(job.Name)
	if jobKey == "" {
		jobKey = SanitizeKeyComponent(job.ID)
	}
	prefix := "sense.alerts."

	switch result.Status {
	case ResultStatusOK, ResultStatusNoAction:
		// Clear any previously stored alerts for this heartbeat job
		keys, err := g.brainWriter.ListAlertKeys(ctx, prefix+jobKey)
		if err != nil {
			log.Printf("[HeartbeatIntegration] Failed to list Brain alert keys for cleanup: %v", err)
			return
		}
		for _, key := range keys {
			if err := g.brainWriter.DeleteAlert(ctx, key); err != nil {
				log.Printf("[HeartbeatIntegration] Failed to delete resolved Brain alert %s: %v", key, err)
			} else {
				log.Printf("[HeartbeatIntegration] Cleared resolved Brain alert: %s", key)
			}
		}

	case ResultStatusAlert, ResultStatusAction:
		// Write each alert action to Brain
		for i, action := range result.Actions {
			if action.Type != ActionTypeAlert && action.Type != ActionTypeNotification {
				continue
			}

			alertType := SanitizeKeyComponent(action.Content)
			// Truncate long alert types to keep keys reasonable
			if len(alertType) > 60 {
				alertType = alertType[:60]
			}
			// Use index suffix to ensure uniqueness when multiple alerts from same job
			key := fmt.Sprintf("%s%s.%d_%s", prefix, jobKey, i, alertType)

			severity := action.Priority.String()
			summary := action.Content
			if len(summary) > 200 {
				summary = summary[:200] + "..."
			}

			value := fmt.Sprintf("severity=%s timestamp=%s message=%s",
				severity,
				time.Now().UTC().Format(time.RFC3339),
				summary,
			)

			if err := g.brainWriter.StoreAlert(ctx, key, value); err != nil {
				log.Printf("[HeartbeatIntegration] Failed to write Brain alert %s: %v", key, err)
			} else {
				log.Printf("[HeartbeatIntegration] Wrote Brain alert: %s (severity=%s)", key, severity)
			}
		}

	case ResultStatusError:
		// Write heartbeat errors as alerts too — they indicate system health issues
		key := fmt.Sprintf("%s%s.error", prefix, jobKey)
		value := fmt.Sprintf("severity=critical timestamp=%s message=Heartbeat execution error: %s",
			time.Now().UTC().Format(time.RFC3339),
			truncateString(result.Message, 200),
		)
		if err := g.brainWriter.StoreAlert(ctx, key, value); err != nil {
			log.Printf("[HeartbeatIntegration] Failed to write Brain error alert %s: %v", key, err)
		}
	}
}

// SanitizeKeyComponent converts a string into a valid Brain dotted-namespace key component.
// Replaces spaces and special characters with underscores, lowercases, and removes
// leading/trailing underscores.
func SanitizeKeyComponent(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	prevUnderscore := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevUnderscore = false
		default:
			// Replace non-alphanumeric with underscore, collapsing runs
			if !prevUnderscore && b.Len() > 0 {
				b.WriteByte('_')
				prevUnderscore = true
			}
		}
	}
	result := b.String()
	return strings.TrimRight(result, "_")
}

// truncateString truncates s to maxLen, appending "..." if truncated.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
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
		if err := g.executeAction(ctx, action, job, deliverWithRetry); err != nil {
			log.Printf("[HeartbeatIntegration] Failed to execute immediate action: %v", err)
			// Continue with other actions even if one fails
		}
	}

	// Delayed (quiet-aware) actions run now outside quiet hours; during quiet
	// hours they are persisted and delivered by FlushDeferred (conduit-31jg.33).
	for i, action := range delayed {
		if g.shouldExecuteDelayedAction(action) {
			if err := g.executeAction(ctx, action, job, deliverWithRetry); err != nil {
				log.Printf("[HeartbeatIntegration] Failed to execute delayed action: %v", err)
			}
			continue
		}
		if err := g.deferAction(action, job, i); err != nil {
			log.Printf("[HeartbeatIntegration] Failed to defer quiet-hours action (dropped): %v", err)
			continue
		}
		log.Printf("[HeartbeatIntegration] Deferred action until quiet hours end: %s", truncateString(action.Content, 80))
	}

	return nil
}

// executeAction executes a single heartbeat action. mode controls whether a
// failed delivery is retried in the background (conduit-31jg.59).
func (g *GatewayIntegration) executeAction(ctx context.Context, action HeartbeatAction, job *scheduler.Job, mode deliveryMode) error {
	switch action.Type {
	case ActionTypeAlert:
		return g.sendAlert(ctx, action, job, mode)

	case ActionTypeNotification:
		return g.sendNotification(ctx, action, job, mode)

	case ActionTypeDelivery:
		return g.sendDelivery(ctx, action, job, mode)

	case ActionTypeCommand:
		return g.executeCommand(ctx, action, job, mode)

	default:
		return fmt.Errorf("unknown action type: %s", action.Type)
	}
}

// sendAlert sends an alert message
func (g *GatewayIntegration) sendAlert(ctx context.Context, action HeartbeatAction, job *scheduler.Job, mode deliveryMode) error {
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
	return g.deliverToTarget(ctx, target, message, actionAlertMeta(action, job.ID), mode)
}

// sendNotification sends a regular notification
func (g *GatewayIntegration) sendNotification(ctx context.Context, action HeartbeatAction, job *scheduler.Job, mode deliveryMode) error {
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
	return g.deliverToTarget(ctx, target, message, actionAlertMeta(action, job.ID), mode)
}

// sendDelivery sends a delivery message (similar to notification but may respect quiet hours)
func (g *GatewayIntegration) sendDelivery(ctx context.Context, action HeartbeatAction, job *scheduler.Job, mode deliveryMode) error {
	target := g.resolveTarget(action.Target, job.Target)
	return g.deliverToTarget(ctx, target, action.Content, actionAlertMeta(action, job.ID), mode)
}

// executeCommand executes a system command action
func (g *GatewayIntegration) executeCommand(ctx context.Context, action HeartbeatAction, job *scheduler.Job, mode deliveryMode) error {
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
	return g.deliverToTarget(ctx, target, message, actionAlertMeta(action, job.ID), mode)
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

// shouldExecuteDelayedAction reports whether a quiet-aware action may run now.
// conduit-31jg.33: uses cfg.AgentHeartbeat quiet hours in the configured
// timezone instead of a hardcoded 22:00-08:00 in server-local time.
func (g *GatewayIntegration) shouldExecuteDelayedAction(action HeartbeatAction) bool {
	if quietAware, ok := action.Metadata["quiet_aware"].(bool); !ok || !quietAware {
		return true
	}
	return !g.quietConfig().IsQuietTime(g.clock())
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

// sendToTarget sends a status message (errors, verbose OK) to the target.
func (g *GatewayIntegration) sendToTarget(ctx context.Context, target, message string) error {
	return g.deliverToTarget(ctx, target, message, defaultAlertMeta, deliverWithRetry)
}

// deliverToTarget sends a message to target ("telegram:chatid" or a bare
// chat id) through the DeliveryRegistry (conduit-31jg.59).
func (g *GatewayIntegration) deliverToTarget(ctx context.Context, target, message string, meta alertMeta, mode deliveryMode) error {
	// Suppress silent response tokens from being delivered to channels
	if channels.IsSilentResponse(message) {
		log.Printf("[HeartbeatIntegration] Silent token suppressed, not delivering to %s", target)
		return nil
	}

	// Sanitize internal markers before sending (the deliverer sanitizes
	// again, so nothing routed through the registry can skip it).
	message = channels.SanitizeOutgoingText(message)

	return g.dispatch(ctx, target, message, meta, mode)
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
		ID:       "agent_heartbeat_main", // Stable ID so dedup check works across restarts
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
