package gateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"conduit/internal/ai"
	"conduit/internal/monitoring"
	"conduit/internal/sessions"
)

// conduit-3kgo: the TurnRunner drives the session processing state.

func sessionState(t *testing.T, store *sessions.Store, key string) sessions.SessionState {
	t.Helper()
	st, _ := store.GetSessionState(key)
	return st
}

func TestTurnRunner_StateProcessingThenIdle(t *testing.T) {
	gw, store, p := newTurnTestGateway(t)
	sess, _ := store.GetOrCreateSession("42", "telegram")
	p.hold(1)

	collector := monitoring.NewMetricsCollector(monitoring.CollectorDependencies{
		SessionStore:   store,
		GatewayMetrics: monitoring.NewGatewayMetrics(),
	})

	done := make(chan struct{})
	go func() { defer close(done); gw.handleIncomingMessage(context.Background(), tgMsg("hello")) }()
	waitEntered(t, p, 1)

	if st := sessionState(t, store, sess.Key); st != sessions.SessionStateProcessing {
		t.Fatalf("state during turn = %q, want processing", st)
	}
	if n := store.GetSessionStateMetrics().ProcessingSessions; n != 1 {
		t.Fatalf("processing count during turn = %d, want 1", n)
	}
	stats, err := collector.GetDetailedStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := stats["sessions"].(map[string]int)["processing"]; got != 1 {
		t.Fatalf("collector processing sessions = %d, want 1", got)
	}
	// A turn running longer than the threshold is reported as stuck.
	time.Sleep(5 * time.Millisecond)
	stuck, err := collector.DetectStuckSessions(context.Background(), time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(stuck) != 1 || stuck[0] != sess.Key {
		t.Fatalf("stuck sessions = %v, want [%s]", stuck, sess.Key)
	}

	p.release(1)
	<-done
	if st := sessionState(t, store, sess.Key); st != sessions.SessionStateIdle {
		t.Fatalf("state after turn = %q, want idle", st)
	}
	if n := store.GetSessionStateMetrics().ProcessingSessions; n != 0 {
		t.Fatalf("processing count after turn = %d, want 0", n)
	}
}

func TestTurnRunner_StateErrorOnFailure(t *testing.T) {
	gw, store, p := newTurnTestGateway(t)
	sess, _ := store.GetOrCreateSession("42", "telegram")
	p.hold(1)

	done := make(chan struct{})
	go func() { defer close(done); gw.handleIncomingMessage(context.Background(), tgMsg("hello")) }()
	waitEntered(t, p, 1)
	p.mu.Lock()
	gate := p.gates[1]
	p.mu.Unlock()
	gate <- errors.New("provider exploded")
	<-done

	info, ok := store.GetSessionStateInfo(sess.Key)
	if !ok || info.State != sessions.SessionStateError {
		t.Fatalf("state after failed turn = %+v, want error", info)
	}
	if info.ErrorCount != 1 {
		t.Fatalf("error count = %d, want 1", info.ErrorCount)
	}
}

func TestTurnRunner_StateIdleOnStop(t *testing.T) {
	gw, store, p := newTurnTestGateway(t)
	sess, _ := store.GetOrCreateSession("42", "telegram")
	p.hold(1)

	done := make(chan struct{})
	go func() { defer close(done); gw.handleIncomingMessage(context.Background(), tgMsg("hello")) }()
	waitEntered(t, p, 1)
	gw.handleIncomingMessage(context.Background(), tgMsg("/stop"))
	<-done

	if st := sessionState(t, store, sess.Key); st != sessions.SessionStateIdle {
		t.Fatalf("state after /stop = %q, want idle (cancellation is not an error)", st)
	}
}

// panicSink panics when the turn begins.
type panicSink struct{ recordingSink }

func (s *panicSink) Begin(context.Context) ai.StreamCallback { panic("sink blew up") }

func TestTurnRunner_StateNotStuckAfterPanic(t *testing.T) {
	gw, store, _ := newTurnTestGateway(t)
	sess, _ := store.GetOrCreateSession("42", "telegram")

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected the panic to propagate")
			}
		}()
		gw.turns().Run(context.Background(), TurnRequest{Session: sess, ChannelID: "telegram", UserID: "42", Text: "hi"}, &panicSink{})
	}()

	if st := sessionState(t, store, sess.Key); st != sessions.SessionStateError {
		t.Fatalf("state after panic = %q, want error", st)
	}
	if n := store.GetSessionStateMetrics().ProcessingSessions; n != 0 {
		t.Fatalf("processing count after panic = %d, want 0", n)
	}
	if gw.turns().Busy(sess.Key) {
		t.Fatal("session still busy after panic")
	}
}

func TestTurnRunner_StateKeepsCanceled(t *testing.T) {
	gw, store, _ := newTurnTestGateway(t)
	sess, _ := store.GetOrCreateSession("42", "telegram")
	if err := store.UpdateSessionState(sess.Key, sessions.SessionStateCanceled, nil); err != nil {
		t.Fatal(err)
	}
	gw.turns().Run(context.Background(), TurnRequest{Session: sess, ChannelID: "telegram", UserID: "42", Text: "hi"}, &recordingSink{})
	if st := sessionState(t, store, sess.Key); st != sessions.SessionStateCanceled {
		t.Fatalf("state = %q, want canceled to stay terminal", st)
	}
}
