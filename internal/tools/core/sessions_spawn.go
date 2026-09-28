package core

import (
	"context"
	"fmt"
	"time"

	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
)

// SessionsSpawnTool spawns new sub-agent sessions
type SessionsSpawnTool struct {
	services *types.ToolServices
}

func NewSessionsSpawnTool(services *types.ToolServices) *SessionsSpawnTool {
	return &SessionsSpawnTool{services: services}
}

func (t *SessionsSpawnTool) Name() string {
	return "SessionsSpawn"
}

func (t *SessionsSpawnTool) Description() string {
	return "Spawn a new sub-agent to work on a task asynchronously. You MUST call this tool to delegate work — describing or narrating a spawn does nothing. The sub-agent runs independently and its result (or failure) is delivered back to you automatically as a new turn when it finishes; with announce=true (default) it is also posted to the user. Do not poll or wait on it — end your turn. SessionStatus is only for an optional one-off progress check."
}

func (t *SessionsSpawnTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task": map[string]interface{}{
				"type":        "string",
				"description": "The complete task prompt for the sub-agent. Be specific — this is the only instruction it receives.",
			},
			"agentId": map[string]interface{}{
				"type":        "string",
				"description": "Specific agent ID to spawn (optional)",
			},
			"model": map[string]interface{}{
				"type":        "string",
				"description": "AI model to use for the sub-agent (optional)",
			},
			"label": map[string]interface{}{
				"type":        "string",
				"description": "Label for the spawned session (optional)",
			},
			"timeoutSeconds": map[string]interface{}{
				"type":        "integer",
				"description": "Session timeout in seconds",
				"default":     300,
			},
			"announce": map[string]interface{}{
				"type":        "boolean",
				"description": "Announce results back to user when complete (default: true). Set to false for quiet orchestration.",
				"default":     true,
			},
		},
		"required": []string{"task"},
	}
}

func (t *SessionsSpawnTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	task, ok := args["task"].(string)
	if !ok {
		return &types.ToolResult{
			Success: false,
			Error:   "task parameter is required and must be a string",
		}, nil
	}

	agentId := toolargs.GetString(args, "agentId", "")
	model := toolargs.GetString(args, "model", "")
	label := toolargs.GetString(args, "label", "")
	timeoutSeconds := toolargs.GetInt(args, "timeoutSeconds", 300)
	announce := toolargs.GetBool(args, "announce", true) // Default: announce results

	if t.services.Gateway == nil {
		return &types.ToolResult{
			Success: false,
			Error:   "gateway service not available",
		}, nil
	}

	// Spawn sub-agent with optional announcement
	sessionKey, err := t.services.Gateway.SpawnSubAgentWithCallback(
		ctx, task, agentId, model, label, timeoutSeconds,
		types.RequestChannelID(ctx), types.RequestUserID(ctx), announce, nil,
	)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to spawn sub-agent: %v", err),
		}, nil
	}

	resultMsg := fmt.Sprintf("Sub-agent spawned (session: %s, task: %q).", sessionKey, truncateTask(task, 80))
	if !announce {
		resultMsg += " Running quietly — the result will be delivered to you automatically when it finishes; no need to poll."
	}

	return &types.ToolResult{
		Success: true,
		Content: resultMsg,
		Data: map[string]interface{}{
			"sessionKey":     sessionKey,
			"task":           task,
			"agentId":        agentId,
			"model":          model,
			"label":          label,
			"timeoutSeconds": timeoutSeconds,
			"announce":       announce,
		},
	}, nil
}

func truncateTask(task string, maxLen int) string {
	if len(task) <= maxLen {
		return task
	}
	return task[:maxLen] + "..."
}

// SelfTest implements types.SelfTester for SessionsSpawnTool.
func (t *SessionsSpawnTool) SelfTest(ctx context.Context, opts *types.SelfTestOptions) *types.SelfTestResult {
	start := time.Now()

	if opts == nil {
		opts = types.DefaultSelfTestOptions()
	}

	result := &types.SelfTestResult{
		Status:   types.SelfTestStatusOK,
		TestedAt: time.Now(),
	}

	deps := []types.DependencyStatus{}

	// Check Gateway service - required for spawning sub-agents
	gatewayDep := types.DependencyStatus{
		Name:     "GatewayService",
		Required: true,
	}

	if t.services == nil || t.services.Gateway == nil {
		gatewayDep.Available = false
		gatewayDep.Status = "not_configured"
		gatewayDep.Message = "Gateway service not available"
		result.Status = types.SelfTestStatusFailed
		result.Message = "SessionsSpawn tool is not functional: gateway service unavailable"
		result.Suggestions = []string{
			"Ensure gateway is properly initialized",
			"Check that ToolServices has Gateway set",
		}
	} else {
		gatewayDep.Available = true
		gatewayDep.Status = "connected"
		result.Capabilities = []string{"spawn_subagent", "async_tasks", "announce_results"}
		result.Status = types.SelfTestStatusOK
		result.Message = "SessionsSpawn tool is fully functional"
	}
	deps = append(deps, gatewayDep)

	result.Dependencies = deps
	result.TestDuration = time.Since(start)

	if opts.IncludeExamples && result.IsFunctional() {
		result.Examples = []types.ToolExample{
			{
				Name:        "Spawn sub-agent with announcement",
				Description: "Spawn a sub-agent that announces results when complete",
				Args: map[string]interface{}{
					"task":     "Research the topic and summarize findings",
					"announce": true,
				},
				Expected: "Sub-agent spawned; results will be announced to user when complete",
			},
			{
				Name:        "Spawn quiet sub-agent",
				Description: "Spawn a sub-agent for background work without announcement",
				Args: map[string]interface{}{
					"task":           "Process data in the background",
					"announce":       false,
					"timeoutSeconds": 600,
				},
				Expected: "Sub-agent spawned quietly; its result wakes this session automatically when complete",
			},
		}
	}

	return result
}
