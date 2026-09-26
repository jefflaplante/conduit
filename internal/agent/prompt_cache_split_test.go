package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"conduit/internal/ai"
	"conduit/internal/tools/types"
	"conduit/internal/workspace"
)

// conduit-31jg.14: Build returns a byte-stable static block and a trailing
// dynamic block, so the provider cache breakpoint can sit on the static one.

func buildAt(t *testing.T, pb *PromptBuilder, ctx context.Context, now time.Time) []ai.SystemBlock {
	t.Helper()
	pb.SetClock(func() time.Time { return now })
	blocks, err := pb.Build(ctx, sessionWithModel("claude-sonnet-4-6"), false)
	if err != nil {
		t.Fatal(err)
	}
	return blocks
}

func TestBuild_StaticBlockStableAcrossMinutes(t *testing.T) {
	pb := newTestPromptBuilder()
	t0 := time.Date(2026, 9, 26, 21, 14, 0, 0, time.UTC)

	a := buildAt(t, pb, context.Background(), t0)
	b := buildAt(t, pb, context.Background(), t0.Add(time.Minute))

	if len(a) != 2 || len(b) != 2 {
		t.Fatalf("want [static, dynamic] blocks, got %d and %d", len(a), len(b))
	}
	if a[0].Dynamic || !a[1].Dynamic {
		t.Fatalf("block flags wrong: static.Dynamic=%v dynamic.Dynamic=%v", a[0].Dynamic, a[1].Dynamic)
	}
	if a[0].Text != b[0].Text {
		t.Errorf("static block changed between builds one minute apart")
	}
	if a[1].Text == b[1].Text {
		t.Errorf("dynamic block should carry the (changed) timestamp")
	}
	if strings.Contains(a[0].Text, "Current time:") || strings.Contains(a[0].Text, "## Time Context") {
		t.Errorf("timestamp leaked into the static block")
	}
	if !strings.Contains(a[1].Text, "21:14") || !strings.Contains(b[1].Text, "21:15") {
		t.Errorf("dynamic block should use the injected clock: %q / %q", a[1].Text, b[1].Text)
	}
}

// Repeated builds must be byte-identical: map-ordered sections (model
// aliases, memory files) used to shuffle on every build.
func TestBuild_StaticBlockDeterministic(t *testing.T) {
	pb := newTestPromptBuilder()
	t0 := time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC)
	first := buildAt(t, pb, context.Background(), t0)[0].Text
	for i := 0; i < 30; i++ {
		if got := buildAt(t, pb, context.Background(), t0)[0].Text; got != first {
			t.Fatalf("static block differs on build %d", i+1)
		}
	}
}

// Two daily memory files (the default today+yesterday lookback) used to be
// emitted in map order: a coin flip per build on the static block.
func TestBuild_MemoryFilesDeterministicOrder(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i, d := range []time.Time{now, now.AddDate(0, 0, -1)} {
		p := filepath.Join(dir, "memory", d.Format("2006-01-02")+".md")
		if err := os.WriteFile(p, []byte(fmt.Sprintf("entry %d\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "SOUL.md"), []byte("soul\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pb := newTestPromptBuilder()
	pb.workspaceContext = workspace.NewWorkspaceContext(dir)

	first := buildAt(t, pb, context.Background(), now)[0].Text
	if !strings.Contains(first, "entry 0") || !strings.Contains(first, "entry 1") {
		t.Fatalf("memory files missing from static block")
	}
	yesterday := strings.Index(first, "entry 1")
	today := strings.Index(first, "entry 0")
	if yesterday > today {
		t.Errorf("memory files should be chronological (sorted by name)")
	}
	for i := 0; i < 30; i++ {
		if got := buildAt(t, pb, context.Background(), now)[0].Text; got != first {
			t.Fatalf("static block differs on build %d", i+1)
		}
	}
}

// Wake context is per-turn too: it must not change the static block.
func TestBuild_WakeContextIsDynamic(t *testing.T) {
	pb := newTestPromptBuilder()
	t0 := time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC)
	plain := buildAt(t, pb, context.Background(), t0)
	woke := buildAt(t, pb, types.WithWakeSource(context.Background(), "inter_session"), t0)
	if plain[0].Text != woke[0].Text {
		t.Errorf("wake source changed the static block (%d vs %d chars)", len(plain[0].Text), len(woke[0].Text))
		for i := 0; i < len(plain[0].Text) && i < len(woke[0].Text); i++ {
			if plain[0].Text[i] != woke[0].Text[i] {
				lo := max(0, i-100)
				t.Logf("plain: %q\nwoke:  %q", plain[0].Text[lo:min(len(plain[0].Text), i+100)], woke[0].Text[lo:min(len(woke[0].Text), i+100)])
				break
			}
		}
	}
	if !strings.Contains(woke[1].Text, "## Wake Context") {
		t.Errorf("wake context missing from dynamic block: %q", woke[1].Text)
	}
}

// The joined form (non-Anthropic providers) is static + "\n\n" + dynamic.
func TestBuild_JoinedFormMatchesFullPrompt(t *testing.T) {
	pb := newTestPromptBuilder()
	t0 := time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC)
	blocks := buildAt(t, pb, context.Background(), t0)
	full := pb.buildFullPromptWithParams(context.Background(), sessionWithModel("claude-sonnet-4-6"), false, pb.sectionParams)
	if full != blocks[0].Text+"\n\n"+blocks[1].Text {
		t.Errorf("joined blocks differ from full prompt text")
	}
}

// The prompt cache in ConduitAgentWithIntegration must preserve the flag.
func TestCopySystemBlocks_PreservesDynamic(t *testing.T) {
	out := copySystemBlocks([]ai.SystemBlock{{Text: "s"}, {Text: "d", Dynamic: true}})
	if out[0].Dynamic || !out[1].Dynamic {
		t.Fatalf("Dynamic flag lost in copy: %+v", out)
	}
}
