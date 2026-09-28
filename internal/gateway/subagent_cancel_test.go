package gateway

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"conduit/internal/ai"
	"conduit/internal/approval"
	"conduit/internal/config"
	"conduit/internal/sessions"
	"conduit/internal/tools/core"
	"conduit/internal/tools/types"
)

// conduit-38cz part 2 (SessionsCancel) and conduit-31jg.84 (/stop cascade).

var _ core.SubAgentCanceller = (*Gateway)(nil)

// hangProvider blocks every call until its ctx ends.
type hangProvider struct{ calls atomic.Int64 }

func (p *hangProvider) Name() string { return "mock" }
func (p *hangProvider) GenerateResponse(ctx context.Context, _ *ai.GenerateRequest) (*ai.GenerateResponse, error) {
	p.calls.Add(1)
	<-ctx.Done()
	return nil, ctx.Err()
}

func newSubAgentCancelGateway(t *testing.T) (*Gateway, *sessions.Store, *hangProvider) {
	t.Helper()
	gw, store := newTestGatewayWithSessions(t)
	gw.setLifecycleCtx(context.Background())
	router, err := ai.NewRouter(config.AIConfig{DefaultProvider: "mock"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	hp := &hangProvider{}
	router.RegisterProvider("mock", hp)
	gw.ai = router
	return gw, store, hp
}

func waitSub(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func sessionCtx(key string) context.Context {
	return types.WithRequestContext(context.Background(), "", "", key)
}

func spawnHanging(t *testing.T, gw *Gateway, parentCtx context.Context, label string) string {
	t.Helper()
	key, err := gw.SpawnSubAgentWithCallback(parentCtx, "long task "+label, "", "", label, 60, "", "", false, nil)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	waitSub(t, "sub-agent "+key+" running", func() bool {
		for _, s := range gw.turns().Snapshot() {
			if s.SessionKey == key && s.Running {
				return true
			}
		}
		return false
	})
	return key
}

func subAgentEnded(gw *Gateway, key string) func() bool {
	return func() bool {
		info, ok := gw.turns().SubAgent(key)
		return ok && !info.EndedAt.IsZero() && !gw.turns().Busy(key)
	}
}

// wakeMessages returns the parent's user rows tagged with a wake source.
func wakeMessages(t *testing.T, store *sessions.Store, key string) map[string][]string {
	t.Helper()
	msgs, err := store.GetMessages(key, 50)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, m := range msgs {
		if src := m.Metadata["wake_source"]; src != "" {
			out[src] = append(out[src], m.Content)
		}
	}
	return out
}

func TestSessionsCancel_ParentCancelsChild(t *testing.T) {
	gw, store, _ := newSubAgentCancelGateway(t)
	parent, err := store.GetOrCreateSession("u1", "chan1")
	if err != nil {
		t.Fatal(err)
	}
	child := spawnHanging(t, gw, sessionCtx(parent.Key), "worker")

	// Lineage: snapshot + persisted context + registry.
	var snapParent string
	for _, s := range gw.turns().Snapshot() {
		if s.SessionKey == child {
			snapParent = s.ParentSessionKey
		}
	}
	if snapParent != parent.Key {
		t.Errorf("TurnSnapshot.ParentSessionKey = %q, want %q", snapParent, parent.Key)
	}
	if kids := gw.turns().SubAgents(parent.Key); len(kids) != 1 || kids[0].SessionKey != child || kids[0].Status != SubAgentRunning {
		t.Fatalf("children = %+v", kids)
	}

	key, n, err := gw.CancelSubAgent(sessionCtx(parent.Key), "", "worker", "no longer needed")
	if err != nil || key != child || n != 1 {
		t.Fatalf("CancelSubAgent = %q,%d,%v", key, n, err)
	}
	waitSub(t, "child ended", subAgentEnded(gw, child))
	waitSub(t, "parent woken", func() bool { return len(wakeMessages(t, store, parent.Key)[types.WakeSourceSubAgentCanceled]) == 1 })

	wakes := wakeMessages(t, store, parent.Key)
	if len(wakes) != 1 {
		t.Errorf("parent wakes = %v, want only %s", wakes, types.WakeSourceSubAgentCanceled)
	}
	if w := wakes[types.WakeSourceSubAgentCanceled][0]; !strings.Contains(w, child) || !strings.Contains(w, "no longer needed") {
		t.Errorf("wake message = %q", w)
	}
	if st, _ := store.GetSessionState(child); st != sessions.SessionStateCanceled {
		t.Errorf("child state = %q, want canceled", st)
	}
	cs, _ := store.GetSession(child)
	if cs.Context[ctxKeySubAgentStatus] != "canceled" || cs.Context[ctxKeyParentSessionKey] != parent.Key || cs.Context["label"] != "worker" {
		t.Errorf("child context = %v", cs.Context)
	}
	info, _ := gw.turns().SubAgent(child)
	if info.Status != SubAgentCanceled || !strings.HasPrefix(info.CanceledBy, parent.Key) {
		t.Errorf("registry info = %+v", info)
	}
	msgs, _ := store.GetMessages(child, 10)
	if last := msgs[len(msgs)-1]; last.Role != "assistant" || !strings.Contains(last.Content, "canceled by") {
		t.Errorf("child last row = %+v", last)
	}

	// A second cancel reports it already ended.
	if _, _, err := gw.CancelSubAgent(sessionCtx(parent.Key), child, "", ""); err == nil || !strings.Contains(err.Error(), "already canceled") {
		t.Errorf("second cancel err = %v", err)
	}
}

func TestSessionsCancel_NonParentRefused_OwnerAllowed(t *testing.T) {
	gw, store, _ := newSubAgentCancelGateway(t)
	parent, _ := store.GetOrCreateSession("u1", "chan1")
	other, _ := store.GetOrCreateSession("u2", "chan2")
	child := spawnHanging(t, gw, sessionCtx(parent.Key), "")

	for name, ctx := range map[string]context.Context{
		"other session":   sessionCtx(other.Key),
		"child itself":    sessionCtx(child),
		"no session":      context.Background(),
		"non-interactive": approval.WithNonInteractive(sessionCtx(other.Key), "heartbeat"),
	} {
		if _, _, err := gw.CancelSubAgent(ctx, child, "", ""); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("%s: err = %v, want permission denied", name, err)
		}
	}
	if info, _ := gw.turns().SubAgent(child); info.Status != SubAgentRunning || !gw.turns().Busy(child) {
		t.Fatalf("refused cancel must leave the child running: %+v", info)
	}

	// The owner (a live human turn) may cancel any sub-agent.
	owner := approval.WithInteractiveOrigin(sessionCtx(other.Key), approval.Origin{Source: "telegram"})
	if _, n, err := gw.CancelSubAgent(owner, child, "", ""); err != nil || n != 1 {
		t.Fatalf("owner cancel = %d,%v", n, err)
	}
	waitSub(t, "child ended", subAgentEnded(gw, child))
	if info, _ := gw.turns().SubAgent(child); !strings.HasPrefix(info.CanceledBy, "owner") {
		t.Errorf("CanceledBy = %q", info.CanceledBy)
	}
	// The spawning parent is still the one woken.
	waitSub(t, "parent woken", func() bool { return len(wakeMessages(t, store, parent.Key)[types.WakeSourceSubAgentCanceled]) == 1 })

	// Unknown sessions are rejected.
	if _, _, err := gw.CancelSubAgent(sessionCtx(parent.Key), "nope", "", ""); err == nil {
		t.Error("cancel of an unknown session must fail")
	}
}

// Cancelling a child also cancels the sub-agents it spawned, quietly (the
// grandchild's parent is gone, so nobody resurrects it with a wake).
func TestSessionsCancel_CascadesToDescendants(t *testing.T) {
	gw, store, _ := newSubAgentCancelGateway(t)
	parent, _ := store.GetOrCreateSession("u1", "chan1")
	child := spawnHanging(t, gw, sessionCtx(parent.Key), "c")
	grand := spawnHanging(t, gw, sessionCtx(child), "g")

	if !gw.turns().subagents.isAncestor(parent.Key, grand) {
		t.Fatal("parent must be an ancestor of the grandchild")
	}
	// The grandparent may cancel the grandchild directly (ancestor).
	if _, n, err := gw.CancelSubAgent(sessionCtx(parent.Key), child, "", ""); err != nil || n != 2 {
		t.Fatalf("cancel = %d,%v, want 2", n, err)
	}
	waitSub(t, "child ended", subAgentEnded(gw, child))
	waitSub(t, "grandchild ended", subAgentEnded(gw, grand))
	if w := wakeMessages(t, store, child); len(w) != 0 {
		t.Errorf("canceled child must not be woken: %v", w)
	}
	waitSub(t, "parent woken once", func() bool { return len(wakeMessages(t, store, parent.Key)[types.WakeSourceSubAgentCanceled]) == 1 })
}

// conduit-31jg.84: /stop in the parent cascades to children and
// grandchildren, reports the count, and wakes nobody.
func TestStopTree_CascadesToChildrenAndGrandchildren(t *testing.T) {
	gw, store, _ := newSubAgentCancelGateway(t)
	parent, _ := store.GetOrCreateSession("u1", "chan1")
	other, _ := store.GetOrCreateSession("u2", "chan2")
	child1 := spawnHanging(t, gw, sessionCtx(parent.Key), "c1")
	child2 := spawnHanging(t, gw, sessionCtx(parent.Key), "c2")
	grand := spawnHanging(t, gw, sessionCtx(child1), "g")
	unrelated := spawnHanging(t, gw, sessionCtx(other.Key), "u")

	res := gw.turns().StopTree(parent.Key)
	if res.SubAgents != 3 || res.StoppedRunning || res.DroppedQueued != 0 {
		t.Fatalf("StopTree = %+v, want 3 sub-agents", res)
	}
	for _, k := range []string{child1, child2, grand} {
		waitSub(t, k+" ended", subAgentEnded(gw, k))
		if info, _ := gw.turns().SubAgent(k); info.Status != SubAgentCanceled || info.CanceledBy != "/stop" {
			t.Errorf("%s = %+v", k, info)
		}
	}
	if info, _ := gw.turns().SubAgent(unrelated); info.Status != SubAgentRunning || !gw.turns().Busy(unrelated) {
		t.Errorf("unrelated sub-agent was stopped: %+v", info)
	}
	// Quiet: /stop's human knows; no session is woken.
	time.Sleep(50 * time.Millisecond)
	for _, k := range []string{parent.Key, child1} {
		if w := wakeMessages(t, store, k); len(w) != 0 {
			t.Errorf("%s woken after /stop: %v", k, w)
		}
	}
	msg, ok := stopResponse(res)
	if !ok || !strings.Contains(msg, "Stopped 3 sub-agent(s)") {
		t.Errorf("stopResponse = %q", msg)
	}
	// A second /stop finds nothing.
	if res := gw.turns().StopTree(parent.Key); res.SubAgents != 0 {
		t.Errorf("second StopTree = %+v", res)
	}
	gw.turns().StopTree(other.Key)
	waitSub(t, "unrelated ended", subAgentEnded(gw, unrelated))
}

func TestStopResponse_SubAgentCounts(t *testing.T) {
	for _, c := range []struct {
		res  StopResult
		want string
	}{
		{StopResult{StoppedRunning: true, SubAgents: 2}, "Stopping current operation... (2 sub-agent(s) stopped)"},
		{StopResult{StoppedRunning: true, DroppedQueued: 1, SubAgents: 1}, "Stopping current operation... (1 queued message(s) dropped) (1 sub-agent(s) stopped)"},
		{StopResult{DroppedQueued: 1}, "Dropped 1 queued message(s)."},
		{StopResult{SubAgents: 4}, "Stopped 4 sub-agent(s)."},
		{StopResult{}, "No active operation to stop."},
	} {
		if got, _ := stopResponse(c.res); got != c.want {
			t.Errorf("stopResponse(%+v) = %q, want %q", c.res, got, c.want)
		}
	}
}

// Spawning during the shutdown drain is refused up front.
func TestSpawnSubAgent_RefusedWhileDraining(t *testing.T) {
	gw, _, _ := newSubAgentCancelGateway(t)
	gw.shutdownMgr = NewShutdownManager(gw.logger, gw)
	gw.shutdownMgr.state.Store(int32(StateDraining))
	if _, err := gw.SpawnSubAgent(context.Background(), "x", "", "", "", 5); err == nil || !strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("spawn while draining err = %v", err)
	}
}

// A cancel that races the sub-agent's final reply keeps the reply: the
// turn completed, so it is delivered as a completion.
func TestSubAgentRegistry_CompletionRacingCancelWins(t *testing.T) {
	r := newSubAgentRegistry()
	r.register(SubAgentInfo{SessionKey: "c", ParentSessionKey: "p"}, func() {})
	r.register(SubAgentInfo{SessionKey: "d", ParentSessionKey: "p"}, func() {})
	r.markTree("p", false, "/stop", true)
	if info, quiet, _ := r.finish("c", SubAgentCompleted); info.Status != SubAgentCompleted || info.CanceledBy != "" || quiet {
		t.Errorf("completed-after-cancel = %+v quiet=%v", info, quiet)
	}
	if info, quiet, _ := r.finish("d", SubAgentFailed); info.Status != SubAgentCanceled || !quiet {
		t.Errorf("failed-after-cancel = %+v quiet=%v, want canceled/quiet", info, quiet)
	}
}

func TestSubAgentRegistry_PruneKeepsLineageOfRunningDescendants(t *testing.T) {
	r := newSubAgentRegistry()
	old := time.Now().Add(-2 * subAgentRetention)
	r.register(SubAgentInfo{SessionKey: "c", ParentSessionKey: "p"}, func() {})
	r.register(SubAgentInfo{SessionKey: "g", ParentSessionKey: "c"}, func() {})
	r.register(SubAgentInfo{SessionKey: "done", ParentSessionKey: "p"}, func() {})
	r.finish("c", SubAgentCompleted)
	r.finish("done", SubAgentCompleted)
	r.mu.Lock()
	r.entries["c"].info.EndedAt = old
	r.entries["done"].info.EndedAt = old
	r.pruneLocked(time.Now())
	_, keepC := r.entries["c"]
	_, keepDone := r.entries["done"]
	r.mu.Unlock()
	if !keepC || keepDone {
		t.Fatalf("prune: keep c=%v (want true: running grandchild), done=%v (want false)", keepC, keepDone)
	}
	if !r.isAncestor("p", "g") {
		t.Error("lineage p→c→g lost")
	}
	keys, _ := r.markTree("p", false, "/stop", true)
	if len(keys) != 1 || keys[0] != "g" {
		t.Errorf("cascade through a finished child = %v, want [g]", keys)
	}
}
