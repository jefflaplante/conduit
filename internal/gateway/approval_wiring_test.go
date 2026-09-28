package gateway

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"conduit/internal/ai"
	"conduit/internal/approval"
	"conduit/internal/protocol"
	"conduit/internal/scheduler"
	"conduit/internal/sessions"
)

// conduit-31jg.43 gateway wiring tests.

// gateProvider is an ai.Provider that records the approval origin of each
// turn and can hold the turn (and therefore the per-session turn lock) open.
type gateProvider struct {
	mu          sync.Mutex
	interactive []bool
	sources     []string
	entered     chan struct{}
	release     chan struct{} // nil => return immediately
}

func (p *gateProvider) Name() string { return "gate" }

func (p *gateProvider) GenerateResponse(ctx context.Context, req *ai.GenerateRequest) (*ai.GenerateResponse, error) {
	o, ok := approval.OriginFrom(ctx)
	// A turn can be prompted when it carries a live interactive origin with
	// a notifier and session/user identity (the checks Manager.Request makes).
	promptable := ok && o.Interactive && o.Notify != nil && o.SessionKey != "" && o.UserID != ""
	p.mu.Lock()
	p.interactive = append(p.interactive, promptable)
	p.sources = append(p.sources, o.Source)
	p.mu.Unlock()
	if p.entered != nil {
		select {
		case p.entered <- struct{}{}:
		default:
		}
	}
	if p.release != nil {
		select {
		case <-p.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &ai.GenerateResponse{Content: "ok", FinishReason: "stop"}, nil
}

func (p *gateProvider) seen() ([]bool, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]bool(nil), p.interactive...), append([]string(nil), p.sources...)
}

type recNotifier struct {
	mu      sync.Mutex
	notices []approval.Notice
}

func (r *recNotifier) notify(_ context.Context, n approval.Notice) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notices = append(r.notices, n)
	return nil
}

func withApprovals(t *testing.T, gw *Gateway) {
	t.Helper()
	gw.approvals = approval.NewManager(approval.Config{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		OnResolved: gw.recordApprovalOutcome,
	})
	t.Cleanup(gw.approvals.Close)
}

// pendingOwnerSend registers an approval as the skill gate would during an
// interactive Telegram turn and returns its ticket plus a channel closed
// when the approved action runs.
func pendingOwnerSend(t *testing.T, gw *Gateway, sess *sessions.Session, userID string) (*approval.Ticket, chan struct{}) {
	t.Helper()
	rec := &recNotifier{}
	ctx := approval.WithInteractiveOrigin(context.Background(), approval.Origin{
		Source: "telegram", ChannelID: sess.ChannelID, UserID: userID, SessionKey: sess.Key, Notify: rec.notify,
	})
	ran := make(chan struct{})
	var once sync.Once
	tk, err := gw.approvals.Request(ctx, approval.Action{
		Kind: "email.send_as_owner", Title: "send as owner to bob",
		Fingerprint: approval.Fingerprint("email.send_as_owner", map[string]string{"to": "bob"}),
	}, func(ctx context.Context, _ approval.Ticket) (string, error) {
		once.Do(func() { close(ran) })
		return "sent", nil
	})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	return tk, ran
}

// The deadlock scenario: a turn holds the per-session router lock while the
// human's YES arrives on the same session. The reply must be consumed on the
// inbound path without touching the turn lock, and the approved action must
// run while the turn is still blocked. A normal message on the same session
// stays queued behind the lock the whole time (proving the lock was held).
func TestApprovalReply_NotBlockedByInFlightTurnLock(t *testing.T) {
	gw, store, router := newTestGatewayWithRouter(t)
	withApprovals(t, gw)

	holder := &gateProvider{entered: make(chan struct{}, 1), release: make(chan struct{})}
	router.RegisterProvider("holder", holder)
	probe := &gateProvider{entered: make(chan struct{}, 1)}
	router.RegisterProvider("probe", probe)

	sess, err := store.GetOrCreateSession("42", "telegram")
	if err != nil {
		t.Fatal(err)
	}

	// 1) In-flight turn holding the session turn lock.
	turnDone := make(chan struct{})
	go func() {
		defer close(turnDone)
		_, _ = router.GenerateResponseWithTools(context.Background(), sess, "send that mail as me", "holder", "")
	}()
	select {
	case <-holder.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("turn never started")
	}
	_, cancelTurn := context.WithCancel(context.Background())
	defer cancelTurn()
	gw.ws.ActiveRequestsMu.Lock()
	gw.ws.ActiveRequests[sess.Key] = cancelTurn
	gw.ws.ActiveRequestsMu.Unlock()

	// 2) Pending approval created by that turn's tool call.
	tk, ran := pendingOwnerSend(t, gw, sess, "42")

	// 3) A normal message queued behind the turn lock.
	queuedDone := make(chan struct{})
	go func() {
		defer close(queuedDone)
		_, _ = router.GenerateResponseWithTools(context.Background(), sess, "another message", "probe", "")
	}()

	// 4) Human replies YES on the same session while the turn is blocked.
	replyDone := make(chan struct{})
	go func() {
		defer close(replyDone)
		gw.handleIncomingMessage(context.Background(), &protocol.IncomingMessage{
			ChannelID: "telegram", UserID: "42", SessionKey: "telegram_42", Text: "YES " + tk.Code,
		})
	}()
	select {
	case <-replyDone:
	case <-time.After(5 * time.Second):
		t.Fatal("DEADLOCK: approval reply blocked behind the in-flight turn")
	}
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("approved action did not run while the turn was in flight")
	}
	select {
	case <-probe.entered:
		t.Fatal("turn lock was not actually held during the test")
	default:
	}

	// The reply must not have been recorded as a user message for the model.
	msgs, err := store.GetMessages(sess.Key, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if m.Role == "user" && m.Content == "YES "+tk.Code {
			t.Fatal("approval reply leaked into the model transcript")
		}
	}

	close(holder.release)
	for _, ch := range []chan struct{}{turnDone, queuedDone} {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("turns did not finish after release")
		}
	}
	gw.approvals.Wait()
}

// Paths the model controls (inter-session sends, its own assistant output)
// and other humans cannot approve; only the bound human's inbound reply can.
func TestApprovalReply_ModelAndForeignPathsCannotApprove(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	withApprovals(t, gw)
	sess, _ := store.GetOrCreateSession("42", "telegram")
	tk, ran := pendingOwnerSend(t, gw, sess, "42")

	// Model -> SessionsSend into its own session.
	if err := gw.SendToSession(context.Background(), sess.Key, "", "YES "+tk.Code); err != nil {
		t.Fatal(err)
	}
	// Model's own assistant output.
	if _, err := store.AddMessage(sess.Key, "assistant", "YES "+tk.Code, nil); err != nil {
		t.Fatal(err)
	}
	// A different Telegram user (different session) replying with the code.
	gw.handleIncomingMessage(context.Background(), &protocol.IncomingMessage{
		ChannelID: "telegram", UserID: "999", Text: "YES " + tk.Code,
	})
	gw.approvals.Wait()
	select {
	case <-ran:
		t.Fatal("approved by a non-human or foreign path")
	default:
	}
	if len(gw.approvals.Pending(sess.Key)) != 1 {
		t.Fatal("approval should still be pending")
	}

	// The bound human can still approve.
	gw.handleIncomingMessage(context.Background(), &protocol.IncomingMessage{
		ChannelID: "telegram", UserID: "42", Text: "yes " + tk.Code,
	})
	gw.approvals.Wait()
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("bound human approval did not run")
	}
	// Outcome recorded in the transcript for the model.
	msgs, _ := store.GetMessages(sess.Key, 50)
	found := false
	for _, m := range msgs {
		if m.Role == "assistant" && strings.Contains(m.Content, "APPROVED request "+tk.Code) {
			found = true
		}
	}
	if !found {
		t.Fatal("approval outcome not recorded in session")
	}
}

// Channel turns are interactive; cron and heartbeat turns are not, even if
// the scheduler is invoked with a context that carried an interactive origin.
func TestTurnOrigins_InteractiveVsScheduled(t *testing.T) {
	gw, store, router := newTestGatewayWithRouter(t)
	withApprovals(t, gw)
	p := &gateProvider{}
	router.RegisterProvider("testprov", p) // default provider

	gw.handleIncomingMessage(context.Background(), &protocol.IncomingMessage{
		ChannelID: "telegram", UserID: "42", Text: "hello",
	})

	interactiveCtx := approval.WithInteractiveOrigin(context.Background(), approval.Origin{
		ChannelID: "telegram", UserID: "42", SessionKey: "x", Notify: (&recNotifier{}).notify,
	})
	_ = store // sessions created by the calls below
	if err := gw.executeScheduledJob(interactiveCtx, &scheduler.Job{ID: "j1", Command: "do the thing", Model: ""}); err != nil {
		t.Logf("cron job returned: %v", err) // delivery may fail without adapters; origin is what we check
	}

	inter, srcs := p.seen()
	if len(inter) != 2 {
		t.Fatalf("want 2 turns, got %d (%v)", len(inter), srcs)
	}
	if !inter[0] || srcs[0] != "telegram" {
		t.Fatalf("channel turn should be interactive: %v %v", inter, srcs)
	}
	if inter[1] || srcs[1] != "cron" {
		t.Fatalf("cron turn must be non-interactive: %v %v", inter, srcs)
	}
}

// TUI (DirectClient) consumes approval replies before storing/queuing them.
func TestDirectClient_ConsumesApprovalReply(t *testing.T) {
	c, store := newTestDirectClient(t)
	gwLike := &Gateway{sessions: store, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	withApprovals(t, gwLike)
	c.config.Approvals = gwLike.approvals

	sess, _ := store.GetOrCreateSession("jeff", "tui_jeff")
	tk, ran := pendingOwnerSend(t, gwLike, sess, "jeff")

	if err := c.SendChatWithID(sess.Key, "YES "+tk.Code, "r1"); err != nil {
		t.Fatal(err)
	}
	gwLike.approvals.Wait()
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("TUI approval did not run")
	}
	msgs, _ := store.GetMessages(sess.Key, 50)
	for _, m := range msgs {
		if m.Role == "user" {
			t.Fatalf("approval reply stored as user message: %q", m.Content)
		}
	}
}
