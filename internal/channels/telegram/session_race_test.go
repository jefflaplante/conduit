package telegram

import (
	"context"
	"testing"
)

// conduit-31jg.73: senders read a.bot/a.ctx through session(); a concurrent
// Start (Manager.RestartAdapter) rewriting them under a.mutex must not race
// (run with -race).
func TestSenders_NoRaceWithRestart(t *testing.T) {
	a := newTestAdapter(&mockBot{})
	defer a.cancel()

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { // simulates Start's field writes during a restart
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			ctx, cancel := context.WithCancel(context.Background())
			a.mutex.Lock()
			a.ctx, a.cancel = ctx, cancel
			a.bot = &mockBot{}
			a.mutex.Unlock()
			cancel()
		}
	}()
	for i := 0; i < 200; i++ {
		if err := a.SendTypingIndicator("42"); err != nil {
			t.Fatalf("SendTypingIndicator: %v", err)
		}
		if err := a.DeleteMessage(42, i); err != nil {
			t.Fatalf("DeleteMessage: %v", err)
		}
	}
	close(stop)
	<-done
}

func TestSession_BeforeStart(t *testing.T) {
	a := &Adapter{}
	b, ctx := a.session()
	if b != nil || ctx == nil {
		t.Fatalf("session() before Start = (%v, %v); want (nil, non-nil ctx)", b, ctx)
	}
	if err := a.SendTypingIndicator("1"); err == nil {
		t.Fatal("expected error before Start")
	}
}
