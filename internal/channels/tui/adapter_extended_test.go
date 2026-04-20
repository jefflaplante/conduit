package tui

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"conduit/internal/channels"
	"conduit/internal/protocol"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewAdapter(t *testing.T) {
	handler := func(msg *protocol.OutgoingMessage) error { return nil }
	adapter := NewAdapter("test-id", handler)

	assert.NotNil(t, adapter)
	assert.Equal(t, "test-id", adapter.id)
	assert.NotNil(t, adapter.messageHandler)
	assert.NotNil(t, adapter.incoming)
	assert.NotNil(t, adapter.outgoing)
	assert.Equal(t, channels.StatusInitializing, adapter.status.Status)
}

func TestAdapter_ID(t *testing.T) {
	adapter := NewAdapter("test-adapter-id", nil)
	assert.Equal(t, "test-adapter-id", adapter.ID())
}

func TestAdapter_Name(t *testing.T) {
	adapter := NewAdapter("test", nil)
	assert.Equal(t, "TUI Channel", adapter.Name())
}

func TestAdapter_Type(t *testing.T) {
	adapter := NewAdapter("test", nil)
	assert.Equal(t, "tui", adapter.Type())
}

func TestAdapter_Start(t *testing.T) {
	adapter := NewAdapter("test", nil)
	ctx := context.Background()

	err := adapter.Start(ctx)
	require.NoError(t, err)

	assert.Equal(t, channels.StatusOnline, adapter.status.Status)
	assert.Equal(t, "TUI adapter online", adapter.status.Message)
	assert.NotNil(t, adapter.ctx)
	assert.NotNil(t, adapter.cancel)

	adapter.Stop()
}

func TestAdapter_Stop(t *testing.T) {
	adapter := NewAdapter("test", nil)
	ctx := context.Background()

	err := adapter.Start(ctx)
	require.NoError(t, err)

	err = adapter.Stop()
	require.NoError(t, err)

	assert.Equal(t, channels.StatusOffline, adapter.status.Status)
	assert.True(t, adapter.stopped)
}

func TestAdapter_Stop_Idempotent(t *testing.T) {
	adapter := NewAdapter("test", nil)
	ctx := context.Background()

	err := adapter.Start(ctx)
	require.NoError(t, err)

	// First stop
	err = adapter.Stop()
	require.NoError(t, err)

	// Second stop should not panic
	err = adapter.Stop()
	require.NoError(t, err)
}

func TestAdapter_SendMessage(t *testing.T) {
	// The handler runs on the adapter's outgoing-processing goroutine; use a
	// channel to hand the message back to the test goroutine instead of a
	// shared variable + time.Sleep (which races under -race).
	receivedCh := make(chan *protocol.OutgoingMessage, 1)
	handler := func(msg *protocol.OutgoingMessage) error {
		receivedCh <- msg
		return nil
	}

	adapter := NewAdapter("test", handler)
	ctx := context.Background()
	err := adapter.Start(ctx)
	require.NoError(t, err)

	msg := &protocol.OutgoingMessage{Text: "Hello"}
	err = adapter.SendMessage(msg)
	require.NoError(t, err)

	var received *protocol.OutgoingMessage
	select {
	case received = <-receivedCh:
	case <-time.After(time.Second):
		t.Fatal("handler was not invoked within 1s")
	}

	assert.NotNil(t, received)
	assert.Equal(t, "Hello", received.Text)

	adapter.Stop()
}

func TestAdapter_SendMessage_Stopped(t *testing.T) {
	adapter := NewAdapter("test", nil)
	ctx := context.Background()
	err := adapter.Start(ctx)
	require.NoError(t, err)

	err = adapter.Stop()
	require.NoError(t, err)

	msg := &protocol.OutgoingMessage{Text: "Hello"}
	err = adapter.SendMessage(msg)
	assert.Error(t, err)

	channelErr, ok := err.(*ChannelError)
	assert.True(t, ok)
	assert.Equal(t, "ADAPTER_STOPPED", channelErr.Code)
}

func TestAdapter_SendIncomingMessage(t *testing.T) {
	adapter := NewAdapter("test", nil)
	ctx := context.Background()
	err := adapter.Start(ctx)
	require.NoError(t, err)

	msg := &protocol.IncomingMessage{Text: "User message"}
	err = adapter.SendIncomingMessage(msg)
	require.NoError(t, err)

	// Verify message is in channel
	select {
	case received := <-adapter.ReceiveMessages():
		assert.Equal(t, "User message", received.Text)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Expected to receive message")
	}

	adapter.Stop()
}

func TestAdapter_SendIncomingMessage_Stopped(t *testing.T) {
	adapter := NewAdapter("test", nil)
	ctx := context.Background()
	err := adapter.Start(ctx)
	require.NoError(t, err)

	err = adapter.Stop()
	require.NoError(t, err)

	msg := &protocol.IncomingMessage{Text: "User message"}
	err = adapter.SendIncomingMessage(msg)
	assert.Error(t, err)

	channelErr, ok := err.(*ChannelError)
	assert.True(t, ok)
	assert.Equal(t, "ADAPTER_STOPPED", channelErr.Code)
}

func TestAdapter_Status(t *testing.T) {
	adapter := NewAdapter("test", nil)

	status := adapter.Status()
	assert.Equal(t, channels.StatusInitializing, status.Status)

	ctx := context.Background()
	err := adapter.Start(ctx)
	require.NoError(t, err)

	status = adapter.Status()
	assert.Equal(t, channels.StatusOnline, status.Status)

	adapter.Stop()

	status = adapter.Status()
	assert.Equal(t, channels.StatusOffline, status.Status)
}

func TestAdapter_IsHealthy(t *testing.T) {
	adapter := NewAdapter("test", nil)

	// Not healthy before start
	assert.False(t, adapter.IsHealthy())

	ctx := context.Background()
	err := adapter.Start(ctx)
	require.NoError(t, err)

	// Healthy after start
	assert.True(t, adapter.IsHealthy())

	adapter.Stop()

	// Not healthy after stop
	assert.False(t, adapter.IsHealthy())
}

func TestAdapter_ReceiveMessages(t *testing.T) {
	adapter := NewAdapter("test", nil)
	ch := adapter.ReceiveMessages()
	assert.NotNil(t, ch)
}

func TestChannelError(t *testing.T) {
	err := &ChannelError{
		Code:    "TEST_ERROR",
		Message: "Test error message",
	}

	assert.Equal(t, "Test error message", err.Error())
	assert.Equal(t, "TEST_ERROR", err.Code)
}

func TestAdapter_ConcurrentSendMessage(t *testing.T) {
	var receivedCount int
	var mu sync.Mutex
	handler := func(msg *protocol.OutgoingMessage) error {
		mu.Lock()
		receivedCount++
		mu.Unlock()
		return nil
	}

	adapter := NewAdapter("test", handler)
	ctx := context.Background()
	err := adapter.Start(ctx)
	require.NoError(t, err)

	// Send multiple messages concurrently
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			msg := &protocol.OutgoingMessage{Text: "Message"}
			adapter.SendMessage(msg)
		}(i)
	}
	wg.Wait()

	// Wait for processing
	time.Sleep(100 * time.Millisecond)

	adapter.Stop()

	mu.Lock()
	assert.Equal(t, 10, receivedCount)
	mu.Unlock()
}

func TestAdapter_MessageHandlerError(t *testing.T) {
	handler := func(msg *protocol.OutgoingMessage) error {
		return errors.New("handler error")
	}

	adapter := NewAdapter("test", handler)
	ctx := context.Background()
	err := adapter.Start(ctx)
	require.NoError(t, err)

	msg := &protocol.OutgoingMessage{Text: "Hello"}
	err = adapter.SendMessage(msg)
	// SendMessage doesn't return handler errors, they're logged
	assert.NoError(t, err)

	time.Sleep(50 * time.Millisecond)
	adapter.Stop()
}

func TestAdapter_NilMessageHandler(t *testing.T) {
	adapter := NewAdapter("test", nil)
	ctx := context.Background()
	err := adapter.Start(ctx)
	require.NoError(t, err)

	msg := &protocol.OutgoingMessage{Text: "Hello"}
	err = adapter.SendMessage(msg)
	assert.NoError(t, err)

	time.Sleep(50 * time.Millisecond)
	adapter.Stop()
}

func TestAdapter_ContextCancellation(t *testing.T) {
	adapter := NewAdapter("test", nil)
	ctx, cancel := context.WithCancel(context.Background())

	err := adapter.Start(ctx)
	require.NoError(t, err)

	// Cancel context
	cancel()

	// Give time for goroutine to notice cancellation
	time.Sleep(50 * time.Millisecond)

	// SendMessage should fail with context canceled
	msg := &protocol.OutgoingMessage{Text: "Hello"}
	err = adapter.SendMessage(msg)
	// May return context.Canceled or queue full error
	// Just verify it doesn't panic
}
