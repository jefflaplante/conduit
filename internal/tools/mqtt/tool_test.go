//go:build with_mqtt

package mqtt

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockMQTTService implements types.MQTTService for testing.
type mockMQTTService struct {
	status           types.MQTTServiceStatus
	events           []types.MQTTEvent
	topics           []types.MQTTTopicSummary
	pubErr           error
	pubResult        *types.MQTTPublishResult
	devices          []types.MQTTDevice
	retainedByPrefix map[string][]types.MQTTRetainedMessage
	retainedPrefixes []string
}

func (m *mockMQTTService) Status() types.MQTTServiceStatus { return m.status }

func (m *mockMQTTService) Recent(limit int) []types.MQTTEvent {
	if limit > len(m.events) {
		return m.events
	}
	return m.events[:limit]
}

func (m *mockMQTTService) RecentForTopic(topic string, limit int) []types.MQTTEvent {
	var result []types.MQTTEvent
	for _, e := range m.events {
		if e.Topic == topic {
			result = append(result, e)
		}
	}
	if limit < len(result) {
		result = result[:limit]
	}
	return result
}

func (m *mockMQTTService) RecentMatching(pattern string, limit int) []types.MQTTEvent {
	return m.events // simplified for tests
}

func (m *mockMQTTService) Topics() []types.MQTTTopicSummary { return m.topics }

func (m *mockMQTTService) Devices() []types.MQTTDevice { return m.devices }

func (m *mockMQTTService) RetainedByPrefix(prefix string) []types.MQTTRetainedMessage {
	if m.retainedByPrefix != nil {
		return m.retainedByPrefix[prefix]
	}
	return nil
}

func (m *mockMQTTService) RetainedPrefixes() []string { return m.retainedPrefixes }

func (m *mockMQTTService) Publish(ctx context.Context, topic string, payload []byte, qos byte, retained bool) (*types.MQTTPublishResult, error) {
	if m.pubErr != nil {
		return nil, m.pubErr
	}
	if m.pubResult != nil {
		return m.pubResult, nil
	}
	return &types.MQTTPublishResult{
		Topic:       topic,
		QoS:         1,
		Retained:    retained,
		PayloadSize: len(payload),
		BrokerAck:   true,
	}, nil
}

func TestMQTTTool_NotConfigured(t *testing.T) {
	tool := NewMQTTTool(&types.ToolServices{})
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "status",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "not configured")
}

func TestMQTTTool_Status(t *testing.T) {
	svc := &mockMQTTService{
		status: types.MQTTServiceStatus{
			Connected:        true,
			BrokerURL:        "tcp://localhost:1883",
			SubscribedTopics: []string{"zigbee2mqtt/#"},
			ActiveTopics:     5,
			TotalEvents:      42,
			PublishAllowed:   false,
		},
	}
	tool := NewMQTTTool(&types.ToolServices{MQTTService: svc})
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "status",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "connected")
	assert.Equal(t, true, result.Data["connected"])
	assert.Equal(t, 5, result.Data["active_topics"])
}

func TestMQTTTool_Topics(t *testing.T) {
	svc := &mockMQTTService{
		topics: []types.MQTTTopicSummary{
			{Topic: "zigbee2mqtt/sensor1", EventCount: 10, LastEvent: time.Now(), LastValue: json.RawMessage(`{"temp":22}`)},
		},
	}
	tool := NewMQTTTool(&types.ToolServices{MQTTService: svc})
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "topics",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "1 active topics")
}

func TestMQTTTool_Recent(t *testing.T) {
	svc := &mockMQTTService{
		events: []types.MQTTEvent{
			{Topic: "zigbee2mqtt/sensor1", Payload: json.RawMessage(`{"temp":22}`), Timestamp: time.Now()},
			{Topic: "zigbee2mqtt/sensor2", Payload: json.RawMessage(`{"temp":19}`), Timestamp: time.Now()},
		},
	}
	tool := NewMQTTTool(&types.ToolServices{MQTTService: svc})
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "recent",
		"limit":  float64(10),
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "2 recent events")
}

func TestMQTTTool_History(t *testing.T) {
	svc := &mockMQTTService{
		events: []types.MQTTEvent{
			{Topic: "zigbee2mqtt/sensor1", Payload: json.RawMessage(`{"temp":22}`), Timestamp: time.Now()},
		},
	}
	tool := NewMQTTTool(&types.ToolServices{MQTTService: svc})

	// Missing topic
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "history",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)

	// With topic
	result, err = tool.Execute(context.Background(), map[string]interface{}{
		"action": "history",
		"topic":  "zigbee2mqtt/sensor1",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
}

func TestMQTTTool_Publish_Success(t *testing.T) {
	svc := &mockMQTTService{}
	tool := NewMQTTTool(&types.ToolServices{MQTTService: svc})
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":  "publish",
		"topic":   "zigbee2mqtt/Light/set",
		"payload": `{"state":"ON"}`,
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "broker ACK confirmed")
	assert.Equal(t, true, result.Data["broker_ack"])
	assert.Equal(t, "zigbee2mqtt/Light/set", result.Data["topic"])
}

func TestMQTTTool_Publish_NotAllowed(t *testing.T) {
	svc := &mockMQTTService{
		pubErr: types.ErrMQTTPublishNotAllowed,
	}
	tool := NewMQTTTool(&types.ToolServices{MQTTService: svc})
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":  "publish",
		"topic":   "test/topic",
		"payload": `{"state":"ON"}`,
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "publish failed")
}

func TestMQTTTool_InvalidAction(t *testing.T) {
	svc := &mockMQTTService{}
	tool := NewMQTTTool(&types.ToolServices{MQTTService: svc})
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "invalid",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
}

func TestMQTTTool_Name(t *testing.T) {
	tool := NewMQTTTool(&types.ToolServices{})
	assert.Equal(t, "MQTT", tool.Name())
}

func TestMQTTTool_ActionDocProvider(t *testing.T) {
	tool := NewMQTTTool(&types.ToolServices{})

	// Verify tool implements ActionDocProvider
	adp, ok := interface{}(tool).(types.ActionDocProvider)
	assert.True(t, ok, "MQTTTool should implement ActionDocProvider")

	docs := adp.GetActionDocs()
	assert.Len(t, docs, 6, "Should have docs for all 6 actions")

	// Check publish requires topic and payload
	pub := docs["publish"]
	assert.Contains(t, pub.RequiredParams, "topic")
	assert.Contains(t, pub.RequiredParams, "payload")
	assert.NotEmpty(t, pub.Returns)

	// Check status has no required params
	status := docs["status"]
	assert.Empty(t, status.RequiredParams)
	assert.NotEmpty(t, status.Returns)

	// Check devices action exists
	devDoc := docs["devices"]
	assert.NotEmpty(t, devDoc.Returns)
}

func TestMQTTTool_Devices_WithZigbeeDevices(t *testing.T) {
	svc := &mockMQTTService{
		devices: []types.MQTTDevice{
			{FriendlyName: "Living Room Sensor", Type: "EndDevice", Manufacturer: "SONOFF", MQTTTopic: "zigbee2mqtt/Living Room Sensor", Description: "Temp sensor"},
			{FriendlyName: "Kitchen Light", Type: "Router", Manufacturer: "Philips", MQTTTopic: "zigbee2mqtt/Kitchen Light", ModelID: "LCA001"},
		},
		retainedPrefixes: []string{"zigbee2mqtt", "solar_assistant"},
		retainedByPrefix: map[string][]types.MQTTRetainedMessage{
			"solar_assistant/": {
				{Topic: "solar_assistant/battery_soc/state"},
				{Topic: "solar_assistant/pv_power/state"},
			},
		},
	}
	tool := NewMQTTTool(&types.ToolServices{MQTTService: svc})
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "devices",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "2 zigbee2mqtt devices")
	assert.Contains(t, result.Content, "1 other MQTT sources")
	assert.NotNil(t, result.Data["zigbee2mqtt_devices"])
	assert.NotNil(t, result.Data["other_sources"])
}

func TestMQTTTool_Devices_Filtered(t *testing.T) {
	svc := &mockMQTTService{
		devices: []types.MQTTDevice{
			{FriendlyName: "Living Room Sensor", Type: "EndDevice", MQTTTopic: "zigbee2mqtt/Living Room Sensor"},
			{FriendlyName: "Kitchen Light", Type: "Router", MQTTTopic: "zigbee2mqtt/Kitchen Light"},
			{FriendlyName: "Bedroom Light", Type: "Router", MQTTTopic: "zigbee2mqtt/Bedroom Light"},
		},
	}
	tool := NewMQTTTool(&types.ToolServices{MQTTService: svc})
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":       "devices",
		"name_pattern": "*light*",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "2 zigbee2mqtt devices")
}

func TestMQTTTool_Devices_Empty(t *testing.T) {
	svc := &mockMQTTService{}
	tool := NewMQTTTool(&types.ToolServices{MQTTService: svc})
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "devices",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "No devices discovered")
}

func TestMQTTTool_Devices_OnlyRetainedSources(t *testing.T) {
	svc := &mockMQTTService{
		retainedPrefixes: []string{"solar_assistant"},
		retainedByPrefix: map[string][]types.MQTTRetainedMessage{
			"solar_assistant/": {
				{Topic: "solar_assistant/battery_soc/state"},
			},
		},
	}
	tool := NewMQTTTool(&types.ToolServices{MQTTService: svc})
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "devices",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "1 other MQTT sources")
	assert.Nil(t, result.Data["zigbee2mqtt_devices"])
	assert.NotNil(t, result.Data["other_sources"])
}
