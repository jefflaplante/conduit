package mqtt

import (
	"encoding/json"
	"testing"
	"time"

	"conduit/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewService(t *testing.T) {
	cfg := config.MQTTConfig{
		BrokerURL:       "tcp://localhost:1883",
		ClientID:        "test-client",
		Topics:          []string{"test/#"},
		BufferMaxAge:    3600,
		BufferMaxEvents: 100,
		BufferMaxTopics: 50,
		PublishAllowed:  true,
	}

	svc := NewService(cfg)
	require.NotNil(t, svc)
	assert.NotNil(t, svc.buffer)
	assert.NotNil(t, svc.retained)
	assert.NotNil(t, svc.deviceRegistry)
	assert.Nil(t, svc.client) // Not connected yet
}

func TestService_Status_NotConnected(t *testing.T) {
	cfg := config.MQTTConfig{
		BrokerURL:       "tcp://localhost:1883",
		Topics:          []string{"zigbee2mqtt/#"},
		BufferMaxAge:    3600,
		BufferMaxEvents: 100,
		BufferMaxTopics: 50,
		PublishAllowed:  false,
	}

	svc := NewService(cfg)
	status := svc.Status()

	assert.False(t, status.Connected)
	assert.Equal(t, "tcp://localhost:1883", status.BrokerURL)
	assert.Equal(t, []string{"zigbee2mqtt/#"}, status.SubscribedTopics)
	assert.Equal(t, 0, status.ActiveTopics)
	assert.Equal(t, int64(0), status.TotalEvents)
	assert.False(t, status.PublishAllowed)
}

func TestService_BufferDelegation(t *testing.T) {
	cfg := config.MQTTConfig{
		BufferMaxAge:    3600,
		BufferMaxEvents: 100,
		BufferMaxTopics: 50,
	}

	svc := NewService(cfg)

	// Add events directly to buffer to test delegation
	now := time.Now()
	svc.buffer.Add(Event{
		Topic:     "test/topic1",
		Payload:   json.RawMessage(`{"val":1}`),
		Timestamp: now,
	})
	svc.buffer.Add(Event{
		Topic:     "test/topic2",
		Payload:   json.RawMessage(`{"val":2}`),
		Timestamp: now.Add(time.Second),
	})

	// Test Recent delegation
	recent := svc.Recent(10)
	assert.Len(t, recent, 2)
	assert.Equal(t, "test/topic2", recent[0].Topic) // Newest first

	// Test RecentForTopic delegation
	forTopic := svc.RecentForTopic("test/topic1", 10)
	require.Len(t, forTopic, 1)
	assert.Equal(t, "test/topic1", forTopic[0].Topic)

	// Test RecentMatching delegation
	matching := svc.RecentMatching("test/*", 10)
	assert.Len(t, matching, 2)

	// Test Topics delegation
	topics := svc.Topics()
	assert.Len(t, topics, 2)
}

func TestService_DevicesDelegation(t *testing.T) {
	cfg := config.MQTTConfig{
		BufferMaxAge:    3600,
		BufferMaxEvents: 100,
		BufferMaxTopics: 50,
	}

	svc := NewService(cfg)

	// Update device registry directly
	err := svc.deviceRegistry.Update([]byte(`[
		{"ieee_address":"0x123","friendly_name":"Test Device","type":"EndDevice"}
	]`))
	require.NoError(t, err)

	// Test Devices delegation
	devices := svc.Devices()
	require.Len(t, devices, 1)
	assert.Equal(t, "Test Device", devices[0].FriendlyName)
}

func TestService_RetainedDelegation(t *testing.T) {
	cfg := config.MQTTConfig{
		BufferMaxAge:    3600,
		BufferMaxEvents: 100,
		BufferMaxTopics: 50,
	}

	svc := NewService(cfg)
	now := time.Now()

	// Set retained messages directly
	svc.retained.Set("zigbee2mqtt/sensor1", []byte(`{"temp":22}`), now)
	svc.retained.Set("homeassistant/state", []byte(`{}`), now)

	// Test RetainedByPrefix delegation
	msgs := svc.RetainedByPrefix("zigbee2mqtt/")
	require.Len(t, msgs, 1)
	assert.Equal(t, "zigbee2mqtt/sensor1", msgs[0].Topic)

	// Test RetainedPrefixes delegation
	prefixes := svc.RetainedPrefixes()
	assert.Len(t, prefixes, 2)
}

func TestService_Publish_NotAllowed(t *testing.T) {
	cfg := config.MQTTConfig{
		PublishAllowed:  false,
		BufferMaxAge:    3600,
		BufferMaxEvents: 100,
		BufferMaxTopics: 50,
	}

	svc := NewService(cfg)

	result, err := svc.Publish(nil, "test/topic", []byte(`{}`), 1, false)
	assert.Nil(t, result)
	assert.Equal(t, ErrPublishNotAllowed, err)
}

func TestService_Publish_NotConnected(t *testing.T) {
	cfg := config.MQTTConfig{
		PublishAllowed:  true,
		BufferMaxAge:    3600,
		BufferMaxEvents: 100,
		BufferMaxTopics: 50,
	}

	svc := NewService(cfg)

	result, err := svc.Publish(nil, "test/topic", []byte(`{}`), 1, false)
	assert.Nil(t, result)
	assert.Equal(t, ErrNotConnected, err)
}

func TestService_Stop_NilClient(t *testing.T) {
	cfg := config.MQTTConfig{
		BufferMaxAge:    3600,
		BufferMaxEvents: 100,
		BufferMaxTopics: 50,
	}

	svc := NewService(cfg)

	// Should not panic
	svc.Stop()
}

func TestService_Status_UpdatesWithEvents(t *testing.T) {
	cfg := config.MQTTConfig{
		BrokerURL:       "tcp://localhost:1883",
		Topics:          []string{"test/#"},
		BufferMaxAge:    3600,
		BufferMaxEvents: 100,
		BufferMaxTopics: 50,
		PublishAllowed:  true,
	}

	svc := NewService(cfg)

	// Initial status
	status := svc.Status()
	assert.Equal(t, 0, status.ActiveTopics)
	assert.Equal(t, int64(0), status.TotalEvents)

	// Add some events
	svc.buffer.Add(Event{Topic: "a", Payload: json.RawMessage(`{}`), Timestamp: time.Now()})
	svc.buffer.Add(Event{Topic: "b", Payload: json.RawMessage(`{}`), Timestamp: time.Now()})

	// Status should reflect changes
	status = svc.Status()
	assert.Equal(t, 2, status.ActiveTopics)
	assert.Equal(t, int64(2), status.TotalEvents)
}
