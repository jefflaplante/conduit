package communication

import (
	"context"
	"fmt"
	"time"

	"conduit/internal/tools/types"
)

// StatusUpdateTool sends progress updates back to the user who initiated the request.
// Unlike Message tool, it doesn't require a target parameter — it automatically
// sends to the originating channel/user from the request context.
type StatusUpdateTool struct {
	services *types.ToolServices
}

func NewStatusUpdateTool(services *types.ToolServices) *StatusUpdateTool {
	return &StatusUpdateTool{services: services}
}

func (t *StatusUpdateTool) Name() string {
	return "StatusUpdate"
}

func (t *StatusUpdateTool) Description() string {
	return "Send a progress update to the user who initiated this request. " +
		"Use this to keep users informed during multi-step or long-running operations. " +
		"Updates are sent to the originating channel automatically."
}

func (t *StatusUpdateTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"message": map[string]interface{}{
				"type":        "string",
				"description": "The progress update message to send (keep concise, 1-2 sentences)",
			},
		},
		"required": []string{"message"},
	}
}

func (t *StatusUpdateTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	message, ok := args["message"].(string)
	if !ok || message == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "message parameter is required",
		}, nil
	}

	// Extract originating channel/user from request context
	channelID := types.RequestChannelID(ctx)
	userID := types.RequestUserID(ctx)

	if channelID == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "no originating channel found in request context",
		}, nil
	}

	// Verify ChannelSender is available
	if t.services == nil || t.services.ChannelSender == nil {
		return &types.ToolResult{
			Success: false,
			Error:   "channel sender service is not available",
		}, nil
	}

	// Format the update with a marker
	formattedMessage := "📍 " + message

	// Send via ChannelSender
	err := t.services.ChannelSender.SendMessage(ctx, channelID, userID, formattedMessage, nil)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to send status update: %v", err),
		}, nil
	}

	return &types.ToolResult{
		Success: true,
		Content: "Status update sent",
		Data: map[string]interface{}{
			"channel": channelID,
			"message": message,
		},
	}, nil
}

// GetUsageExamples implements types.UsageExampleProvider.
func (t *StatusUpdateTool) GetUsageExamples() []types.ToolExample {
	return []types.ToolExample{
		{
			Name:        "Report search progress",
			Description: "Inform user that you're searching for files",
			Args: map[string]interface{}{
				"message": "Searching codebase for authentication-related files...",
			},
			Expected: "Sends update to user's chat",
		},
		{
			Name:        "Report findings",
			Description: "Inform user of intermediate results",
			Args: map[string]interface{}{
				"message": "Found 5 relevant files, analyzing implementation patterns...",
			},
			Expected: "Sends update to user's chat",
		},
		{
			Name:        "Report approach change",
			Description: "Inform user when changing strategy",
			Args: map[string]interface{}{
				"message": "Initial search found no matches, expanding to include test files...",
			},
			Expected: "Sends update to user's chat",
		},
	}
}

// SelfTest implements types.SelfTester for StatusUpdateTool.
func (t *StatusUpdateTool) SelfTest(ctx context.Context, opts *types.SelfTestOptions) *types.SelfTestResult {
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

	// Check ChannelSender service
	channelSenderDep := types.DependencyStatus{
		Name:     "ChannelSender",
		Required: true,
	}

	if t.services == nil || t.services.ChannelSender == nil {
		channelSenderDep.Available = false
		channelSenderDep.Status = "not_configured"
		channelSenderDep.Message = "ChannelSender service not available in ToolServices"
		result.Status = types.SelfTestStatusFailed
		result.Message = "StatusUpdate service is not configured"
		result.Suggestions = []string{
			"Verify gateway is running",
			"Check channel configuration in config.json",
		}
	} else {
		channelSenderDep.Available = true
		channelSenderDep.Status = "connected"

		// Check if there are any channels available
		channelStatus := t.services.ChannelSender.GetChannelStatusMap()

		if len(channelStatus) == 0 {
			result.Status = types.SelfTestStatusDegraded
			result.Message = "StatusUpdate service available but no channels configured"
			result.Suggestions = []string{
				"Configure at least one channel (Telegram, Discord, etc.)",
				"StatusUpdate requires an active channel to send updates",
			}
			result.UnavailableCapabilities = []string{"send-update"}
			result.Capabilities = []string{}
		} else {
			// Count online channels
			onlineCount := 0
			for _, status := range channelStatus {
				if status == "online" || status == "connected" {
					onlineCount++
				}
			}

			if onlineCount == 0 {
				result.Status = types.SelfTestStatusDegraded
				result.Message = fmt.Sprintf("Channels configured but none online (found %d)", len(channelStatus))
				result.UnavailableCapabilities = []string{"send-update"}
				result.Suggestions = []string{
					"Check channel connectivity",
					"StatusUpdate requires at least one online channel",
				}
			} else {
				result.Status = types.SelfTestStatusOK
				result.Message = fmt.Sprintf("StatusUpdate tool fully functional (%d/%d channels online)",
					onlineCount, len(channelStatus))
				result.Capabilities = []string{"send-update"}

				if opts.Verbose {
					result.Details = map[string]interface{}{
						"channel_count": len(channelStatus),
						"online_count":  onlineCount,
					}
				}
			}
		}
	}
	deps = append(deps, channelSenderDep)

	result.Dependencies = deps
	result.TestDuration = time.Since(start)

	if opts.IncludeExamples && result.IsFunctional() {
		result.Examples = t.GetUsageExamples()
	}

	return result
}
