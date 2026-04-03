package channels

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestStatusCode_Constants(t *testing.T) {
	// Verify all status code constants are defined
	assert.Equal(t, StatusCode("initializing"), StatusInitializing)
	assert.Equal(t, StatusCode("online"), StatusOnline)
	assert.Equal(t, StatusCode("offline"), StatusOffline)
	assert.Equal(t, StatusCode("error"), StatusError)
	assert.Equal(t, StatusCode("reconnecting"), StatusReconnecting)
}

func TestChannelStatus_Fields(t *testing.T) {
	now := time.Now()
	status := ChannelStatus{
		Status:    StatusOnline,
		Message:   "test message",
		Details:   map[string]interface{}{"key": "value"},
		Timestamp: now,
	}

	assert.Equal(t, StatusOnline, status.Status)
	assert.Equal(t, "test message", status.Message)
	assert.Equal(t, "value", status.Details["key"])
	assert.Equal(t, now, status.Timestamp)
}

func TestChannelConfig_Fields(t *testing.T) {
	config := ChannelConfig{
		ID:      "test-id",
		Type:    "telegram",
		Name:    "Test Channel",
		Enabled: true,
		Config: map[string]interface{}{
			"bot_token": "abc123",
		},
	}

	assert.Equal(t, "test-id", config.ID)
	assert.Equal(t, "telegram", config.Type)
	assert.Equal(t, "Test Channel", config.Name)
	assert.True(t, config.Enabled)
	assert.Equal(t, "abc123", config.Config["bot_token"])
}

func TestChannelConfig_DisabledDefault(t *testing.T) {
	config := ChannelConfig{
		ID:   "test",
		Type: "telegram",
	}

	assert.False(t, config.Enabled)
}
