package gateway

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"conduit/internal/agent"
	"conduit/internal/approval"
	"conduit/internal/channels"
	"conduit/internal/config"
	"conduit/internal/heartbeat"
	"conduit/internal/protocol"
	"conduit/internal/scheduler"
	"conduit/internal/sessions"
	"conduit/internal/tools/types"
)

// executeScheduledJob is called when a Go cron job fires. It routes heartbeat
// jobs through the HEARTBEAT.md execution framework and runs plain cron jobs
// as AI prompts against a per-job synthetic session.
//
// conduit-31jg.66: both run their LLM turn on the shared TurnRunner, marked
// non-interactive and owned by the job (TurnRequest.ScheduledJob): transcript
// written inside the turn lock, the turn visible in ActiveRequests (/stop,
// status), usage/cost and compaction like any turn. ctx is the scheduler's
// run context, so the drain's scheduler interrupt (conduit-31jg.77) cancels
// the turn and the run is recorded as interrupted.
func (g *Gateway) executeScheduledJob(ctx context.Context, job *scheduler.Job) error {
	g.logger.Info("executing scheduled job", "job_id", job.ID, "command", job.Command)
	ctx = withScheduledJobID(ctx, job.ID)

	// Check if this is a heartbeat job.
	if heartbeat.IsHeartbeatJob(job) {
		// conduit-31jg.43: no live human; approval-gated actions fail closed
		// even if this job was triggered from inside an interactive turn.
		ctx = approval.WithNonInteractive(ctx, "heartbeat")
		g.logger.Debug("routing to heartbeat execution framework", "job_id", job.ID)
		return g.monitoring.HeartbeatIntegration.ExecuteHeartbeat(ctx, job)
	}

	// Handle regular cron jobs.
	ctx = approval.WithNonInteractive(ctx, "cron") // conduit-31jg.43

	// Create a session for this job.
	sessionKey := fmt.Sprintf(agent.CronSessionKeyPrefix+"%s_%d", job.ID, time.Now().UnixNano())
	session, err := g.sessions.GetOrCreateSession("cron", sessionKey)
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}

	// Resolve model alias.
	model := job.Model
	if model == "" {
		model = g.getDefaultModel()
	} else if fullModel, exists := g.getModelAliases()[strings.ToLower(model)]; exists && fullModel != "" {
		model = fullModel
	}

	// Store model and skill filter in the session context (persisted: the
	// runner re-reads the session inside the turn lock).
	jobContext := map[string]string{"model": model}
	if len(job.Skills) > 0 {
		jobContext["skill_filter"] = strings.Join(job.Skills, ",")
	}
	if err := g.sessions.SetSessionContextBatch(session.Key, jobContext); err != nil {
		return fmt.Errorf("failed to configure job session: %w", err)
	}

	// Execute the job command as an AI prompt.
	res := g.turns().Run(ctx, TurnRequest{
		Session:              session,
		Text:                 job.Command,
		NonInteractiveSource: "cron",
		ScheduledJob:         job.ID,
	}, discardTurnSink{})
	if res.Err != nil {
		return fmt.Errorf("AI execution failed: %w", res.Err)
	}
	responseContent := res.Raw

	// If there's a target, send the result there.
	if job.Target != "" {
		// Check for silent response patterns - don't send these to the target.
		if responseContent == "" || channels.IsSilentResponse(responseContent) {
			g.logger.Debug("job completed with silent response, not sending to target", "job_id", job.ID)
			return nil
		}

		// Target format: "telegram:chatid" or just "chatid".
		parts := strings.SplitN(job.Target, ":", 2)
		var channelID, userID string
		if len(parts) == 2 {
			channelID = parts[0]
			userID = parts[1]
		} else {
			channelID = "telegram"
			userID = job.Target
		}

		outgoingMsg := &protocol.OutgoingMessage{
			BaseMessage: protocol.BaseMessage{
				Type:      protocol.TypeOutgoingMessage,
				ID:        fmt.Sprintf(agent.CronSessionKeyPrefix+"%s_%d", job.ID, time.Now().UnixNano()),
				Timestamp: time.Now(),
			},
			ChannelID: channelID,
			UserID:    userID,
			Text:      responseContent,
		}

		if err := g.channelManager.SendMessage(outgoingMsg); err != nil {
			g.logger.Error("failed to send job output", "job_id", job.ID, "target", job.Target, "error", err)
		}
	}

	g.logger.Info("job completed", "job_id", job.ID, "response_chars", len(responseContent))
	return nil
}

// scheduledJobKey carries the running scheduler job's ID to the heartbeat
// AI executor (conduit-31jg.66).
type scheduledJobKey struct{}

func withScheduledJobID(ctx context.Context, jobID string) context.Context {
	return context.WithValue(ctx, scheduledJobKey{}, jobID)
}

func scheduledJobID(ctx context.Context) string {
	id, _ := ctx.Value(scheduledJobKey{}).(string)
	return id
}

// turnAIExecutor runs the heartbeat's LLM prompt on the shared TurnRunner
// (conduit-31jg.66) instead of calling the router directly. Installed on the
// heartbeat integration by the gateway (SetAIExecutor).
type turnAIExecutor struct {
	g *Gateway

	mu sync.Mutex
	// stored maps session key → the user row a previous attempt stored for
	// the same prompt, so the heartbeat executor's retries do not append the
	// prompt to the transcript again.
	stored map[string]storedPrompt
}

type storedPrompt struct {
	prompt string
	msgID  string
}

func newTurnAIExecutor(g *Gateway) *turnAIExecutor {
	return &turnAIExecutor{g: g, stored: make(map[string]storedPrompt)}
}

// ExecutePrompt implements heartbeat.AIExecutor.
func (e *turnAIExecutor) ExecutePrompt(ctx context.Context, session *sessions.Session, prompt, model string) (heartbeat.AIResponse, error) {
	if model != "" {
		if err := e.g.sessions.SetSessionContext(session.Key, "model", model); err != nil {
			return nil, fmt.Errorf("configure heartbeat session: %w", err)
		}
	}
	e.mu.Lock()
	prev, retry := e.stored[session.Key]
	e.mu.Unlock()
	req := TurnRequest{
		Session:              session,
		Text:                 prompt,
		NonInteractiveSource: "heartbeat",
		ScheduledJob:         scheduledJobID(ctx),
	}
	if req.ScheduledJob == "" {
		req.ScheduledJob = "heartbeat"
	}
	if retry && prev.prompt == prompt {
		req.PersistedUserMessageID = prev.msgID
	}

	res := e.g.turns().Run(ctx, req, discardTurnSink{})

	e.mu.Lock()
	if res.Err != nil && res.UserMessageID != "" {
		e.stored[session.Key] = storedPrompt{prompt: prompt, msgID: res.UserMessageID}
	} else {
		delete(e.stored, session.Key)
	}
	e.mu.Unlock()

	switch {
	case res.Cancelled, res.Dropped:
		// Wrap context.Canceled so the heartbeat executor stops retrying a
		// turn that was stopped (/stop, shutdown).
		return nil, fmt.Errorf("heartbeat turn stopped (%v): %w", res.Err, context.Canceled)
	case res.Err != nil:
		return nil, res.Err
	}
	return turnAIResponse{res}, nil
}

// turnAIResponse adapts a TurnResult to heartbeat.AIResponse.
type turnAIResponse struct{ res *TurnResult }

func (r turnAIResponse) GetContent() string { return r.res.Raw }

func (r turnAIResponse) GetUsage() interface{} { return r.res.Usage }

// ScheduleJob adds a new scheduled job
func (g *Gateway) ScheduleJob(job *types.SchedulerJob) error {
	if g.scheduler == nil {
		return fmt.Errorf("scheduler not initialized")
	}

	// Convert types.SchedulerJob to scheduler.Job
	schedJob := &scheduler.Job{
		ID:       job.ID,
		Name:     job.Name,
		Schedule: job.Schedule,
		Type:     scheduler.JobType(job.Type),
		Command:  job.Command,
		Model:    job.Model,
		Target:   job.Target,
		Enabled:  job.Enabled,
		OneShot:  job.OneShot,
		Skills:   job.Skills,
	}

	return g.scheduler.AddJob(schedJob)
}

// CancelJob removes a scheduled job
func (g *Gateway) CancelJob(jobID string) error {
	if g.scheduler == nil {
		return fmt.Errorf("scheduler not initialized")
	}
	return g.scheduler.RemoveJob(jobID)
}

// ListJobs returns all scheduled jobs
func (g *Gateway) ListJobs() []*types.SchedulerJob {
	if g.scheduler == nil {
		return nil
	}

	jobs := g.scheduler.ListJobs()
	result := make([]*types.SchedulerJob, len(jobs))
	for i, job := range jobs {
		result[i] = &types.SchedulerJob{
			ID:       job.ID,
			Name:     job.Name,
			Schedule: job.Schedule,
			Type:     string(job.Type),
			Command:  job.Command,
			Model:    job.Model,
			Target:   job.Target,
			Enabled:  job.Enabled,
			OneShot:  job.OneShot,
			Skills:   job.Skills,
		}
	}
	return result
}

// EnableJob enables a scheduled job
func (g *Gateway) EnableJob(jobID string) error {
	if g.scheduler == nil {
		return fmt.Errorf("scheduler not initialized")
	}
	return g.scheduler.EnableJob(jobID)
}

// DisableJob disables a scheduled job
func (g *Gateway) DisableJob(jobID string) error {
	if g.scheduler == nil {
		return fmt.Errorf("scheduler not initialized")
	}
	return g.scheduler.DisableJob(jobID)
}

// RunJobNow executes a job immediately
func (g *Gateway) RunJobNow(jobID string) error {
	if g.scheduler == nil {
		return fmt.Errorf("scheduler not initialized")
	}
	return g.scheduler.RunNow(jobID)
}

// GetSchedulerStatus returns scheduler status
func (g *Gateway) GetSchedulerStatus() map[string]interface{} {
	if g.scheduler == nil {
		return map[string]interface{}{"enabled": false}
	}
	status := g.scheduler.Status()
	status["enabled"] = true
	return status
}

// ScheduleHeartbeatJob schedules a new heartbeat job using the HEARTBEAT.md execution framework
func (g *Gateway) ScheduleHeartbeatJob(schedule, target, model string, enabled bool) error {
	if g.monitoring == nil || g.monitoring.HeartbeatIntegration == nil {
		return fmt.Errorf("heartbeat integration not available")
	}
	return g.monitoring.HeartbeatIntegration.ScheduleHeartbeatJob(schedule, target, model, enabled)
}

// GetHeartbeatJobCount returns the number of active heartbeat jobs
func (g *Gateway) GetHeartbeatJobCount() int {
	if g.monitoring == nil || g.monitoring.HeartbeatIntegration == nil {
		return 0
	}
	return g.monitoring.HeartbeatIntegration.GetHeartbeatJobCount()
}

// RemoveHeartbeatJobs removes all heartbeat jobs from the scheduler
func (g *Gateway) RemoveHeartbeatJobs() error {
	if g.monitoring == nil || g.monitoring.HeartbeatIntegration == nil {
		return fmt.Errorf("heartbeat integration not available")
	}
	return g.monitoring.HeartbeatIntegration.RemoveHeartbeatJobs()
}

// initializeAgentHeartbeat sets up automatic agent heartbeat jobs based on configuration
func (g *Gateway) initializeAgentHeartbeat(cfg *config.Config) error {
	if !cfg.AgentHeartbeat.Enabled {
		log.Printf("[AgentHeartbeat] Agent heartbeat disabled in configuration")
		return nil
	}

	// Convert interval minutes to cron schedule (6-field format: seconds, minutes, hours, day, month, weekday)
	cronSchedule := fmt.Sprintf("0 */%d * * * *", cfg.AgentHeartbeat.IntervalMinutes)

	// Determine target from alert targets (use first one if available)
	var target string
	if len(cfg.AgentHeartbeat.AlertTargets) > 0 {
		// Format: "telegram:chat_id" or similar
		firstTarget := cfg.AgentHeartbeat.AlertTargets[0]
		if firstTarget.Type == "telegram" {
			if chatID, exists := firstTarget.Config["chat_id"]; exists {
				target = fmt.Sprintf("telegram:%s", chatID)
			}
		}
	}

	// Create the main agent heartbeat job
	jobID := "agent_heartbeat_main"

	// Check if the stable-ID job already exists (normal restart — no action needed).
	existingJobs := g.scheduler.ListJobs()
	for _, job := range existingJobs {
		if job.ID == jobID {
			log.Printf("[AgentHeartbeat] Job %s already exists, skipping auto-creation", jobID)
			return nil
		}
	}

	// Migrate legacy heartbeat jobs: earlier releases used a timestamp-based ID
	// (e.g. "heartbeat_<nanoseconds>") so the stable-ID check above never matched,
	// causing a second job to be registered on every restart.  Remove any such
	// legacy jobs before creating the canonical one.
	for _, job := range existingJobs {
		if strings.HasPrefix(job.ID, "heartbeat_") {
			log.Printf("[AgentHeartbeat] Removing legacy heartbeat job %s before registering canonical job %s", job.ID, jobID)
			if err := g.scheduler.RemoveJob(job.ID); err != nil {
				log.Printf("[AgentHeartbeat] Warning: failed to remove legacy job %s: %v", job.ID, err)
			}
		}
	}

	// Schedule the heartbeat job
	if err := g.ScheduleHeartbeatJob(cronSchedule, target, cfg.AgentHeartbeat.Model, true); err != nil {
		return fmt.Errorf("failed to schedule agent heartbeat job: %w", err)
	}

	log.Printf("[AgentHeartbeat] Auto-created heartbeat job: %s (schedule: %s, target: %s)",
		jobID, cronSchedule, target)

	// Update metrics with current job counts
	g.updateHeartbeatJobMetrics()

	return nil
}

// updateHeartbeatJobMetrics updates the metrics collector with current heartbeat job counts
func (g *Gateway) updateHeartbeatJobMetrics() {
	if g.monitoring == nil || g.monitoring.MetricsCollector == nil || g.scheduler == nil {
		return
	}

	jobs := g.scheduler.ListJobs()
	var total, enabled int

	for _, job := range jobs {
		if strings.HasPrefix(job.ID, "heartbeat_") ||
			strings.Contains(strings.ToLower(job.Command), "heartbeat") ||
			strings.Contains(strings.ToLower(job.Name), "heartbeat") {
			total++
			if job.Enabled {
				enabled++
			}
		}
	}

	g.monitoring.MetricsCollector.UpdateHeartbeatJobs(total, enabled)
}

// initializeREMCycle sets up automatic REM sleep cycle job based on configuration
func (g *Gateway) initializeREMCycle(cfg *config.Config) error {
	if !cfg.Brain.Enabled || !cfg.Brain.REMEnabled {
		log.Printf("[REMCycle] REM sleep cycle disabled in configuration")
		return nil
	}

	if g.remCycle == nil {
		log.Printf("[REMCycle] REM cycle not initialized, skipping job creation")
		return nil
	}

	jobID := "rem_sleep_nightly"

	// Check if job already exists (avoid duplicates on restart)
	existingJobs := g.scheduler.ListJobs()
	for _, job := range existingJobs {
		if job.ID == jobID {
			log.Printf("[REMCycle] Job %s already exists, skipping auto-creation", jobID)
			return nil
		}
	}

	// Create the REM cycle job
	job := &scheduler.Job{
		ID:       jobID,
		Name:     "REM Sleep Consolidation",
		Schedule: cfg.Brain.REMSchedule,
		Type:     scheduler.JobTypeGo,
		Command:  "brain rem_cycle",
		Model:    "haiku",
		Enabled:  true,
		Metadata: map[string]interface{}{
			"rem_sleep": true,
			"brain":     true,
		},
	}

	if err := g.scheduler.AddJob(job); err != nil {
		return fmt.Errorf("failed to schedule REM cycle job: %w", err)
	}

	log.Printf("[REMCycle] Auto-created REM sleep cycle job: %s (schedule: %s)", jobID, cfg.Brain.REMSchedule)

	return nil
}
