package telegram

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"conduit/internal/protocol"
)

// conduit-31jg.26: Stop used to close(a.incoming) while the poller could
// still be inside handleUpdate / photo / voice sending on it, which panics,
// and a second Stop panicked on the double close.
func TestAdapterStop_ConcurrentWithInboundUpdates_NoPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a := &Adapter{
		id:          "tg",
		name:        "tg",
		bot:         &mockBot{},
		ctx:         ctx,
		cancel:      cancel,
		incoming:    make(chan *protocol.IncomingMessage, 4), // small: exercise the drop path too
		seenUpdates: make(map[int64]time.Time),
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			<-start
			for i := 0; i < 200; i++ {
				a.handleUpdate(ctx, nil, &models.Update{
					ID: int64(g*1000 + i),
					Message: &models.Message{
						ID:   i,
						Text: "hi",
						Chat: models.Chat{ID: 42},
						From: &models.User{ID: 42},
					},
				})
			}
		}(g)
	}
	// Drain a little so senders keep flowing past the buffer.
	go func() {
		for {
			select {
			case <-a.incoming:
			case <-time.After(200 * time.Millisecond):
				return
			}
		}
	}()

	close(start)
	time.Sleep(time.Millisecond)
	if err := a.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	wg.Wait()

	// Idempotent.
	if err := a.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

// Stop must wait for the poller goroutine (bot.Start returns only after the
// last handler call), but not forever.
func TestAdapterStop_WaitsForPoller(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	a := &Adapter{name: "tg", ctx: ctx, cancel: cancel, runDone: runDone,
		incoming: make(chan *protocol.IncomingMessage, 1)}

	exited := make(chan struct{})
	go func() {
		<-ctx.Done()
		time.Sleep(50 * time.Millisecond) // simulate an in-flight handler
		close(runDone)
		close(exited)
	}()

	if err := a.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case <-exited:
	default:
		t.Fatal("Stop returned before the poller goroutine exited")
	}

	// Bounded: a wedged poller must not hang Stop.
	old := stopWaitTimeout
	stopWaitTimeout = 50 * time.Millisecond
	defer func() { stopWaitTimeout = old }()
	_, cancel2 := context.WithCancel(context.Background())
	b := &Adapter{name: "tg", cancel: cancel2, runDone: make(chan struct{})}
	done := make(chan struct{})
	go func() { _ = b.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop hung on a wedged poller")
	}
}
