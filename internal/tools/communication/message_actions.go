package communication

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
)

func (t *MessageTool) reactToMessage(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	messageId := toolargs.GetString(args, "messageId", "")
	emoji := toolargs.GetString(args, "emoji", "")

	if messageId == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "messageId parameter is required for react action",
		}, nil
	}

	if emoji == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "emoji parameter is required for react action",
		}, nil
	}

	// For now, this would need to be implemented with specific channel APIs
	return &types.ToolResult{
		Success: false,
		Error:   "react action not yet implemented",
	}, nil
}

func (t *MessageTool) deleteMessage(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	messageId := toolargs.GetString(args, "messageId", "")

	if messageId == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "messageId parameter is required for delete action",
		}, nil
	}

	// For now, this would need to be implemented with specific channel APIs
	return &types.ToolResult{
		Success: false,
		Error:   "delete action not yet implemented",
	}, nil
}

func (t *MessageTool) editMessage(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	messageId := toolargs.GetString(args, "messageId", "")
	message := toolargs.GetString(args, "message", "")

	if messageId == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "messageId parameter is required for edit action",
		}, nil
	}

	if message == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "message parameter is required for edit action",
		}, nil
	}

	// For now, this would need to be implemented with specific channel APIs
	return &types.ToolResult{
		Success: false,
		Error:   "edit action not yet implemented",
	}, nil
}

func (t *MessageTool) getChannelStatus(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	// Get real channel status from services
	var status map[string]interface{}
	if t.services != nil && t.services.ChannelSender != nil {
		channelStatus := t.services.ChannelSender.GetChannelStatusMap()
		status = make(map[string]interface{})
		// conduit-31jg.71: only the real status string. The fabricated
		// last_activity ("an hour ago") and message_count (0) placeholders
		// were rendered to the model as if they were facts.
		for channelID, statusStr := range channelStatus {
			status[channelID] = map[string]interface{}{
				"status": statusStr,
			}
		}
	} else {
		// Service unavailable
		return types.NewErrorResult("service_unavailable",
			"Channel status service is not available").
			WithSuggestions([]string{
				"Check if gateway is running",
				"Verify channel configuration",
				"Try again in a moment",
			}), nil
	}

	if len(status) == 0 {
		return &types.ToolResult{
			Success: true,
			Content: "No channels configured.",
			Data:    status,
		}, nil
	}

	content := t.formatChannelStatus(status)

	return &types.ToolResult{
		Success: true,
		Content: content,
		Data:    status,
	}, nil
}

// formatChannelStatus renders map[channelID]{"status": string, ...} as one
// sorted line per channel. conduit-31jg.71: it used to look for an
// "enabled" bool and an int64 message_count that getChannelStatus never
// sets, so Content held only channel names (plus a fake last-activity time).
func (t *MessageTool) formatChannelStatus(status map[string]interface{}) string {
	if len(status) == 0 {
		return "No channels configured."
	}

	ids := make([]string, 0, len(status))
	for id := range status {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("Channel Status (%d):\n", len(ids)))
	for _, channelID := range ids {
		line := "- " + channelID
		switch info := status[channelID].(type) {
		case string:
			line += ": " + info
		case map[string]interface{}:
			if st, ok := info["status"].(string); ok && st != "" {
				line += ": " + st
			} else if enabled, ok := info["enabled"].(bool); ok {
				if enabled {
					line += ": enabled"
				} else {
					line += ": disabled"
				}
			}
			if n, ok := toInt64(info["message_count"]); ok {
				line += fmt.Sprintf(" (messages: %d)", n)
			}
			if ts, ok := info["last_activity"].(time.Time); ok && !ts.IsZero() {
				line += " last activity " + ts.Format(time.RFC3339)
			}
		}
		builder.WriteString(line + "\n")
	}
	builder.WriteString("Use the channel ID as the target (or \"channel:userID\").\n")

	return builder.String()
}

func toInt64(v interface{}) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case int32:
		return int64(n), true
	case float64:
		return int64(n), true
	}
	return 0, false
}
