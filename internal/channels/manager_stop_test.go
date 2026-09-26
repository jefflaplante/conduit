package channels

import (
	"context"
	"sync"
	"testing"
	"time"

	"conduit/internal/protocol"
)

// streamAdapter is a minimal adapter that, like the real Telegram adapter
// after conduit-31jg.26, never closes its incoming channel.
type streamAdapter struct {
	id       string
	incoming chan *protocol.IncomingMessage
	mu       sync.Mutex
	starts   int
}

func (a *streamAdapter) ID() string   { return a.id }
func (a *streamAdapter) Name() string { return a.id }
func (a *streamAdapter) Type() string { return "stream" }
func (a *streamAdapter) Start(context.Context) error {
	a.mu.Lock()
	a.starts++
	a.mu.Unlock()
	return nil
}
func (a *streamAdapter) Stop() error                                 { return nil }
func (a *streamAdapter) SendMessage(*protocol.OutgoingMessage) error { return nil }
func (a *streamAdapter) ReceiveMessages() <-chan *protocol.IncomingMessage {
	return a.incoming
}
func (a *streamAdapter) Status() ChannelStatus {
	return ChannelStatus{Status: StatusOnline, Details: map[string]interface{}{}}
}
func (a *streamAdapter) IsHealthy() bool { return true }

type streamFactory struct{ a *streamAdapter }

func (f *streamFactory) SupportsType(t string) bool                          { return t == "stream" }
func (f *streamFactory) GetSupportedTypes() []string                         { return []string{"stream"} }
func (f *streamFactory) CreateAdapter(ChannelConfig) (ChannelAdapter, error) { return f.a, nil }

// conduit-31jg.26: Stop used to close m.incoming / m.outgoing while
// forwarders and SendMessage callers were still sending -> panic.
func TestManagerStop_WhileSendsInFlight_NoPanic(t *testing.T) {
	ad := &streamAdapter{id: "s1", incoming: make(chan *protocol.IncomingMessage, 8)}
	m := NewManager()
	m.RegisterFactory(&streamFactory{a: ad})
	if err := m.Start(context.Background(), []ChannelConfig{{ID: "s1", Type: "stream", Enabled: true}}); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	// Outgoing senders (gateway turns, tools).
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = m.SendMessage(&protocol.OutgoingMessage{ChannelID: "s1", Text: "x"})
			}
		}()
	}
	// Inbound producer (adapter poller) feeding the forwarder.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			case ad.incoming <- &protocol.IncomingMessage{ChannelID: "s1"}:
			default:
			}
		}
	}()
	// Consumer (gateway processMessages) must never see a nil message.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			case msg := <-m.ReceiveMessages():
				if msg == nil {
					t.Error("received nil message: incoming channel was closed")
					return
				}
			}
		}
	}()

	time.Sleep(20 * time.Millisecond)
	done := make(chan struct{})
	go func() {
		_ = m.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop hung")
	}
	time.Sleep(20 * time.Millisecond) // senders keep going after Stop
	close(stop)
	wg.Wait()

	if err := m.SendMessage(&protocol.OutgoingMessage{ChannelID: "s1"}); err == nil {
		t.Error("SendMessage after Stop should fail")
	}
	if err := m.Stop(); err != nil { // idempotent
		t.Fatalf("second Stop: %v", err)
	}
}

func TestManagerSendMessage_BeforeStart_NoPanic(t *testing.T) {
	m := NewManager()
	if err := m.SendMessage(&protocol.OutgoingMessage{}); err == nil {
		t.Fatal("expected error before Start")
	}
}

// RestartAdapter used to leave the adapter with a closed incoming channel,
// so the next inbound message panicked. Messages must still flow after it.
func TestManagerRestartAdapter_KeepsForwarding(t *testing.T) {
	ad := &streamAdapter{id: "s1", incoming: make(chan *protocol.IncomingMessage, 8)}
	m := NewManager()
	m.RegisterFactory(&streamFactory{a: ad})
	if err := m.Start(context.Background(), []ChannelConfig{{ID: "s1", Type: "stream", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()

	if err := m.RestartAdapter("s1"); err != nil {
		t.Fatalf("RestartAdapter: %v", err)
	}
	ad.incoming <- &protocol.IncomingMessage{ChannelID: "s1", Text: "after"}
	select {
	case msg := <-m.ReceiveMessages():
		if msg == nil || msg.Text != "after" {
			t.Fatalf("unexpected message %#v", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("message not forwarded after restart")
	}
	ad.mu.Lock()
	defer ad.mu.Unlock()
	if ad.starts != 2 {
		t.Errorf("starts = %d, want 2", ad.starts)
	}
}
