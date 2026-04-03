package mqtt

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"conduit/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestServiceWithData(t *testing.T) *Service {
	t.Helper()
	cfg := config.MQTTConfig{
		BrokerURL:       "tcp://localhost:1883",
		Topics:          []string{"zigbee2mqtt/#"},
		BufferMaxAge:    3600,
		BufferMaxEvents: 100,
		BufferMaxTopics: 50,
		PublishAllowed:  false,
	}

	svc := NewService(cfg)

	// Add test events
	now := time.Now()
	svc.buffer.Add(Event{
		Topic:     "zigbee2mqtt/sensor1",
		Payload:   json.RawMessage(`{"temperature":22.5}`),
		Timestamp: now,
		Retained:  false,
	})
	svc.buffer.Add(Event{
		Topic:     "zigbee2mqtt/sensor2",
		Payload:   json.RawMessage(`{"humidity":55}`),
		Timestamp: now.Add(time.Second),
		Retained:  true,
	})

	// Add retained messages
	svc.retained.Set("zigbee2mqtt/sensor1", []byte(`{"temperature":22.5}`), now)
	svc.retained.Set("homeassistant/state", []byte(`{"state":"running"}`), now)

	// Add devices
	err := svc.deviceRegistry.Update([]byte(`[
		{"ieee_address":"0xabc123","friendly_name":"Living Room Sensor","type":"EndDevice","supported":true},
		{"ieee_address":"0xdef456","friendly_name":"Kitchen Light","type":"Router","manufacturer":"Philips"}
	]`))
	require.NoError(t, err)

	return svc
}

func TestServiceAdapter_Status(t *testing.T) {
	svc := newTestServiceWithData(t)
	adapter := NewServiceAdapter(svc)

	status := adapter.Status()

	assert.False(t, status.Connected)
	assert.Equal(t, "tcp://localhost:1883", status.BrokerURL)
	assert.Equal(t, []string{"zigbee2mqtt/#"}, status.SubscribedTopics)
	assert.Equal(t, 2, status.ActiveTopics)
	assert.Equal(t, int64(2), status.TotalEvents)
	assert.False(t, status.PublishAllowed)
}

func TestServiceAdapter_Recent(t *testing.T) {
	svc := newTestServiceWithData(t)
	adapter := NewServiceAdapter(svc)

	events := adapter.Recent(10)
	require.Len(t, events, 2)

	// Check first event (newest)
	assert.Equal(t, "zigbee2mqtt/sensor2", events[0].Topic)
	assert.Equal(t, json.RawMessage(`{"humidity":55}`), events[0].Payload)
	assert.True(t, events[0].Retained)

	// Check second event
	assert.Equal(t, "zigbee2mqtt/sensor1", events[1].Topic)
	assert.False(t, events[1].Retained)
}

func TestServiceAdapter_RecentForTopic(t *testing.T) {
	svc := newTestServiceWithData(t)
	adapter := NewServiceAdapter(svc)

	events := adapter.RecentForTopic("zigbee2mqtt/sensor1", 10)
	require.Len(t, events, 1)
	assert.Equal(t, "zigbee2mqtt/sensor1", events[0].Topic)

	// Non-existent topic
	events = adapter.RecentForTopic("nonexistent", 10)
	assert.Len(t, events, 0)
}

func TestServiceAdapter_RecentMatching(t *testing.T) {
	svc := newTestServiceWithData(t)
	adapter := NewServiceAdapter(svc)

	events := adapter.RecentMatching("zigbee2mqtt/*", 10)
	assert.Len(t, events, 2)

	// Non-matching pattern
	events = adapter.RecentMatching("homeassistant/*", 10)
	assert.Len(t, events, 0)
}

func TestServiceAdapter_Topics(t *testing.T) {
	svc := newTestServiceWithData(t)
	adapter := NewServiceAdapter(svc)

	topics := adapter.Topics()
	require.Len(t, topics, 2)

	// Verify conversion
	topicMap := make(map[string]bool)
	for _, ts := range topics {
		topicMap[ts.Topic] = true
		assert.Equal(t, 1, ts.EventCount)
		assert.NotZero(t, ts.LastEvent)
		assert.NotEmpty(t, ts.LastValue)
	}
	assert.True(t, topicMap["zigbee2mqtt/sensor1"])
	assert.True(t, topicMap["zigbee2mqtt/sensor2"])
}

func TestServiceAdapter_Devices(t *testing.T) {
	svc := newTestServiceWithData(t)
	adapter := NewServiceAdapter(svc)

	devices := adapter.Devices()
	require.Len(t, devices, 2)

	// Find Living Room Sensor
	var sensor *struct {
		FriendlyName string
		MQTTTopic    string
		Supported    bool
	}
	for _, d := range devices {
		if d.FriendlyName == "Living Room Sensor" {
			sensor = &struct {
				FriendlyName string
				MQTTTopic    string
				Supported    bool
			}{d.FriendlyName, d.MQTTTopic, d.Supported}
			break
		}
	}

	require.NotNil(t, sensor, "Living Room Sensor not found")
	assert.Equal(t, "zigbee2mqtt/Living Room Sensor", sensor.MQTTTopic)
	assert.True(t, sensor.Supported)
}

func TestServiceAdapter_RetainedByPrefix(t *testing.T) {
	svc := newTestServiceWithData(t)
	adapter := NewServiceAdapter(svc)

	msgs := adapter.RetainedByPrefix("zigbee2mqtt/")
	require.Len(t, msgs, 1)
	assert.Equal(t, "zigbee2mqtt/sensor1", msgs[0].Topic)
	assert.Equal(t, json.RawMessage(`{"temperature":22.5}`), msgs[0].Payload)
	assert.NotZero(t, msgs[0].Timestamp)
}

func TestServiceAdapter_RetainedPrefixes(t *testing.T) {
	svc := newTestServiceWithData(t)
	adapter := NewServiceAdapter(svc)

	prefixes := adapter.RetainedPrefixes()
	assert.Len(t, prefixes, 2)
}

func TestServiceAdapter_Publish_NotAllowed(t *testing.T) {
	svc := newTestServiceWithData(t)
	adapter := NewServiceAdapter(svc)

	result, err := adapter.Publish(context.Background(), "test/topic", []byte(`{}`), 1, false)
	assert.Nil(t, result)
	assert.Equal(t, ErrPublishNotAllowed, err)
}

func TestServiceAdapter_Publish_NotConnected(t *testing.T) {
	cfg := config.MQTTConfig{
		PublishAllowed:  true,
		BufferMaxAge:    3600,
		BufferMaxEvents: 100,
		BufferMaxTopics: 50,
	}
	svc := NewService(cfg)
	adapter := NewServiceAdapter(svc)

	result, err := adapter.Publish(context.Background(), "test/topic", []byte(`{}`), 1, false)
	assert.Nil(t, result)
	assert.Equal(t, ErrNotConnected, err)
}

func TestConvertEvents(t *testing.T) {
	now := time.Now()
	events := []Event{
		{Topic: "a", Payload: json.RawMessage(`{"a":1}`), Timestamp: now, Retained: true},
		{Topic: "b", Payload: json.RawMessage(`{"b":2}`), Timestamp: now.Add(time.Second), Retained: false},
	}

	converted := convertEvents(events)
	require.Len(t, converted, 2)

	assert.Equal(t, "a", converted[0].Topic)
	assert.Equal(t, json.RawMessage(`{"a":1}`), converted[0].Payload)
	assert.Equal(t, now, converted[0].Timestamp)
	assert.True(t, converted[0].Retained)

	assert.Equal(t, "b", converted[1].Topic)
	assert.Equal(t, json.RawMessage(`{"b":2}`), converted[1].Payload)
	assert.Equal(t, now.Add(time.Second), converted[1].Timestamp)
	assert.False(t, converted[1].Retained)
}

func TestConvertEvents_Empty(t *testing.T) {
	converted := convertEvents(nil)
	assert.Len(t, converted, 0)

	converted = convertEvents([]Event{})
	assert.Len(t, converted, 0)
}
