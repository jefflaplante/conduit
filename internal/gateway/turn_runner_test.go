package gateway

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"conduit/internal/ai"
	"conduit/internal/protocol"
	"conduit/internal/sessions"
)

// conduit-31jg.35 / .22 / .23 / .49 TurnRunner tests.

// turnProvider records the messages of every request and can hold a call
// open until released (or its context is cancelled).
type turnProvider struct {
	mu       sync.Mutex
	requests [][]ai.ChatMessage
	calls    int
	entered  chan int           // receives the 1-based call number on entry
	gates    map[int]chan error // call number → gate; nil entry = no gate
	usage    ai.Usage
}

func newTurnProvider() *turnProvider {
	return &turnProvider{entered: make(chan int, 16), gates: map[int]chan error{}}
}

// hold makes call n block until release(n) (or ctx cancel).
func (p *turnProvider) hold(n int) {
	p.mu.Lock()
	p.gates[n] = make(chan error, 1)
	p.mu.Unlock()
}

func (p *turnProvider) release(n int) {
	p.mu.Lock()
	g := p.gates[n]
	p.mu.Unlock()
	g <- nil
}

func (p *turnProvider) Name() string { return "turnprov" }

func (p *turnProvider) GenerateResponse(ctx context.Context, req *ai.GenerateRequest) (*ai.GenerateResponse, error) {
	p.mu.Lock()
	p.calls++
	n := p.calls
	msgs := append([]ai.ChatMessage(nil), req.Messages...)
	p.requests = append(p.requests, msgs)
	gate := p.gates[n]
	usage := p.usage
	p.mu.Unlock()
	p.entered <- n
	if gate != nil {
		select {
		case err := <-gate:
			if err != nil {
				return nil, err
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &ai.GenerateResponse{Content: fmt.Sprintf("reply %d", n), FinishReason: "stop", Usage: usage}, nil
}

func (p *turnProvider) request(n int) []ai.ChatMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requests[n-1]
}

func (p *turnProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func waitEntered(t *testing.T, p *turnProvider, want int) {
	t.Helper()
	select {
	case n := <-p.entered:
		if n != want {
			t.Fatalf("provider call %d entered, want %d", n, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("provider call %d never started", want)
	}
}

func waitForCond(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (r *TurnRunner) queuedCount(key string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.queued[key])
}

func newTurnTestGateway(t *testing.T) (*Gateway, *sessions.Store, *turnProvider) {
	t.Helper()
	gw, store, router := newTestGatewayWithRouter(t)
	gw.logger = newTestLogger()
	router.SetSessionStore(store) // history comes from the transcript
	p := newTurnProvider()
	router.RegisterProvider("testprov", p) // default provider
	return gw, store, p
}

func tgMsg(text string) *protocol.IncomingMessage {
	return &protocol.IncomingMessage{ChannelID: "telegram", UserID: "42", Text: text}
}

func transcript(t *testing.T, store *sessions.Store, key string) []string {
	t.Helper()
	msgs, err := store.GetMessages(key, 100)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Role+":"+m.Content)
	}
	return out
}

func countContaining(msgs []ai.ChatMessage, role, sub string) int {
	n := 0
	for _, m := range msgs {
		if m.Role == role && strings.Contains(m.Content, sub) {
			n++
		}
	}
	return n
}

// conduit-31jg.22: message B arriving while A runs must see A's reply, and
// the transcript must read uA,aA,uB,aB.
func TestTurnRunner_QueuedTurnSeesPreviousReply(t *testing.T) {
	gw, store, p := newTurnTestGateway(t)
	sess, _ := store.GetOrCreateSession("42", "telegram")
	p.hold(1)

	doneA := make(chan struct{})
	go func() { defer close(doneA); gw.handleIncomingMessage(context.Background(), tgMsg("message A")) }()
	waitEntered(t, p, 1)

	doneB := make(chan struct{})
	go func() { defer close(doneB); gw.handleIncomingMessage(context.Background(), tgMsg("message B")) }()
	waitForCond(t, "B queued", func() bool { return gw.turns().queuedCount(sess.Key) == 1 })

	// B is queued: its user row must NOT be in the transcript yet.
	if got := transcript(t, store, sess.Key); len(got) != 1 || got[0] != "user:message A" {
		t.Fatalf("transcript while B queued = %v, want only uA", got)
	}

	p.release(1)
	<-doneA
	waitEntered(t, p, 2)
	<-doneB

	reqB := p.request(2)
	if countContaining(reqB, "assistant", "reply 1") != 1 {
		t.Fatalf("B's history lacks A's reply: %+v", reqB)
	}
	if countContaining(reqB, "user", "message B") != 1 {
		t.Fatalf("B's message should appear exactly once: %+v", reqB)
	}
	want := []string{"user:message A", "assistant:reply 1", "user:message B", "assistant:reply 2"}
	if got := transcript(t, store, sess.Key); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("transcript = %v, want %v", got, want)
	}
}

// conduit-31jg.23: /stop while B is queued cancels the RUNNING turn A and
// drops B (never run, never persisted).
func TestTurnRunner_StopCancelsRunningAndDropsQueued(t *testing.T) {
	gw, store, p := newTurnTestGateway(t)
	sess, _ := store.GetOrCreateSession("42", "telegram")
	p.hold(1)

	doneA := make(chan struct{})
	go func() { defer close(doneA); gw.handleIncomingMessage(context.Background(), tgMsg("message A")) }()
	waitEntered(t, p, 1)
	doneB := make(chan struct{})
	go func() { defer close(doneB); gw.handleIncomingMessage(context.Background(), tgMsg("message B")) }()
	waitForCond(t, "B queued", func() bool { return gw.turns().queuedCount(sess.Key) == 1 })

	gw.handleIncomingMessage(context.Background(), tgMsg("/stop"))

	for name, ch := range map[string]chan struct{}{"A": doneA, "B": doneB} {
		select {
		case <-ch:
		case <-time.After(10 * time.Second):
			t.Fatalf("turn %s not stopped", name)
		}
	}
	if n := p.callCount(); n != 1 {
		t.Fatalf("queued turn B reached the provider (%d calls)", n)
	}
	if got := transcript(t, store, sess.Key); len(got) != 1 || got[0] != "user:message A" {
		t.Fatalf("transcript = %v, want only uA (A cancelled, B dropped)", got)
	}
	if gw.turns().Busy(sess.Key) {
		t.Fatal("session still busy after /stop")
	}
}

// conduit-31jg.23 (the reported bug): after A finishes, the queued turn B
// must be the one /stop cancels. Previously B's cancel func had been
// overwritten into ActiveRequests before it ran and A's cleanup deleted it,
// leaving B unstoppable.
func TestTurnRunner_StopReachesPreviouslyQueuedTurn(t *testing.T) {
	gw, store, p := newTurnTestGateway(t)
	sess, _ := store.GetOrCreateSession("42", "telegram")
	p.hold(1)
	p.hold(2)

	doneA := make(chan struct{})
	go func() { defer close(doneA); gw.handleIncomingMessage(context.Background(), tgMsg("message A")) }()
	waitEntered(t, p, 1)
	doneB := make(chan struct{})
	go func() { defer close(doneB); gw.handleIncomingMessage(context.Background(), tgMsg("message B")) }()
	waitForCond(t, "B queued", func() bool { return gw.turns().queuedCount(sess.Key) == 1 })

	p.release(1)
	<-doneA
	waitEntered(t, p, 2)

	gw.handleIncomingMessage(context.Background(), tgMsg("/stop"))
	select {
	case <-doneB:
	case <-time.After(10 * time.Second):
		t.Fatal("/stop did not cancel the running turn B")
	}
	want := []string{"user:message A", "assistant:reply 1", "user:message B"}
	if got := transcript(t, store, sess.Key); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("transcript = %v, want %v", got, want)
	}
}

// Stop with nothing in flight is a no-op; the direct Stop API reports counts.
func TestTurnRunner_StopCounts(t *testing.T) {
	gw, _, _ := newTurnTestGateway(t)
	if running, dropped := gw.turns().Stop("nope"); running || dropped != 0 {
		t.Fatalf("Stop on idle session = %v,%d", running, dropped)
	}
	if s, ok := stopResponse(true, 2); !ok || !strings.Contains(s, "2 queued") {
		t.Fatalf("stopResponse = %q", s)
	}
}

// fakeCompactor records auto-compaction triggers.
type fakeCompactor struct {
	threshold int
	compacted chan string
}

func (f *fakeCompactor) ShouldCompact(promptTokens int, _ string) bool {
	return promptTokens >= f.threshold
}

func (f *fakeCompactor) Compact(_ context.Context, s *sessions.Session) (*ai.CompactionResult, error) {
	f.compacted <- s.Key
	return nil, nil
}

// conduit-31jg.49: a Telegram-path turn over the threshold auto-compacts
// (previously only ws_chat.go triggered it).
func TestTurnRunner_TelegramTurnTriggersCompaction(t *testing.T) {
	gw, store, p := newTurnTestGateway(t)
	sess, _ := store.GetOrCreateSession("42", "telegram")
	fc := &fakeCompactor{threshold: 1000, compacted: make(chan string, 1)}
	gw.turns().compactor = fc

	p.usage = ai.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}
	gw.handleIncomingMessage(context.Background(), tgMsg("small"))
	select {
	case k := <-fc.compacted:
		t.Fatalf("compacted %s below threshold", k)
	case <-time.After(100 * time.Millisecond):
	}

	p.usage = ai.Usage{PromptTokens: 5000, CompletionTokens: 5, TotalTokens: 5005}
	gw.handleIncomingMessage(context.Background(), tgMsg("big"))
	select {
	case k := <-fc.compacted:
		if k != sess.Key {
			t.Fatalf("compacted %s, want %s", k, sess.Key)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Telegram turn over threshold did not trigger compaction")
	}
}

// conduit-31jg.22: the stored text of a photo message ("[Photo] …") differs
// from the text sent to the model; the user message must still be sent once.
func TestTurnRunner_PhotoMessageNotSentTwice(t *testing.T) {
	gw, store, p := newTurnTestGateway(t)
	sess, _ := store.GetOrCreateSession("42", "telegram")

	msg := tgMsg("what is this bird")
	msg.Attachments = []protocol.Attachment{{Type: "image", MediaType: "image/jpeg", Data: []byte{1, 2, 3}}}
	gw.handleIncomingMessage(context.Background(), msg)

	req := p.request(1)
	if n := countContaining(req, "user", "what is this bird"); n != 1 {
		t.Fatalf("user message sent %d times: %+v", n, req)
	}
	last := req[len(req)-1]
	if last.Role != "user" || len(last.Attachments) != 1 {
		t.Fatalf("last message should be the user turn with the photo: %+v", last)
	}
	if got := transcript(t, store, sess.Key); got[0] != "user:[Photo] what is this bird" {
		t.Fatalf("stored user row = %q", got[0])
	}
}

// conduit-31jg.22: a "[System: …]" reflection suffix on the model-facing text
// must not duplicate the stored user message.
func TestTurnRunner_ReflectionSuffixNotSentTwice(t *testing.T) {
	gw, store, p := newTurnTestGateway(t)
	sess, _ := store.GetOrCreateSession("42", "telegram")
	gw.turns().hooks = stubHooks{farewell: true, prompt: "reflect now"}

	gw.handleIncomingMessage(context.Background(), tgMsg("goodnight"))

	req := p.request(1)
	if n := countContaining(req, "user", "goodnight"); n != 1 {
		t.Fatalf("user message sent %d times: %+v", n, req)
	}
	if !strings.Contains(req[len(req)-1].Content, "[System: reflect now]") {
		t.Fatalf("reflection prompt not injected: %+v", req[len(req)-1])
	}
	if got := transcript(t, store, sess.Key); got[0] != "user:goodnight" {
		t.Fatalf("stored user row = %q (suffix must not be persisted)", got[0])
	}
}

type stubHooks struct {
	farewell bool
	prompt   string
}

func (s stubHooks) IsFarewell(string) bool                             { return s.farewell }
func (s stubHooks) ReflectionPrompt() string                           { return s.prompt }
func (s stubHooks) ReflectionEnabled() bool                            { return true }
func (s stubHooks) AfterReflection(context.Context, *sessions.Session) {}

// conduit-31jg.22: a woken session's inter-session message is already in the
// transcript and may not be the last row; it must be sent exactly once.
func TestTurnRunner_WakeMessageNotSentTwice(t *testing.T) {
	gw, store, p := newTurnTestGateway(t)
	gw.ctx = context.Background()
	sess, _ := store.GetOrCreateSession("42", "telegram")
	if _, err := store.AddMessage(sess.Key, "user", "ping from sibling", map[string]string{"source": "inter_session"}); err != nil {
		t.Fatal(err)
	}
	gw.wakeSession(sess.Key)

	req := p.request(1)
	if n := countContaining(req, "user", "ping from sibling"); n != 1 {
		t.Fatalf("wake message sent %d times: %+v", n, req)
	}
	want := []string{"user:ping from sibling", "assistant:reply 1"}
	if got := transcript(t, store, sess.Key); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("transcript = %v, want %v", got, want)
	}
}

// A queued WebSocket/TUI turn and a channel turn share one lock and one
// registry: a DirectClient built with the gateway's runner is stoppable via
// the gateway's ActiveRequests (shutdown drain) and vice versa.
func TestTurnRunner_DirectClientUsesSharedRunner(t *testing.T) {
	gw, store, p := newTurnTestGateway(t)
	c := NewDirectClient(DirectClientConfig{
		ParentCtx: context.Background(), UserID: "jeff", Sessions: store, AI: gw.ai, Turns: gw.turns(),
	})
	defer c.Close()
	sess, _ := store.GetOrCreateSession("jeff", "tui_jeff")
	p.hold(1)
	if err := c.SendChatWithID(sess.Key, "hello", "r1"); err != nil {
		t.Fatal(err)
	}
	waitEntered(t, p, 1)
	gw.ws.ActiveRequestsMu.RLock()
	_, registered := gw.ws.ActiveRequests[sess.Key]
	gw.ws.ActiveRequestsMu.RUnlock()
	if !registered {
		t.Fatal("TUI turn not registered in the gateway's ActiveRequests")
	}
	c.handleCommand(sess.Key, "/stop")
	waitForCond(t, "TUI turn stopped", func() bool { return !gw.turns().Busy(sess.Key) })
}
