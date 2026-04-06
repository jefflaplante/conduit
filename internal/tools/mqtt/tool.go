package mqtt

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
)

// MQTTTool exposes MQTT event data and control to the AI agent.
type MQTTTool struct {
	services *types.ToolServices
}

// NewMQTTTool creates a new MQTT tool.
func NewMQTTTool(services *types.ToolServices) *MQTTTool {
	return &MQTTTool{services: services}
}

func (t *MQTTTool) Name() string { return "MQTT" }
func (t *MQTTTool) Description() string {
	return `Query home automation sensors and control smart devices via MQTT (zigbee2mqtt, Home Assistant).

Actions:
- devices: Discover all paired devices and MQTT sources (START HERE)
- status: Check MQTT connection and event counts
- topics: List active event streams with current values
- recent: Get recent events across all topics, optionally filtered by pattern
- history: Get event history for one specific device/topic
- publish: Send a command to control a device (lights, switches, etc.)

Typical Workflow:
1. Use action=devices to discover all paired devices and MQTT sources
2. Use action=topics to see which devices are actively publishing
3. Use action=history with a topic to see a device's recent state values
4. For control: use action=publish with the correct topic and payload format

Examples:
- Discover devices: action=devices
- Filter devices: action=devices, name_pattern="*light*"
- Active streams: action=topics
- Check connection: action=status
- Recent motion events: action=recent, topic_pattern="*motion*", limit=10
- Device history: action=history, topic="zigbee2mqtt/Living Room Sensor", limit=10
- Control device: action=publish, topic="<topic from discovery>", payload="<format from history>"

Publishing Commands:
- FIRST check device history (action=topics or action=history) to learn the correct topic and payload format
- Payload formats vary by device—some use simple values (ON, OFF), others use JSON
- Store learned device patterns in memory so you don't have to re-learn them`
}

func (t *MQTTTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"devices", "status", "topics", "recent", "history", "publish"},
				"description": "Action: devices (discover all paired devices—START HERE), status (connection info), topics (active event streams), recent (recent events), history (one device's events), publish (control a device)",
			},
			"name_pattern": map[string]interface{}{
				"type":        "string",
				"description": "Glob pattern for devices action to filter by name (e.g. '*light*', '*sensor*')",
			},
			"topic": map[string]interface{}{
				"type":        "string",
				"description": "Exact MQTT topic path. Use action=topics first to discover available topics and their naming patterns.",
			},
			"topic_pattern": map[string]interface{}{
				"type":        "string",
				"description": "Glob pattern for recent action (e.g. '*motion*', 'zigbee2mqtt/*', '*Sensor')",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "Max events to return for recent/history (default 20, max 100)",
			},
			"payload": map[string]interface{}{
				"type":        "string",
				"description": "Command payload for publish. Check device history first to see the format it expects—many use simple values (ON, OFF, TOGGLE), some use JSON.",
			},
			"qos": map[string]interface{}{
				"type":        "integer",
				"description": "QoS level for publish (0-2, minimum 1 enforced for broker ACK)",
			},
			"retained": map[string]interface{}{
				"type":        "boolean",
				"description": "Whether to publish as retained message (default false)",
			},
		},
		"required": []string{"action"},
	}
}

func (t *MQTTTool) GetActionDocs() map[string]types.ActionDoc {
	return map[string]types.ActionDoc{
		"devices": {
			Description:    "Discover all paired devices and MQTT sources (start here)",
			OptionalParams: []string{"name_pattern"},
			Returns:        "zigbee2mqtt devices with mqtt_topic, retained state prefixes for other sources",
		},
		"status": {
			Description: "Check MQTT connection state and event counts",
			Returns:     "connected, broker_url, active_topics, total_events, publish_allowed",
		},
		"topics": {
			Description:    "List active event streams with current values",
			OptionalParams: []string{"limit"},
			Returns:        "array of {topic, event_count, last_event, last_value}",
		},
		"recent": {
			Description:    "Get recent events, optionally filtered by glob pattern",
			OptionalParams: []string{"topic_pattern", "limit"},
			Returns:        "array of {topic, payload, timestamp, retained}",
		},
		"history": {
			Description:    "Get event history for one specific device topic",
			RequiredParams: []string{"topic"},
			OptionalParams: []string{"limit"},
			Returns:        "array of {topic, payload, timestamp, retained}",
		},
		"publish": {
			Description:    "Send a command to a device. Check history first to learn payload format",
			RequiredParams: []string{"topic", "payload"},
			OptionalParams: []string{"qos", "retained"},
			Returns:        "topic, qos, retained, payload_size, broker_ack",
		},
	}
}

func (t *MQTTTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	if t.services.MQTTService == nil {
		return &types.ToolResult{
			Success: false,
			Error:   "MQTT is not configured. Add an 'mqtt' section to your config with enabled: true.",
		}, nil
	}

	action := toolargs.GetString(args, "action", "")
	switch action {
	case "devices":
		return t.devices(args)
	case "status":
		return t.status()
	case "topics":
		return t.topics()
	case "recent":
		return t.recent(args)
	case "history":
		return t.history(args)
	case "publish":
		return t.publish(ctx, args)
	default:
		return types.NewErrorResult("invalid_action",
			fmt.Sprintf("Unknown action: %s", action)).
			WithParameter("action", action).
			WithAvailableValues([]string{"devices", "status", "topics", "recent", "history", "publish"}), nil
	}
}

func (t *MQTTTool) status() (*types.ToolResult, error) {
	s := t.services.MQTTService.Status()
	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("MQTT %s — %d active topics, %d total events",
			connStr(s.Connected), s.ActiveTopics, s.TotalEvents),
		Data: map[string]interface{}{
			"connected":         s.Connected,
			"broker_url":        s.BrokerURL,
			"subscribed_topics": s.SubscribedTopics,
			"active_topics":     s.ActiveTopics,
			"total_events":      s.TotalEvents,
			"publish_allowed":   s.PublishAllowed,
		},
	}, nil
}

func (t *MQTTTool) devices(args map[string]interface{}) (*types.ToolResult, error) {
	namePattern := toolargs.GetString(args, "name_pattern", "")

	// Get zigbee2mqtt devices
	allDevices := t.services.MQTTService.Devices()

	// Apply name filter if provided
	var devices []types.MQTTDevice
	if namePattern != "" {
		for _, d := range allDevices {
			matched, _ := matchGlobInsensitive(namePattern, d.FriendlyName)
			if matched {
				devices = append(devices, d)
			}
		}
	} else {
		devices = allDevices
	}

	// Get retained state prefixes (for non-zigbee2mqtt sources)
	prefixes := t.services.MQTTService.RetainedPrefixes()

	// Build response
	data := map[string]interface{}{}
	var parts []string

	if len(devices) > 0 {
		deviceItems := make([]interface{}, len(devices))
		for i, d := range devices {
			item := map[string]interface{}{
				"friendly_name": d.FriendlyName,
				"mqtt_topic":    d.MQTTTopic,
				"type":          d.Type,
			}
			if d.Manufacturer != "" {
				item["manufacturer"] = d.Manufacturer
			}
			if d.ModelID != "" {
				item["model_id"] = d.ModelID
			}
			if d.Description != "" {
				item["description"] = d.Description
			}
			if d.Disabled {
				item["disabled"] = true
			}
			deviceItems[i] = item
		}
		data["zigbee2mqtt_devices"] = deviceItems
		parts = append(parts, fmt.Sprintf("%d zigbee2mqtt devices", len(devices)))
	}

	// Summarize retained state by prefix (excluding zigbee2mqtt which is covered by device registry)
	var otherPrefixes []string
	for _, p := range prefixes {
		if p != "zigbee2mqtt" {
			otherPrefixes = append(otherPrefixes, p)
		}
	}
	if len(otherPrefixes) > 0 {
		retainedSources := make([]interface{}, 0, len(otherPrefixes))
		for _, prefix := range otherPrefixes {
			msgs := t.services.MQTTService.RetainedByPrefix(prefix + "/")
			retainedSources = append(retainedSources, map[string]interface{}{
				"prefix":          prefix,
				"retained_topics": len(msgs),
			})
		}
		data["other_sources"] = retainedSources
		parts = append(parts, fmt.Sprintf("%d other MQTT sources", len(otherPrefixes)))
	}

	if len(parts) == 0 {
		return &types.ToolResult{
			Success: true,
			Content: "No devices discovered yet. Retained messages from the broker may still be arriving. Try again in a few seconds, or use action=topics to check for active event streams.",
		}, nil
	}

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Discovered: %s", joinParts(parts)),
		Data:    data,
	}, nil
}

func (t *MQTTTool) topics() (*types.ToolResult, error) {
	summaries := t.services.MQTTService.Topics()
	if len(summaries) == 0 {
		return &types.ToolResult{
			Success: true,
			Content: "No active MQTT topics. Waiting for device data.",
		}, nil
	}

	items := make([]interface{}, len(summaries))
	for i, s := range summaries {
		items[i] = map[string]interface{}{
			"topic":       s.Topic,
			"event_count": s.EventCount,
			"last_event":  s.LastEvent,
			"last_value":  tryParseJSON(s.LastValue),
		}
	}

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("%d active topics", len(summaries)),
		Data: map[string]interface{}{
			"topics": items,
		},
	}, nil
}

func (t *MQTTTool) recent(args map[string]interface{}) (*types.ToolResult, error) {
	limit := clampLimit(toolargs.GetInt(args, "limit", 20))
	pattern := toolargs.GetString(args, "topic_pattern", "")

	var events []types.MQTTEvent
	if pattern != "" {
		events = t.services.MQTTService.RecentMatching(pattern, limit)
	} else {
		events = t.services.MQTTService.Recent(limit)
	}

	return eventsResult(events, "recent events"), nil
}

func (t *MQTTTool) history(args map[string]interface{}) (*types.ToolResult, error) {
	topic := toolargs.GetString(args, "topic", "")
	if topic == "" {
		return types.NewErrorResult("missing_parameter", "topic is required for history action").
			WithParameter("topic", nil).
			WithExamples([]string{"zigbee2mqtt/Living Room Sensor", "zigbee2mqtt/Front Door Contact"}), nil
	}
	limit := clampLimit(toolargs.GetInt(args, "limit", 20))
	events := t.services.MQTTService.RecentForTopic(topic, limit)
	return eventsResult(events, fmt.Sprintf("events for %s", topic)), nil
}

func (t *MQTTTool) publish(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	topic := toolargs.GetString(args, "topic", "")
	if topic == "" {
		return types.NewErrorResult("missing_parameter", "topic is required for publish").
			WithParameter("topic", nil), nil
	}
	payloadStr := toolargs.GetString(args, "payload", "")
	if payloadStr == "" {
		return types.NewErrorResult("missing_parameter", "payload is required for publish").
			WithParameter("payload", nil), nil
	}

	qos := byte(toolargs.GetInt(args, "qos", 0))
	if qos > 2 {
		qos = 0
	}
	retained := toolargs.GetBool(args, "retained", false)

	result, err := t.services.MQTTService.Publish(ctx, topic, []byte(payloadStr), qos, retained)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("publish failed: %v", err),
		}, nil
	}

	ackStatus := "broker ACK confirmed (QoS 1 PUBACK)"
	if !result.BrokerAck {
		ackStatus = "sent (no broker ACK — QoS 0)"
	}

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Published to %s — %s (%d bytes, retained=%v)", result.Topic, ackStatus, result.PayloadSize, result.Retained),
		Data: map[string]interface{}{
			"topic":        result.Topic,
			"qos":          result.QoS,
			"retained":     result.Retained,
			"payload_size": result.PayloadSize,
			"broker_ack":   result.BrokerAck,
		},
	}, nil
}

// --- helpers ---

func eventsResult(events []types.MQTTEvent, desc string) *types.ToolResult {
	if len(events) == 0 {
		return &types.ToolResult{
			Success: true,
			Content: fmt.Sprintf("No %s found.", desc),
		}
	}
	items := make([]interface{}, len(events))
	for i, e := range events {
		items[i] = map[string]interface{}{
			"topic":     e.Topic,
			"payload":   tryParseJSON(e.Payload),
			"timestamp": e.Timestamp,
			"retained":  e.Retained,
		}
	}
	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("%d %s", len(events), desc),
		Data: map[string]interface{}{
			"events": items,
		},
	}
}

func tryParseJSON(raw json.RawMessage) interface{} {
	if len(raw) == 0 {
		return nil
	}
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	return v
}

func connStr(connected bool) string {
	if connected {
		return "connected"
	}
	return "disconnected"
}

func clampLimit(n int) int {
	if n <= 0 {
		return 20
	}
	if n > 100 {
		return 100
	}
	return n
}

func matchGlobInsensitive(pattern, name string) (bool, error) {
	return path.Match(strings.ToLower(pattern), strings.ToLower(name))
}

func joinParts(parts []string) string {
	return strings.Join(parts, ", ")
}

// SelfTest implements types.SelfTester for MQTTTool.
func (t *MQTTTool) SelfTest(ctx context.Context, opts *types.SelfTestOptions) *types.SelfTestResult {
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

	// Check MQTT service
	mqttDep := types.DependencyStatus{
		Name:     "MQTTService",
		Required: true,
	}

	if t.services == nil || t.services.MQTTService == nil {
		mqttDep.Available = false
		mqttDep.Status = "not_configured"
		mqttDep.Message = "MQTT service not enabled in config"
		result.Status = types.SelfTestStatusFailed
		result.Message = "MQTT is not configured"
		result.Suggestions = []string{
			"Add 'mqtt' section to config with enabled: true",
			"Configure broker_url in mqtt config",
		}
	} else {
		mqttDep.Available = true

		// Get MQTT status to determine capabilities
		status := t.services.MQTTService.Status()

		if !status.Connected {
			mqttDep.Status = "disconnected"
			mqttDep.Message = "Not connected to MQTT broker"
			result.Status = types.SelfTestStatusDegraded
			result.Message = "MQTT configured but not connected to broker"
			result.Capabilities = []string{"status"}
			result.UnavailableCapabilities = []string{"devices", "topics", "recent", "history", "publish"}
			result.Suggestions = []string{
				"Check MQTT broker is running",
				"Verify broker_url in config",
				"Check network connectivity",
			}
		} else {
			mqttDep.Status = "connected"
			result.Capabilities = []string{"devices", "status", "topics", "recent", "history"}

			if status.PublishAllowed {
				result.Capabilities = append(result.Capabilities, "publish")
			} else {
				result.UnavailableCapabilities = []string{"publish"}
			}

			result.Status = types.SelfTestStatusOK
			result.Message = fmt.Sprintf("MQTT connected — %d active topics, %d total events",
				status.ActiveTopics, status.TotalEvents)

			if opts.Verbose {
				result.Details = map[string]interface{}{
					"broker_url":        status.BrokerURL,
					"subscribed_topics": status.SubscribedTopics,
					"active_topics":     status.ActiveTopics,
					"total_events":      status.TotalEvents,
					"publish_allowed":   status.PublishAllowed,
				}
			}
		}
	}
	deps = append(deps, mqttDep)

	result.Dependencies = deps
	result.TestDuration = time.Since(start)

	if opts.IncludeExamples && result.IsFunctional() {
		result.Examples = []types.ToolExample{
			{
				Name:        "Discover devices",
				Description: "List all paired MQTT devices",
				Args: map[string]interface{}{
					"action": "devices",
				},
				Expected: "Returns zigbee2mqtt devices and other MQTT sources",
			},
			{
				Name:        "Check status",
				Description: "Get MQTT connection status",
				Args: map[string]interface{}{
					"action": "status",
				},
				Expected: "Returns connection state and event counts",
			},
			{
				Name:        "Get device history",
				Description: "Get recent events for a specific device",
				Args: map[string]interface{}{
					"action": "history",
					"topic":  "zigbee2mqtt/Living Room Sensor",
					"limit":  10,
				},
				Expected: "Returns recent events for the device",
			},
		}
	}

	return result
}
