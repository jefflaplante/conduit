package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"conduit/internal/ai"
	"conduit/internal/sessions"
	"conduit/internal/tools/types"
)

// conduit-31jg.65: the agent-level prompt cache keeps only the static
// block; the dynamic block (time, wake context, situation awareness) is
// re-rendered on every turn, even while the cache is warm.

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newClockedAgent(t *testing.T) (*ConduitAgentWithIntegration, *fakeClock) {
	t.Helper()
	a := newTestAgent()
	clk := &fakeClock{t: time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)}
	a.SetClock(clk.now)
	return a, clk
}

func mustBuild(t *testing.T, a *ConduitAgentWithIntegration, ctx context.Context, s *sessions.Session) []ai.SystemBlock {
	t.Helper()
	blocks, err := a.BuildSystemPrompt(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 2 || blocks[0].Dynamic || !blocks[1].Dynamic {
		t.Fatalf("want [static, dynamic] blocks, got %d: %+v", len(blocks), blocks)
	}
	return blocks
}

func TestPromptCache_DynamicBlockFollowsClockWhileWarm(t *testing.T) {
	a, clk := newClockedAgent(t)
	s := sessionWithModel("claude-sonnet-4-6")
	ctx := context.Background()

	first := mustBuild(t, a, ctx, s)
	if !strings.Contains(first[1].Text, "09:00") {
		t.Fatalf("dynamic block should show the injected clock: %q", first[1].Text)
	}

	clk.advance(2 * time.Minute) // well inside the 5-minute TTL
	second := mustBuild(t, a, ctx, s)
	if !strings.Contains(second[1].Text, "09:02") {
		t.Errorf("warm cache served a stale timestamp: %q", second[1].Text)
	}
	if second[0].Text != first[0].Text {
		t.Errorf("static block changed within TTL")
	}

	// Wake context is per-turn too: a warm hit must still render it.
	woke := mustBuild(t, a, types.WithWakeSource(ctx, "inter_session"), s)
	if !strings.Contains(woke[1].Text, "## Wake Context") {
		t.Errorf("wake context missing from dynamic block on cache hit: %q", woke[1].Text)
	}
	if woke[0].Text != first[0].Text {
		t.Errorf("wake source changed the cached static block")
	}
}

func TestPromptCache_StaticBlockNotRebuiltWithinTTL(t *testing.T) {
	a, clk := newClockedAgent(t)
	s := sessionWithModel("claude-sonnet-4-6")
	ctx := context.Background()

	first := mustBuild(t, a, ctx, s)
	// Change static-section input behind the cache's back: a rebuild would
	// pick it up, a cache hit must not.
	a.promptBuilder.identity.APIKeyIdentity = "You are CHANGED-IDENTITY."
	a.promptBuilder.identity.OAuthIdentity = "You are CHANGED-IDENTITY."

	clk.advance(4 * time.Minute)
	hit := mustBuild(t, a, ctx, s)
	if hit[0].Text != first[0].Text || strings.Contains(hit[0].Text, "CHANGED-IDENTITY") {
		t.Fatalf("static block was rebuilt within TTL")
	}

	clk.advance(2 * time.Minute) // past the TTL
	miss := mustBuild(t, a, ctx, s)
	if !strings.Contains(miss[0].Text, "CHANGED-IDENTITY") {
		t.Errorf("static block should be rebuilt after TTL expiry")
	}
	if !strings.Contains(miss[1].Text, "09:06") {
		t.Errorf("dynamic block wrong after rebuild: %q", miss[1].Text)
	}
}

// Budget-constrained models: a cache hit renders the same dynamic sections
// a fresh build would (including dropping the ones the budget dropped).
func TestPromptCache_HitMatchesFreshBuildForSmallModel(t *testing.T) {
	a, clk := newClockedAgent(t)
	ctx := context.Background()
	for _, model := range []string{"gemma2", "claude-sonnet-4-6"} {
		s := sessionWithModel(model)
		if _, err := a.BuildSystemPrompt(ctx, s); err != nil {
			t.Fatal(err)
		}
		clk.advance(time.Minute)
		hit, err := a.BuildSystemPrompt(ctx, s)
		if err != nil {
			t.Fatal(err)
		}
		fresh, err := a.promptBuilder.Build(ctx, s, a.detectOAuthFromSession(s))
		if err != nil {
			t.Fatal(err)
		}
		if len(hit) != len(fresh) {
			t.Fatalf("%s: cache hit has %d blocks, fresh build %d", model, len(hit), len(fresh))
		}
		for i := range hit {
			if hit[i] != fresh[i] {
				t.Errorf("%s: block %d differs between cache hit and fresh build:\nhit:   %q\nfresh: %q", model, i, hit[i].Text, fresh[i].Text)
			}
		}
	}
}
