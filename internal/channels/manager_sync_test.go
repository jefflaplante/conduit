package channels

import (
	"context"
	"strings"
	"testing"
	"time"

	"conduit/internal/protocol"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// conduit-wfai: SendMessageSync delivers through the adapter and returns the
// real send error. The async SendMessage path only reports enqueue success —
// the router goroutine logs and drops adapter failures — so the heartbeat
// alert audit (alert_history) recorded action_result=success even when the
// adapter failed ("chat not found"). The heartbeat deliverer uses the sync
// path; tool sends keep the async queue.
func newStartedManager(t *testing.T, types ...string) (*Manager, *mockFactory) {
	t.Helper()
	m := NewManager()
	factory := newMockFactory(types...)
	m.RegisterFactory(factory)
	configs := make([]ChannelConfig, 0, len(types))
	for i, adptType := range types {
		configs = append(configs, ChannelConfig{
			ID:      adptType + "1",
			Type:    adptType,
			Name:    "Test " + adptType,
			Enabled: true,
		})
		_ = i
	}
	require.NoError(t, m.Start(context.Background(), configs))
	t.Cleanup(func() { _ = m.Stop() })
	return m, factory
}

func syncTestMessage(channelID, text string) *protocol.OutgoingMessage {
	return &protocol.OutgoingMessage{
		BaseMessage: protocol.BaseMessage{
			Type:      protocol.TypeOutgoingMessage,
			ID:        "sync_test_msg",
			Timestamp: time.Now(),
		},
		ChannelID: channelID,
		UserID:    "42",
		Text:      text,
	}
}

func TestManager_SendMessageSync_DeliversViaAdapter(t *testing.T) {
	m, factory := newStartedManager(t, "telegram")

	err := m.SendMessageSync(syncTestMessage("telegram1", "alert body [[reply_to_current]]"))
	require.NoError(t, err)

	adapter := factory.adapters["telegram1"]
	adapter.mutex.Lock()
	defer adapter.mutex.Unlock()
	require.Len(t, adapter.sentMsgs, 1)
	// Preprocessing mirrors route(): reply tags stripped, text sanitized.
	assert.Equal(t, "alert body", adapter.sentMsgs[0].Text)
}

func TestManager_SendMessageSync_ReturnsAdapterError(t *testing.T) {
	m, factory := newStartedManager(t, "telegram")

	adapter := factory.adapters["telegram1"]
	adapter.mutex.Lock()
	adapter.sendErr = assert.AnError
	adapter.mutex.Unlock()

	err := m.SendMessageSync(syncTestMessage("telegram1", "alert body"))
	require.Error(t, err)
	assert.ErrorIs(t, err, assert.AnError)
}

func TestManager_SendMessageSync_NoAdapter(t *testing.T) {
	m, _ := newStartedManager(t, "telegram")

	err := m.SendMessageSync(syncTestMessage("missing", "alert body"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no adapter found")
}

func TestManager_SendMessageSync_NotRunning(t *testing.T) {
	m := NewManager()

	err := m.SendMessageSync(syncTestMessage("telegram1", "alert body"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not running")
}

func TestManager_SendMessageSync_ShuttingDown(t *testing.T) {
	m, _ := newStartedManager(t, "telegram")
	require.NoError(t, m.Stop())

	err := m.SendMessageSync(syncTestMessage("telegram1", "alert body"))
	require.Error(t, err)
	assert.True(t,
		strings.Contains(err.Error(), "shutting down"),
		"want shutdown error, got %v", err)
}
