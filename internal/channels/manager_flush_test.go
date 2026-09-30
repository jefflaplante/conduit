package channels

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"conduit/internal/protocol"
)

// recordingAdapter delivers slowly and, like the Telegram adapter, fails
// sends once stopped (its request context is cancelled).
type recordingAdapter struct {
	streamAdapter
	delay   time.Duration
	block   chan struct{} // non-nil: SendMessage waits on it or Stop
	mu      sync.Mutex
	sent    []string
	stopped chan struct{}
	once    sync.Once
}

func newRecordingAdapter(id string, delay time.Duration) *recordingAdapter {
	return &recordingAdapter{
		streamAdapter: streamAdapter{id: id, incoming: make(chan *protocol.IncomingMessage)},
		delay:         delay,
		stopped:       make(chan struct{}),
	}
}

func (a *recordingAdapter) Stop() error {
	a.once.Do(func() { close(a.stopped) })
	return nil
}

func (a *recordingAdapter) SendMessage(msg *protocol.OutgoingMessage) error {
	wait := time.After(a.delay)
	if a.block != nil {
		wait = nil
	}
	select {
	case <-wait:
	case <-a.block:
	case <-a.stopped:
		return errors.New("context canceled")
	}
	a.mu.Lock()
	a.sent = append(a.sent, msg.Text)
	a.mu.Unlock()
	return nil
}

func (a *recordingAdapter) sentCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.sent)
}

type recordingFactory struct{ a *recordingAdapter }

func (f *recordingFactory) SupportsType(t string) bool                          { return t == "rec" }
func (f *recordingFactory) GetSupportedTypes() []string                         { return []string{"rec"} }
func (f *recordingFactory) CreateAdapter(ChannelConfig) (ChannelAdapter, error) { return f.a, nil }

func startRecording(t *testing.T, ad *recordingAdapter) *Manager {
	t.Helper()
	m := NewManager()
	m.RegisterFactory(&recordingFactory{a: ad})
	if err := m.Start(context.Background(), []ChannelConfig{{ID: "r1", Type: "rec", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	return m
}

// conduit-25o4: messages accepted by SendMessage before Stop must reach the
// adapter before it is stopped (a heartbeat alert queued during the deploy
// drain was lost with "context canceled").
func TestManagerStop_FlushesQueuedMessages(t *testing.T) {
	ad := newRecordingAdapter("r1", 10*time.Millisecond)
	m := startRecording(t, ad)

	const n = 5
	for i := 0; i < n; i++ {
		if err := m.SendMessage(&protocol.OutgoingMessage{ChannelID: "r1", Text: "alert"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	if got := ad.sentCount(); got != n {
		t.Fatalf("delivered %d of %d queued messages before shutdown", got, n)
	}
	if err := m.SendMessage(&protocol.OutgoingMessage{ChannelID: "r1", Text: "late"}); err == nil {
		t.Fatal("SendMessage after Stop must fail, not queue a message nobody will send")
	}
}

// The flush is bounded: a wedged adapter cannot hold shutdown past
// flushTimeout.
func TestManagerStop_FlushBounded(t *testing.T) {
	old := flushTimeout
	flushTimeout = 50 * time.Millisecond
	t.Cleanup(func() { flushTimeout = old })

	ad := newRecordingAdapter("r1", 0)
	ad.block = make(chan struct{}) // never released: only Stop unblocks
	m := startRecording(t, ad)
	for i := 0; i < 3; i++ {
		if err := m.SendMessage(&protocol.OutgoingMessage{ChannelID: "r1", Text: "stuck"}); err != nil {
			t.Fatal(err)
		}
	}

	done := make(chan struct{})
	start := time.Now()
	go func() { _ = m.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop hung on a wedged adapter")
	}
	if d := time.Since(start); d < flushTimeout {
		t.Fatalf("Stop returned after %v, before giving the flush %v", d, flushTimeout)
	}
}
