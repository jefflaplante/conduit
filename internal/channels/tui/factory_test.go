package tui

import (
	"testing"

	"conduit/internal/channels"
	"conduit/internal/protocol"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewFactory(t *testing.T) {
	handler := func(msg *protocol.OutgoingMessage) error { return nil }
	factory := NewFactory(handler)

	assert.NotNil(t, factory)
	assert.NotNil(t, factory.messageHandler)
}

func TestFactory_SupportsType(t *testing.T) {
	factory := NewFactory(nil)

	tests := []struct {
		adapterType string
		expected    bool
	}{
		{"tui", true},
		{"TUI", false}, // case sensitive
		{"telegram", false},
		{"discord", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.adapterType, func(t *testing.T) {
			assert.Equal(t, tt.expected, factory.SupportsType(tt.adapterType))
		})
	}
}

func TestFactory_GetSupportedTypes(t *testing.T) {
	factory := NewFactory(nil)

	types := factory.GetSupportedTypes()
	assert.Len(t, types, 1)
	assert.Equal(t, "tui", types[0])
}

func TestFactory_CreateAdapter(t *testing.T) {
	handler := func(msg *protocol.OutgoingMessage) error { return nil }
	factory := NewFactory(handler)

	config := channels.ChannelConfig{
		ID:      "test-tui",
		Type:    "tui",
		Name:    "Test TUI",
		Enabled: true,
	}

	adapter, err := factory.CreateAdapter(config)
	require.NoError(t, err)
	require.NotNil(t, adapter)

	assert.Equal(t, "test-tui", adapter.ID())
	assert.Equal(t, "TUI Channel", adapter.Name())
	assert.Equal(t, "tui", adapter.Type())
}

func TestFactory_CreateAdapter_WrongType(t *testing.T) {
	factory := NewFactory(nil)

	config := channels.ChannelConfig{
		ID:      "test",
		Type:    "telegram",
		Name:    "Test",
		Enabled: true,
	}

	adapter, err := factory.CreateAdapter(config)
	assert.Error(t, err)
	assert.Nil(t, adapter)
	assert.Contains(t, err.Error(), "unsupported adapter type")
}

func TestFactory_CreateAdapter_NilHandler(t *testing.T) {
	factory := NewFactory(nil)

	config := channels.ChannelConfig{
		ID:      "test-tui",
		Type:    "tui",
		Name:    "Test TUI",
		Enabled: true,
	}

	// Should still create adapter even with nil handler
	adapter, err := factory.CreateAdapter(config)
	require.NoError(t, err)
	require.NotNil(t, adapter)
}
