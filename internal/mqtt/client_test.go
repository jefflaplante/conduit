package mqtt

import (
	"testing"

	"conduit/internal/config"

	"github.com/stretchr/testify/assert"
)

func TestNewClient(t *testing.T) {
	cfg := config.MQTTConfig{
		BrokerURL: "tcp://localhost:1883",
		ClientID:  "test-client",
		Topics:    []string{"test/#"},
		QoS:       1,
	}

	var receivedEvent *Event
	onMessage := func(e Event) {
		receivedEvent = &e
	}

	client := NewClient(cfg, onMessage)

	assert.NotNil(t, client)
	assert.Equal(t, cfg.BrokerURL, client.cfg.BrokerURL)
	assert.Equal(t, cfg.ClientID, client.cfg.ClientID)
	assert.False(t, client.connected)
	assert.Nil(t, client.pahoClient) // Not connected yet

	// Verify onMessage callback is stored
	assert.NotNil(t, client.onMessage)

	// Test that receivedEvent pointer works as expected (callback is stored)
	assert.Nil(t, receivedEvent)
}

func TestClient_IsConnected_Initial(t *testing.T) {
	cfg := config.MQTTConfig{
		BrokerURL: "tcp://localhost:1883",
	}

	client := NewClient(cfg, nil)

	// Initially not connected
	assert.False(t, client.IsConnected())
}

func TestClient_Close_NilPahoClient(t *testing.T) {
	cfg := config.MQTTConfig{
		BrokerURL: "tcp://localhost:1883",
	}

	client := NewClient(cfg, nil)

	// Should not panic when pahoClient is nil
	client.Close()
	assert.False(t, client.IsConnected())
}

func TestPublishResult_Fields(t *testing.T) {
	result := PublishResult{
		Topic:       "test/topic",
		QoS:         1,
		Retained:    true,
		PayloadSize: 42,
		BrokerAck:   true,
	}

	assert.Equal(t, "test/topic", result.Topic)
	assert.Equal(t, byte(1), result.QoS)
	assert.True(t, result.Retained)
	assert.Equal(t, 42, result.PayloadSize)
	assert.True(t, result.BrokerAck)
}
