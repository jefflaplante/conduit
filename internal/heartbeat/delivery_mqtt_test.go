package heartbeat

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"conduit/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockMQTTPublisher implements MQTTPublisher for testing.
type mockMQTTPublisher struct {
	publishedTopic   string
	publishedPayload []byte
	publishedQoS     byte
	publishedRetain  bool
	publishErr       error
}

func (m *mockMQTTPublisher) Publish(_ context.Context, topic string, payload []byte, qos byte, retained bool) (any, error) {
	m.publishedTopic = topic
	m.publishedPayload = payload
	m.publishedQoS = qos
	m.publishedRetain = retained
	return nil, m.publishErr
}

func TestMQTTDeliverer_Type(t *testing.T) {
	d := NewMQTTDeliverer(&mockMQTTPublisher{})
	assert.Equal(t, "mqtt", d.Type())
}

func TestMQTTDeliverer_Deliver_Success(t *testing.T) {
	mock := &mockMQTTPublisher{}
	d := NewMQTTDeliverer(mock)

	alert := Alert{
		ID:        "test-123",
		Source:    "test",
		Title:     "Test Alert",
		Message:   "This is a test",
		Severity:  AlertSeverityInfo,
		Status:    AlertStatusPending,
		CreatedAt: time.Now(),
	}

	target := config.AlertTarget{
		Name: "mqtt-test",
		Type: "mqtt",
		Config: map[string]string{
			"topic": "alerts/test",
		},
	}

	err := d.Deliver(context.Background(), alert, target)
	require.NoError(t, err)

	assert.Equal(t, "alerts/test", mock.publishedTopic)
	assert.Equal(t, byte(1), mock.publishedQoS) // default QoS
	assert.False(t, mock.publishedRetain)       // default retain

	// Verify payload is valid JSON containing alert data
	var decoded Alert
	err = json.Unmarshal(mock.publishedPayload, &decoded)
	require.NoError(t, err)
	assert.Equal(t, alert.ID, decoded.ID)
	assert.Equal(t, alert.Title, decoded.Title)
}

func TestMQTTDeliverer_Deliver_CustomQoSAndRetain(t *testing.T) {
	mock := &mockMQTTPublisher{}
	d := NewMQTTDeliverer(mock)

	alert := Alert{ID: "test-456", Source: "test", Status: AlertStatusPending, CreatedAt: time.Now()}
	target := config.AlertTarget{
		Name: "mqtt-custom",
		Type: "mqtt",
		Config: map[string]string{
			"topic":  "alerts/custom",
			"qos":    "2",
			"retain": "true",
		},
	}

	err := d.Deliver(context.Background(), alert, target)
	require.NoError(t, err)

	assert.Equal(t, byte(2), mock.publishedQoS)
	assert.True(t, mock.publishedRetain)
}

func TestMQTTDeliverer_Deliver_MissingTopic(t *testing.T) {
	d := NewMQTTDeliverer(&mockMQTTPublisher{})

	target := config.AlertTarget{
		Name:   "mqtt-no-topic",
		Type:   "mqtt",
		Config: map[string]string{},
	}

	err := d.Deliver(context.Background(), Alert{Status: AlertStatusPending}, target)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "topic")
}

func TestMQTTDeliverer_Deliver_PublishError(t *testing.T) {
	mock := &mockMQTTPublisher{
		publishErr: errors.New("connection lost"),
	}
	d := NewMQTTDeliverer(mock)

	target := config.AlertTarget{
		Name: "mqtt-fail",
		Type: "mqtt",
		Config: map[string]string{
			"topic": "alerts/fail",
		},
	}

	err := d.Deliver(context.Background(), Alert{Status: AlertStatusPending, CreatedAt: time.Now()}, target)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mqtt publish failed")
	assert.Contains(t, err.Error(), "connection lost")
}

func TestMQTTDeliverer_Deliver_InvalidQoS(t *testing.T) {
	mock := &mockMQTTPublisher{}
	d := NewMQTTDeliverer(mock)

	target := config.AlertTarget{
		Name: "mqtt-bad-qos",
		Type: "mqtt",
		Config: map[string]string{
			"topic": "alerts/test",
			"qos":   "invalid",
		},
	}

	err := d.Deliver(context.Background(), Alert{Status: AlertStatusPending, CreatedAt: time.Now()}, target)
	require.NoError(t, err)
	assert.Equal(t, byte(1), mock.publishedQoS) // falls back to default
}
