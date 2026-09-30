package gateway

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"conduit/internal/channels"
	"conduit/internal/config"
	"conduit/internal/protocol"
)

// ctxAdapter behaves like the Telegram adapter: sends run on the context
// passed to Start, so cancelling the gateway lifecycle context fails any
// send still queued or in flight with "context canceled".
type ctxAdapter struct {
	ctx   context.Context
	delay time.Duration
	mu    sync.Mutex
	sent  int
	lost  int
}

func (a *ctxAdapter) ID() string                      { return "tg" }
func (a *ctxAdapter) Name() string                    { return "tg" }
func (a *ctxAdapter) Type() string                    { return "ctx" }
func (a *ctxAdapter) Start(ctx context.Context) error { a.ctx = ctx; return nil }
func (a *ctxAdapter) Stop() error                     { return nil }
func (a *ctxAdapter) ReceiveMessages() <-chan *protocol.IncomingMessage {
	return make(chan *protocol.IncomingMessage)
}
func (a *ctxAdapter) Status() channels.ChannelStatus {
	return channels.ChannelStatus{Status: channels.StatusOnline}
}
func (a *ctxAdapter) IsHealthy() bool { return true }
func (a *ctxAdapter) SendMessage(*protocol.OutgoingMessage) error {
	select {
	case <-time.After(a.delay):
		a.mu.Lock()
		a.sent++
		a.mu.Unlock()
		return nil
	case <-a.ctx.Done():
		a.mu.Lock()
		a.lost++
		a.mu.Unlock()
		return a.ctx.Err()
	}
}

type ctxFactory struct{ a *ctxAdapter }

func (f *ctxFactory) SupportsType(t string) bool  { return t == "ctx" }
func (f *ctxFactory) GetSupportedTypes() []string { return []string{"ctx"} }
func (f *ctxFactory) CreateAdapter(channels.ChannelConfig) (channels.ChannelAdapter, error) {
	return f.a, nil
}

// conduit-25o4: a heartbeat alert queued as the drain finished was lost on a
// SIGHUP deploy — the lifecycle cancel stopped the Telegram adapter before
// the channel manager's router had sent it.
func TestShutdown_FlushesOutgoingBeforeCancel(t *testing.T) {
	lifecycle, cancel := context.WithCancel(context.Background())
	defer cancel()

	ad := &ctxAdapter{delay: 50 * time.Millisecond}
	cm := channels.NewManager()
	cm.RegisterFactory(&ctxFactory{a: ad})
	if err := cm.Start(lifecycle, []channels.ChannelConfig{{ID: "tg", Type: "ctx", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	defer cm.Stop()

	gw := newTestGatewayForShutdown(t, &config.Config{DataDir: t.TempDir()})
	gw.channelManager = cm
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	sm := NewShutdownManager(logger, gw)
	cancelled := make(chan struct{})
	sm.SetCancel(func() { cancel(); close(cancelled) })

	// The last drained job's alert: queued, not yet sent.
	for i := 0; i < 3; i++ {
		if err := cm.SendMessage(&protocol.OutgoingMessage{ChannelID: "tg", Text: "alert"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := sm.BeginShutdown("test", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("lifecycle context was never cancelled")
	}

	ad.mu.Lock()
	defer ad.mu.Unlock()
	if ad.sent != 3 || ad.lost != 0 {
		t.Fatalf("sent=%d lost=%d: queued messages must be delivered before the lifecycle cancel", ad.sent, ad.lost)
	}
}

// The flush is bounded even if an adapter never returns.
func TestShutdown_OutgoingFlushBounded(t *testing.T) {
	oldMin, oldMax := minOutgoingFlush, maxOutgoingFlush
	minOutgoingFlush, maxOutgoingFlush = 20*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { minOutgoingFlush, maxOutgoingFlush = oldMin, oldMax })

	lifecycle, cancel := context.WithCancel(context.Background())
	defer cancel()
	ad := &ctxAdapter{delay: time.Hour}
	cm := channels.NewManager()
	cm.RegisterFactory(&ctxFactory{a: ad})
	if err := cm.Start(lifecycle, []channels.ChannelConfig{{ID: "tg", Type: "ctx", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	defer cm.Stop()
	if err := cm.SendMessage(&protocol.OutgoingMessage{ChannelID: "tg", Text: "stuck"}); err != nil {
		t.Fatal(err)
	}

	gw := newTestGatewayForShutdown(t, &config.Config{DataDir: t.TempDir()})
	gw.channelManager = cm
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	sm := NewShutdownManager(logger, gw)
	cancelled := make(chan struct{})
	sm.SetCancel(func() { cancel(); close(cancelled) })

	start := time.Now()
	if err := sm.BeginShutdown("test", 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("a wedged adapter held shutdown past the flush bound")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("shutdown took %v", d)
	}
}
