package channels

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// restartableAdapter is a mockAdapter whose Stop leaves its message
// channel open (like the real adapters, conduit-31jg.26), so it survives
// RestartAdapter followed by Manager.Stop.
type restartableAdapter struct{ *mockAdapter }

func (r restartableAdapter) Stop() error {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.stopped = true
	return nil
}

// flakyFactory fails the first `failures` CreateAdapter calls, then returns
// healthy mock adapters. Safe for the retry watchdog goroutine.
type flakyFactory struct {
	mu       sync.Mutex
	failures int
	calls    int
	adapters map[string]*mockAdapter
}

func newFlakyFactory(failures int) *flakyFactory {
	return &flakyFactory{failures: failures, adapters: make(map[string]*mockAdapter)}
}

func (f *flakyFactory) SupportsType(t string) bool  { return t == "telegram" }
func (f *flakyFactory) GetSupportedTypes() []string { return []string{"telegram"} }

func (f *flakyFactory) CreateAdapter(config ChannelConfig) (ChannelAdapter, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls <= f.failures {
		return nil, errors.New("transient network error")
	}
	a := newMockAdapter(config.ID, config.Name, config.Type)
	f.adapters[config.ID] = a
	return restartableAdapter{a}, nil
}

func (f *flakyFactory) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *flakyFactory) adapter(id string) *mockAdapter {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.adapters[id]
}

func withRetryInterval(t *testing.T, d time.Duration) {
	t.Helper()
	old := retryInterval
	retryInterval = d
	t.Cleanup(func() { retryInterval = old })
}

func retryPendingCount(m *Manager) int {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	return len(m.retryConfigs)
}

var tgConfig = ChannelConfig{ID: "tg1", Type: "telegram", Name: "Telegram 1", Enabled: true}

// bd-twb0: a channel whose adapter fails at Start is kept and shown, not
// silently dropped, and the watchdog brings it up once creation succeeds.
func TestManager_Start_RetainsFailedConfigAndWatchdogRecovers(t *testing.T) {
	withRetryInterval(t, 10*time.Millisecond)
	f := newFlakyFactory(2)
	m := NewManager()
	m.RegisterFactory(f)

	require.NoError(t, m.Start(context.Background(), []ChannelConfig{tgConfig}))
	defer m.Stop()

	assert.Eventually(t, func() bool {
		_, ok := m.GetAdapter("tg1")
		return ok
	}, 5*time.Second, 5*time.Millisecond, "watchdog never recovered the adapter")

	assert.Equal(t, 0, retryPendingCount(m), "recovered config still queued for retry")
	assert.Empty(t, m.RetryPendingStatuses())
	assert.Equal(t, 3, f.callCount(), "want 2 failed attempts + 1 success")
	assert.True(t, f.adapter("tg1").started)
}

// While pending, the channel is visible in status output with the error.
func TestManager_RetryPendingVisibleInStatus(t *testing.T) {
	withRetryInterval(t, time.Hour) // no watchdog retries during the test
	f := newFlakyFactory(1)
	m := NewManager()
	m.RegisterFactory(f)

	require.NoError(t, m.Start(context.Background(), []ChannelConfig{tgConfig}))
	defer m.Stop()

	_, registered := m.GetAdapter("tg1")
	require.False(t, registered)
	require.Equal(t, 1, retryPendingCount(m))

	st, ok := m.GetStatus()["tg1"]
	require.True(t, ok, "retry-pending channel missing from GetStatus")
	assert.Equal(t, StatusError, st.Status)
	assert.Contains(t, st.Message, "transient network error")
	assert.Equal(t, true, st.Details["retry_pending"])
	assert.Equal(t, 1, st.Details["attempts"])

	assert.Equal(t, string(StatusError), m.GetStatusMap()["tg1"])
	assert.Contains(t, m.RetryPendingStatuses(), "tg1")
}

func TestManager_EnsureAdapter_RecoversRetryPending(t *testing.T) {
	withRetryInterval(t, time.Hour)
	f := newFlakyFactory(1)
	m := NewManager()
	m.RegisterFactory(f)
	require.NoError(t, m.Start(context.Background(), []ChannelConfig{tgConfig}))
	defer m.Stop()
	require.Equal(t, 1, retryPendingCount(m))

	require.NoError(t, m.EnsureAdapter("tg1"))

	_, registered := m.GetAdapter("tg1")
	assert.True(t, registered)
	assert.Equal(t, 0, retryPendingCount(m))
	assert.Equal(t, 2, f.callCount())
}

func TestManager_EnsureAdapter_FailureStaysQueued(t *testing.T) {
	withRetryInterval(t, time.Hour)
	f := newFlakyFactory(2)
	m := NewManager()
	m.RegisterFactory(f)
	require.NoError(t, m.Start(context.Background(), []ChannelConfig{tgConfig}))
	defer m.Stop()

	require.Error(t, m.EnsureAdapter("tg1"))
	require.Equal(t, 1, retryPendingCount(m))
	assert.Equal(t, 2, m.RetryPendingStatuses()["tg1"].Details["attempts"])
}

func TestManager_EnsureAdapter_HealthyIsNoop(t *testing.T) {
	f := newFlakyFactory(0)
	m := NewManager()
	m.RegisterFactory(f)
	require.NoError(t, m.Start(context.Background(), []ChannelConfig{tgConfig}))
	defer m.Stop()

	a := f.adapter("tg1")
	require.NoError(t, m.EnsureAdapter("tg1"))

	got, _ := m.GetAdapter("tg1")
	assert.Same(t, a, got.(restartableAdapter).mockAdapter, "healthy adapter was replaced")
	assert.False(t, a.stopped, "healthy adapter was restarted")
	assert.Equal(t, 1, f.callCount())
}

func TestManager_EnsureAdapter_UnhealthyRestarts(t *testing.T) {
	f := newFlakyFactory(0)
	m := NewManager()
	m.RegisterFactory(f)
	require.NoError(t, m.Start(context.Background(), []ChannelConfig{tgConfig}))
	defer m.Stop()

	a := f.adapter("tg1")
	a.mutex.Lock()
	a.healthy = false
	a.started = false
	a.mutex.Unlock()

	require.NoError(t, m.EnsureAdapter("tg1"))

	a.mutex.Lock()
	defer a.mutex.Unlock()
	assert.True(t, a.stopped, "unhealthy adapter not stopped")
	assert.True(t, a.started, "unhealthy adapter not restarted")
	assert.Equal(t, 1, f.callCount(), "restart must reuse the adapter, not create one")
}

func TestManager_EnsureAdapter_Unknown(t *testing.T) {
	m := NewManager()
	m.RegisterFactory(newFlakyFactory(0))
	require.NoError(t, m.Start(context.Background(), nil))
	defer m.Stop()

	assert.ErrorContains(t, m.EnsureAdapter("nope"), "adapter not found")
}

// Disable then enable: RemoveAdapter stops it, EnsureAdapter recreates it
// from the config given to Start.
func TestManager_RemoveThenEnsureAdapter(t *testing.T) {
	f := newFlakyFactory(0)
	m := NewManager()
	m.RegisterFactory(f)
	require.NoError(t, m.Start(context.Background(), []ChannelConfig{tgConfig}))
	defer m.Stop()

	require.NoError(t, m.RemoveAdapter("tg1"))
	_, registered := m.GetAdapter("tg1")
	require.False(t, registered)

	require.NoError(t, m.EnsureAdapter("tg1"))
	_, registered = m.GetAdapter("tg1")
	assert.True(t, registered)
	assert.Equal(t, 2, f.callCount())
}

// Removing a retry-pending channel takes it off the watchdog's list.
func TestManager_RemoveAdapter_DropsRetryPending(t *testing.T) {
	withRetryInterval(t, time.Hour)
	f := newFlakyFactory(1)
	m := NewManager()
	m.RegisterFactory(f)
	require.NoError(t, m.Start(context.Background(), []ChannelConfig{tgConfig}))
	defer m.Stop()
	require.Equal(t, 1, retryPendingCount(m))

	require.NoError(t, m.RemoveAdapter("tg1"))
	assert.Equal(t, 0, retryPendingCount(m))
	assert.NotContains(t, m.GetStatus(), "tg1")
}

func TestManager_CreateAdapter_RefusesDuplicate(t *testing.T) {
	f := newFlakyFactory(0)
	m := NewManager()
	m.RegisterFactory(f)
	require.NoError(t, m.Start(context.Background(), []ChannelConfig{tgConfig}))
	defer m.Stop()

	assert.ErrorContains(t, m.CreateAdapter(tgConfig), "already exists")
	assert.Equal(t, 1, f.callCount())
}

// The watchdog exits with the manager: Stop returns (m.wg.Wait) while
// channels are still pending, and no retry happens after it.
func TestManager_Stop_EndsRetryWatchdog(t *testing.T) {
	withRetryInterval(t, 5*time.Millisecond)
	f := newFlakyFactory(1 << 30) // never succeeds
	m := NewManager()
	m.RegisterFactory(f)
	require.NoError(t, m.Start(context.Background(), []ChannelConfig{tgConfig}))

	assert.Eventually(t, func() bool { return f.callCount() >= 3 }, 5*time.Second, 5*time.Millisecond)

	done := make(chan struct{})
	go func() { _ = m.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop hung waiting for the retry watchdog")
	}

	n := f.callCount()
	time.Sleep(30 * time.Millisecond)
	assert.Equal(t, n, f.callCount(), "watchdog kept retrying after Stop")
}
