package channels

import (
	"context"
	"sync"
	"testing"
	"time"

	"conduit/internal/protocol"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockAdapter implements ChannelAdapter for testing
type mockAdapter struct {
	id        string
	name      string
	adptType  string
	status    ChannelStatus
	incoming  chan *protocol.IncomingMessage
	healthy   bool
	startErr  error
	stopErr   error
	sendErr   error
	started   bool
	stopped   bool
	sentMsgs  []*protocol.OutgoingMessage
	mutex     sync.Mutex
	typingErr error
	typingID  string
}

func newMockAdapter(id, name, adptType string) *mockAdapter {
	return &mockAdapter{
		id:       id,
		name:     name,
		adptType: adptType,
		incoming: make(chan *protocol.IncomingMessage, 10),
		healthy:  true,
		status: ChannelStatus{
			Status:    StatusOnline,
			Message:   "Online",
			Details:   make(map[string]interface{}),
			Timestamp: time.Now(),
		},
	}
}

func (m *mockAdapter) ID() string   { return m.id }
func (m *mockAdapter) Name() string { return m.name }
func (m *mockAdapter) Type() string { return m.adptType }

func (m *mockAdapter) Start(ctx context.Context) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if m.startErr != nil {
		return m.startErr
	}
	m.started = true
	return nil
}

func (m *mockAdapter) Stop() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if m.stopErr != nil {
		return m.stopErr
	}
	m.stopped = true
	close(m.incoming)
	return nil
}

func (m *mockAdapter) SendMessage(msg *protocol.OutgoingMessage) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if m.sendErr != nil {
		return m.sendErr
	}
	m.sentMsgs = append(m.sentMsgs, msg)
	return nil
}

func (m *mockAdapter) ReceiveMessages() <-chan *protocol.IncomingMessage {
	return m.incoming
}

func (m *mockAdapter) Status() ChannelStatus {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	return m.status
}

func (m *mockAdapter) IsHealthy() bool {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	return m.healthy
}

// SendTypingIndicator implements the TypingIndicator interface
func (m *mockAdapter) SendTypingIndicator(chatID string) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	if m.typingErr != nil {
		return m.typingErr
	}
	m.typingID = chatID
	return nil
}

// mockFactory implements ChannelFactory for testing
type mockFactory struct {
	supportedTypes []string
	adapters       map[string]*mockAdapter
	createErr      error
}

func newMockFactory(types ...string) *mockFactory {
	return &mockFactory{
		supportedTypes: types,
		adapters:       make(map[string]*mockAdapter),
	}
}

func (f *mockFactory) SupportsType(adapterType string) bool {
	for _, t := range f.supportedTypes {
		if t == adapterType {
			return true
		}
	}
	return false
}

func (f *mockFactory) CreateAdapter(config ChannelConfig) (ChannelAdapter, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	adapter := newMockAdapter(config.ID, config.Name, config.Type)
	f.adapters[config.ID] = adapter
	return adapter, nil
}

func (f *mockFactory) GetSupportedTypes() []string {
	return f.supportedTypes
}

func TestNewManager(t *testing.T) {
	m := NewManager()
	require.NotNil(t, m)
	assert.NotNil(t, m.adapters)
	assert.NotNil(t, m.factories)
	assert.NotNil(t, m.incoming)
	assert.NotNil(t, m.outgoing)
	assert.NotNil(t, m.messageStats)
}

func TestManager_RegisterFactory(t *testing.T) {
	m := NewManager()
	factory := newMockFactory("telegram", "discord")

	m.RegisterFactory(factory)

	// Verify factory is registered for both types
	assert.Equal(t, factory, m.factories["telegram"])
	assert.Equal(t, factory, m.factories["discord"])
}

func TestManager_Start(t *testing.T) {
	m := NewManager()
	factory := newMockFactory("telegram")
	m.RegisterFactory(factory)

	configs := []ChannelConfig{
		{ID: "tg1", Type: "telegram", Name: "Telegram 1", Enabled: true},
	}

	ctx := context.Background()
	err := m.Start(ctx, configs)
	require.NoError(t, err)

	// Verify adapter was created
	adapter, exists := m.GetAdapter("tg1")
	assert.True(t, exists)
	assert.NotNil(t, adapter)

	// Cleanup
	m.Stop()
}

func TestManager_Start_SkipsDisabled(t *testing.T) {
	m := NewManager()
	factory := newMockFactory("telegram")
	m.RegisterFactory(factory)

	configs := []ChannelConfig{
		{ID: "disabled", Type: "telegram", Name: "Disabled", Enabled: false},
	}

	ctx := context.Background()
	err := m.Start(ctx, configs)
	require.NoError(t, err)

	// Verify adapter was NOT created
	_, exists := m.GetAdapter("disabled")
	assert.False(t, exists)

	m.Stop()
}

func TestManager_CreateAdapter_NoFactory(t *testing.T) {
	m := NewManager()

	config := ChannelConfig{ID: "test", Type: "unknown", Name: "Test", Enabled: true}
	err := m.CreateAdapter(config)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no factory found")
}

func TestManager_RemoveAdapter(t *testing.T) {
	m := NewManager()
	factory := newMockFactory("telegram")
	m.RegisterFactory(factory)

	ctx, cancel := context.WithCancel(context.Background())
	m.ctx = ctx
	m.cancel = cancel

	// Create adapter
	config := ChannelConfig{ID: "test", Type: "telegram", Name: "Test", Enabled: true}
	err := m.CreateAdapter(config)
	require.NoError(t, err)

	// Remove it
	err = m.RemoveAdapter("test")
	assert.NoError(t, err)

	// Verify it's gone
	_, exists := m.GetAdapter("test")
	assert.False(t, exists)

	cancel()
}

func TestManager_RemoveAdapter_NotFound(t *testing.T) {
	m := NewManager()

	err := m.RemoveAdapter("nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "adapter not found")
}

func TestManager_GetAdapter(t *testing.T) {
	m := NewManager()
	factory := newMockFactory("telegram")
	m.RegisterFactory(factory)

	ctx, cancel := context.WithCancel(context.Background())
	m.ctx = ctx
	m.cancel = cancel

	config := ChannelConfig{ID: "test", Type: "telegram", Name: "Test", Enabled: true}
	err := m.CreateAdapter(config)
	require.NoError(t, err)

	// Get existing adapter
	adapter, exists := m.GetAdapter("test")
	assert.True(t, exists)
	assert.NotNil(t, adapter)
	assert.Equal(t, "test", adapter.ID())

	// Get nonexistent adapter
	_, exists = m.GetAdapter("nonexistent")
	assert.False(t, exists)

	cancel()
}

func TestManager_GetAdapters(t *testing.T) {
	m := NewManager()
	factory := newMockFactory("telegram")
	m.RegisterFactory(factory)

	ctx, cancel := context.WithCancel(context.Background())
	m.ctx = ctx
	m.cancel = cancel

	// Create multiple adapters
	configs := []ChannelConfig{
		{ID: "test1", Type: "telegram", Name: "Test 1", Enabled: true},
		{ID: "test2", Type: "telegram", Name: "Test 2", Enabled: true},
	}

	for _, cfg := range configs {
		err := m.CreateAdapter(cfg)
		require.NoError(t, err)
	}

	// Get all adapters
	adapters := m.GetAdapters()
	assert.Len(t, adapters, 2)
	assert.Contains(t, adapters, "test1")
	assert.Contains(t, adapters, "test2")

	cancel()
}

func TestManager_GetHealthyAdapters(t *testing.T) {
	m := NewManager()
	factory := newMockFactory("telegram")
	m.RegisterFactory(factory)

	ctx, cancel := context.WithCancel(context.Background())
	m.ctx = ctx
	m.cancel = cancel

	// Create adapters
	config := ChannelConfig{ID: "healthy", Type: "telegram", Name: "Healthy", Enabled: true}
	err := m.CreateAdapter(config)
	require.NoError(t, err)

	// All adapters start healthy by default in our mock
	healthy := m.GetHealthyAdapters()
	assert.Len(t, healthy, 1)

	cancel()
}

func TestManager_GetAvailableTargets(t *testing.T) {
	m := NewManager()

	// No adapters
	targets := m.GetAvailableTargets()
	assert.Len(t, targets, 1)
	assert.Equal(t, "No channels configured", targets[0])

	// With adapters
	factory := newMockFactory("telegram")
	m.RegisterFactory(factory)

	ctx, cancel := context.WithCancel(context.Background())
	m.ctx = ctx
	m.cancel = cancel

	config := ChannelConfig{ID: "test", Type: "telegram", Name: "Test", Enabled: true}
	err := m.CreateAdapter(config)
	require.NoError(t, err)

	targets = m.GetAvailableTargets()
	assert.Len(t, targets, 1)
	assert.Contains(t, targets[0], "test")
	assert.Contains(t, targets[0], "online")

	cancel()
}

func TestManager_GetStatusMap(t *testing.T) {
	m := NewManager()
	factory := newMockFactory("telegram")
	m.RegisterFactory(factory)

	ctx, cancel := context.WithCancel(context.Background())
	m.ctx = ctx
	m.cancel = cancel

	config := ChannelConfig{ID: "test", Type: "telegram", Name: "Test", Enabled: true}
	err := m.CreateAdapter(config)
	require.NoError(t, err)

	statusMap := m.GetStatusMap()
	assert.Len(t, statusMap, 1)
	assert.Equal(t, "online", statusMap["test"])

	cancel()
}

func TestManager_GetMessageStats(t *testing.T) {
	m := NewManager()
	factory := newMockFactory("telegram")
	m.RegisterFactory(factory)

	ctx, cancel := context.WithCancel(context.Background())
	m.ctx = ctx
	m.cancel = cancel

	config := ChannelConfig{ID: "test", Type: "telegram", Name: "Test", Enabled: true}
	err := m.CreateAdapter(config)
	require.NoError(t, err)

	stats := m.GetMessageStats()
	assert.Len(t, stats, 1)
	assert.Equal(t, int64(0), stats["test"])

	cancel()
}

func TestManager_SendTypingIndicator(t *testing.T) {
	m := NewManager()
	factory := newMockFactory("telegram")
	m.RegisterFactory(factory)

	ctx, cancel := context.WithCancel(context.Background())
	m.ctx = ctx
	m.cancel = cancel

	config := ChannelConfig{ID: "test", Type: "telegram", Name: "Test", Enabled: true}
	err := m.CreateAdapter(config)
	require.NoError(t, err)

	// Get the mock adapter
	mockAdpt := factory.adapters["test"]

	// Call SendTypingIndicator
	m.SendTypingIndicator("test", "12345")

	// Verify the typing indicator was sent
	assert.Equal(t, "12345", mockAdpt.typingID)

	cancel()
}

func TestManager_SendTypingIndicator_AdapterNotFound(t *testing.T) {
	m := NewManager()

	// Should not panic when adapter doesn't exist
	m.SendTypingIndicator("nonexistent", "12345")
}

func TestManager_ReceiveMessages(t *testing.T) {
	m := NewManager()
	ch := m.ReceiveMessages()
	assert.NotNil(t, ch)
}

func TestReplyTagRe(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"reply_to_current", "[[reply_to_current]]", true},
		{"reply_to with ID", "[[reply_to:123]]", true},
		{"reply_to with whitespace", "[[ reply_to_current ]]", true},
		{"reply_to ID with whitespace", "[[ reply_to: 456 ]]", true},
		{"no match", "regular text", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, ReplyTagRe.MatchString(tt.input))
		})
	}
}

func TestStripReplyTags_Extended(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"preserves normal text", "Hello world", "Hello world"},
		{"strips single tag", "Hello [[reply_to_current]] world", "Hello  world"},
		{"strips tag with ID", "Reply [[reply_to:123]] here", "Reply  here"},
		{"strips multiple tags", "[[reply_to_current]] text [[reply_to:456]]", "text"},
		{"handles whitespace in tag", "Test [[ reply_to_current ]]", "Test"},
		{"empty result trimmed", "[[reply_to_current]]", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := StripReplyTags(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestProcessReplyTags_Extended(t *testing.T) {
	tests := []struct {
		name                  string
		text                  string
		sourceID              string
		expectedText          string
		expectedReplyToMsgID  string
		expectMetadataCreated bool
	}{
		{
			name:                  "no tags",
			text:                  "Hello world",
			expectedText:          "Hello world",
			expectedReplyToMsgID:  "",
			expectMetadataCreated: false,
		},
		{
			name:                  "reply_to_current with source",
			text:                  "Reply [[reply_to_current]]",
			sourceID:              "42",
			expectedText:          "Reply",
			expectedReplyToMsgID:  "42",
			expectMetadataCreated: true,
		},
		{
			name:                  "explicit ID overrides source",
			text:                  "Reply [[reply_to:99]]",
			sourceID:              "42",
			expectedText:          "Reply",
			expectedReplyToMsgID:  "99",
			expectMetadataCreated: true,
		},
		{
			name:                  "reply_to_current without source",
			text:                  "Reply [[reply_to_current]]",
			sourceID:              "",
			expectedText:          "Reply",
			expectedReplyToMsgID:  "",
			expectMetadataCreated: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := &protocol.OutgoingMessage{Text: tt.text}
			if tt.sourceID != "" {
				msg.Metadata = map[string]string{"source_message_id": tt.sourceID}
			}

			processReplyTags(msg)

			assert.Equal(t, tt.expectedText, msg.Text)
			if tt.expectMetadataCreated {
				assert.NotNil(t, msg.Metadata)
				if tt.expectedReplyToMsgID != "" {
					assert.Equal(t, tt.expectedReplyToMsgID, msg.Metadata["reply_to_message_id"])
				}
			}
		})
	}
}
