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
	status types.MQTTServiceStatus
	events []types.MQTTEvent
	topics []types.MQTTTopicSummary
	pubErr error
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

func (m *mockMQTTService) Publish(ctx context.Context, topic string, payload []byte, qos byte, retained bool) error {
	return m.pubErr
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

func TestMQTTTool_Publish_NotAllowed(t *testing.T) {
	svc := &mockMQTTService{
		pubErr: nil, // won't reach because tool checks config first
		status: types.MQTTServiceStatus{PublishAllowed: false},
	}
	// Use a service that returns an error for publish
	svc.pubErr = types.ErrMQTTPublishNotAllowed
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
