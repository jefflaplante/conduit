package gateway

import (
	"context"
	"testing"

	"conduit/internal/brain"
	"conduit/internal/tools/types"
)

// reqCtx builds a context the way the gateway does for an inbound message.
func reqCtx(userID string) context.Context {
	return types.WithRequestContext(context.Background(), "telegram", userID, "sess-"+userID)
}

// conduit-31jg.30: the adapter scopes WM to the request's user, so two
// channel users no longer share the "default" bucket.
func TestBrainAdapter_WMIsolatedPerRequestUser(t *testing.T) {
	a := newBrainAdapter(newTestBrainForGateway(t))
	alice, bob := reqCtx("alice"), reqCtx("bob")

	if err := a.Store(alice, "pref.color", "blue", types.BrainTierWorking, "user"); err != nil {
		t.Fatal(err)
	}
	if e, _ := a.Get(bob, "pref.color"); e != nil {
		t.Fatalf("bob sees alice's WM entry: %+v", e)
	}
	if res, _ := a.Recall(bob, "color", 10); len(res) != 0 {
		t.Fatalf("bob recalls alice's WM: %+v", res[0])
	}
	if e, _ := a.Get(alice, "pref.color"); e == nil || e.Value != "blue" {
		t.Fatalf("alice lost her own WM entry: %+v", e)
	}

	// Scratchpad and WM now agree on the bucket.
	if err := a.Push(alice, "", "note"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Peek(bob, ""); err == nil {
		t.Fatal("bob sees alice's scratchpad")
	}
	if v, err := a.Peek(alice, "alice"); err != nil || v != "note" {
		t.Fatalf("alice scratchpad by explicit ID: %q %v", v, err)
	}

	// Status reflects only the caller's WM.
	st, _ := a.Status(bob)
	if st.WMEntries != 0 {
		t.Fatalf("bob status counts alice's WM: %d", st.WMEntries)
	}
}

// An explicit brain.WithUserID wins over the request user (e.g. sub-agent
// scope, briefing's "system").
func TestBrainAdapter_ExplicitBrainUserWins(t *testing.T) {
	a := newBrainAdapter(newTestBrainForGateway(t))
	ctx := brain.WithUserID(reqCtx("alice"), "subagent:x")
	if err := a.Store(ctx, "k", "v", types.BrainTierWorking, "sub-agent"); err != nil {
		t.Fatal(err)
	}
	if e, _ := a.Get(reqCtx("alice"), "k"); e != nil {
		t.Fatal("write with explicit brain user leaked into request user's bucket")
	}
}

// A sub-agent reads its parent's WM through the same scoping the gateway
// applies in SpawnSubAgentWithCallback, and its writes stay private.
func TestBrainAdapter_SubAgentReadsParentWM(t *testing.T) {
	a := newBrainAdapter(newTestBrainForGateway(t))
	parent := reqCtx("alice")
	if err := a.Store(parent, "task.context", "migrating the solar dashboard", types.BrainTierWorking, "user"); err != nil {
		t.Fatal(err)
	}

	// Sub-agents run on the gateway lifecycle context, not the request ctx.
	child := withSubAgentBrainScope(context.Background(), effectiveBrainUserID(parent), "subagent_1")

	e, err := a.Get(child, "task.context")
	if err != nil || e == nil || e.Value != "migrating the solar dashboard" {
		t.Fatalf("sub-agent can't read parent WM: %+v %v", e, err)
	}
	if res, _ := a.Recall(child, "solar dashboard", 5); len(res) == 0 || res[0].Key != "task.context" {
		t.Fatalf("sub-agent recall missed parent WM: %+v", res)
	}

	if err := a.Store(child, "task.result", "done", types.BrainTierWorking, "sub-agent"); err != nil {
		t.Fatal(err)
	}
	if e, _ := a.Get(parent, "task.result"); e != nil {
		t.Fatal("sub-agent write landed in parent WM")
	}
	if e, _ := a.Get(reqCtx("bob"), "task.context"); e != nil {
		t.Fatal("unrelated user reads alice's WM")
	}

	// Nested sub-agent: parent is the first sub-agent's own bucket.
	grandchild := withSubAgentBrainScope(context.Background(), effectiveBrainUserID(child), "subagent_2")
	if e, _ := a.Get(grandchild, "task.result"); e == nil {
		t.Fatal("nested sub-agent can't read its direct parent's WM")
	}
}
