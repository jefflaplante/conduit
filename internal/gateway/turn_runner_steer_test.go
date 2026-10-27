package gateway

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"conduit/internal/ai"
	"conduit/internal/approval"
	"conduit/internal/sessions"
	"conduit/internal/tools"
	"conduit/internal/tools/types"
)

// conduit-5uw3 mid-turn steering tests.

// gatedToolRegistry runs every tool call as a no-op that blocks until
// released, so a test can queue messages while a round's tools run.
type gatedToolRegistry struct {
	entered chan string
	gate    chan struct{}
}

func newGatedToolRegistry() *gatedToolRegistry {
	return &gatedToolRegistry{entered: make(chan string, 16), gate: make(chan struct{})}
}

func (g *gatedToolRegistry) ExecuteTool(ctx context.Context, name string, _ map[string]interface{}) (*tools.ToolResult, error) {
	g.entered <- name
	select {
	case <-g.gate:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &tools.ToolResult{Success: true, Content: "probe ok"}, nil
}

func (g *gatedToolRegistry) waitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-g.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("tool call never started")
	}
}

// newSteerTestGateway is newTurnTestGateway with a real tool loop: provider
// call 1 asks for one Probe tool call, every later call answers.
func newSteerTestGateway(t *testing.T) (*Gateway, *sessions.Store, *turnProvider, *gatedToolRegistry) {
	t.Helper()
	gw, store, _ := newTestGatewayWithRouter(t)
	reg := newGatedToolRegistry()
	engine := tools.NewExecutionEngine(reg, 1, 0, 10)
	router, err := ai.NewRouterWithExecution(gw.config.AI, nil, tools.NewExecutionEngineAdapter(engine))
	if err != nil {
		t.Fatalf("NewRouterWithExecution: %v", err)
	}
	router.SetSessionStore(store)
	p := newTurnProvider()
	p.toolCalls = func(n int) []ai.ToolCall {
		if n == 1 {
			return []ai.ToolCall{{ID: "c1", Name: "Probe", Args: map[string]interface{}{}}}
		}
		return nil
	}
	router.RegisterProvider("testprov", p)
	gw.ai = router
	gw.logger = newTestLogger()
	return gw, store, p, reg
}

// A message from the same user that arrives while the turn's tools run is
// folded into the next round: the model sees it before answering, it is
// stored before the reply, and it never gets a turn of its own.
func TestSteer_QueuedMessageFoldedIntoRunningTurn(t *testing.T) {
	gw, store, p, reg := newSteerTestGateway(t)
	sess, _ := store.GetOrCreateSession("42", "telegram")

	doneA := make(chan struct{})
	go func() { defer close(doneA); gw.handleIncomingMessage(context.Background(), tgMsg("deploy to staging")) }()
	waitEntered(t, p, 1)
	reg.waitEntered(t)

	doneB := make(chan struct{})
	go func() {
		defer close(doneB)
		gw.handleIncomingMessage(context.Background(), tgMsg("actually use prod-2"))
	}()
	waitForCond(t, "B offered", func() bool {
		gw.turns().mu.Lock()
		defer gw.turns().mu.Unlock()
		box := gw.turns().steer[sess.Key]
		return box != nil && len(box.pending) == 1
	})

	close(reg.gate)
	waitEntered(t, p, 2)
	<-doneA
	select {
	case <-doneB:
	case <-time.After(10 * time.Second):
		t.Fatal("steered turn B never finished")
	}

	if n := p.callCount(); n != 2 {
		t.Fatalf("provider calls = %d, want 2 (B must not run its own turn)", n)
	}
	round2 := p.request(2)
	last := round2[len(round2)-1]
	if last.Role != "user" || !strings.Contains(last.Content, "actually use prod-2") || !strings.HasPrefix(last.Content, steerPrefix) {
		t.Fatalf("round 2 must end with the folded message, got %+v", last)
	}
	if round2[len(round2)-2].Role != "tool" {
		t.Fatalf("folded message must follow the round's tool results: %+v", round2)
	}
	want := []string{"user:deploy to staging", "user:actually use prod-2", "assistant:reply 2"}
	if got := transcript(t, store, sess.Key); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("transcript = %v, want %v", got, want)
	}
	msgs, _ := store.GetMessages(sess.Key, 10)
	if msgs[1].Metadata["steered_into"] != msgs[0].ID {
		t.Fatalf("folded message metadata = %v, want steered_into=%s", msgs[1].Metadata, msgs[0].ID)
	}
	if gw.turns().Busy(sess.Key) {
		t.Fatal("session still busy after both turns ended")
	}
}

// A turn that never reaches another round boundary (no tool calls) leaves
// the offer untaken: the message runs as its own turn, after the reply.
func TestSteer_UntakenOfferRunsAsNormalTurn(t *testing.T) {
	gw, store, p, _ := newSteerTestGateway(t)
	p.toolCalls = nil
	sess, _ := store.GetOrCreateSession("42", "telegram")
	p.hold(1)

	doneA := make(chan struct{})
	go func() { defer close(doneA); gw.handleIncomingMessage(context.Background(), tgMsg("message A")) }()
	waitEntered(t, p, 1)
	doneB := make(chan struct{})
	go func() { defer close(doneB); gw.handleIncomingMessage(context.Background(), tgMsg("message B")) }()
	waitForCond(t, "B queued", func() bool { return gw.turns().queuedCount(sess.Key) == 1 })

	p.release(1)
	<-doneA
	waitEntered(t, p, 2)
	<-doneB

	want := []string{"user:message A", "assistant:reply 1", "user:message B", "assistant:reply 2"}
	if got := transcript(t, store, sess.Key); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("transcript = %v, want %v", got, want)
	}
}

// Once a non-steerable turn queues behind the running one, later messages
// must not overtake it: they are not offered and run in arrival order.
func TestSteer_NonSteerableQueuedTurnClosesBox(t *testing.T) {
	gw, store, p, reg := newSteerTestGateway(t)
	sess, _ := store.GetOrCreateSession("42", "telegram")

	doneA := make(chan struct{})
	go func() { defer close(doneA); gw.handleIncomingMessage(context.Background(), tgMsg("message A")) }()
	waitEntered(t, p, 1)
	reg.waitEntered(t)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		gw.turns().Run(context.Background(), TurnRequest{
			Session: sess, ChannelID: "telegram", UserID: "42", Text: "wake note",
			NonInteractiveSource: "test",
		}, discardTurnSink{})
	}()
	waitForCond(t, "wake turn queued", func() bool { return gw.turns().queuedCount(sess.Key) == 1 })
	go func() { defer wg.Done(); gw.handleIncomingMessage(context.Background(), tgMsg("message B")) }()
	waitForCond(t, "B queued", func() bool { return gw.turns().queuedCount(sess.Key) == 2 })

	gw.turns().mu.Lock()
	pending := len(gw.turns().steer[sess.Key].pending)
	gw.turns().mu.Unlock()
	if pending != 0 {
		t.Fatalf("B was offered past a queued non-steerable turn (pending=%d)", pending)
	}

	close(reg.gate)
	<-doneA
	wg.Wait()
	if n := p.callCount(); n != 4 {
		t.Fatalf("provider calls = %d, want 4 (A: 2 rounds, wake, B)", n)
	}
	for _, m := range p.request(2) {
		if strings.Contains(m.Content, "message B") {
			t.Fatal("B was folded into A")
		}
	}
}

// A message from another endpoint (another user, WS connection or TUI
// client) on the same session is never folded in: the running turn's reply
// would not reach it.
func TestSteer_OfferRequiresSameScope(t *testing.T) {
	r := &TurnRunner{steer: map[string]*steerBox{}}
	origin := &approval.Origin{}
	r.openSteerLocked("k", TurnRequest{SteerScope: "ws:c1", Origin: origin, Text: "a"})
	if e := r.offerSteerLocked("k", TurnRequest{SteerScope: "ws:c2", Origin: origin, Text: "b"}); e != nil {
		t.Fatal("message from another connection was offered")
	}
	if e := r.offerSteerLocked("k", TurnRequest{SteerScope: "ws:c1", Origin: origin, Text: "c"}); e != nil {
		t.Fatal("box must stay closed after a non-matching turn queued")
	}
}

// Photos, farewells, already-persisted (wake) and non-opted-in turns are
// never steered.
func TestSteer_CanSteer(t *testing.T) {
	origin := &approval.Origin{}
	plain := TurnRequest{SteerScope: "s", Origin: origin, Text: "hi"}
	r := &TurnRunner{}
	if !r.canSteer(plain) {
		t.Fatal("plain human message should be steerable")
	}
	cases := map[string]TurnRequest{
		"not opted in": {Origin: origin, Text: "hi"},
		"no origin":    {SteerScope: "s", Text: "hi"},
		"attachment":   {SteerScope: "s", Origin: origin, Text: "hi", Attachments: []ai.Attachment{{Type: "image"}}},
		"persisted":    {SteerScope: "s", Origin: origin, Text: "hi", PersistedUserMessageID: "m1"},
		"empty":        {SteerScope: "s", Origin: origin, Text: "  "},
	}
	for name, req := range cases {
		if r.canSteer(req) {
			t.Errorf("%s: should not be steerable", name)
		}
	}
	if (&TurnRunner{hooks: stubHooks{farewell: true}}).canSteer(plain) {
		t.Error("farewell should not be steerable")
	}
}

// The source only serves its own session, so a nested turn that inherited
// the ctx cannot drain the parent's messages.
func TestSteer_SourceBoundToSession(t *testing.T) {
	gw, _, _, _ := newSteerTestGateway(t)
	r := gw.turns()
	r.mu.Lock()
	r.steer["k"] = &steerBox{pending: []*steerEntry{{req: TurnRequest{Text: "x"}, wake: func() {}}}}
	r.mu.Unlock()
	src := r.steeringSource("k", "m0")
	if got := src(types.WithRequestContext(context.Background(), "telegram", "42", "other")); got != nil {
		t.Fatalf("other session drained %v", got)
	}
	r.mu.Lock()
	n := len(r.steer["k"].pending)
	r.mu.Unlock()
	if n != 1 {
		t.Fatal("offer lost by a foreign drain")
	}
}
