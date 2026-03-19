package mqtt

import (
	"context"
	"encoding/json"
	"fmt"

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

func (t *MQTTTool) Name() string        { return "MQTT" }
func (t *MQTTTool) Description() string {
	return `Query home automation sensors and control smart devices via MQTT (zigbee2mqtt, Home Assistant).

Actions:
- status: Check MQTT connection and event counts
- topics: List all devices/sensors with their current values (start here to discover devices)
- recent: Get recent events across all topics, optionally filtered by pattern
- history: Get event history for one specific device/topic
- publish: Send a command to control a device (lights, switches, etc.)

Typical Workflow:
1. Use action=topics to discover available devices and their topic names
2. Use action=history with a topic to see a device's recent state values
3. For control: inspect history to see the payload format the device uses, then publish with matching format

Examples:
- List all devices: action=topics
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
				"enum":        []string{"status", "topics", "recent", "history", "publish"},
				"description": "Action: status (connection info), topics (list devices—START HERE), recent (recent events), history (one device's events), publish (control a device)",
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
		"status": {
			Description: "Check MQTT connection state and event counts",
			Returns:     "connected, broker_url, active_topics, total_events, publish_allowed",
		},
		"topics": {
			Description:    "List all active device topics with last value (start here for discovery)",
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

	action := getStr(args, "action")
	switch action {
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
			WithAvailableValues([]string{"status", "topics", "recent", "history", "publish"}), nil
	}
}

func (t *MQTTTool) status() (*types.ToolResult, error) {
	s := t.services.MQTTService.Status()
	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("MQTT %s — %d active topics, %d total events",
			connStr(s.Connected), s.ActiveTopics, s.TotalEvents),
		Data: map[string]interface{}{
			"connected":        s.Connected,
			"broker_url":       s.BrokerURL,
			"subscribed_topics": s.SubscribedTopics,
			"active_topics":    s.ActiveTopics,
			"total_events":     s.TotalEvents,
			"publish_allowed":  s.PublishAllowed,
		},
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
	limit := clampLimit(getInt(args, "limit", 20))
	pattern := getStr(args, "topic_pattern")

	var events []types.MQTTEvent
	if pattern != "" {
		events = t.services.MQTTService.RecentMatching(pattern, limit)
	} else {
		events = t.services.MQTTService.Recent(limit)
	}

	return eventsResult(events, "recent events"), nil
}

func (t *MQTTTool) history(args map[string]interface{}) (*types.ToolResult, error) {
	topic := getStr(args, "topic")
	if topic == "" {
		return types.NewErrorResult("missing_parameter", "topic is required for history action").
			WithParameter("topic", nil).
			WithExamples([]string{"zigbee2mqtt/Living Room Sensor", "zigbee2mqtt/Front Door Contact"}), nil
	}
	limit := clampLimit(getInt(args, "limit", 20))
	events := t.services.MQTTService.RecentForTopic(topic, limit)
	return eventsResult(events, fmt.Sprintf("events for %s", topic)), nil
}

func (t *MQTTTool) publish(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	topic := getStr(args, "topic")
	if topic == "" {
		return types.NewErrorResult("missing_parameter", "topic is required for publish").
			WithParameter("topic", nil), nil
	}
	payloadStr := getStr(args, "payload")
	if payloadStr == "" {
		return types.NewErrorResult("missing_parameter", "payload is required for publish").
			WithParameter("payload", nil), nil
	}

	qos := byte(getInt(args, "qos", 0))
	if qos > 2 {
		qos = 0
	}
	retained := getBool(args, "retained")

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

func getStr(args map[string]interface{}, key string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getInt(args map[string]interface{}, key string, defaultVal int) int {
	if v, ok := args[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		case json.Number:
			if i, err := n.Int64(); err == nil {
				return int(i)
			}
		}
	}
	return defaultVal
}

func getBool(args map[string]interface{}, key string) bool {
	if v, ok := args[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}
