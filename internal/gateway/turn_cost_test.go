package gateway

import (
	"context"
	"math"
	"strconv"
	"testing"

	"conduit/internal/ai"
	"conduit/internal/config"
)

// conduit-31jg.57: a turn on an override-priced model records a non-zero
// session cost; a turn on an unpriced model is counted as unpriced rather
// than silently adding $0.

func TestTurnRunner_SessionCostUsesPricingOverrides(t *testing.T) {
	gw, store, p := newTurnTestGateway(t)
	// The test router's default route is testprov/llama3; price it with a
	// provider-prefixed override key, as the owner's openrouter entries are.
	gw.ai.SetPricingResolver(ai.NewPricingResolver(map[string]config.PricingOverride{
		"testprov/llama3": {InputPerMToken: 1.4, OutputPerMToken: 4.4},
	}))
	p.usage = ai.Usage{PromptTokens: 10_000, CompletionTokens: 1_000, TotalTokens: 11_000}

	gw.handleIncomingMessage(context.Background(), tgMsg("hello"))
	gw.handleIncomingMessage(context.Background(), tgMsg("again"))

	sess, _ := store.GetOrCreateSession("42", "telegram")
	got, _ := strconv.ParseFloat(sess.Context["session_total_cost"], 64)
	perTurn := 10_000*1.4/1e6 + 1_000*4.4/1e6
	if math.Abs(got-2*perTurn) > 1e-6 {
		t.Fatalf("session_total_cost = %v, want %v", got, 2*perTurn)
	}
	if n := sess.Context["session_unpriced_requests"]; n != "" {
		t.Errorf("session_unpriced_requests = %q, want unset", n)
	}
}

func TestTurnRunner_UnpricedModelCountedNotFree(t *testing.T) {
	gw, store, p := newTurnTestGateway(t)
	gw.ai.SetPricingResolver(ai.NewPricingResolver(nil)) // llama3 has no price
	p.usage = ai.Usage{PromptTokens: 10_000, CompletionTokens: 1_000, TotalTokens: 11_000}

	gw.handleIncomingMessage(context.Background(), tgMsg("hello"))

	sess, _ := store.GetOrCreateSession("42", "telegram")
	if n := sess.Context["session_unpriced_requests"]; n != "1" {
		t.Fatalf("session_unpriced_requests = %q, want 1", n)
	}
	if got, _ := strconv.ParseFloat(sess.Context["session_total_cost"], 64); got != 0 {
		t.Errorf("session_total_cost = %v, want 0 (unknown, not added)", got)
	}
}
