package communication

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	toolargs "conduit/internal/tools/args"
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
	return "Send messages via configured channels (Telegram, Discord, etc.). Use action=\"status\" to discover available channels and targets."
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
	action := toolargs.GetString(args, "action", "send")

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
				"target":  "123456789",
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
				"targets": []string{"telegram", "discord", "123456789"},
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
			Examples:          []interface{}{"telegram", "discord", "123456789"},
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
				[]string{"123456789", "1234567890"},
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

// SelfTest implements types.SelfTester for MessageTool.
func (t *MessageTool) SelfTest(ctx context.Context, opts *types.SelfTestOptions) *types.SelfTestResult {
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
		result.Message = "Message service is not configured"
		result.Suggestions = []string{
			"Verify gateway is running",
			"Check channel configuration in config.json",
		}
	} else {
		channelSenderDep.Available = true
		channelSenderDep.Status = "connected"

		// Get channel status to determine capabilities
		channelStatus := t.services.ChannelSender.GetChannelStatusMap()
		availableTargets := t.services.ChannelSender.GetAvailableTargets()

		if len(channelStatus) == 0 {
			result.Status = types.SelfTestStatusDegraded
			result.Message = "Message service available but no channels configured"
			result.Suggestions = []string{
				"Configure at least one channel (Telegram, Discord, etc.)",
				"Use Message with action='status' to verify channel setup",
			}
			result.UnavailableCapabilities = []string{"send", "broadcast"}
			result.Capabilities = []string{"status"}
		} else {
			// Count online channels
			onlineCount := 0
			var offlineChannels []string
			for channel, status := range channelStatus {
				if status == "online" || status == "connected" {
					onlineCount++
				} else {
					offlineChannels = append(offlineChannels, channel)
				}
			}
			sort.Strings(offlineChannels)

			if onlineCount == 0 {
				result.Status = types.SelfTestStatusDegraded
				result.Message = fmt.Sprintf("Channels configured but none online (found %d)", len(channelStatus))
				result.UnavailableCapabilities = []string{"send", "broadcast"}
				result.Capabilities = []string{"status"}
				result.Suggestions = []string{
					"Check channel connectivity",
					fmt.Sprintf("Offline channels: %s", strings.Join(offlineChannels, ", ")),
				}
			} else {
				result.Status = types.SelfTestStatusOK
				result.Message = fmt.Sprintf("Message tool fully functional (%d/%d channels online)",
					onlineCount, len(channelStatus))
				result.Capabilities = []string{"send", "broadcast", "status"}

				if opts.Verbose {
					result.Details = map[string]interface{}{
						"channel_count":     len(channelStatus),
						"online_count":      onlineCount,
						"available_targets": availableTargets,
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

// IncludeDataInModelOutput: conduit-31jg.39 opted this tool in; since
// conduit-31jg.71 Content carries everything (channel IDs and statuses,
// broadcast targets, send options) and Data merely duplicates it or echoes
// the message body, so it is opted out to save tokens.
func (t *MessageTool) IncludeDataInModelOutput() bool { return false }
