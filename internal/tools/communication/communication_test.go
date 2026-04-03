package communication

import (
	"context"
	"errors"
	"testing"

	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockChannelSender implements types.ChannelSender for testing
type mockChannelSender struct {
	sendError      error
	sentMessages   []sentMessage
	channelStatus  map[string]string
	availableTargets []string
}

type sentMessage struct {
	channelID string
	userID    string
	content   string
	metadata  map[string]string
}

func (m *mockChannelSender) SendMessage(ctx context.Context, channelID, userID, content string, metadata map[string]string) error {
	m.sentMessages = append(m.sentMessages, sentMessage{
		channelID: channelID,
		userID:    userID,
		content:   content,
		metadata:  metadata,
	})
	return m.sendError
}

func (m *mockChannelSender) GetChannelStatusMap() map[string]string {
	return m.channelStatus
}

func (m *mockChannelSender) GetAvailableTargets() []string {
	return m.availableTargets
}

func newMockChannelSender() *mockChannelSender {
	return &mockChannelSender{
		channelStatus: map[string]string{
			"telegram": "online",
			"discord":  "online",
		},
		availableTargets: []string{"telegram", "discord"},
	}
}

// conditionalFailSender fails for specific channels
type conditionalFailSender struct {
	sentMessages     []sentMessage
	channelStatus    map[string]string
	availableTargets []string
	failChannels     map[string]error
}

func (m *conditionalFailSender) SendMessage(ctx context.Context, channelID, userID, content string, metadata map[string]string) error {
	m.sentMessages = append(m.sentMessages, sentMessage{
		channelID: channelID,
		userID:    userID,
		content:   content,
		metadata:  metadata,
	})
	if err, ok := m.failChannels[channelID]; ok {
		return err
	}
	return nil
}

func (m *conditionalFailSender) GetChannelStatusMap() map[string]string {
	return m.channelStatus
}

func (m *conditionalFailSender) GetAvailableTargets() []string {
	return m.availableTargets
}

func newTestServices(sender types.ChannelSender) *types.ToolServices {
	return &types.ToolServices{
		ChannelSender: sender,
	}
}

// =============================================================================
// MessageTool Tests
// =============================================================================

func TestMessageTool_Name(t *testing.T) {
	tool := NewMessageTool(nil)
	assert.Equal(t, "Message", tool.Name())
}

func TestMessageTool_Description(t *testing.T) {
	tool := NewMessageTool(nil)
	desc := tool.Description()
	assert.Contains(t, desc, "Send messages")
	assert.Contains(t, desc, "channels")
}

func TestMessageTool_Parameters(t *testing.T) {
	tool := NewMessageTool(nil)
	params := tool.Parameters()

	assert.Equal(t, "object", params["type"])

	props, ok := params["properties"].(map[string]interface{})
	require.True(t, ok)

	// Check that key parameters exist
	assert.Contains(t, props, "action")
	assert.Contains(t, props, "target")
	assert.Contains(t, props, "targets")
	assert.Contains(t, props, "message")
	assert.Contains(t, props, "messageId")
	assert.Contains(t, props, "emoji")
	assert.Contains(t, props, "silent")
	assert.Contains(t, props, "asVoice")
	assert.Contains(t, props, "replyTo")
	assert.Contains(t, props, "effectId")
	assert.Contains(t, props, "imagePath")
}

func TestMessageTool_Execute_UnknownAction(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewMessageTool(newTestServices(sender))

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "unknown_action",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "unknown action")
}

func TestMessageTool_Execute_DefaultAction(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewMessageTool(newTestServices(sender))

	// When no action specified, should default to "send" which requires target
	result, err := tool.Execute(context.Background(), map[string]interface{}{})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "Target parameter is required")
}

// -----------------------------------------------------------------------------
// Send Action Tests
// -----------------------------------------------------------------------------

func TestMessageTool_SendMessage_Success(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewMessageTool(newTestServices(sender))

	ctx := types.WithRequestContext(context.Background(), "telegram", "user123", "session1")
	result, err := tool.Execute(ctx, map[string]interface{}{
		"action":  "send",
		"target":  "telegram",
		"message": "Hello, world!",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "sent successfully")

	require.Len(t, sender.sentMessages, 1)
	assert.Equal(t, "telegram", sender.sentMessages[0].channelID)
	assert.Equal(t, "user123", sender.sentMessages[0].userID)
	assert.Equal(t, "Hello, world!", sender.sentMessages[0].content)
}

func TestMessageTool_SendMessage_MissingTarget(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewMessageTool(newTestServices(sender))

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":  "send",
		"message": "Hello!",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "Target parameter is required")
	assert.Contains(t, result.Data["error_type"].(string), "missing_parameter")
}

func TestMessageTool_SendMessage_MissingMessage(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewMessageTool(newTestServices(sender))

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "send",
		"target": "telegram",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "Message parameter is required")
}

func TestMessageTool_SendMessage_WithOptions(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewMessageTool(newTestServices(sender))

	ctx := types.WithRequestContext(context.Background(), "telegram", "user123", "session1")
	result, err := tool.Execute(ctx, map[string]interface{}{
		"action":   "send",
		"target":   "telegram",
		"message":  "Silent message",
		"silent":   true,
		"asVoice":  true,
		"replyTo":  "12345",
		"effectId": "balloons",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)

	// Check options in result data
	data := result.Data
	opts, ok := data["options"].(map[string]interface{})
	require.True(t, ok)
	assert.True(t, opts["silent"].(bool))
	assert.True(t, opts["asVoice"].(bool))
	assert.Equal(t, "12345", opts["replyTo"])
	assert.Equal(t, "balloons", opts["effectId"])
}

func TestMessageTool_SendMessage_WithImagePath(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewMessageTool(newTestServices(sender))

	ctx := types.WithRequestContext(context.Background(), "telegram", "user123", "session1")
	result, err := tool.Execute(ctx, map[string]interface{}{
		"action":    "send",
		"target":    "telegram",
		"message":   "Check out this chart",
		"imagePath": "/tmp/chart.png",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)

	require.Len(t, sender.sentMessages, 1)
	assert.Equal(t, "/tmp/chart.png", sender.sentMessages[0].metadata["image_path"])
}

func TestMessageTool_SendMessage_WithReplyToMetadata(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewMessageTool(newTestServices(sender))

	ctx := types.WithRequestContext(context.Background(), "telegram", "user123", "session1")
	result, err := tool.Execute(ctx, map[string]interface{}{
		"action":  "send",
		"target":  "telegram",
		"message": "Reply message",
		"replyTo": "99999",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)

	require.Len(t, sender.sentMessages, 1)
	assert.Equal(t, "99999", sender.sentMessages[0].metadata["reply_to_message_id"])
}

func TestMessageTool_SendMessage_ServiceUnavailable(t *testing.T) {
	tool := NewMessageTool(&types.ToolServices{
		ChannelSender: nil, // No channel sender
	})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":  "send",
		"target":  "telegram",
		"message": "Hello!",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "service is not available")
}

func TestMessageTool_SendMessage_NilServices(t *testing.T) {
	tool := NewMessageTool(nil)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":  "send",
		"target":  "telegram",
		"message": "Hello!",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "service is not available")
}

func TestMessageTool_SendMessage_SendError(t *testing.T) {
	sender := newMockChannelSender()
	sender.sendError = errors.New("connection timeout")
	tool := NewMessageTool(newTestServices(sender))

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":  "send",
		"target":  "telegram",
		"message": "Hello!",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "Failed to send message")
	assert.Contains(t, result.Error, "timeout")
}

func TestMessageTool_SendMessage_ErrorCategorization(t *testing.T) {
	tests := []struct {
		name          string
		errMsg        string
		expectedType  string
	}{
		{"not found error", "channel not found", "invalid_parameter"},
		{"invalid error", "invalid channel ID", "invalid_parameter"},
		{"permission error", "permission denied", "permission_denied"},
		{"forbidden error", "access forbidden", "permission_denied"},
		{"timeout error", "connection timeout", "service_unavailable"},
		{"connection error", "connection refused", "service_unavailable"},
		{"generic error", "something went wrong", "internal_error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sender := newMockChannelSender()
			sender.sendError = errors.New(tt.errMsg)
			tool := NewMessageTool(newTestServices(sender))

			result, err := tool.Execute(context.Background(), map[string]interface{}{
				"action":  "send",
				"target":  "telegram",
				"message": "Hello!",
			})

			require.NoError(t, err)
			assert.False(t, result.Success)
			assert.Equal(t, tt.expectedType, result.Data["error_type"])
		})
	}
}

// -----------------------------------------------------------------------------
// Broadcast Action Tests
// -----------------------------------------------------------------------------

func TestMessageTool_BroadcastMessage_Success(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewMessageTool(newTestServices(sender))

	ctx := types.WithRequestContext(context.Background(), "telegram", "user123", "session1")
	result, err := tool.Execute(ctx, map[string]interface{}{
		"action":  "broadcast",
		"targets": []interface{}{"telegram", "discord"},
		"message": "Broadcast message",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "broadcast successfully")
	assert.Contains(t, result.Content, "2 targets")

	// Should have sent to both targets
	require.Len(t, sender.sentMessages, 2)
}

func TestMessageTool_BroadcastMessage_MissingMessage(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewMessageTool(newTestServices(sender))

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":  "broadcast",
		"targets": []interface{}{"telegram"},
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "Message parameter is required")
}

func TestMessageTool_BroadcastMessage_MissingTargets(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewMessageTool(newTestServices(sender))

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":  "broadcast",
		"message": "Hello!",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "Targets parameter is required")
}

func TestMessageTool_BroadcastMessage_EmptyTargets(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewMessageTool(newTestServices(sender))

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":  "broadcast",
		"targets": []interface{}{},
		"message": "Hello!",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "Targets parameter is required")
}

func TestMessageTool_BroadcastMessage_InvalidTargets(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewMessageTool(newTestServices(sender))

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":  "broadcast",
		"targets": []interface{}{"nonexistent_channel"},
		"message": "Hello!",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "Invalid targets")
}

func TestMessageTool_BroadcastMessage_WithSilentOption(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewMessageTool(newTestServices(sender))

	ctx := types.WithRequestContext(context.Background(), "telegram", "user123", "session1")
	result, err := tool.Execute(ctx, map[string]interface{}{
		"action":  "broadcast",
		"targets": []interface{}{"telegram"},
		"message": "Silent broadcast",
		"silent":  true,
	})

	require.NoError(t, err)
	assert.True(t, result.Success)

	opts, ok := result.Data["options"].(map[string]interface{})
	require.True(t, ok)
	assert.True(t, opts["silent"].(bool))
}

func TestMessageTool_BroadcastMessage_PartialFailure(t *testing.T) {
	// Create a sender that fails on specific channels
	sender := &conditionalFailSender{
		channelStatus: map[string]string{
			"telegram": "online",
			"discord":  "online",
		},
		availableTargets: []string{"telegram", "discord"},
		failChannels:     map[string]error{"discord": errors.New("discord connection failed")},
	}

	tool := NewMessageTool(newTestServices(sender))

	ctx := types.WithRequestContext(context.Background(), "telegram", "user123", "session1")
	result, err := tool.Execute(ctx, map[string]interface{}{
		"action":  "broadcast",
		"targets": []interface{}{"telegram", "discord"},
		"message": "Test message",
	})

	require.NoError(t, err)
	// Should fail because one target failed
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "discord")
}

// -----------------------------------------------------------------------------
// React Action Tests
// -----------------------------------------------------------------------------

func TestMessageTool_ReactToMessage_MissingMessageId(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewMessageTool(newTestServices(sender))

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "react",
		"emoji":  "👍",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "messageId parameter is required")
}

func TestMessageTool_ReactToMessage_MissingEmoji(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewMessageTool(newTestServices(sender))

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":    "react",
		"messageId": "12345",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "emoji parameter is required")
}

func TestMessageTool_ReactToMessage_NotImplemented(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewMessageTool(newTestServices(sender))

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":    "react",
		"messageId": "12345",
		"emoji":     "👍",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "not yet implemented")
}

// -----------------------------------------------------------------------------
// Delete Action Tests
// -----------------------------------------------------------------------------

func TestMessageTool_DeleteMessage_MissingMessageId(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewMessageTool(newTestServices(sender))

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "delete",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "messageId parameter is required")
}

func TestMessageTool_DeleteMessage_NotImplemented(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewMessageTool(newTestServices(sender))

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":    "delete",
		"messageId": "12345",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "not yet implemented")
}

// -----------------------------------------------------------------------------
// Edit Action Tests
// -----------------------------------------------------------------------------

func TestMessageTool_EditMessage_MissingMessageId(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewMessageTool(newTestServices(sender))

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":  "edit",
		"message": "Updated message",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "messageId parameter is required")
}

func TestMessageTool_EditMessage_MissingMessage(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewMessageTool(newTestServices(sender))

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":    "edit",
		"messageId": "12345",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "message parameter is required")
}

func TestMessageTool_EditMessage_NotImplemented(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewMessageTool(newTestServices(sender))

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":    "edit",
		"messageId": "12345",
		"message":   "Updated message",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "not yet implemented")
}

// -----------------------------------------------------------------------------
// Status Action Tests
// -----------------------------------------------------------------------------

func TestMessageTool_GetChannelStatus_Success(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewMessageTool(newTestServices(sender))

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "status",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "Channel Status")
}

func TestMessageTool_GetChannelStatus_NoChannels(t *testing.T) {
	sender := &mockChannelSender{
		channelStatus:    map[string]string{},
		availableTargets: []string{},
	}
	tool := NewMessageTool(newTestServices(sender))

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "status",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "No channels configured")
}

func TestMessageTool_GetChannelStatus_ServiceUnavailable(t *testing.T) {
	tool := NewMessageTool(&types.ToolServices{
		ChannelSender: nil,
	})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "status",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "not available")
}

// -----------------------------------------------------------------------------
// isValidTarget Tests
// -----------------------------------------------------------------------------

func TestMessageTool_IsValidTarget(t *testing.T) {
	tool := NewMessageTool(nil)

	channelStatus := map[string]string{
		"telegram": "online",
		"discord":  "online",
	}

	tests := []struct {
		name     string
		target   string
		expected bool
	}{
		{"existing channel", "telegram", true},
		{"another existing channel", "discord", true},
		{"non-existing channel", "slack", false},
		{"username target", "@username", true},
		{"hashtag channel", "#general", true},
		{"formatted target", "provider:type:id", true},
		{"empty target", "", false},
		{"nil status", "telegram", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var status map[string]string
			if tt.name != "nil status" {
				status = channelStatus
			}
			result := tool.isValidTarget(tt.target, status)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// -----------------------------------------------------------------------------
// formatChannelStatus Tests
// -----------------------------------------------------------------------------

func TestMessageTool_FormatChannelStatus(t *testing.T) {
	tool := NewMessageTool(nil)

	t.Run("empty status", func(t *testing.T) {
		result := tool.formatChannelStatus(nil)
		assert.Equal(t, "No channels configured.", result)
	})

	t.Run("with channels", func(t *testing.T) {
		status := map[string]interface{}{
			"telegram": map[string]interface{}{
				"enabled":       true,
				"message_count": int64(100),
			},
		}
		result := tool.formatChannelStatus(status)
		assert.Contains(t, result, "Channel Status")
		assert.Contains(t, result, "telegram")
	})
}

// -----------------------------------------------------------------------------
// ValidateParameters Tests
// -----------------------------------------------------------------------------

func TestMessageTool_ValidateParameters_ValidSend(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewMessageTool(newTestServices(sender))

	result := tool.ValidateParameters(context.Background(), map[string]interface{}{
		"action":  "send",
		"target":  "telegram",
		"message": "Hello!",
	})

	assert.True(t, result.Valid)
	assert.Empty(t, result.Errors)
}

func TestMessageTool_ValidateParameters_InvalidAction(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewMessageTool(newTestServices(sender))

	result := tool.ValidateParameters(context.Background(), map[string]interface{}{
		"action": "invalid_action",
	})

	assert.False(t, result.Valid)
	require.Len(t, result.Errors, 1)
	assert.Equal(t, "action", result.Errors[0].Parameter)
	assert.Contains(t, result.Errors[0].Message, "not a valid action")
}

func TestMessageTool_ValidateParameters_MissingSendParams(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewMessageTool(newTestServices(sender))

	result := tool.ValidateParameters(context.Background(), map[string]interface{}{
		"action": "send",
	})

	assert.False(t, result.Valid)
	// Should have errors for both target and message
	assert.GreaterOrEqual(t, len(result.Errors), 2)
}

func TestMessageTool_ValidateParameters_WhitespaceMessage(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewMessageTool(newTestServices(sender))

	result := tool.ValidateParameters(context.Background(), map[string]interface{}{
		"action":  "send",
		"target":  "telegram",
		"message": "   ",
	})

	assert.False(t, result.Valid)
	// Should have an error for whitespace-only message
	hasMessageError := false
	for _, err := range result.Errors {
		if err.Parameter == "message" {
			hasMessageError = true
			break
		}
	}
	assert.True(t, hasMessageError)
}

func TestMessageTool_ValidateParameters_BroadcastMissingTargets(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewMessageTool(newTestServices(sender))

	result := tool.ValidateParameters(context.Background(), map[string]interface{}{
		"action":  "broadcast",
		"message": "Hello!",
	})

	assert.False(t, result.Valid)
	hasTargetsError := false
	for _, err := range result.Errors {
		if err.Parameter == "targets" {
			hasTargetsError = true
			break
		}
	}
	assert.True(t, hasTargetsError)
}

func TestMessageTool_ValidateParameters_ReactMissingParams(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewMessageTool(newTestServices(sender))

	result := tool.ValidateParameters(context.Background(), map[string]interface{}{
		"action": "react",
	})

	assert.False(t, result.Valid)
	// Should have errors for both messageId and emoji
	assert.GreaterOrEqual(t, len(result.Errors), 2)
}

func TestMessageTool_ValidateParameters_DeleteMissingMessageId(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewMessageTool(newTestServices(sender))

	result := tool.ValidateParameters(context.Background(), map[string]interface{}{
		"action": "delete",
	})

	assert.False(t, result.Valid)
	hasMessageIdError := false
	for _, err := range result.Errors {
		if err.Parameter == "messageId" {
			hasMessageIdError = true
			break
		}
	}
	assert.True(t, hasMessageIdError)
}

func TestMessageTool_ValidateParameters_EditMissingParams(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewMessageTool(newTestServices(sender))

	result := tool.ValidateParameters(context.Background(), map[string]interface{}{
		"action": "edit",
	})

	assert.False(t, result.Valid)
	// Should have errors for both messageId and message
	assert.GreaterOrEqual(t, len(result.Errors), 2)
}

func TestMessageTool_ValidateParameters_Status(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewMessageTool(newTestServices(sender))

	result := tool.ValidateParameters(context.Background(), map[string]interface{}{
		"action": "status",
	})

	assert.True(t, result.Valid)
	assert.Empty(t, result.Errors)
}

func TestMessageTool_ValidateParameters_InvalidTarget(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewMessageTool(newTestServices(sender))

	result := tool.ValidateParameters(context.Background(), map[string]interface{}{
		"action":  "send",
		"target":  "nonexistent_channel",
		"message": "Hello!",
	})

	assert.False(t, result.Valid)
	hasTargetError := false
	for _, err := range result.Errors {
		if err.Parameter == "target" {
			hasTargetError = true
			break
		}
	}
	assert.True(t, hasTargetError)
}

func TestMessageTool_ValidateParameters_NoChannelSender(t *testing.T) {
	tool := NewMessageTool(&types.ToolServices{
		ChannelSender: nil,
	})

	result := tool.ValidateParameters(context.Background(), map[string]interface{}{
		"action":  "send",
		"target":  "telegram",
		"message": "Hello!",
	})

	assert.False(t, result.Valid)
}

// -----------------------------------------------------------------------------
// GetUsageExamples Tests
// -----------------------------------------------------------------------------

func TestMessageTool_GetUsageExamples(t *testing.T) {
	tool := NewMessageTool(nil)
	examples := tool.GetUsageExamples()

	assert.NotEmpty(t, examples)

	// Check for key examples
	hasSimpleSend := false
	hasReply := false
	hasBroadcast := false
	hasStatus := false
	hasPhoto := false

	for _, ex := range examples {
		switch ex.Name {
		case "Send a simple message":
			hasSimpleSend = true
			assert.Equal(t, "send", ex.Args["action"])
		case "Send with reply":
			hasReply = true
			assert.Contains(t, ex.Args, "replyTo")
		case "Broadcast to multiple channels":
			hasBroadcast = true
			assert.Equal(t, "broadcast", ex.Args["action"])
		case "Check channel status":
			hasStatus = true
			assert.Equal(t, "status", ex.Args["action"])
		case "Send a photo":
			hasPhoto = true
			assert.Contains(t, ex.Args, "imagePath")
		}
	}

	assert.True(t, hasSimpleSend, "should have simple send example")
	assert.True(t, hasReply, "should have reply example")
	assert.True(t, hasBroadcast, "should have broadcast example")
	assert.True(t, hasStatus, "should have status example")
	assert.True(t, hasPhoto, "should have photo example")
}

// -----------------------------------------------------------------------------
// GetSchemaHints Tests
// -----------------------------------------------------------------------------

func TestMessageTool_GetSchemaHints(t *testing.T) {
	tool := NewMessageTool(nil)
	hints := tool.GetSchemaHints()

	assert.NotEmpty(t, hints)

	// Check key parameters have hints
	assert.Contains(t, hints, "action")
	assert.Contains(t, hints, "target")
	assert.Contains(t, hints, "targets")
	assert.Contains(t, hints, "message")
	assert.Contains(t, hints, "emoji")
	assert.Contains(t, hints, "effectId")
	assert.Contains(t, hints, "messageId")
	assert.Contains(t, hints, "replyTo")
	assert.Contains(t, hints, "imagePath")

	// Check that hints have examples
	actionHints := hints["action"]
	assert.NotEmpty(t, actionHints.Examples)
	assert.NotEmpty(t, actionHints.ValidationHints)

	// Check target hints have discovery info
	targetHints := hints["target"]
	assert.Equal(t, "channels", targetHints.DiscoveryType)
}

// =============================================================================
// StatusUpdateTool Tests
// =============================================================================

func TestStatusUpdateTool_Name(t *testing.T) {
	tool := NewStatusUpdateTool(nil)
	assert.Equal(t, "StatusUpdate", tool.Name())
}

func TestStatusUpdateTool_Description(t *testing.T) {
	tool := NewStatusUpdateTool(nil)
	desc := tool.Description()
	assert.Contains(t, desc, "progress update")
	assert.Contains(t, desc, "user who initiated")
}

func TestStatusUpdateTool_Parameters(t *testing.T) {
	tool := NewStatusUpdateTool(nil)
	params := tool.Parameters()

	assert.Equal(t, "object", params["type"])

	props, ok := params["properties"].(map[string]interface{})
	require.True(t, ok)
	assert.Contains(t, props, "message")

	required, ok := params["required"].([]string)
	require.True(t, ok)
	assert.Contains(t, required, "message")
}

func TestStatusUpdateTool_Execute_Success(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewStatusUpdateTool(newTestServices(sender))

	ctx := types.WithRequestContext(context.Background(), "telegram", "user123", "session1")
	result, err := tool.Execute(ctx, map[string]interface{}{
		"message": "Processing request...",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, "Status update sent", result.Content)

	// Check the message was sent
	require.Len(t, sender.sentMessages, 1)
	assert.Equal(t, "telegram", sender.sentMessages[0].channelID)
	assert.Equal(t, "user123", sender.sentMessages[0].userID)
	assert.Contains(t, sender.sentMessages[0].content, "Processing request...")
	// Should have marker emoji
	assert.Contains(t, sender.sentMessages[0].content, "📍")
}

func TestStatusUpdateTool_Execute_MissingMessage(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewStatusUpdateTool(newTestServices(sender))

	result, err := tool.Execute(context.Background(), map[string]interface{}{})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "message parameter is required")
}

func TestStatusUpdateTool_Execute_EmptyMessage(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewStatusUpdateTool(newTestServices(sender))

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"message": "",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "message parameter is required")
}

func TestStatusUpdateTool_Execute_NonStringMessage(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewStatusUpdateTool(newTestServices(sender))

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"message": 123,
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "message parameter is required")
}

func TestStatusUpdateTool_Execute_NoChannelInContext(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewStatusUpdateTool(newTestServices(sender))

	// Context without channel ID
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"message": "Update message",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "no originating channel found")
}

func TestStatusUpdateTool_Execute_ServiceUnavailable(t *testing.T) {
	tool := NewStatusUpdateTool(&types.ToolServices{
		ChannelSender: nil,
	})

	ctx := types.WithRequestContext(context.Background(), "telegram", "user123", "session1")
	result, err := tool.Execute(ctx, map[string]interface{}{
		"message": "Update message",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "not available")
}

func TestStatusUpdateTool_Execute_NilServices(t *testing.T) {
	tool := NewStatusUpdateTool(nil)

	ctx := types.WithRequestContext(context.Background(), "telegram", "user123", "session1")
	result, err := tool.Execute(ctx, map[string]interface{}{
		"message": "Update message",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "not available")
}

func TestStatusUpdateTool_Execute_SendError(t *testing.T) {
	sender := newMockChannelSender()
	sender.sendError = errors.New("network error")
	tool := NewStatusUpdateTool(newTestServices(sender))

	ctx := types.WithRequestContext(context.Background(), "telegram", "user123", "session1")
	result, err := tool.Execute(ctx, map[string]interface{}{
		"message": "Update message",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "failed to send status update")
	assert.Contains(t, result.Error, "network error")
}

func TestStatusUpdateTool_Execute_ResultData(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewStatusUpdateTool(newTestServices(sender))

	ctx := types.WithRequestContext(context.Background(), "telegram", "user123", "session1")
	result, err := tool.Execute(ctx, map[string]interface{}{
		"message": "Processing...",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, "telegram", result.Data["channel"])
	assert.Equal(t, "Processing...", result.Data["message"])
}

func TestStatusUpdateTool_GetUsageExamples(t *testing.T) {
	tool := NewStatusUpdateTool(nil)
	examples := tool.GetUsageExamples()

	assert.NotEmpty(t, examples)
	assert.GreaterOrEqual(t, len(examples), 3)

	// All examples should have message arg
	for _, ex := range examples {
		assert.Contains(t, ex.Args, "message")
	}
}

// =============================================================================
// TTSTool Tests
// =============================================================================

func TestTTSTool_Name(t *testing.T) {
	tool := NewTTSTool(nil)
	assert.Equal(t, "Tts", tool.Name())
}

func TestTTSTool_Description(t *testing.T) {
	tool := NewTTSTool(nil)
	desc := tool.Description()
	assert.Contains(t, desc, "text to speech")
}

func TestTTSTool_Parameters(t *testing.T) {
	tool := NewTTSTool(nil)
	params := tool.Parameters()

	assert.Equal(t, "object", params["type"])

	props, ok := params["properties"].(map[string]interface{})
	require.True(t, ok)

	assert.Contains(t, props, "text")
	assert.Contains(t, props, "voice")
	assert.Contains(t, props, "rate")
	assert.Contains(t, props, "format")
	assert.Contains(t, props, "channel")

	required, ok := params["required"].([]string)
	require.True(t, ok)
	assert.Contains(t, required, "text")
}

func TestTTSTool_Execute_MissingText(t *testing.T) {
	tool := NewTTSTool(nil)

	result, err := tool.Execute(context.Background(), map[string]interface{}{})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "text parameter is required")
}

func TestTTSTool_Execute_EmptyText(t *testing.T) {
	tool := NewTTSTool(nil)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"text": "   ",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "cannot be empty")
}

func TestTTSTool_Execute_NonStringText(t *testing.T) {
	tool := NewTTSTool(nil)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"text": 123,
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "text parameter is required")
}

func TestTTSTool_OptimizeFormatForChannel(t *testing.T) {
	tool := NewTTSTool(nil)

	tests := []struct {
		channel  string
		default_ string
		expected string
	}{
		{"telegram", "mp3", "ogg"},
		{"Telegram", "mp3", "ogg"},
		{"TELEGRAM", "mp3", "ogg"},
		{"discord", "ogg", "mp3"},
		{"Discord", "ogg", "mp3"},
		{"whatsapp", "mp3", "ogg"},
		{"unknown", "wav", "wav"},
		{"", "mp3", "mp3"},
	}

	for _, tt := range tests {
		t.Run(tt.channel, func(t *testing.T) {
			result := tool.optimizeFormatForChannel(tt.channel, tt.default_)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestTTSTool_GetSchemaHints(t *testing.T) {
	tool := NewTTSTool(nil)
	hints := tool.GetSchemaHints()

	assert.NotEmpty(t, hints)

	// Check key parameters have hints
	assert.Contains(t, hints, "text")
	assert.Contains(t, hints, "voice")
	assert.Contains(t, hints, "rate")
	assert.Contains(t, hints, "format")
	assert.Contains(t, hints, "channel")

	// Check text hints
	textHints := hints["text"]
	assert.NotEmpty(t, textHints.Examples)
	assert.NotEmpty(t, textHints.ValidationHints)

	// Check voice hints have examples
	voiceHints := hints["voice"]
	assert.NotEmpty(t, voiceHints.Examples)

	// Check channel hints have discovery type
	channelHints := hints["channel"]
	assert.Equal(t, "channels", channelHints.DiscoveryType)
}

// =============================================================================
// Edge Cases and Integration Tests
// =============================================================================

func TestMessageTool_BroadcastWithEmptyStringsInTargets(t *testing.T) {
	sender := newMockChannelSender()
	tool := NewMessageTool(newTestServices(sender))

	result := tool.ValidateParameters(context.Background(), map[string]interface{}{
		"action":  "broadcast",
		"targets": []interface{}{"telegram", "", "discord"},
		"message": "Hello!",
	})

	assert.False(t, result.Valid)
	// Should have an error for the empty target
	hasEmptyTargetError := false
	for _, err := range result.Errors {
		if err.Parameter == "targets[1]" {
			hasEmptyTargetError = true
			break
		}
	}
	assert.True(t, hasEmptyTargetError)
}

func TestMessageTool_GenerateSuggestions(t *testing.T) {
	tool := NewMessageTool(nil)

	// Test with target error
	errors := []types.ValidationError{
		{Parameter: "target", Message: "not found"},
	}
	suggestions := tool.generateSuggestions(errors, "send")
	assert.NotEmpty(t, suggestions)

	// Test with message error
	errors = []types.ValidationError{
		{Parameter: "message", Message: "required"},
	}
	suggestions = tool.generateSuggestions(errors, "send")
	assert.NotEmpty(t, suggestions)

	// Test with broadcast specific errors
	errors = []types.ValidationError{
		{Parameter: "targets", Message: "required"},
	}
	suggestions = tool.generateSuggestions(errors, "broadcast")
	assert.NotEmpty(t, suggestions)

	// Test with react action
	errors = []types.ValidationError{
		{Parameter: "emoji", Message: "required"},
	}
	suggestions = tool.generateSuggestions(errors, "react")
	assert.NotEmpty(t, suggestions)

	// Test with multiple errors
	errors = []types.ValidationError{
		{Parameter: "target", Message: "not found"},
		{Parameter: "message", Message: "required"},
	}
	suggestions = tool.generateSuggestions(errors, "send")
	assert.NotEmpty(t, suggestions)
}

func TestRequestContext_Values(t *testing.T) {
	ctx := types.WithRequestContext(context.Background(), "chan123", "user456", "sess789")

	assert.Equal(t, "chan123", types.RequestChannelID(ctx))
	assert.Equal(t, "user456", types.RequestUserID(ctx))
	assert.Equal(t, "sess789", types.RequestSessionKey(ctx))
}

func TestRequestContext_EmptyValues(t *testing.T) {
	ctx := context.Background()

	assert.Equal(t, "", types.RequestChannelID(ctx))
	assert.Equal(t, "", types.RequestUserID(ctx))
	assert.Equal(t, "", types.RequestSessionKey(ctx))
}
