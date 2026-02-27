package gateway

import (
	"fmt"
	"log"
	"strings"

	"conduit/internal/config"
	"conduit/internal/scheduler"
	"conduit/internal/tools/types"
)

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
	if g.heartbeatIntegration == nil {
		return fmt.Errorf("heartbeat integration not available")
	}
	return g.heartbeatIntegration.ScheduleHeartbeatJob(schedule, target, model, enabled)
}

// GetHeartbeatJobCount returns the number of active heartbeat jobs
func (g *Gateway) GetHeartbeatJobCount() int {
	if g.heartbeatIntegration == nil {
		return 0
	}
	return g.heartbeatIntegration.GetHeartbeatJobCount()
}

// RemoveHeartbeatJobs removes all heartbeat jobs from the scheduler
func (g *Gateway) RemoveHeartbeatJobs() error {
	if g.heartbeatIntegration == nil {
		return fmt.Errorf("heartbeat integration not available")
	}
	return g.heartbeatIntegration.RemoveHeartbeatJobs()
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

	// Check if job already exists (avoid duplicates on restart)
	existingJobs := g.scheduler.ListJobs()
	for _, job := range existingJobs {
		if job.ID == jobID {
			log.Printf("[AgentHeartbeat] Job %s already exists, skipping auto-creation", jobID)
			return nil
		}
	}

	// Schedule the heartbeat job
	if err := g.ScheduleHeartbeatJob(cronSchedule, target, "", true); err != nil {
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
	if g.metricsCollector == nil || g.scheduler == nil {
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

	g.metricsCollector.UpdateHeartbeatJobs(total, enabled)
}
