package communication

import (
	"context"
	"fmt"

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
