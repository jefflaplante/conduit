package scheduling

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
	"github.com/google/uuid"
)

func (t *CronTool) scheduleJob(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	if t.services.Gateway == nil {
		return &types.ToolResult{
			Success: false,
			Error:   "gateway service not available",
		}, nil
	}

	command := toolargs.GetString(args, "command", "")
	if command == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "command parameter is required",
		}, nil
	}

	var schedule string
	oneshot := toolargs.GetBool(args, "oneshot", false)

	// Check if delayMinutes is provided (simple scheduling)
	if delayMinutes := toolargs.GetInt(args, "delayMinutes", 0); delayMinutes > 0 {
		// Convert minutes to a cron schedule for the target time
		targetTime := time.Now().Add(time.Duration(delayMinutes) * time.Minute)
		schedule = fmt.Sprintf("%d %d %d %d *",
			targetTime.Minute(), targetTime.Hour(), targetTime.Day(), int(targetTime.Month()))
		oneshot = true // Delay-based schedules are always one-shot
	} else {
		schedule = toolargs.GetString(args, "schedule", "")
		if schedule == "" {
			return &types.ToolResult{
				Success: false,
				Error:   "either schedule or delayMinutes parameter is required",
			}, nil
		}
	}

	// Determine job type
	jobType := toolargs.GetString(args, "jobType", "go")
	if jobType != "go" && jobType != "system" {
		return &types.ToolResult{
			Success: false,
			Error:   "jobType must be 'go' or 'system'",
		}, nil
	}

	// Extract skills filter
	var jobSkills []string
	if raw, ok := args["skills"]; ok {
		if arr, ok := raw.([]interface{}); ok {
			for _, v := range arr {
				if s, ok := v.(string); ok && s != "" {
					jobSkills = append(jobSkills, s)
				}
			}
		}
	}

	// Create job
	job := &types.SchedulerJob{
		ID:       uuid.New().String()[:8],
		Name:     toolargs.GetString(args, "name", ""),
		Schedule: schedule,
		Type:     jobType,
		Command:  command,
		Model:    toolargs.GetString(args, "model", ""),
		Target:   toolargs.GetString(args, "target", ""),
		Enabled:  true,
		OneShot:  oneshot,
		Skills:   jobSkills,
	}

	// Default name for delay-based schedules
	if job.Name == "" && toolargs.GetInt(args, "delayMinutes", 0) > 0 {
		job.Name = fmt.Sprintf("Reminder in %d minutes", toolargs.GetInt(args, "delayMinutes", 0))
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
		if len(job.Skills) > 0 {
			builder.WriteString(fmt.Sprintf("   skills: %s\n", strings.Join(job.Skills, ", ")))
		}
		if job.OneShot {
			builder.WriteString("   (runs once)\n")
		}
		// conduit-2six: surface failing jobs.
		if job.ConsecutiveFailures > 0 {
			builder.WriteString(fmt.Sprintf("   FAILING: %d consecutive failed run(s)", job.ConsecutiveFailures))
			if job.LastError != "" {
				builder.WriteString(" - last error: " + truncateForList(job.LastError, 160))
			}
			builder.WriteString("\n")
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

	jobId := toolargs.GetString(args, "jobId", "")
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

	jobId := toolargs.GetString(args, "jobId", "")
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

	jobId := toolargs.GetString(args, "jobId", "")
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

	jobId := toolargs.GetString(args, "jobId", "")
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
	content += formatFailingJobs(status["failing_jobs"])

	return &types.ToolResult{
		Success: true,
		Content: content,
		Data:    status,
	}, nil
}

// formatFailingJobs renders the scheduler status "failing_jobs" map (job ID
// -> consecutive failures, conduit-2six) as a status line.
func formatFailingJobs(v interface{}) string {
	failing, ok := v.(map[string]int)
	if !ok {
		return ""
	}
	if len(failing) == 0 {
		return "Failing Jobs: none\n"
	}
	ids := make([]string, 0, len(failing))
	for id := range failing {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf("%s (%d consecutive)", id, failing[id])
	}
	return "Failing Jobs: " + strings.Join(parts, ", ") + "\n"
}

// truncateForList shortens s to at most n bytes on a rune boundary.
func truncateForList(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
