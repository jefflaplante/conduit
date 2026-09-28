package main

import (
	"database/sql"
	"math"
	"path/filepath"
	"testing"

	"conduit/internal/briefing"
	"conduit/internal/config"

	_ "modernc.org/sqlite"
)

// conduit-31jg.53: CLI Brains must carry the configured recency weight so
// brain migration 9 subtracts the same weight the gateway would.
func TestBrainCLIOptions_CarriesRecencyWeight(t *testing.T) {
	if got := brainCLIOptions(nil); len(got) != 0 {
		t.Fatalf("nil cfg: got %d options, want 0", len(got))
	}
	cfg := &config.Config{}
	if got := brainCLIOptions(cfg); len(got) != 0 {
		t.Fatalf("unset recency weight: got %d options, want 0", len(got))
	}
	cfg.Brain.RecencyWeight = 0.25
	if got := brainCLIOptions(cfg); len(got) != 1 {
		t.Fatalf("recency weight set: got %d options, want 1", len(got))
	}
}

// conduit-31jg.87: the briefing CLI Brain stores LTM rows whose base salience
// depends on the access/tier weights and access-count cap, so it must use the
// configured values, not the brain defaults (access 0.4, tier 0.2).
func TestStoreBriefingInBrain_UsesConfiguredSalienceWeights(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "brain.db")
	cfg := &config.Config{}
	cfg.Brain.Enabled = true
	cfg.Brain.Path = dbPath
	cfg.Brain.AccessWeight = 0.5
	cfg.Brain.TierWeight = 0.5
	cfg.Brain.AccessCountCap = 10

	storeBriefingInBrain(cfg, &briefing.Briefing{Summary: "weights"})

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	var salience float64
	if err := db.QueryRow(`SELECT salience FROM brain_ltm WHERE key = ?`, briefingBrainKey).Scan(&salience); err != nil {
		t.Fatalf("read salience: %v", err)
	}
	// base = min(access_count/cap, 1)*access_weight + 0.8*tier_weight
	want := (1.0/10.0)*0.5 + 0.8*0.5
	if math.Abs(salience-want) > 1e-9 {
		t.Fatalf("stored base salience = %v, want %v (configured weights not applied)", salience, want)
	}
}
