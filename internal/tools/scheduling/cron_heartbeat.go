package scheduling

import (
	"context"
	"fmt"
	"strings"

	"conduit/internal/tools/types"
)

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
		strings.HasPrefix(job.ID, "agent_heartbeat") ||
		strings.Contains(strings.ToLower(job.Command), "heartbeat") ||
		strings.Contains(strings.ToLower(job.Name), "heartbeat")
}
