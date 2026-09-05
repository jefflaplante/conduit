package gateway

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"conduit/internal/agent"
	"conduit/internal/channels"
	"conduit/internal/config"
	"conduit/internal/heartbeat"
	"conduit/internal/protocol"
	"conduit/internal/scheduler"
	"conduit/internal/tools/types"
)

// executeScheduledJob is called when a Go cron job fires. It routes heartbeat
// jobs through the HEARTBEAT.md execution framework and runs plain cron jobs
// as AI prompts against a per-job synthetic session.
func (g *Gateway) executeScheduledJob(ctx context.Context, job *scheduler.Job) error {
	g.logger.Info("executing scheduled job", "job_id", job.ID, "command", job.Command)

	// Check if this is a heartbeat job.
	if heartbeat.IsHeartbeatJob(job) {
		g.logger.Debug("routing to heartbeat execution framework", "job_id", job.ID)
		return g.monitoring.HeartbeatIntegration.ExecuteHeartbeat(ctx, job)
	}

	// Handle regular cron jobs.
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

	// Store model and skill filter in session context for prompt/tool filtering.
	if session.Context == nil {
		session.Context = make(map[string]string)
	}
	session.Context["model"] = model
	if len(job.Skills) > 0 {
		session.Context["skill_filter"] = strings.Join(job.Skills, ",")
	}

	// Execute the job command as an AI prompt.
	response, err := g.ai.GenerateResponseWithTools(ctx, session, job.Command, "", model)
	if err != nil {
		return fmt.Errorf("AI execution failed: %w", err)
	}

	// Usage accounting is router-level since bd-27hs: GenerateResponseWithTools
	// records last_* / session_*_tokens_total to the session store for every
	// path. No per-path recording here.

	// If there's a target, send the result there.
	if job.Target != "" {
		responseContent := response.GetContent()

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

	g.logger.Info("job completed", "job_id", job.ID, "response_chars", len(response.GetContent()))
	return nil
}

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
