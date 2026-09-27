package rem

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"conduit/internal/brain"
	"conduit/internal/database"
	"conduit/internal/reflection"
)

// ReflectResult holds the output of the REM reflect phase.
type ReflectResult struct {
	EntriesProcessed int
	ClustersFound    int
	ScoresBackfilled int
	// PatternsPromoted counts reflect.tools.* LTM entries written (or, in a
	// dry run, that would be written) from SPAR pattern rows. conduit-31jg.54
	PatternsPromoted int
	PromotedKeys     []string `json:",omitempty"`
}

// Pattern promotion thresholds (conduit-31jg.54). A SPAR pivot/circular
// crossing becomes a Brain LTM fact only when it recurs across sessions
// within the window; one bad turn is noise, the same failure in several
// independent conversations is a lesson.
const (
	patternMinSessions    = 2
	patternMinOccurrences = 2
	// patternSource is the provenance of promoted pattern entries (system:
	// sources go stale after 14 days in grooming).
	patternSource = "system:rem-reflect"
	// maxPatternValueLen keeps promoted values within the Situation
	// Awareness per-line budget (renderCategory truncates at 200).
	maxPatternValueLen = 200
)

// defaultReflectWindowDays bounds the look-back for clusters and pattern
// consolidation when PruneAgeDays is unset. Matches reflection retention.
const defaultReflectWindowDays = 30

// Reflect mines cross-session patterns from unprocessed reflection entries:
// it writes tool+outcome cluster summaries and consolidated SPAR patterns to
// Brain LTM, backfills heuristic scores on unscored session summaries, and
// marks the entries as processed.
//
// conduit-31jg.54: the phase works on a rowid high-water mark taken at the
// start and uses aggregate SQL throughout, so a large backlog (the live DB
// had ~430k never-processed rows) is neither loaded into memory nor bound as
// one giant IN (...) list. Rows inserted while the phase runs are left for
// the next cycle.
func (r *REMCycle) Reflect(ctx context.Context, dryRun bool) (*ReflectResult, error) {
	result := &ReflectResult{}
	store := reflection.NewStore(r.db)

	// Step 1: snapshot the unprocessed set.
	var hw sql.NullInt64
	var count int
	var earliestStr sql.NullString
	if err := r.db.QueryRowContext(ctx, `
		SELECT MAX(rowid), COUNT(*), MIN(timestamp)
		FROM brain_reflections WHERE rem_processed = 0`).Scan(&hw, &count, &earliestStr); err != nil {
		return result, fmt.Errorf("query unprocessed reflections: %w", err)
	}
	if count == 0 || !hw.Valid {
		return result, nil
	}
	result.EntriesProcessed = count

	windowStart := time.Now().Add(-time.Duration(r.reflectWindowDays()) * 24 * time.Hour)
	earliest := windowStart
	if earliestStr.Valid {
		if t, err := parseReflectionTime(earliestStr.String); err == nil && t.After(windowStart) {
			earliest = t
		}
	}

	// Step 2: tool+outcome stats since the earliest unprocessed entry, but no
	// further back than the retention window (a first run over months of
	// backlog would otherwise mint a cluster for every tool ever used).
	stats, err := store.QueryToolStats(ctx, earliest)
	if err != nil {
		return result, fmt.Errorf("query tool stats: %w", err)
	}

	// Step 3: clusters — tool+outcome groups with count >= 3.
	clusters := identifyClusters(stats, earliest)
	result.ClustersFound = len(clusters)

	// Step 4: consolidate SPAR pattern crossings. conduit-31jg.54
	promos, err := r.consolidatePatterns(ctx, store, windowStart, hw.Int64)
	if err != nil {
		return result, fmt.Errorf("consolidate patterns: %w", err)
	}
	result.PatternsPromoted = len(promos)
	for _, p := range promos {
		result.PromotedKeys = append(result.PromotedKeys, p.key)
	}

	// Step 5: heuristic score backfill (before marking, it reads the
	// unprocessed set).
	backfilled, err := r.backfillScores(ctx, hw.Int64, dryRun)
	if err != nil {
		return result, fmt.Errorf("backfill scores: %w", err)
	}
	result.ScoresBackfilled = backfilled

	if dryRun {
		return result, nil
	}

	// Step 6: write clusters and promoted patterns to Brain LTM.
	for _, cl := range clusters {
		key := fmt.Sprintf("reflect.clusters.%s.%s", cl.Tool, cl.Outcome)
		if err := r.brain.Store(ctx, key, formatClusterSummary(cl), brain.TierLongTerm, "rem:reflect"); err != nil {
			return result, fmt.Errorf("store cluster %s: %w", key, err)
		}
	}
	for _, p := range promos {
		if err := r.brain.Store(ctx, p.key, p.value, brain.TierLongTerm, patternSource); err != nil {
			return result, fmt.Errorf("store pattern %s: %w", p.key, err)
		}
	}

	// Step 7: mark the snapshot processed in one statement.
	if err := database.RetryOnBusy(5, func() error {
		_, err := r.db.ExecContext(ctx,
			`UPDATE brain_reflections SET rem_processed = 1 WHERE rem_processed = 0 AND rowid <= ?`, hw.Int64)
		return err
	}); err != nil {
		return result, fmt.Errorf("mark processed: %w", err)
	}

	return result, nil
}

func (r *REMCycle) reflectWindowDays() int {
	if r.config.PruneAgeDays > 0 {
		return r.config.PruneAgeDays
	}
	return defaultReflectWindowDays
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

// patternPromotion is one reflect.tools.* LTM entry to write.
type patternPromotion struct {
	key, value string
}

// patternGroup aggregates the pattern rows sharing one Brain key.
type patternGroup struct {
	key         string
	kind        string // "consecutive_failure" | "circular"
	tool        string
	occurrences int
	sessions    map[string]struct{}
	first, last time.Time
	lastInsight string
	failStreak  int  // consecutive failures at the pivot (RetryCount)
	hasNew      bool // at least one row in the current unprocessed snapshot
}

// consolidatePatterns turns SPAR TypePattern rows (pivot = a tool failed
// FailureTracker's threshold times in a row within one turn; circular = the
// same 2-3 call sequence repeated 3x) into one Brain LTM entry per pattern
// key (reflect.tools.<tool>.consecutive_failure, reflect.tools.circular.<hash>).
//
// Counts are recomputed over every pattern row in the window, processed or
// not, so repeated REM runs refresh a single entry instead of appending. A
// key is (re)written only when the current snapshot holds new evidence for it
// and it has recurred in >= patternMinSessions distinct sessions and
// >= patternMinOccurrences turns — a single bad turn never reaches LTM.
// conduit-31jg.54
func (r *REMCycle) consolidatePatterns(ctx context.Context, store *reflection.ReflectionStore, since time.Time, hw int64) ([]patternPromotion, error) {
	rows, err := store.QueryPatterns(ctx, since)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	// Which of these rows are in the current snapshot (unprocessed, rowid <=
	// hw)? Pattern rows are few, so an ID set is cheap.
	newIDs, err := r.unprocessedPatternIDs(ctx, hw)
	if err != nil {
		return nil, err
	}

	groups := make(map[string]*patternGroup)
	for _, e := range rows {
		key := patternKey(e)
		if key == "" {
			continue
		}
		g := groups[key]
		if g == nil {
			g = &patternGroup{key: key, sessions: make(map[string]struct{}), first: e.Timestamp}
			switch {
			case hasTag(e, "circular"):
				g.kind = "circular"
			default:
				g.kind = "consecutive_failure"
			}
			groups[key] = g
		}
		g.occurrences++
		g.sessions[e.SessionKey] = struct{}{}
		if e.Tool != "" {
			g.tool = e.Tool
		}
		if e.RetryCount > g.failStreak {
			g.failStreak = e.RetryCount
		}
		if !e.Timestamp.Before(g.last) {
			g.last = e.Timestamp
			if e.Insight != "" {
				g.lastInsight = e.Insight
			}
		}
		if e.Timestamp.Before(g.first) {
			g.first = e.Timestamp
		}
		if _, ok := newIDs[e.ID]; ok {
			g.hasNew = true
		}
	}

	var out []patternPromotion
	for _, g := range groups {
		if !g.hasNew || len(g.sessions) < patternMinSessions || g.occurrences < patternMinOccurrences {
			continue
		}
		out = append(out, patternPromotion{key: g.key, value: formatPattern(g)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out, nil
}

func (r *REMCycle) unprocessedPatternIDs(ctx context.Context, hw int64) (map[string]struct{}, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id FROM brain_reflections
		WHERE type = ? AND rem_processed = 0 AND rowid <= ?`, string(reflection.TypePattern), hw)
	if err != nil {
		return nil, fmt.Errorf("query unprocessed patterns: %w", err)
	}
	defer rows.Close()
	ids := make(map[string]struct{})
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = struct{}{}
	}
	return ids, rows.Err()
}

// patternKey is the Brain key a pattern row consolidates into: the related
// key recorded by the SPAR hook, or one derived from the tool for rows that
// predate it.
func patternKey(e *reflection.ReflectionEntry) string {
	for _, k := range e.RelatedKeys {
		if strings.HasPrefix(k, "reflect.tools.") {
			return k
		}
	}
	if e.Tool != "" && hasTag(e, "consecutive_failure") {
		return reflection.ConsecutiveFailureKey(e.Tool)
	}
	return ""
}

func hasTag(e *reflection.ReflectionEntry, tag string) bool {
	for _, t := range e.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

// formatPattern renders a promoted pattern as one Situation Awareness line
// (<= maxPatternValueLen bytes, detail truncated first).
func formatPattern(g *patternGroup) string {
	span := g.first.Local().Format("Jan 2")
	if last := g.last.Local().Format("Jan 2"); last != span {
		span += "–" + last
	}
	var head, detail string
	switch g.kind {
	case "circular":
		loop := strings.TrimPrefix(g.lastInsight, "circular tool-call pattern: ")
		head = fmt.Sprintf("Circular tool loop (%s) in %d turns across %d sessions (%s).",
			loop, g.occurrences, len(g.sessions), span)
		detail = " Change approach instead of repeating the same calls."
	default:
		tool := g.tool
		if tool == "" {
			tool = strings.TrimSuffix(strings.TrimPrefix(g.key, "reflect.tools."), ".consecutive_failure")
		}
		streak := "repeatedly"
		if g.failStreak > 1 {
			streak = fmt.Sprintf("%d+ times", g.failStreak)
		}
		head = fmt.Sprintf("%s failed %s in a row in %d turns across %d sessions (%s).",
			tool, streak, g.occurrences, len(g.sessions), span)
		if msg := strings.Join(strings.Fields(g.lastInsight), " "); msg != "" {
			detail = " Last error: " + msg
		}
	}
	return truncateUTF8(head+detail, maxPatternValueLen)
}

// truncateUTF8 cuts s to at most n bytes on a rune boundary, marking the cut.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	const ellipsis = "…"
	cut := n - len(ellipsis)
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + ellipsis
}

func parseReflectionTime(s string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized timestamp %q", s)
}

// backfillScores assigns heuristic scores to TypeSessionSummary entries in
// the unprocessed snapshot (rowid <= hw) that have score=0, judging each by
// the session's unprocessed tool outcomes: 0 failures → 4, some → 2, all → 1.
// Summaries whose session has no tool outcomes are left unscored.
func (r *REMCycle) backfillScores(ctx context.Context, hw int64, dryRun bool) (int, error) {
	// Aggregate tool outcomes per session once (a correlated join per
	// summary let the planner rescan the whole unprocessed set for each of
	// ~2.6k summaries on the live DB), then attach to the summaries.
	rows, err := r.db.QueryContext(ctx, `
		WITH summaries AS (
			SELECT id, session_key FROM brain_reflections
			WHERE type = ? AND score = 0 AND rem_processed = 0 AND rowid <= ?
		), sess AS (
			SELECT session_key,
			       COUNT(*) AS total,
			       SUM(CASE WHEN outcome IN (?, ?) THEN 1 ELSE 0 END) AS failures
			FROM brain_reflections
			WHERE type = ? AND rem_processed = 0 AND rowid <= ?
			  AND session_key IN (SELECT session_key FROM summaries)
			GROUP BY session_key
		)
		SELECT summaries.id, COALESCE(sess.total, 0), COALESCE(sess.failures, 0)
		FROM summaries LEFT JOIN sess ON sess.session_key = summaries.session_key`,
		string(reflection.TypeSessionSummary), hw,
		string(reflection.OutcomeFailure), string(reflection.OutcomeTimeout),
		string(reflection.TypeToolOutcome), hw)
	if err != nil {
		return 0, fmt.Errorf("query unscored summaries: %w", err)
	}
	type scored struct {
		id    string
		score int
	}
	var todo []scored
	for rows.Next() {
		var id string
		var total, failures int
		if err := rows.Scan(&id, &total, &failures); err != nil {
			rows.Close()
			return 0, err
		}
		var score int
		switch {
		case total == 0:
			continue // no tool outcomes to judge
		case failures == 0:
			score = 4
		case failures < total:
			score = 2
		default:
			score = 1
		}
		todo = append(todo, scored{id, score})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if dryRun {
		return len(todo), nil
	}
	for i, s := range todo {
		if _, err := r.db.ExecContext(ctx,
			"UPDATE brain_reflections SET score = ? WHERE id = ?", s.score, s.id); err != nil {
			return i, fmt.Errorf("backfill score for %s: %w", s.id, err)
		}
	}
	return len(todo), nil
}

// reflectSummary returns a human-readable summary line for the report log.
func reflectSummary(r *ReflectResult) string {
	if r == nil {
		return "Reflect: not run"
	}
	parts := []string{
		fmt.Sprintf("entries processed: %d", r.EntriesProcessed),
		fmt.Sprintf("clusters found: %d", r.ClustersFound),
		fmt.Sprintf("patterns promoted: %d", r.PatternsPromoted),
		fmt.Sprintf("scores backfilled: %d", r.ScoresBackfilled),
	}
	return "Reflect: " + strings.Join(parts, ", ")
}
