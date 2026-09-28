package core

import (
	"context"
	"fmt"
	"time"

	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
)

// SubAgentCanceller is the gateway capability behind SessionsCancel
// (conduit-38cz). It is asserted on ToolServices.Gateway rather than added
// to types.GatewayService so tool fakes need not implement it.
type SubAgentCanceller interface {
	// CancelSubAgent cancels the running sub-agent sessionKey (or the one
	// labelled label) and its descendants, returning the canceled key and
	// the number of sub-agents canceled. Only an ancestor session or the
	// owner may cancel.
	CancelSubAgent(ctx context.Context, sessionKey, label, reason string) (string, int, error)
}

// SessionsCancelTool cancels a sub-agent spawned with SessionsSpawn.
type SessionsCancelTool struct {
	services *types.ToolServices
}

// NewSessionsCancelTool creates the SessionsCancel tool.
func NewSessionsCancelTool(services *types.ToolServices) *SessionsCancelTool {
	return &SessionsCancelTool{services: services}
}

func (t *SessionsCancelTool) Name() string { return "SessionsCancel" }

func (t *SessionsCancelTool) Description() string {
	return "Cancel a running sub-agent that this session spawned with SessionsSpawn (by session key or label). " +
		"Its current LLM call and tool loop stop and any sub-agents it spawned are canceled too. " +
		"The cancel takes effect immediately, and a cancellation notice is delivered back to you automatically as a new turn (like a finished sub-agent's result) — no need to poll SessionStatus to confirm it. " +
		"Only the spawning session, an ancestor of it, or the owner can cancel a sub-agent."
}

func (t *SessionsCancelTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"sessionKey": map[string]interface{}{
				"type":        "string",
				"description": "Session key of the sub-agent (returned by SessionsSpawn)",
			},
			"label": map[string]interface{}{
				"type":        "string",
				"description": "Label given at spawn time (alternative to sessionKey)",
			},
			"reason": map[string]interface{}{
				"type":        "string",
				"description": "Why it is being canceled (optional; recorded and shown in the cancel notice)",
			},
		},
	}
}

func (t *SessionsCancelTool) canceller() SubAgentCanceller {
	if t.services == nil || t.services.Gateway == nil {
		return nil
	}
	c, _ := t.services.Gateway.(SubAgentCanceller)
	return c
}

func (t *SessionsCancelTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	sessionKey := toolargs.GetString(args, "sessionKey", "")
	label := toolargs.GetString(args, "label", "")
	reason := toolargs.GetString(args, "reason", "")
	if sessionKey == "" && label == "" {
		return &types.ToolResult{Success: false, Error: "either sessionKey or label must be provided"}, nil
	}
	c := t.canceller()
	if c == nil {
		return &types.ToolResult{Success: false, Error: "sub-agent cancel is not available (gateway service missing)"}, nil
	}
	key, n, err := c.CancelSubAgent(ctx, sessionKey, label, reason)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to cancel sub-agent: %v", err)}, nil
	}
	msg := fmt.Sprintf("Sub-agent %s canceled.", key)
	if n > 1 {
		msg = fmt.Sprintf("Sub-agent %s canceled, with %d sub-agent(s) it spawned.", key, n-1)
	}
	return &types.ToolResult{
		Success: true,
		Content: msg,
		Data:    map[string]interface{}{"sessionKey": key, "canceled": n, "reason": reason},
	}, nil
}

// SelfTest implements types.SelfTester.
func (t *SessionsCancelTool) SelfTest(ctx context.Context, opts *types.SelfTestOptions) *types.SelfTestResult {
	start := time.Now()
	result := &types.SelfTestResult{Status: types.SelfTestStatusOK, TestedAt: start}
	dep := types.DependencyStatus{Name: "GatewayService", Required: true}
	if t.canceller() == nil {
		dep.Status = "not_configured"
		dep.Message = "Gateway service (with sub-agent cancel) not available"
		result.Status = types.SelfTestStatusFailed
		result.Message = "SessionsCancel tool is not functional: gateway service unavailable"
		result.Suggestions = []string{"Ensure gateway is properly initialized"}
	} else {
		dep.Available = true
		dep.Status = "connected"
		result.Capabilities = []string{"cancel_by_key", "cancel_by_label", "cascade_descendants"}
		result.Message = "SessionsCancel tool is fully functional"
	}
	result.Dependencies = []types.DependencyStatus{dep}
	result.TestDuration = time.Since(start)
	return result
}
