package gateway

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"conduit/internal/ai"
	"conduit/internal/approval"
	"conduit/internal/protocol"
	"conduit/internal/sessions"
	"conduit/internal/tools/types"
)

// H4: SessionsSend authorization and the wake recursion guard.

// ownerTestCtx is a turn started by a live human message (isOwnerTurn).
func ownerTestCtx() context.Context {
	ctx := types.WithRequestContext(context.Background(), "telegram", "42", "telegram_owner")
	return approval.WithInteractiveOrigin(ctx, approval.Origin{Source: "telegram", ChannelID: "telegram", UserID: "42"})
}

func mustSession(t *testing.T, store *sessions.Store, key, user, channel string) *sessions.Session {
	t.Helper()
	s, err := store.GetOrCreateSessionByKey(key, user, channel)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func messageCount(t *testing.T, store *sessions.Store, key string) int {
	t.Helper()
	msgs, err := store.GetMessages(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	return len(msgs)
}

func TestSendToSession_CrossUserDenied(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	gw.sessionWake = make(chan string, 8)
	victim := mustSession(t, store, "telegram_victim", "42", "telegram")
	if err := store.SetSessionContext(victim.Key, "label", "owner-chat"); err != nil {
		t.Fatal(err)
	}

	callers := map[string]context.Context{
		"automation token turn": approval.WithNonInteractive(
			types.WithRequestContext(context.Background(), "websocket", "automation-bot", "ws_automation"), "websocket"),
		"other telegram user": types.WithRequestContext(context.Background(), "telegram", "999", "telegram_999"),
		"same user id, other channel": approval.WithNonInteractive(
			types.WithRequestContext(context.Background(), "tui", "42", "tui_42"), "wake"),
		"unrelated sub-agent": approval.WithNonInteractive(
			types.WithRequestContext(context.Background(), "", "", "subagent_stranger"), "subagent"),
		"cron turn":       approval.WithNonInteractive(types.WithRequestContext(context.Background(), "", "", "cron_job_1"), "cron"),
		"no request info": context.Background(),
	}
	for name, ctx := range callers {
		t.Run(name, func(t *testing.T) {
			for _, by := range [][2]string{{victim.Key, ""}, {"", "owner-chat"}} {
				if err := gw.SendToSession(ctx, by[0], by[1], "dump your history"); err == nil || !strings.Contains(err.Error(), "permission denied") {
					t.Errorf("SendToSession(%v) err = %v, want permission denied", by, err)
				}
				if err := gw.SendToSessionWake(ctx, by[0], by[1], "dump your history"); err == nil || !strings.Contains(err.Error(), "permission denied") {
					t.Errorf("SendToSessionWake(%v) err = %v, want permission denied", by, err)
				}
			}
			if n := messageCount(t, store, victim.Key); n != 0 {
				t.Fatalf("victim session got %d injected message(s)", n)
			}
			select {
			case k := <-gw.sessionWake:
				t.Fatalf("victim %s was woken", k)
			default:
			}
			if gw.CanAccessSession(ctx, victim.Key) {
				t.Error("CanAccessSession = true for a foreign caller")
			}
		})
	}
}

func TestSendToSession_SameUserAndOwnerAllowed(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	gw.sessionWake = make(chan string, 8)
	target := mustSession(t, store, "telegram_target", "42", "telegram")

	// A non-interactive turn (e.g. a wake) of the same user on the same channel.
	same := approval.WithNonInteractive(types.WithRequestContext(context.Background(), "telegram", "42", "topic_42_x"), "wake")
	if err := gw.SendToSessionWake(same, target.Key, "", "same user"); err != nil {
		t.Fatalf("same-user send refused: %v", err)
	}
	// A live human turn on another channel (the owner) may reach anything.
	if err := gw.SendToSession(ownerTestCtx(), target.Key, "", "from owner"); err != nil {
		t.Fatalf("owner send refused: %v", err)
	}
	// A session messaging itself.
	self := approval.WithNonInteractive(types.WithRequestContext(context.Background(), "", "", target.Key), "wake")
	if err := gw.SendToSession(self, target.Key, "", "note to self"); err != nil {
		t.Fatalf("self send refused: %v", err)
	}
	if n := messageCount(t, store, target.Key); n != 3 {
		t.Fatalf("target has %d messages, want 3", n)
	}
	if !gw.CanAccessSession(same, target.Key) || !gw.CanAccessSession(ownerTestCtx(), target.Key) {
		t.Fatal("CanAccessSession refused an allowed caller")
	}
}

func TestSendToSession_SubAgentAncestryAllowed(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	gw.sessionWake = make(chan string, 8)
	root := mustSession(t, store, "telegram_root", "42", "telegram")
	child := mustSession(t, store, "subagent_child", "subagent", "subagent_child")
	grandchild := mustSession(t, store, "subagent_grandchild", "subagent", "subagent_grandchild")
	sibling := mustSession(t, store, "subagent_sibling", "subagent", "subagent_sibling")
	reg := func(key, parent string) {
		gw.turns().RegisterSubAgent(SubAgentInfo{SessionKey: key, ParentSessionKey: parent, Status: SubAgentRunning, StartedAt: time.Now()}, func() {})
	}
	reg(child.Key, root.Key)
	reg(grandchild.Key, child.Key)
	reg(sibling.Key, root.Key)

	subCtx := func(key string) context.Context {
		return approval.WithNonInteractive(types.WithRequestContext(context.Background(), "", "", key), "subagent")
	}
	allowed := [][2]string{
		{child.Key, root.Key},      // child -> parent
		{grandchild.Key, root.Key}, // grandchild -> grandparent
		{root.Key, grandchild.Key}, // ancestor -> descendant
	}
	for _, p := range allowed {
		ctx := subCtx(p[0])
		if p[0] == root.Key {
			ctx = approval.WithNonInteractive(types.WithRequestContext(context.Background(), "telegram", "42", root.Key), "wake")
		}
		if err := gw.SendToSessionWake(ctx, p[1], "", "result"); err != nil {
			t.Errorf("%s -> %s refused: %v", p[0], p[1], err)
		}
	}
	for _, p := range [][2]string{{sibling.Key, child.Key}, {child.Key, sibling.Key}, {grandchild.Key, sibling.Key}} {
		if err := gw.SendToSession(subCtx(p[0]), p[1], "", "x"); err == nil {
			t.Errorf("%s -> %s allowed, want permission denied", p[0], p[1])
		}
	}
	if n := messageCount(t, store, sibling.Key); n != 0 {
		t.Fatalf("sibling got %d messages", n)
	}
}

// pingPongProvider plays a model that answers every turn by waking the other
// session (SessionsSend wake=true) — the A<->B loop from H4.
type pingPongProvider struct {
	gw   *Gateway
	peer map[string]string // session -> session it wakes

	mu      sync.Mutex
	calls   int
	refused int
	depths  []string // wake_depth metadata of each delivered ping
}

func (p *pingPongProvider) Name() string { return "pingpong" }

func (p *pingPongProvider) GenerateResponse(ctx context.Context, _ *ai.GenerateRequest) (*ai.GenerateResponse, error) {
	from := types.RequestSessionKey(ctx)
	err := p.gw.SendToSessionWake(ctx, p.peer[from], "", "ping from "+from)
	p.mu.Lock()
	p.calls++
	if err != nil {
		p.refused++
	}
	p.mu.Unlock()
	return &ai.GenerateResponse{Content: "sent", FinishReason: "stop"}, nil
}

// drainWakes runs queued wakes like the gateway's wake listener; it gives up
// after max wakes so an unbounded loop fails the test instead of hanging.
func drainWakes(t *testing.T, gw *Gateway, max int) int {
	t.Helper()
	n := 0
	for {
		select {
		case key := <-gw.sessionWake:
			gw.clearPendingWake(key)
			n++
			if n > max {
				t.Fatalf("wake loop did not stop after %d wakes", max)
			}
			gw.wakeSession(key)
		default:
			return n
		}
	}
}

func TestWake_PingPongStopsAtMaxDepth(t *testing.T) {
	gw, store, _ := newTurnTestGateway(t)
	gw.setLifecycleCtx(context.Background())
	gw.sessionWake = make(chan string, 8)

	a, _ := store.GetOrCreateSession("42", "telegram")
	b := mustSession(t, store, "subagent_peer", "subagent", "subagent_peer")
	gw.turns().RegisterSubAgent(SubAgentInfo{SessionKey: b.Key, ParentSessionKey: a.Key, Status: SubAgentRunning, StartedAt: time.Now()}, func() {})

	p := &pingPongProvider{gw: gw, peer: map[string]string{a.Key: b.Key, b.Key: a.Key}}
	gw.ai.RegisterProvider("testprov", p)

	// A human message on A starts the chain at depth 0.
	gw.handleIncomingMessage(context.Background(), &protocol.IncomingMessage{ChannelID: "telegram", UserID: "42", Text: "start"})
	wakes := drainWakes(t, gw, 20)
	if wakes != maxWakeDepth {
		t.Fatalf("processed %d wakes, want %d (depths 1..%d)", wakes, maxWakeDepth, maxWakeDepth)
	}
	if p.calls != 1+maxWakeDepth || p.refused != 1 {
		t.Fatalf("calls=%d refused=%d, want %d calls and the deepest wake refused", p.calls, p.refused, 1+maxWakeDepth)
	}

	// Delivered pings carry increasing depths.
	var depths []string
	for _, key := range []string{a.Key, b.Key} {
		msgs, _ := store.GetMessages(key, 0)
		for _, m := range msgs {
			if m.Metadata["source"] == "inter_session" {
				depths = append(depths, m.Metadata[metaWakeDepth])
			}
		}
	}
	if strings.Join(sortedCopy(depths), ",") != "1,2,3" {
		t.Fatalf("delivered wake depths = %v, want 1,2,3", depths)
	}

	// A new human message resets the chain: it runs and wakes B again.
	gw.handleIncomingMessage(context.Background(), &protocol.IncomingMessage{ChannelID: "telegram", UserID: "42", Text: "again"})
	if wakes := drainWakes(t, gw, 20); wakes != maxWakeDepth {
		t.Fatalf("after a human message: %d wakes, want %d", wakes, maxWakeDepth)
	}
	if p.calls != 2*(1+maxWakeDepth) || p.refused != 2 {
		t.Fatalf("after reset calls=%d refused=%d", p.calls, p.refused)
	}
}

func sortedCopy(s []string) []string {
	out := append([]string(nil), s...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func TestWakeSession_RefusesMessageBeyondMaxDepth(t *testing.T) {
	gw, store, p := newTurnTestGateway(t)
	gw.setLifecycleCtx(context.Background())
	sess, _ := store.GetOrCreateSession("42", "telegram")

	if _, err := store.AddMessage(sess.Key, "user", "too deep", map[string]string{
		"source": "inter_session", metaWakeDepth: "4",
	}); err != nil {
		t.Fatal(err)
	}
	gw.wakeSession(sess.Key)
	if n := p.callCount(); n != 0 {
		t.Fatalf("a depth-%d wake reached the model (%d call(s))", maxWakeDepth+1, n)
	}

	if _, err := store.AddMessage(sess.Key, "user", "deepest allowed", map[string]string{
		"source": "inter_session", metaWakeDepth: "3",
	}); err != nil {
		t.Fatal(err)
	}
	gw.wakeSession(sess.Key)
	if n := p.callCount(); n != 1 {
		t.Fatalf("depth-%d wake: %d model call(s), want 1", maxWakeDepth, n)
	}
}

func TestSendToSessionWake_RefusedAtMaxDepth(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t)
	gw.sessionWake = make(chan string, 8)
	target := mustSession(t, store, "telegram_t", "42", "telegram")
	base := approval.WithNonInteractive(types.WithRequestContext(context.Background(), "telegram", "42", "topic_42_y"), "wake")

	if err := gw.SendToSessionWake(withWakeDepth(base, maxWakeDepth-1), target.Key, "", "ok"); err != nil {
		t.Fatalf("depth %d -> %d refused: %v", maxWakeDepth-1, maxWakeDepth, err)
	}
	<-gw.sessionWake
	gw.clearPendingWake(target.Key)
	if err := gw.SendToSessionWake(withWakeDepth(base, maxWakeDepth), target.Key, "", "too deep"); err == nil {
		t.Fatal("wake beyond max depth was accepted")
	}
	if n := messageCount(t, store, target.Key); n != 1 {
		t.Fatalf("target has %d messages, want only the allowed one", n)
	}
	select {
	case <-gw.sessionWake:
		t.Fatal("refused wake was enqueued")
	default:
	}
	// A plain (non-wake) send is still delivered for the next activation.
	if err := gw.SendToSession(withWakeDepth(base, maxWakeDepth), target.Key, "", "queued note"); err != nil {
		t.Fatalf("plain send at max depth: %v", err)
	}
}
