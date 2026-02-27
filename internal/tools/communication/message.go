package communication

import (
	"context"
	"fmt"
	"strings"
	"time"

	"conduit/internal/tools/schema"
	"conduit/internal/tools/types"
)

// MessageTool sends messages via configured channels
type MessageTool struct {
	services *types.ToolServices
}

func NewMessageTool(services *types.ToolServices) *MessageTool {
	return &MessageTool{services: services}
}

func (t *MessageTool) Name() string {
	return "Message"
}

func (t *MessageTool) Description() string {
	return "Send messages via configured channels (Telegram, Discord, etc.)"
}

func (t *MessageTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"send", "broadcast", "react", "delete", "edit", "status"},
				"description": "Action to perform",
				"default":     "send",
			},
			"target": map[string]interface{}{
				"type":        "string",
				"description": "Target channel/user ID or name",
			},
			"targets": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Multiple targets for broadcast action",
			},
			"message": map[string]interface{}{
				"type":        "string",
				"description": "Message content to send",
			},
			"messageId": map[string]interface{}{
				"type":        "string",
				"description": "Message ID for delete/edit/react actions",
			},
			"emoji": map[string]interface{}{
				"type":        "string",
				"description": "Emoji for react action",
			},
			"silent": map[string]interface{}{
				"type":        "boolean",
				"description": "Send message silently (no notification)",
				"default":     false,
			},
			"asVoice": map[string]interface{}{
				"type":        "boolean",
				"description": "Send as voice message (Telegram)",
				"default":     false,
			},
			"replyTo": map[string]interface{}{
				"type":        "string",
				"description": "Message ID to reply to",
			},
			"effectId": map[string]interface{}{
				"type":        "string",
				"description": "Message effect ID (e.g., invisible-ink, balloons)",
			},
			"imagePath": map[string]interface{}{
				"type":        "string",
				"description": "Path to an image file to send as a photo (e.g., /tmp/chart.png)",
			},
		},
		"required": []string{"action"},
	}
}

func (t *MessageTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	action := t.getStringArg(args, "action", "send")

	switch action {
	case "send":
		return t.sendMessage(ctx, args)
	case "broadcast":
		return t.broadcastMessage(ctx, args)
	case "react":
		return t.reactToMessage(ctx, args)
	case "delete":
		return t.deleteMessage(ctx, args)
	case "edit":
		return t.editMessage(ctx, args)
	case "status":
		return t.getChannelStatus(ctx, args)
	default:
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("unknown action: %s", action),
		}, nil
	}
}

func (t *MessageTool) sendMessage(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	target := t.getStringArg(args, "target", "")
	message := t.getStringArg(args, "message", "")

	// Enhanced parameter validation with helpful error messages
	if target == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "Target parameter is required for send action",
			Data: map[string]interface{}{
				"error_type": "missing_parameter",
				"parameter":  "target",
				"examples":   []string{"telegram", "discord", "1098302846"},
				"suggestions": []string{
					"Use Message tool with action='status' to see available channels",
					"Specify a valid channel ID or target",
				},
			},
		}, nil
	}

	if message == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "Message parameter is required for send action",
			Data: map[string]interface{}{
				"error_type": "missing_parameter",
				"parameter":  "message",
				"examples":   []string{"Hello!", "Task completed successfully"},
				"suggestions": []string{
					"Provide message content to send",
				},
			},
		}, nil
	}

	// Build options
	options := make(map[string]interface{})
	if silent := t.getBoolArg(args, "silent", false); silent {
		options["silent"] = true
	}
	if asVoice := t.getBoolArg(args, "asVoice", false); asVoice {
		options["asVoice"] = true
	}
	if replyTo := t.getStringArg(args, "replyTo", ""); replyTo != "" {
		options["replyTo"] = replyTo
	}
	if effectId := t.getStringArg(args, "effectId", ""); effectId != "" {
		options["effectId"] = effectId
	}

	// Use current session's user ID so the adapter knows who to send to
	userID := types.RequestUserID(ctx)

	// Build metadata for the outgoing message
	var metadata map[string]string
	if imagePath := t.getStringArg(args, "imagePath", ""); imagePath != "" {
		metadata = map[string]string{"image_path": imagePath}
	}
	if replyTo := t.getStringArg(args, "replyTo", ""); replyTo != "" {
		if metadata == nil {
			metadata = make(map[string]string)
		}
		metadata["reply_to_message_id"] = replyTo
	}

	// Send message via ChannelSender
	var err error
	if t.services != nil && t.services.ChannelSender != nil {
		err = t.services.ChannelSender.SendMessage(ctx, target, userID, message, metadata)
	} else {
		return &types.ToolResult{
			Success: false,
			Error:   "Message service is not available",
			Data: map[string]interface{}{
				"error_type": "service_unavailable",
				"suggestions": []string{
					"Check if gateway is running",
					"Verify channel configuration",
					"Try again in a moment",
				},
			},
		}, nil
	}

	if err != nil {
		// Enhanced error categorization with actionable suggestions
		errorType := "internal_error"
		suggestions := []string{"Try again", "Check channel configuration"}

		errStr := err.Error()
		if strings.Contains(errStr, "not found") || strings.Contains(errStr, "invalid") {
			errorType = "invalid_parameter"
			suggestions = []string{
				"Verify the target channel exists",
				"Use Message tool with action='status' to check channels",
				"Check channel ID format (should match available channels)",
			}
		} else if strings.Contains(errStr, "permission") || strings.Contains(errStr, "forbidden") {
			errorType = "permission_denied"
			suggestions = []string{
				"Check bot permissions in the target channel",
				"Ensure bot is member of the channel",
				"Verify bot has send message permission",
			}
		} else if strings.Contains(errStr, "timeout") || strings.Contains(errStr, "connection") {
			errorType = "service_unavailable"
			suggestions = []string{
				"Check network connectivity",
				"Try again in a moment",
				"Verify service is running",
			}
		}

		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("Failed to send message: %v", err),
			Data: map[string]interface{}{
				"error_type":     errorType,
				"parameter":      "target",
				"provided_value": target,
				"suggestions":    suggestions,
			},
		}, nil
	}

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Message sent successfully to %s", target),
		Data: map[string]interface{}{
			"action":  "send",
			"target":  target,
			"message": message,
			"options": options,
		},
	}, nil
}

func (t *MessageTool) broadcastMessage(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	message := t.getStringArg(args, "message", "")
	if message == "" {
		return types.NewErrorResult("missing_parameter",
			"Message parameter is required for broadcast action").
			WithParameter("message", nil).
			WithExamples([]string{"Hello everyone!", "System notification"}).
			WithSuggestions([]string{
				"Provide message content to broadcast",
			}), nil
	}

	// Get targets
	var targets []string
	if targetsInterface, ok := args["targets"]; ok {
		if targetsSlice, ok := targetsInterface.([]interface{}); ok {
			for _, target := range targetsSlice {
				if targetStr, ok := target.(string); ok {
					targets = append(targets, targetStr)
				}
			}
		}
	}

	if len(targets) == 0 {
		availableTargets := []string{"No channels configured"}
		if t.services != nil && t.services.ChannelSender != nil {
			availableTargets = t.services.ChannelSender.GetAvailableTargets()
		}

		return types.NewErrorResult("missing_parameter",
			"Targets parameter is required for broadcast action and must contain at least one target").
			WithParameter("targets", nil).
			WithAvailableValues(availableTargets).
			WithExamples([]string{"[\"telegram\", \"#general\"]", "[\"@user1\", \"@user2\"]"}).
			WithSuggestions([]string{
				"Provide an array of target channels or users",
				"Use 'Message' tool with action='status' to see available channels",
			}), nil
	}

	// Build options
	options := make(map[string]interface{})
	if silent := t.getBoolArg(args, "silent", false); silent {
		options["silent"] = true
	}

	// Validate targets and broadcast message to each individually
	var errors []string
	var invalidTargets []string
	channelStatus := make(map[string]string)
	if t.services != nil && t.services.ChannelSender != nil {
		channelStatus = t.services.ChannelSender.GetChannelStatusMap()
	}

	userID := types.RequestUserID(ctx)

	for _, target := range targets {
		if !t.isValidTarget(target, channelStatus) {
			invalidTargets = append(invalidTargets, target)
			continue
		}

		if t.services != nil && t.services.ChannelSender != nil {
			err := t.services.ChannelSender.SendMessage(ctx, target, userID, message, nil)
			if err != nil {
				errors = append(errors, fmt.Sprintf("%s: %v", target, err))
			}
		} else {
			errors = append(errors, fmt.Sprintf("%s: ChannelSender not available", target))
		}
	}

	// Handle invalid targets
	if len(invalidTargets) > 0 {
		availableTargets := []string{"No channels configured"}
		if t.services != nil && t.services.ChannelSender != nil {
			availableTargets = t.services.ChannelSender.GetAvailableTargets()
		}

		context := map[string]interface{}{
			"channel_status":  channelStatus,
			"invalid_targets": invalidTargets,
		}

		return types.NewErrorResult("invalid_parameter",
			fmt.Sprintf("Invalid targets: %s", strings.Join(invalidTargets, ", "))).
			WithParameter("targets", invalidTargets).
			WithAvailableValues(availableTargets).
			WithSuggestions([]string{
				"Use 'Message' tool with action='status' to check channel status",
				"Remove invalid targets from the list",
				"Verify target format and availability",
			}).
			WithContext(context), nil
	}

	// Handle send errors
	if len(errors) > 0 {
		return types.NewErrorResult("service_unavailable",
			fmt.Sprintf("Failed to broadcast to some targets: %s", strings.Join(errors, ", "))).
			WithSuggestions([]string{
				"Check failed targets individually",
				"Verify channel connectivity",
				"Try again for failed targets",
			}), nil
	}

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Message broadcast successfully to %d targets", len(targets)),
		Data: map[string]interface{}{
			"action":  "broadcast",
			"targets": targets,
			"message": message,
			"options": options,
		},
	}, nil
}

func (t *MessageTool) reactToMessage(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	messageId := t.getStringArg(args, "messageId", "")
	emoji := t.getStringArg(args, "emoji", "")

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
	messageId := t.getStringArg(args, "messageId", "")

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
	messageId := t.getStringArg(args, "messageId", "")
	message := t.getStringArg(args, "message", "")

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
		for channelID, statusStr := range channelStatus {
			status[channelID] = map[string]interface{}{
				"status":        statusStr,
				"last_activity": time.Now().Add(-1 * time.Hour), // Placeholder
				"message_count": 0,                              // Placeholder
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

func (t *MessageTool) formatChannelStatus(status map[string]interface{}) string {
	if len(status) == 0 {
		return "No channels configured."
	}

	var builder strings.Builder
	builder.WriteString("Channel Status:\n\n")

	for channelId, info := range status {
		builder.WriteString(fmt.Sprintf("**%s**\n", channelId))

		if channelInfo, ok := info.(map[string]interface{}); ok {
			if enabled, ok := channelInfo["enabled"].(bool); ok {
				status := "Disabled"
				if enabled {
					status = "Enabled"
				}
				builder.WriteString(fmt.Sprintf("  Status: %s\n", status))
			}
			if lastActivity, ok := channelInfo["last_activity"].(time.Time); ok {
				builder.WriteString(fmt.Sprintf("  Last Activity: %s\n", lastActivity.Format("2006-01-02 15:04:05")))
			}
			if messageCount, ok := channelInfo["message_count"].(int64); ok {
				builder.WriteString(fmt.Sprintf("  Messages Sent: %d\n", messageCount))
			}
		}
		builder.WriteString("\n")
	}

	return builder.String()
}

// Helper methods
func (t *MessageTool) getStringArg(args map[string]interface{}, key, defaultVal string) string {
	if val, ok := args[key].(string); ok {
		return val
	}
	return defaultVal
}

func (t *MessageTool) getBoolArg(args map[string]interface{}, key string, defaultVal bool) bool {
	if val, ok := args[key].(bool); ok {
		return val
	}
	return defaultVal
}

// isValidTarget checks if a target is valid against available channels
func (t *MessageTool) isValidTarget(target string, channelStatus map[string]string) bool {
	if channelStatus == nil {
		return false
	}

	// Direct channel match
	if _, exists := channelStatus[target]; exists {
		return true
	}

	// Check for pattern matches (e.g., @username, #channel)
	if strings.HasPrefix(target, "@") || strings.HasPrefix(target, "#") {
		// These would require channel-specific validation
		// For now, allow them through as potentially valid
		return true
	}

	// Check for provider:type:id format
	if strings.Count(target, ":") >= 1 {
		return true
	}

	return false
}

// ValidateParameters implements types.ParameterValidator interface
func (t *MessageTool) ValidateParameters(ctx context.Context, args map[string]interface{}) *types.ValidationResult {
	result := &types.ValidationResult{Valid: true}

	// Validate action parameter
	action := t.getStringArg(args, "action", "send")
	validActions := []string{"send", "broadcast", "react", "delete", "edit", "status"}
	actionValid := false
	for _, validAction := range validActions {
		if action == validAction {
			actionValid = true
			break
		}
	}

	if !actionValid {
		result.Valid = false
		result.Errors = append(result.Errors, types.ValidationError{
			Parameter:       "action",
			Message:         fmt.Sprintf("'%s' is not a valid action", action),
			ProvidedValue:   action,
			AvailableValues: validActions,
			Examples:        []interface{}{"send", "status", "broadcast"},
			ErrorType:       "invalid_value",
		})
	}

	// Action-specific validation
	switch action {
	case "send":
		t.validateSendParameters(ctx, args, result)
	case "broadcast":
		t.validateBroadcastParameters(ctx, args, result)
	case "react":
		t.validateReactParameters(ctx, args, result)
	case "delete", "edit":
		t.validateMessageIdParameters(ctx, args, result, action)
	case "status":
		// No additional parameters required for status
	}

	// Generate suggestions if there are errors
	if !result.Valid {
		result.Suggestions = t.generateSuggestions(result.Errors, action)
	}

	return result
}

// validateSendParameters validates parameters for send action
func (t *MessageTool) validateSendParameters(ctx context.Context, args map[string]interface{}, result *types.ValidationResult) {
	target := t.getStringArg(args, "target", "")
	message := t.getStringArg(args, "message", "")

	// Target validation
	if target == "" {
		result.Valid = false
		availableTargets := []string{"No channels configured"}
		if t.services != nil && t.services.ChannelSender != nil {
			availableTargets = t.services.ChannelSender.GetAvailableTargets()
		}

		result.Errors = append(result.Errors, types.ValidationError{
			Parameter:       "target",
			Message:         "is required for send action",
			AvailableValues: availableTargets,
			Examples:        []interface{}{"telegram", "@username", "#channel", "1098302846"},
			DiscoveryHint:   "Use action='status' to see available channels",
			ErrorType:       "missing_required",
		})
	} else {
		// Validate target exists and is available
		t.validateChannelTarget(ctx, target, result)
	}

	// Message validation
	if message == "" {
		result.Valid = false
		result.Errors = append(result.Errors, types.ValidationError{
			Parameter: "message",
			Message:   "is required for send action",
			Examples:  []interface{}{"Hello!", "Task completed successfully.", "**Bold** and _italic_ text supported"},
			ErrorType: "missing_required",
		})
	} else if len(strings.TrimSpace(message)) == 0 {
		result.Valid = false
		result.Errors = append(result.Errors, types.ValidationError{
			Parameter:     "message",
			Message:       "cannot be empty or contain only whitespace",
			ProvidedValue: message,
			Examples:      []interface{}{"Hello!", "Task completed successfully."},
			ErrorType:     "invalid_value",
		})
	}
}

// validateBroadcastParameters validates parameters for broadcast action
func (t *MessageTool) validateBroadcastParameters(ctx context.Context, args map[string]interface{}, result *types.ValidationResult) {
	message := t.getStringArg(args, "message", "")

	// Message validation
	if message == "" {
		result.Valid = false
		result.Errors = append(result.Errors, types.ValidationError{
			Parameter: "message",
			Message:   "is required for broadcast action",
			Examples:  []interface{}{"System announcement", "Maintenance completed"},
			ErrorType: "missing_required",
		})
	}

	// Targets validation
	var targets []string
	if targetsInterface, ok := args["targets"]; ok {
		if targetsSlice, ok := targetsInterface.([]interface{}); ok {
			for _, target := range targetsSlice {
				if targetStr, ok := target.(string); ok {
					targets = append(targets, targetStr)
				}
			}
		}
	}

	if len(targets) == 0 {
		result.Valid = false
		availableTargets := []string{"No channels configured"}
		if t.services != nil && t.services.ChannelSender != nil {
			availableTargets = t.services.ChannelSender.GetAvailableTargets()
		}

		result.Errors = append(result.Errors, types.ValidationError{
			Parameter:       "targets",
			Message:         "is required for broadcast action and must contain at least one target",
			AvailableValues: availableTargets,
			Examples:        []interface{}{[]string{"telegram", "discord"}, []string{"1098302846"}},
			ErrorType:       "missing_required",
		})
	} else {
		// Validate each target
		for i, target := range targets {
			paramName := fmt.Sprintf("targets[%d]", i)
			if target == "" {
				result.Valid = false
				result.Errors = append(result.Errors, types.ValidationError{
					Parameter:     paramName,
					Message:       "cannot be empty",
					ProvidedValue: target,
					ErrorType:     "invalid_value",
				})
			} else {
				// Create a temporary validation result for this specific target
				tempResult := &types.ValidationResult{Valid: true}
				t.validateChannelTarget(ctx, target, tempResult)

				// Transfer any errors to main result with adjusted parameter name
				for _, err := range tempResult.Errors {
					err.Parameter = paramName
					result.Errors = append(result.Errors, err)
					result.Valid = false
				}
			}
		}
	}
}

// validateReactParameters validates parameters for react action
func (t *MessageTool) validateReactParameters(ctx context.Context, args map[string]interface{}, result *types.ValidationResult) {
	messageId := t.getStringArg(args, "messageId", "")
	emoji := t.getStringArg(args, "emoji", "")

	if messageId == "" {
		result.Valid = false
		result.Errors = append(result.Errors, types.ValidationError{
			Parameter: "messageId",
			Message:   "is required for react action",
			Examples:  []interface{}{"12345", "987654321"},
			ErrorType: "missing_required",
		})
	}

	if emoji == "" {
		result.Valid = false
		result.Errors = append(result.Errors, types.ValidationError{
			Parameter: "emoji",
			Message:   "is required for react action",
			Examples:  []interface{}{"👍", "❤️", "😂", "🤔", "💡", "✅"},
			ErrorType: "missing_required",
		})
	}
}

// validateMessageIdParameters validates parameters for delete/edit actions
func (t *MessageTool) validateMessageIdParameters(ctx context.Context, args map[string]interface{}, result *types.ValidationResult, action string) {
	messageId := t.getStringArg(args, "messageId", "")

	if messageId == "" {
		result.Valid = false
		result.Errors = append(result.Errors, types.ValidationError{
			Parameter: "messageId",
			Message:   fmt.Sprintf("is required for %s action", action),
			Examples:  []interface{}{"12345", "987654321"},
			ErrorType: "missing_required",
		})
	}

	if action == "edit" {
		message := t.getStringArg(args, "message", "")
		if message == "" {
			result.Valid = false
			result.Errors = append(result.Errors, types.ValidationError{
				Parameter: "message",
				Message:   "is required for edit action",
				Examples:  []interface{}{"Updated message content", "Corrected information"},
				ErrorType: "missing_required",
			})
		}
	}
}

// validateChannelTarget validates a specific channel target
func (t *MessageTool) validateChannelTarget(ctx context.Context, target string, result *types.ValidationResult) {
	if t.services == nil || t.services.ChannelSender == nil {
		result.Valid = false
		result.Errors = append(result.Errors, types.ValidationError{
			Parameter:     "target",
			Message:       "channel service is not available",
			ProvidedValue: target,
			ErrorType:     "service_unavailable",
		})
		return
	}

	channelStatus := t.services.ChannelSender.GetChannelStatusMap()
	if !t.isValidTarget(target, channelStatus) {
		result.Valid = false
		availableTargets := t.services.ChannelSender.GetAvailableTargets()

		errorMsg := fmt.Sprintf("channel '%s' not found or unavailable", target)

		// Check if target exists but is offline
		if status, exists := channelStatus[target]; exists && status == "offline" {
			errorMsg = fmt.Sprintf("channel '%s' is offline", target)
		}

		result.Errors = append(result.Errors, types.ValidationError{
			Parameter:       "target",
			Message:         errorMsg,
			ProvidedValue:   target,
			AvailableValues: availableTargets,
			Examples:        []interface{}{"telegram", "discord", "1098302846"},
			DiscoveryHint:   "Use action='status' to check current channel availability",
			ErrorType:       "invalid_parameter",
		})
	}
}

// generateSuggestions creates helpful suggestions based on validation errors
func (t *MessageTool) generateSuggestions(errors []types.ValidationError, action string) []string {
	var suggestions []string

	hasTargetError := false
	hasMessageError := false

	for _, err := range errors {
		switch err.Parameter {
		case "target", "targets":
			hasTargetError = true
		case "message":
			hasMessageError = true
		}
	}

	if hasTargetError {
		suggestions = append(suggestions,
			"Use 'Message' tool with action='status' to see all available channels",
			"Check that channels are properly configured and online")
	}

	if hasMessageError && action == "send" {
		suggestions = append(suggestions,
			"Provide message content to send to the target channel")
	}

	// Action-specific suggestions
	switch action {
	case "broadcast":
		if hasTargetError {
			suggestions = append(suggestions,
				"Provide an array of target channel IDs for broadcast")
		}
	case "react":
		suggestions = append(suggestions,
			"Reactions require a valid message ID and emoji character")
	}

	// General suggestions
	if len(errors) > 1 {
		suggestions = append(suggestions,
			"Multiple parameter errors - fix required parameters first")
	}

	return suggestions
}

// GetUsageExamples implements types.UsageExampleProvider for MessageTool.
func (t *MessageTool) GetUsageExamples() []types.ToolExample {
	return []types.ToolExample{
		{
			Name:        "Send a simple message",
			Description: "Send a text message to a Telegram channel",
			Args: map[string]interface{}{
				"action":  "send",
				"target":  "telegram",
				"message": "Hello! Task completed successfully.",
			},
			Expected: "Sends message to the configured Telegram channel",
		},
		{
			Name:        "Send with reply",
			Description: "Reply to a specific message in a channel",
			Args: map[string]interface{}{
				"action":  "send",
				"target":  "1098302846",
				"message": "Got it! I'll take care of that.",
				"replyTo": "12345",
			},
			Expected: "Sends message as a reply to message ID 12345",
		},
		{
			Name:        "Send voice message",
			Description: "Send a message as voice using text-to-speech",
			Args: map[string]interface{}{
				"action":  "send",
				"target":  "telegram",
				"message": "This will be converted to voice message.",
				"asVoice": true,
			},
			Expected: "Converts text to speech and sends as voice message",
		},
		{
			Name:        "Broadcast to multiple channels",
			Description: "Send the same message to multiple targets",
			Args: map[string]interface{}{
				"action":  "broadcast",
				"targets": []string{"telegram", "discord", "1098302846"},
				"message": "System maintenance completed successfully.",
			},
			Expected: "Sends message to all specified channels simultaneously",
		},
		{
			Name:        "Check channel status",
			Description: "Get status information for all configured channels",
			Args: map[string]interface{}{
				"action": "status",
			},
			Expected: "Returns connectivity status and information for all channels",
		},
		{
			Name:        "Send with markdown formatting",
			Description: "Send a message with bold and italic formatting",
			Args: map[string]interface{}{
				"action":  "send",
				"target":  "telegram",
				"message": "**Task completed!** _Duration: 5 minutes_\n\n`Status: Success`",
			},
			Expected: "Sends formatted message with bold, italic, and code formatting",
		},
		{
			Name:        "Send a photo",
			Description: "Send an image file as a photo with an optional caption",
			Args: map[string]interface{}{
				"action":    "send",
				"target":    "telegram",
				"message":   "Here is the chart you requested",
				"imagePath": "/tmp/chart.png",
			},
			Expected: "Sends the image as a Telegram photo with the message as caption",
		},
	}
}

// GetSchemaHints implements types.EnhancedSchemaProvider.
// Returns hints for enhancing the message tool schema with discovery data.
func (t *MessageTool) GetSchemaHints() map[string]schema.SchemaHints {
	return map[string]schema.SchemaHints{
		"action": {
			Examples: []interface{}{"send", "status", "broadcast"},
			ValidationHints: []string{
				"'send' for single target, 'broadcast' for multiple",
				"'status' to check channel connectivity",
				"'react', 'delete', 'edit' not yet implemented",
			},
		},
		"target": {
			Examples:          []interface{}{"telegram", "discord", "1098302846"},
			DiscoveryType:     "channels",
			EnumFromDiscovery: false, // Show available channels as examples, don't restrict
			ValidationHints: []string{
				"Use channel ID from available channels",
				"Use 'status' action to see current channel availability",
				"Channels must be online to receive messages",
			},
		},
		"targets": {
			Examples: []interface{}{
				[]string{"telegram", "discord"},
				[]string{"1098302846", "1234567890"},
			},
			ValidationHints: []string{
				"Array of channel IDs for broadcast action",
				"Each target must be a valid channel from available list",
				"Broadcast only works with online channels",
			},
		},
		"message": {
			Examples: []interface{}{
				"Hello!",
				"Task completed successfully.",
				"**Bold text** and _italic text_ supported",
				"Use `code` for inline code blocks",
			},
			ValidationHints: []string{
				"Markdown formatting supported for most channels",
				"Keep messages concise for best delivery",
				"Empty messages not allowed",
			},
		},
		"emoji": {
			Examples: []interface{}{"👍", "❤️", "😂", "🤔", "💡", "✅", "👀"},
			ValidationHints: []string{
				"Unicode emoji for react action",
				"Single emoji characters work best",
				"Some channels may not support all emoji reactions",
			},
		},
		"effectId": {
			Examples: []interface{}{"invisible-ink", "balloons", "confetti", "heart"},
			ValidationHints: []string{
				"Telegram-specific message effects",
				"Not all clients support effects",
				"Effects may not work in all chat types",
			},
		},
		"messageId": {
			Examples: []interface{}{"12345", "987654321"},
			ValidationHints: []string{
				"Numeric message ID from channel",
				"Required for react, delete, edit actions",
				"Must be a valid message ID that exists in the channel",
			},
		},
		"replyTo": {
			Examples: []interface{}{"12345", "987654321"},
			ValidationHints: []string{
				"Numeric message ID to reply to",
				"Message must exist and be accessible",
				"Creates a threaded reply on supported platforms",
			},
		},
		"imagePath": {
			Examples: []interface{}{"/tmp/chart.png", "/tmp/screenshot.jpg"},
			ValidationHints: []string{
				"Absolute path to an image file on disk",
				"Supported formats: PNG, JPG, GIF",
				"The image is uploaded as a photo; message text becomes the caption",
			},
		},
	}
}
