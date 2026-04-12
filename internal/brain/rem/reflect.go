package rem

import (
	"context"
	"fmt"
	"strings"
	"time"

	"conduit/internal/brain"
	"conduit/internal/reflection"
)

// ReflectResult holds the output of the REM reflect phase.
type ReflectResult struct {
	EntriesProcessed int
	ClustersFound    int
	ScoresBackfilled int
}

// Reflect mines cross-session patterns from unprocessed reflection entries,
// writes cluster summaries to Brain LTM, marks entries as processed, and
// backfills heuristic scores on unscored session summaries.
func (r *REMCycle) Reflect(ctx context.Context, dryRun bool) (*ReflectResult, error) {
	result := &ReflectResult{}

	store := reflection.NewStore(r.db)

	// Step 1: Query unprocessed reflection entries for raw data
	unprocessed, err := store.QueryUnprocessed(ctx)
	if err != nil {
		return result, fmt.Errorf("query unprocessed reflections: %w", err)
	}

	if len(unprocessed) == 0 {
		return result, nil
	}

	result.EntriesProcessed = len(unprocessed)

	// Step 2: Query aggregated tool stats since earliest unprocessed entry
	earliest := unprocessed[0].Timestamp
	stats, err := store.QueryToolStats(ctx, earliest)
	if err != nil {
		return result, fmt.Errorf("query tool stats: %w", err)
	}

	// Step 3: Identify clusters — tool+outcome groups with count >= 3
	clusters := identifyClusters(stats, earliest)
	result.ClustersFound = len(clusters)

	if !dryRun {
		// Step 4: Write cluster summaries to Brain LTM
		for _, cl := range clusters {
			key := fmt.Sprintf("reflect.clusters.%s.%s", cl.Tool, cl.Outcome)
			value := formatClusterSummary(cl)
			if err := r.brain.Store(ctx, key, value, brain.TierLongTerm, "rem:reflect"); err != nil {
				return result, fmt.Errorf("store cluster %s: %w", key, err)
			}
		}

		// Step 5: Mark all unprocessed entries as processed
		ids := make([]string, len(unprocessed))
		for i, entry := range unprocessed {
			ids[i] = entry.ID
		}
		if err := store.MarkProcessed(ctx, ids); err != nil {
			return result, fmt.Errorf("mark processed: %w", err)
		}
	}

	// Step 6: Heuristic score backfill for unscored session summaries
	backfilled, err := r.backfillScores(ctx, unprocessed, dryRun)
	if err != nil {
		return result, fmt.Errorf("backfill scores: %w", err)
	}
	result.ScoresBackfilled = backfilled

	return result, nil
}

// cluster represents a group of reflection entries sharing tool+outcome.
type cluster struct {
	Tool        string
	Outcome     string
	Count       int
	AvgDuration time.Duration
	Earliest    time.Time
}

// identifyClusters filters tool stats to groups with count >= 3.
func identifyClusters(stats []reflection.ToolStat, earliest time.Time) []cluster {
	var clusters []cluster
	for _, s := range stats {
		if s.Count >= 3 {
			clusters = append(clusters, cluster{
				Tool:        s.Tool,
				Outcome:     string(s.Outcome),
				Count:       s.Count,
				AvgDuration: s.AvgDuration,
				Earliest:    earliest,
			})
		}
	}
	return clusters
}

// formatClusterSummary produces a human-readable summary for a cluster.
func formatClusterSummary(cl cluster) string {
	return fmt.Sprintf("Tool %s has %d %s since %s. Avg duration: %dms.",
		cl.Tool, cl.Count, pluralizeOutcome(cl.Outcome),
		cl.Earliest.Format("2006-01-02 15:04"),
		cl.AvgDuration.Milliseconds())
}

// pluralizeOutcome returns the plural form of an outcome string.
func pluralizeOutcome(outcome string) string {
	if strings.HasSuffix(outcome, "s") {
		return outcome + "es"
	}
	return outcome + "s"
}

// backfillScores assigns heuristic scores to TypeSessionSummary entries
// that have score=0. Heuristic: 0 failures in session → score 4,
// some failures → score 2, all failures → score 1.
func (r *REMCycle) backfillScores(ctx context.Context, entries []*reflection.ReflectionEntry, dryRun bool) (int, error) {
	// Group entries by session
	sessions := make(map[string][]*reflection.ReflectionEntry)
	for _, e := range entries {
		sessions[e.SessionKey] = append(sessions[e.SessionKey], e)
	}

	// Find session summaries with score=0
	var backfilled int
	for _, e := range entries {
		if e.Type != reflection.TypeSessionSummary || e.Score != 0 {
			continue
		}

		// Count failures in this session's entries
		sessionEntries := sessions[e.SessionKey]
		totalToolOutcomes := 0
		failures := 0
		for _, se := range sessionEntries {
			if se.Type == reflection.TypeToolOutcome {
				totalToolOutcomes++
				if se.Outcome == reflection.OutcomeFailure || se.Outcome == reflection.OutcomeTimeout {
					failures++
				}
			}
		}

		// Apply heuristic
		var score int
		switch {
		case totalToolOutcomes == 0:
			// No tool outcomes to judge — skip scoring
			continue
		case failures == 0:
			score = 4
		case failures < totalToolOutcomes:
			score = 2
		default:
			// All tool outcomes are failures
			score = 1
		}

		if !dryRun {
			_, err := r.db.ExecContext(ctx,
				"UPDATE brain_reflections SET score = ? WHERE id = ?",
				score, e.ID)
			if err != nil {
				return backfilled, fmt.Errorf("backfill score for %s: %w", e.ID, err)
			}
		}
		backfilled++
	}

	return backfilled, nil
}

// reflectSummary returns a human-readable summary line for the report log.
func reflectSummary(r *ReflectResult) string {
	if r == nil {
		return "Reflect: not run"
	}
	parts := []string{
		fmt.Sprintf("entries processed: %d", r.EntriesProcessed),
		fmt.Sprintf("clusters found: %d", r.ClustersFound),
		fmt.Sprintf("scores backfilled: %d", r.ScoresBackfilled),
	}
	return "Reflect: " + strings.Join(parts, ", ")
}
