package telegram

import (
	"context"
	"testing"
	"time"

	"conduit/internal/channels"
	"conduit/internal/protocol"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewFactory(t *testing.T) {
	factory := NewFactory()
	assert.NotNil(t, factory)
	assert.Nil(t, factory.db)
}

func TestNewFactoryWithDB(t *testing.T) {
	// nil db is allowed
	factory := NewFactoryWithDB(nil, nil)
	assert.NotNil(t, factory)
	assert.Nil(t, factory.db)
}

func TestFactory_SupportsType(t *testing.T) {
	factory := NewFactory()

	tests := []struct {
		adapterType string
		expected    bool
	}{
		{"telegram", true},
		{"Telegram", false}, // case sensitive
		{"tui", false},
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
	factory := NewFactory()

	types := factory.GetSupportedTypes()
	assert.Len(t, types, 1)
	assert.Equal(t, "telegram", types[0])
}

func TestFactory_CreateAdapter_MissingToken(t *testing.T) {
	factory := NewFactory()

	config := channels.ChannelConfig{
		ID:      "test",
		Type:    "telegram",
		Name:    "Test",
		Enabled: true,
		Config:  map[string]interface{}{},
	}

	adapter, err := factory.CreateAdapter(config)
	assert.Error(t, err)
	assert.Nil(t, adapter)
	assert.Contains(t, err.Error(), "bot_token is required")
}

func TestFactory_CreateAdapter_WithToken(t *testing.T) {
	factory := NewFactory()

	config := channels.ChannelConfig{
		ID:      "test-tg",
		Type:    "telegram",
		Name:    "Test Telegram",
		Enabled: true,
		Config: map[string]interface{}{
			"bot_token": "123456:ABC-DEF",
		},
	}

	adapter, err := factory.CreateAdapter(config)
	require.NoError(t, err)
	require.NotNil(t, adapter)

	assert.Equal(t, "test-tg", adapter.ID())
	assert.Equal(t, "Test Telegram", adapter.Name())
	assert.Equal(t, "telegram", adapter.Type())
}

func TestFactory_CreateAdapter_WithAllOptions(t *testing.T) {
	factory := NewFactory()

	config := channels.ChannelConfig{
		ID:      "test-tg",
		Type:    "telegram",
		Name:    "Test Telegram",
		Enabled: true,
		Config: map[string]interface{}{
			"bot_token":    "123456:ABC-DEF",
			"webhook_mode": true,
			"webhook_url":  "https://example.com/webhook",
			"debug":        true,
		},
	}

	adapter, err := factory.CreateAdapter(config)
	require.NoError(t, err)

	// Cast to access internal config
	tgAdapter, ok := adapter.(*Adapter)
	require.True(t, ok)

	assert.Equal(t, "123456:ABC-DEF", tgAdapter.config.BotToken)
	assert.True(t, tgAdapter.config.WebhookMode)
	assert.Equal(t, "https://example.com/webhook", tgAdapter.config.WebhookURL)
	assert.True(t, tgAdapter.config.Debug)
}

func TestAdapter_HandleUpdate_DeduplicatesByUpdateID(t *testing.T) {
	mb := &mockBot{}
	adapter := newTestAdapter(mb)
	defer adapter.cancel()
	adapter.incoming = make(chan *protocol.IncomingMessage, 10)
	adapter.seenUpdates = make(map[int64]time.Time)

	update := &models.Update{
		ID: 42,
		Message: &models.Message{
			ID:   1,
			Text: "hello",
			Chat: models.Chat{ID: 12345, Type: models.ChatTypePrivate},
			From: &models.User{FirstName: "Alice"},
		},
	}

	adapter.handleUpdate(context.Background(), nil, update)
	adapter.handleUpdate(context.Background(), nil, update)

	// Drain: only one message should arrive
	select {
	case <-adapter.incoming:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected first message to be delivered")
	}

	select {
	case msg := <-adapter.incoming:
		t.Fatalf("expected duplicate update to be suppressed, got %+v", msg)
	case <-time.After(50 * time.Millisecond):
		// Expected: no second message
	}
}

func TestAdapter_IsPairingEnabled(t *testing.T) {
	adapter := &Adapter{
		id:   "test",
		name: "Test",
	}

	// Without pairing manager
	assert.False(t, adapter.IsPairingEnabled())

	// With pairing manager
	adapter.pairingMgr = &PairingManager{}
	assert.True(t, adapter.IsPairingEnabled())
}
