package communication

import (
	"context"
	"fmt"
	"sort"
	"strings"

	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
)

// sendErrorResult builds a failed send result. conduit-31jg.73: the
// suggestions/examples go into ErrorDetails, which the execution engine
// renders for failed results; Data (kept for API compatibility) is never
// shown to the model on failure, so suggestions there were invisible.
func sendErrorResult(errorType, msg, param string, value interface{}, examples, suggestions []string) *types.ToolResult {
	r := types.NewErrorResult(errorType, msg).WithSuggestions(suggestions)
	if len(examples) > 0 {
		r.WithExamples(examples)
	}
	data := map[string]interface{}{"error_type": errorType, "suggestions": suggestions}
	if param != "" {
		r.WithParameter(param, value)
		data["parameter"] = param
		if value != nil {
			data["provided_value"] = value
		}
	}
	if len(examples) > 0 {
		data["examples"] = examples
	}
	r.Data = data
	return r
}

func (t *MessageTool) sendMessage(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	target := toolargs.GetString(args, "target", "")
	message := toolargs.GetString(args, "message", "")

	// Enhanced parameter validation with helpful error messages
	if target == "" {
		return sendErrorResult("missing_parameter", "Target parameter is required for send action",
			"target", nil,
			[]string{"telegram", "discord", "123456789"},
			[]string{
				"Use Message tool with action='status' to see available channels",
				"Specify a valid channel ID or target",
			}), nil
	}

	if message == "" {
		return sendErrorResult("missing_parameter", "Message parameter is required for send action",
			"message", nil,
			[]string{"Hello!", "Task completed successfully"},
			[]string{"Provide message content to send"}), nil
	}

	// Build options
	options := make(map[string]interface{})
	if silent := toolargs.GetBool(args, "silent", false); silent {
		options["silent"] = true
	}
	if asVoice := toolargs.GetBool(args, "asVoice", false); asVoice {
		options["asVoice"] = true
	}
	if replyTo := toolargs.GetString(args, "replyTo", ""); replyTo != "" {
		options["replyTo"] = replyTo
	}
	if effectId := toolargs.GetString(args, "effectId", ""); effectId != "" {
		options["effectId"] = effectId
	}

	// Parse target format: "telegram:chatid" → channelID="telegram", userID="chatid"
	// If no ":" prefix, use the session's user ID and treat target as the channel name.
	channelID, targetUserID := parseTarget(target)
	if targetUserID == "" {
		// Bare channel name (e.g. "telegram") — use the session's originating user ID
		targetUserID = types.RequestUserID(ctx)
	}

	// Build metadata for the outgoing message
	var metadata map[string]string
	if imagePath := toolargs.GetString(args, "imagePath", ""); imagePath != "" {
		metadata = map[string]string{"image_path": imagePath}
	}
	if replyTo := toolargs.GetString(args, "replyTo", ""); replyTo != "" {
		if metadata == nil {
			metadata = make(map[string]string)
		}
		metadata["reply_to_message_id"] = replyTo
	}

	// Send message via ChannelSender
	var err error
	if t.services != nil && t.services.ChannelSender != nil {
		err = t.services.ChannelSender.SendMessage(ctx, channelID, targetUserID, message, metadata)
	} else {
		return sendErrorResult("service_unavailable", "Message service is not available",
			"", nil, nil,
			[]string{
				"Check if gateway is running",
				"Verify channel configuration",
				"Try again in a moment",
			}), nil
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

		return sendErrorResult(errorType, fmt.Sprintf("Failed to send message: %v", err),
			"target", target, nil, suggestions), nil
	}

	return &types.ToolResult{
		Success: true,
		Content: sendSuccessContent(target, channelID, targetUserID, options),
		Data: map[string]interface{}{
			"action":  "send",
			"target":  target,
			"message": message,
			"options": options,
		},
	}, nil
}

func (t *MessageTool) broadcastMessage(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	message := toolargs.GetString(args, "message", "")
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
	if silent := toolargs.GetBool(args, "silent", false); silent {
		options["silent"] = true
	}

	// Validate targets and broadcast message to each individually
	var errors []string
	var invalidTargets []string
	channelStatus := make(map[string]string)
	if t.services != nil && t.services.ChannelSender != nil {
		channelStatus = t.services.ChannelSender.GetChannelStatusMap()
	}

	sessionUserID := types.RequestUserID(ctx)

	for _, target := range targets {
		if !t.isValidTarget(target, channelStatus) {
			invalidTargets = append(invalidTargets, target)
			continue
		}

		if t.services != nil && t.services.ChannelSender != nil {
			chanID, chanUserID := parseTarget(target)
			if chanUserID == "" {
				chanUserID = sessionUserID
			}
			err := t.services.ChannelSender.SendMessage(ctx, chanID, chanUserID, message, nil)
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
		Content: fmt.Sprintf("Message broadcast successfully to %d targets: %s", len(targets), strings.Join(targets, ", ")),
		Data: map[string]interface{}{
			"action":  "broadcast",
			"targets": targets,
			"message": message,
			"options": options,
		},
	}, nil
}

// parseTarget splits a target string of the form "channel:userID" into its two
// components. If the target contains no ":" separator (e.g. "telegram"), the
// raw value is returned as channelID and userID is empty.
// Examples:
//
//	"telegram:123456789" → ("telegram", "123456789")
//	"telegram"            → ("telegram", "")
func parseTarget(target string) (channelID, userID string) {
	if idx := strings.IndexByte(target, ':'); idx >= 0 {
		return target[:idx], target[idx+1:]
	}
	return target, ""
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

// sendSuccessContent describes a successful send, including the resolved
// channel/user and any options, so the model does not need Data (which only
// echoes the message body back). conduit-31jg.71
func sendSuccessContent(target, channelID, userID string, options map[string]interface{}) string {
	content := fmt.Sprintf("Message sent successfully to %s", target)
	if userID != "" && !strings.Contains(target, ":") {
		content += fmt.Sprintf(" (channel %s, user %s)", channelID, userID)
	}
	if len(options) > 0 {
		keys := make([]string, 0, len(options))
		for k, v := range options {
			keys = append(keys, fmt.Sprintf("%s=%v", k, v))
		}
		sort.Strings(keys)
		content += " [" + strings.Join(keys, ", ") + "]"
	}
	return content
}
