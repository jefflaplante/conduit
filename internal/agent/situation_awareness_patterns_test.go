package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"conduit/internal/brain"
	"conduit/internal/brain/rem"
	"conduit/internal/reflection"
)

// conduit-31jg.54: a SPAR pivot pattern that recurs across sessions is
// consolidated by REM Reflect into reflect.tools.* and rendered in the
// Situation Awareness section of the next session's prompt.
func TestSituationAwareness_ShowsPromotedToolPatterns(t *testing.T) {
	b, err := brain.New(filepath.Join(t.TempDir(), "brain.db"), brain.WithAutoFlushInterval(0))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ctx := context.Background()

	store := reflection.NewStore(b.DB())
	mw := reflection.NewReflectionMiddleware(store, reflection.DefaultConfig())
	// The execution engine's pivot hook fires in two different sessions.
	mw.RecordConsecutiveFailure("telegram:1", "WebFetch", 3, "dial tcp: i/o timeout")
	mw.RecordConsecutiveFailure("tui:2", "WebFetch", 3, "dial tcp: i/o timeout")

	cycle := rem.NewREMCycle(b, b.DB(), rem.REMConfig{PruneAgeDays: 30, WorkspaceDir: t.TempDir()})
	if _, err := cycle.Run(ctx, []string{"reflect"}, false); err != nil {
		t.Fatal(err)
	}

	pb := &PromptBuilder{brainService: b}
	section := pb.buildSituationAwareness(ctx, &SectionParams{})
	if !strings.Contains(section, "### Tool Pitfalls") {
		t.Fatalf("missing Tool Pitfalls category:\n%s", section)
	}
	if !strings.Contains(section, "WebFetch failed 3+ times in a row in 2 turns across 2 sessions") {
		t.Fatalf("promoted pattern not rendered:\n%s", section)
	}
}
