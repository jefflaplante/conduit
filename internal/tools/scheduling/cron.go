package scheduling

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"conduit/internal/tools/types"

	"github.com/google/uuid"
)

// CronTool manages scheduled jobs via the gateway's scheduler
type CronTool struct {
	services *types.ToolServices
}

func NewCronTool(services *types.ToolServices) *CronTool {
	return &CronTool{services: services}
}

func (t *CronTool) Name() string {
	return "Cron"
}

func (t *CronTool) Description() string {
	return `Schedule recurring tasks and reminders. Supports heartbeat job management.

Job Types:
- "go" (default): In-process scheduling, can run AI prompts and spawn sub-agents
- "system": System crontab, runs shell commands without LLM involvement

Regular Actions:
- Schedule a reminder: action=schedule, command="Remind Jeff to check email", delayMinutes=30
- Daily report: action=schedule, schedule="0 9 * * *", command="Generate daily briefing", type="go"
- System backup: action=schedule, schedule="0 2 * * *", command="/usr/local/bin/backup.sh", type="system"

Heartbeat Management:
- List heartbeat jobs: action=heartbeat_list
- Enable all heartbeat jobs: action=heartbeat_enable  
- Disable all heartbeat jobs: action=heartbeat_disable
- Check heartbeat status: action=heartbeat_status`
}

func (t *CronTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"schedule", "list", "cancel", "run", "enable", "disable", "status", "heartbeat_list", "heartbeat_enable", "heartbeat_disable", "heartbeat_status"},
				"description": "Cron operation to perform",
			},
			"schedule": map[string]interface{}{
				"type":        "string",
				"description": "Cron expression (e.g., '0 9 * * 1' for 9 AM on Mondays, '*/15 * * * *' for every 15 min)",
			},
			"command": map[string]interface{}{
				"type":        "string",
				"description": "For go jobs: AI prompt/task. For system jobs: shell command",
			},
			"name": map[string]interface{}{
				"type":        "string",
				"description": "Human-readable job name",
			},
			"jobType": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"go", "system"},
				"description": "Job type: 'go' for AI-powered tasks, 'system' for shell commands",
				"default":     "go",
			},
			"model": map[string]interface{}{
				"type":        "string",
				"description": "AI model for go jobs (e.g., 'haiku', 'sonnet', 'opus')",
			},
			"target": map[string]interface{}{
				"type":        "string",
				"description": "DO NOT SET - automatically uses current chat. Only set for cross-channel routing.",
			},
			"jobId": map[string]interface{}{
				"type":        "string",
				"description": "Job ID for cancel/run/enable/disable actions",
			},
			"oneshot": map[string]interface{}{
				"type":        "boolean",
				"description": "Run once then delete (for reminders)",
				"default":     false,
			},
			"delayMinutes": map[string]interface{}{
				"type":        "integer",
				"description": "Schedule to run in X minutes (alternative to cron expression)",
			},
		},
		"required": []string{"action"},
	}
}

func (t *CronTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	action := t.getStringArg(args, "action", "")

	switch action {
	case "schedule":
		return t.scheduleJob(ctx, args)
	case "list":
		return t.listJobs(ctx, args)
	case "cancel":
		return t.cancelJob(ctx, args)
	case "run":
		return t.runJob(ctx, args)
	case "enable":
		return t.enableJob(ctx, args)
	case "disable":
		return t.disableJob(ctx, args)
	case "status":
		return t.getStatus(ctx, args)
	case "heartbeat_list":
		return t.listHeartbeatJobs(ctx, args)
	case "heartbeat_enable":
		return t.enableHeartbeatJobs(ctx, args)
	case "heartbeat_disable":
		return t.disableHeartbeatJobs(ctx, args)
	case "heartbeat_status":
		return t.getHeartbeatStatus(ctx, args)
	default:
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("unknown action: %s", action),
		}, nil
	}
}

func (t *CronTool) scheduleJob(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	if t.services.Gateway == nil {
		return &types.ToolResult{
			Success: false,
			Error:   "gateway service not available",
		}, nil
	}

	command := t.getStringArg(args, "command", "")
	if command == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "command parameter is required",
		}, nil
	}

	var schedule string
	oneshot := t.getBoolArg(args, "oneshot", false)

	// Check if delayMinutes is provided (simple scheduling)
	if delayMinutes := t.getIntArg(args, "delayMinutes", 0); delayMinutes > 0 {
		// Convert minutes to a cron schedule for the target time
		targetTime := time.Now().Add(time.Duration(delayMinutes) * time.Minute)
		schedule = fmt.Sprintf("%d %d %d %d *",
			targetTime.Minute(), targetTime.Hour(), targetTime.Day(), int(targetTime.Month()))
		oneshot = true // Delay-based schedules are always one-shot
	} else {
		schedule = t.getStringArg(args, "schedule", "")
		if schedule == "" {
			return &types.ToolResult{
				Success: false,
				Error:   "either schedule or delayMinutes parameter is required",
			}, nil
		}
	}

	// Determine job type
	jobType := t.getStringArg(args, "jobType", "go")
	if jobType != "go" && jobType != "system" {
		return &types.ToolResult{
			Success: false,
			Error:   "jobType must be 'go' or 'system'",
		}, nil
	}

	// Create job
	job := &types.SchedulerJob{
		ID:       uuid.New().String()[:8],
		Name:     t.getStringArg(args, "name", ""),
		Schedule: schedule,
		Type:     jobType,
		Command:  command,
		Model:    t.getStringArg(args, "model", ""),
		Target:   t.getStringArg(args, "target", ""),
		Enabled:  true,
		OneShot:  oneshot,
	}

	// Default name for delay-based schedules
	if job.Name == "" && t.getIntArg(args, "delayMinutes", 0) > 0 {
		job.Name = fmt.Sprintf("Reminder in %d minutes", t.getIntArg(args, "delayMinutes", 0))
	}

	// For go jobs, ALWAYS use current chat as target (ignore any passed value)
	if job.Type == "go" {
		if currentUserID := types.RequestUserID(ctx); currentUserID != "" {
			job.Target = currentUserID
			log.Printf("[Cron] Set target to current user: %s", job.Target)
		} else {
			log.Printf("[Cron] WARNING: CurrentUserID is empty, target will be empty")
		}
	}

	// Schedule the job
	if err := t.services.Gateway.ScheduleJob(job); err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to schedule job: %v", err),
		}, nil
	}

	// Keep response minimal - just confirm it's scheduled
	description := "Job scheduled."
	if oneshot {
		description = "Reminder set."
	}

	return &types.ToolResult{
		Success: true,
		Content: description,
		Data: map[string]interface{}{
			"jobId":    job.ID,
			"name":     job.Name,
			"schedule": job.Schedule,
			"type":     job.Type,
			"oneshot":  job.OneShot,
		},
	}, nil
}

func (t *CronTool) listJobs(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	if t.services.Gateway == nil {
		return &types.ToolResult{
			Success: false,
			Error:   "gateway service not available",
		}, nil
	}

	jobs := t.services.Gateway.ListJobs()

	if len(jobs) == 0 {
		return &types.ToolResult{
			Success: true,
			Content: "No scheduled jobs.",
			Data:    map[string]interface{}{"count": 0},
		}, nil
	}

	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("%d scheduled job(s):\n\n", len(jobs)))

	for i, job := range jobs {
		name := job.Name
		if name == "" {
			name = "Unnamed"
		}

		status := ""
		if !job.Enabled {
			status = " (disabled)"
		}

		jobType := ""
		if job.Type == "system" {
			jobType = " [system]"
		}

		builder.WriteString(fmt.Sprintf("%d. %s%s%s\n", i+1, name, jobType, status))
		builder.WriteString(fmt.Sprintf("   %s\n", job.Schedule))
		if job.OneShot {
			builder.WriteString("   (runs once)\n")
		}
	}

	return &types.ToolResult{
		Success: true,
		Content: builder.String(),
		Data: map[string]interface{}{
			"jobs":  jobs,
			"count": len(jobs),
		},
	}, nil
}

func (t *CronTool) cancelJob(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	if t.services.Gateway == nil {
		return &types.ToolResult{
			Success: false,
			Error:   "gateway service not available",
		}, nil
	}

	jobId := t.getStringArg(args, "jobId", "")
	if jobId == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "jobId parameter is required",
		}, nil
	}

	if err := t.services.Gateway.CancelJob(jobId); err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to cancel job: %v", err),
		}, nil
	}

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Job %s cancelled.", jobId),
		Data:    map[string]interface{}{"jobId": jobId},
	}, nil
}

func (t *CronTool) runJob(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	if t.services.Gateway == nil {
		return &types.ToolResult{
			Success: false,
			Error:   "gateway service not available",
		}, nil
	}

	jobId := t.getStringArg(args, "jobId", "")
	if jobId == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "jobId parameter is required",
		}, nil
	}

	if err := t.services.Gateway.RunJobNow(jobId); err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to run job: %v", err),
		}, nil
	}

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Job %s triggered.", jobId),
		Data:    map[string]interface{}{"jobId": jobId},
	}, nil
}

func (t *CronTool) enableJob(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	if t.services.Gateway == nil {
		return &types.ToolResult{
			Success: false,
			Error:   "gateway service not available",
		}, nil
	}

	jobId := t.getStringArg(args, "jobId", "")
	if jobId == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "jobId parameter is required",
		}, nil
	}

	if err := t.services.Gateway.EnableJob(jobId); err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to enable job: %v", err),
		}, nil
	}

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Job %s enabled.", jobId),
		Data:    map[string]interface{}{"jobId": jobId},
	}, nil
}

func (t *CronTool) disableJob(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	if t.services.Gateway == nil {
		return &types.ToolResult{
			Success: false,
			Error:   "gateway service not available",
		}, nil
	}

	jobId := t.getStringArg(args, "jobId", "")
	if jobId == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "jobId parameter is required",
		}, nil
	}

	if err := t.services.Gateway.DisableJob(jobId); err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to disable job: %v", err),
		}, nil
	}

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Job %s disabled.", jobId),
		Data:    map[string]interface{}{"jobId": jobId},
	}, nil
}

func (t *CronTool) getStatus(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	if t.services.Gateway == nil {
		return &types.ToolResult{
			Success: false,
			Error:   "gateway service not available",
		}, nil
	}

	status := t.services.Gateway.GetSchedulerStatus()

	content := "Scheduler Status:\n\n"
	content += fmt.Sprintf("Enabled: %v\n", status["enabled"])
	content += fmt.Sprintf("Total Jobs: %v\n", status["total_jobs"])
	content += fmt.Sprintf("Go Jobs: %v\n", status["go_jobs"])
	content += fmt.Sprintf("System Jobs: %v\n", status["system_jobs"])
	content += fmt.Sprintf("Active Cron Entries: %v\n", status["cron_entries"])

	return &types.ToolResult{
		Success: true,
		Content: content,
		Data:    status,
	}, nil
}

// Helper methods
func (t *CronTool) getStringArg(args map[string]interface{}, key, defaultVal string) string {
	if val, ok := args[key].(string); ok {
		return val
	}
	return defaultVal
}

func (t *CronTool) getIntArg(args map[string]interface{}, key string, defaultVal int) int {
	if val, ok := args[key].(float64); ok {
		return int(val)
	}
	if val, ok := args[key].(int); ok {
		return val
	}
	return defaultVal
}

func (t *CronTool) getBoolArg(args map[string]interface{}, key string, defaultVal bool) bool {
	if val, ok := args[key].(bool); ok {
		return val
	}
	return defaultVal
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// listHeartbeatJobs lists all heartbeat-related jobs
func (t *CronTool) listHeartbeatJobs(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	if t.services.Gateway == nil {
		return &types.ToolResult{
			Success: false,
			Error:   "gateway service not available",
		}, nil
	}

	allJobs := t.services.Gateway.ListJobs()
	var heartbeatJobs []*types.SchedulerJob

	// Filter for heartbeat jobs (jobs with ID starting with "heartbeat_" or command containing "HEARTBEAT")
	for _, job := range allJobs {
		if isHeartbeatJob(job) {
			heartbeatJobs = append(heartbeatJobs, job)
		}
	}

	if len(heartbeatJobs) == 0 {
		return &types.ToolResult{
			Success: true,
			Content: "No heartbeat jobs found.",
			Data:    map[string]interface{}{"count": 0},
		}, nil
	}

	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("%d heartbeat job(s):\n\n", len(heartbeatJobs)))

	for i, job := range heartbeatJobs {
		name := job.Name
		if name == "" {
			name = job.ID
		}

		status := ""
		if !job.Enabled {
			status = " (disabled)"
		}

		builder.WriteString(fmt.Sprintf("%d. %s%s\n", i+1, name, status))
		builder.WriteString(fmt.Sprintf("   Schedule: %s\n", job.Schedule))
		if job.Target != "" {
			builder.WriteString(fmt.Sprintf("   Target: %s\n", job.Target))
		}
	}

	return &types.ToolResult{
		Success: true,
		Content: builder.String(),
		Data: map[string]interface{}{
			"jobs":  heartbeatJobs,
			"count": len(heartbeatJobs),
		},
	}, nil
}

// enableHeartbeatJobs enables all heartbeat jobs
func (t *CronTool) enableHeartbeatJobs(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	if t.services.Gateway == nil {
		return &types.ToolResult{
			Success: false,
			Error:   "gateway service not available",
		}, nil
	}

	allJobs := t.services.Gateway.ListJobs()
	var enabledCount int
	var errors []string

	for _, job := range allJobs {
		if isHeartbeatJob(job) && !job.Enabled {
			if err := t.services.Gateway.EnableJob(job.ID); err != nil {
				errors = append(errors, fmt.Sprintf("Failed to enable %s: %v", job.ID, err))
			} else {
				enabledCount++
			}
		}
	}

	var content string
	if enabledCount > 0 {
		content = fmt.Sprintf("Enabled %d heartbeat job(s).", enabledCount)
	} else {
		content = "No heartbeat jobs needed enabling."
	}

	if len(errors) > 0 {
		content += "\n\nErrors:\n" + strings.Join(errors, "\n")
	}

	return &types.ToolResult{
		Success: len(errors) == 0,
		Content: content,
		Data: map[string]interface{}{
			"enabled_count": enabledCount,
			"errors":        errors,
		},
	}, nil
}

// disableHeartbeatJobs disables all heartbeat jobs
func (t *CronTool) disableHeartbeatJobs(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	if t.services.Gateway == nil {
		return &types.ToolResult{
			Success: false,
			Error:   "gateway service not available",
		}, nil
	}

	allJobs := t.services.Gateway.ListJobs()
	var disabledCount int
	var errors []string

	for _, job := range allJobs {
		if isHeartbeatJob(job) && job.Enabled {
			if err := t.services.Gateway.DisableJob(job.ID); err != nil {
				errors = append(errors, fmt.Sprintf("Failed to disable %s: %v", job.ID, err))
			} else {
				disabledCount++
			}
		}
	}

	var content string
	if disabledCount > 0 {
		content = fmt.Sprintf("Disabled %d heartbeat job(s).", disabledCount)
	} else {
		content = "No heartbeat jobs needed disabling."
	}

	if len(errors) > 0 {
		content += "\n\nErrors:\n" + strings.Join(errors, "\n")
	}

	return &types.ToolResult{
		Success: len(errors) == 0,
		Content: content,
		Data: map[string]interface{}{
			"disabled_count": disabledCount,
			"errors":         errors,
		},
	}, nil
}

// getHeartbeatStatus gets status of heartbeat jobs and overall heartbeat system
func (t *CronTool) getHeartbeatStatus(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	if t.services.Gateway == nil {
		return &types.ToolResult{
			Success: false,
			Error:   "gateway service not available",
		}, nil
	}

	allJobs := t.services.Gateway.ListJobs()
	var heartbeatJobs []*types.SchedulerJob
	var enabledCount, disabledCount int

	for _, job := range allJobs {
		if isHeartbeatJob(job) {
			heartbeatJobs = append(heartbeatJobs, job)
			if job.Enabled {
				enabledCount++
			} else {
				disabledCount++
			}
		}
	}

	content := "Heartbeat System Status:\n\n"
	content += fmt.Sprintf("Total Heartbeat Jobs: %d\n", len(heartbeatJobs))
	content += fmt.Sprintf("Enabled: %d\n", enabledCount)
	content += fmt.Sprintf("Disabled: %d\n", disabledCount)

	// Health check
	healthy := enabledCount > 0 && len(heartbeatJobs) > 0
	content += fmt.Sprintf("System Health: %s\n", map[bool]string{true: "✅ Healthy", false: "⚠️ No Active Jobs"}[healthy])

	return &types.ToolResult{
		Success: true,
		Content: content,
		Data: map[string]interface{}{
			"total_jobs":    len(heartbeatJobs),
			"enabled_jobs":  enabledCount,
			"disabled_jobs": disabledCount,
			"healthy":       healthy,
		},
	}, nil
}

// isHeartbeatJob determines if a job is a heartbeat job
func isHeartbeatJob(job *types.SchedulerJob) bool {
	return strings.HasPrefix(job.ID, "heartbeat_") ||
		strings.Contains(strings.ToLower(job.Command), "heartbeat") ||
		strings.Contains(strings.ToLower(job.Name), "heartbeat")
}
