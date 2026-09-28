package communication

import (
	"context"
	"fmt"
	"strings"

	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
)

// ValidateParameters implements types.ParameterValidator interface
func (t *MessageTool) ValidateParameters(ctx context.Context, args map[string]interface{}) *types.ValidationResult {
	result := &types.ValidationResult{Valid: true}

	// Validate action parameter
	action := toolargs.GetString(args, "action", "send")
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
	target := toolargs.GetString(args, "target", "")
	message := toolargs.GetString(args, "message", "")

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
			Examples:        []interface{}{"telegram", "@username", "#channel", "123456789"},
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
	message := toolargs.GetString(args, "message", "")

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
			Examples:        []interface{}{[]string{"telegram", "discord"}, []string{"123456789"}},
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
	messageId := toolargs.GetString(args, "messageId", "")
	emoji := toolargs.GetString(args, "emoji", "")

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
	messageId := toolargs.GetString(args, "messageId", "")

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
		message := toolargs.GetString(args, "message", "")
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
			Examples:        []interface{}{"telegram", "discord", "123456789"},
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
