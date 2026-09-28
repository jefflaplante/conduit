package scheduling

import (
	"context"
	"fmt"
	"time"

	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
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

Skill Scoping (important for small-context models):
- Set skills=["solar"] to load ONLY the "solar" skill into the prompt and tools for that job
- This dramatically reduces prompt size; use it when a job only needs specific skills
- Omit skills or set skills=[] to load all skills (default)

Regular Actions:
- Schedule a reminder: action=schedule, command="Remind the user to check email", delayMinutes=30
- Daily report: action=schedule, schedule="0 9 * * *", command="Generate daily briefing", type="go"
- Scoped job: action=schedule, schedule="*/30 * * * *", command="Check solar production", model="haiku", skills=["solar"]
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
			"skills": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Skill names to load for this job (empty = all skills). Use to reduce prompt size for small-context models.",
			},
		},
		"required": []string{"action"},
	}
}

func (t *CronTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	action := toolargs.GetString(args, "action", "")

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

// SelfTest implements types.SelfTester for CronTool.
func (t *CronTool) SelfTest(ctx context.Context, opts *types.SelfTestOptions) *types.SelfTestResult {
	start := time.Now()

	if opts == nil {
		opts = types.DefaultSelfTestOptions()
	}

	result := &types.SelfTestResult{
		Status:       types.SelfTestStatusOK,
		Capabilities: []string{},
		TestedAt:     time.Now(),
	}

	deps := []types.DependencyStatus{}

	// Check Gateway service (provides scheduler)
	gatewayDep := types.DependencyStatus{
		Name:     "Gateway",
		Required: true,
	}

	if t.services == nil || t.services.Gateway == nil {
		gatewayDep.Available = false
		gatewayDep.Status = "not_configured"
		gatewayDep.Message = "Gateway service not available in ToolServices"
		result.Status = types.SelfTestStatusFailed
		result.Message = "Cron service is not available — gateway not configured"
		result.Suggestions = []string{
			"Verify gateway is running",
			"Check scheduler configuration",
		}
	} else {
		gatewayDep.Available = true
		gatewayDep.Status = "connected"
		result.Capabilities = []string{
			"schedule", "list", "cancel", "run", "enable", "disable", "status",
			"heartbeat_list", "heartbeat_enable", "heartbeat_disable", "heartbeat_status",
		}

		// Get scheduler status for verbose output
		if opts.Verbose {
			status := t.services.Gateway.GetSchedulerStatus()
			jobs := t.services.Gateway.ListJobs()
			result.Details = map[string]interface{}{
				"scheduler_status": status,
				"job_count":        len(jobs),
			}
		}

		result.Status = types.SelfTestStatusOK
		result.Message = "Cron tool is fully functional"
	}
	deps = append(deps, gatewayDep)

	result.Dependencies = deps
	result.TestDuration = time.Since(start)

	if opts.IncludeExamples && result.IsFunctional() {
		result.Examples = []types.ToolExample{
			{
				Name:        "Set a reminder",
				Description: "Schedule a reminder in 30 minutes",
				Args: map[string]interface{}{
					"action":       "schedule",
					"command":      "Remind the user to check email",
					"delayMinutes": 30,
				},
				Expected: "Reminder scheduled as a one-shot job",
			},
			{
				Name:        "List jobs",
				Description: "List all scheduled jobs",
				Args: map[string]interface{}{
					"action": "list",
				},
				Expected: "Returns list of scheduled jobs",
			},
			{
				Name:        "Schedule recurring task",
				Description: "Schedule a daily task at 9 AM",
				Args: map[string]interface{}{
					"action":   "schedule",
					"schedule": "0 9 * * *",
					"command":  "Generate daily briefing",
					"name":     "Daily Briefing",
				},
				Expected: "Job scheduled with cron expression",
			},
		}
	}

	return result
}

// IncludeDataInModelOutput opts this tool into having ToolResult.Data
// rendered for the model: ids and lists needed for follow-up calls live
// only in Data (conduit-31jg.39).
func (t *CronTool) IncludeDataInModelOutput() bool { return true }
