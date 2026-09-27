package main

import (
	"context"
	"path/filepath"
	"testing"

	"conduit/internal/brain"
	"conduit/internal/briefing"
	"conduit/internal/config"
)

// conduit-31jg.61: the briefing must survive the CLI Brain closing and be
// visible to a separate (gateway) Brain instance through the same reader the
// Situation Awareness section uses (Brain.List on the "sense.briefing."
// prefix, under an arbitrary chat user's context).
func TestStoreBriefingInBrain_VisibleToGatewayAfterCLIExit(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "brain.db")
	cfg := &config.Config{}
	cfg.Brain.Enabled = true
	cfg.Brain.Path = dbPath

	storeBriefingInBrain(cfg, &briefing.Briefing{Summary: "first briefing"})
	// Second run overwrites the same key rather than adding a row.
	storeBriefingInBrain(cfg, &briefing.Briefing{Summary: "second briefing"})

	// The CLI Brain is closed by now; open a fresh instance like the gateway.
	gw, err := brain.New(dbPath)
	if err != nil {
		t.Fatalf("open gateway brain: %v", err)
	}
	defer gw.Close()

	ctx := brain.WithUserID(context.Background(), "telegram:12345")
	entries, err := gw.List(ctx, "sense.briefing.", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want exactly 1 briefing entry, got %d: %+v", len(entries), entries)
	}
	e := entries[0]
	if e.Key != briefingBrainKey || e.Tier != brain.TierLongTerm {
		t.Fatalf("unexpected entry key=%q tier=%q", e.Key, e.Tier)
	}
	if e.Value != "second briefing" {
		t.Fatalf("want latest value, got %q", e.Value)
	}
	if e.ExpiresAt == nil {
		t.Fatalf("briefing should carry a TTL")
	}

	got, err := gw.Get(ctx, briefingBrainKey)
	if err != nil || got == nil || got.Value != "second briefing" {
		t.Fatalf("Get: entry=%+v err=%v", got, err)
	}
}

func TestStoreBriefingInBrain_DisabledIsNoop(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "brain.db")
	cfg := &config.Config{}
	cfg.Brain.Path = dbPath
	storeBriefingInBrain(cfg, &briefing.Briefing{Summary: "x"})
	if matches, _ := filepath.Glob(dbPath + "*"); len(matches) != 0 {
		t.Fatalf("brain DB should not be created when brain is disabled: %v", matches)
	}
}
