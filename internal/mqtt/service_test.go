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

// TestService_Stop_WaitsForPruneGoroutine is a regression test for conduit-2yo8.
// Stop must block until the background prune goroutine has actually exited;
// otherwise a caller that immediately re-initializes or reuses the service can
// race with a still-running prune pass.
//
// The startPruneLoop hook lets us exercise the goroutine lifecycle without
// connecting to a real broker. We instrument the goroutine via the test-only
// pruneExited channel: with the WaitGroup fix in place, Stop's wg.Wait()
// returns only after the goroutine's deferred close(pruneExited) has run, so
// the channel is guaranteed to be closed by the time Stop returns.
func TestService_Stop_WaitsForPruneGoroutine(t *testing.T) {
	cfg := config.MQTTConfig{
		BufferMaxAge:    3600,
		BufferMaxEvents: 100,
		BufferMaxTopics: 50,
	}

	// Run multiple iterations to make the test robust against scheduling luck:
	// without the WaitGroup the channel-close race would manifest sporadically.
	const iterations = 50
	for i := 0; i < iterations; i++ {
		svc := NewService(cfg)
		svc.pruneExited = make(chan struct{})

		// Use a very short interval so the ticker is active and the goroutine
		// is parked in the select when Stop fires.
		svc.startPruneLoop(context.Background(), time.Millisecond)

		// Give the goroutine a moment to actually enter its select loop.
		time.Sleep(2 * time.Millisecond)

		stopReturned := make(chan struct{})
		go func() {
			svc.Stop()
			close(stopReturned)
		}()

		select {
		case <-stopReturned:
			// Stop returned. With the fix, the prune goroutine must have
			// already exited (and thus closed pruneExited).
			select {
			case <-svc.pruneExited:
				// Good — goroutine exited before Stop returned.
			default:
				t.Fatalf("iteration %d: Stop returned before prune goroutine exited", i)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("iteration %d: Stop did not return within 2s", i)
		}
	}
}

// TestService_Stop_WaitsForBlockedPrune verifies Stop's synchronous semantics
// even when the prune goroutine is doing work at cancellation time. We use a
// large ticker interval so the goroutine is parked in the select on
// pruneCtx.Done(); after cancel, the deferred close(pruneExited) runs before
// wg.Done(), which is what wg.Wait() observes.
func TestService_Stop_WaitsForBlockedPrune(t *testing.T) {
	cfg := config.MQTTConfig{
		BufferMaxAge:    3600,
		BufferMaxEvents: 100,
		BufferMaxTopics: 50,
	}

	svc := NewService(cfg)
	svc.pruneExited = make(chan struct{})
	svc.startPruneLoop(context.Background(), time.Hour)

	// Ensure goroutine is parked in select.
	time.Sleep(5 * time.Millisecond)

	// Sanity: pruneExited is not yet closed.
	select {
	case <-svc.pruneExited:
		t.Fatal("pruneExited closed before Stop was called")
	default:
	}

	svc.Stop()

	// Stop returned — pruneExited must be closed.
	select {
	case <-svc.pruneExited:
		// Expected.
	default:
		t.Fatal("pruneExited not closed after Stop returned")
	}
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
