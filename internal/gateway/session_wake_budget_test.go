package gateway

import (
	"context"
	"testing"
	"time"

	"conduit/internal/tools/core"
)

// bd-unz3 (supersedes the 5-minute budget found in the bd-3ose census):
// a wake turn hosts the entire LLM chain — initial call, every tool round,
// and every EmptyGuard retry — and may legitimately spawn sub-agent chains
// running at DefaultSubAgentTimeoutSeconds. The old 5-minute wall was shorter
// than the chain it hosted, so deep heartbeat/wake turns died at exactly
// 5m0.000s (three silent deaths the night of Oct 14-15 alone). These tests
// pin the invariant: the wake budget must always exceed the sub-agent chain
// budget it can contain, and the wake context must actually carry it.
func TestWakeTurnTimeoutExceedsSubAgentChainBudget(t *testing.T) {
	if wakeTurnTimeout <= core.DefaultSubAgentTimeoutSeconds {
		t.Fatalf("wakeTurnTimeout (%v) must exceed the sub-agent chain default (%v): a wake turn can host such a chain plus its own tool rounds",
			wakeTurnTimeout, core.DefaultSubAgentTimeoutSeconds)
	}
}

func TestWakeTurnBudgetCarriesDeadline(t *testing.T) {
	parent := context.Background()
	ctx, cancel := wakeTurnBudget(parent)
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("wake turn context has no deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= core.DefaultSubAgentTimeoutSeconds {
		t.Fatalf("wake turn budget %v must exceed sub-agent chain default %v",
			remaining, core.DefaultSubAgentTimeoutSeconds)
	}

	// The cancel func must actually terminate the derived context.
	cancel()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("wakeTurnBudget cancel func did not cancel the context")
	}
}
