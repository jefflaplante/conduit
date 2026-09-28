package brain

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"conduit/internal/database"
)

// defaultContextWeight is the ranking boost applied per entry whose key or
// value contains any token from the optional recall context. The entry's
// blended score is multiplied by (1 + defaultContextWeight) when any context
// token overlaps. See RecallWithContext.
const defaultContextWeight = 0.3

func (b *Brain) Recall(ctx context.Context, query string, limit int) ([]*Entry, error) {
	return b.RecallWithContext(ctx, query, limit, "")
}

// RecallWithContext performs the same fuzzy recall as Recall but accepts an
// optional context string. If context is non-empty, entries whose key or value
// contain any context token (case-insensitive, same tokenization as the query)
// have their final score boosted by (1 + defaultContextWeight). Context never
// filters results — it only re-ranks. An empty context is identical to Recall.
func (b *Brain) RecallWithContext(ctx context.Context, query string, limit int, contextStr string) ([]*Entry, error) {
	if limit <= 0 {
		limit = 20
	}

	terms := TokenizeQuery(query)
	if len(terms) == 0 {
		return nil, nil
	}

	// Tokenize the optional context — reused for keyword-overlap boost during ranking.
	var contextTerms []string
	if contextStr != "" {
		contextTerms = TokenizeQuery(contextStr)
	}

	type scoredEntry struct {
		entry      *Entry
		matchScore float64
	}

	var scored []scoredEntry
	seen := make(map[string]bool)

	userID := userIDFromCtx(ctx)
	parentID := parentUserIDFromCtx(ctx)
	now := time.Now()

	// conduit-31jg.32: match, bump access and snapshot our own WM hits in a
	// single write-locked pass. Only copies go into `scored`: the sort below
	// runs without the lock and the entries are returned to callers, while
	// autoFlush/Consolidate/Store keep mutating the live entries under b.mu.
	b.mu.Lock()
	if wm, ok := b.working[userID]; ok {
		for _, entry := range wm {
			if entry.ExpiresAt != nil && !entry.ExpiresAt.After(now) {
				continue
			}
			if ms := queryMatchScore(entry, terms); ms > 0 {
				entry.AccessedAt = now
				entry.AccessCount++
				entry.Salience = b.computeSalience(entry)
				copied := *entry
				scored = append(scored, scoredEntry{&copied, ms})
				seen[entry.Key] = true
			}
		}
	}
	// Include parent's WM, then the shared bucket (read-only copies, deduped
	// by key). conduit-31jg.30
	for _, bucket := range readOnlyBuckets(userID, parentID) {
		for _, entry := range b.working[bucket] {
			if entry.ExpiresAt != nil && !entry.ExpiresAt.After(now) {
				continue
			}
			if !seen[entry.Key] {
				if ms := queryMatchScore(entry, terms); ms > 0 {
					copied := *entry
					scored = append(scored, scoredEntry{&copied, ms})
					seen[entry.Key] = true
				}
			}
		}
	}
	b.mu.Unlock()

	// Build OR-joined SQL query with per-term match counting.
	var whereClauses []string
	var matchExprs []string
	var whereArgs []interface{}
	var matchArgs []interface{}
	// conduit-31jg.31: terms are LIKE-escaped (%, _ and \ literal) so a
	// query like "100%" doesn't become a wildcard pattern.
	for _, term := range terms {
		pat := likeContains(term)
		whereClauses = append(whereClauses, `(LOWER(key) LIKE ? ESCAPE '\' OR LOWER(value) LIKE ? ESCAPE '\')`)
		whereArgs = append(whereArgs, pat, pat)
		matchExprs = append(matchExprs, `(CASE WHEN LOWER(key) LIKE ? ESCAPE '\' OR LOWER(value) LIKE ? ESCAPE '\' THEN 1 ELSE 0 END)`)
		matchArgs = append(matchArgs, pat, pat)
	}

	sqlQuery := fmt.Sprintf(
		`SELECT key, value, created_at, accessed_at, access_count, %s AS eff_salience, source, stale, expires_at, warmth,
		(%s) AS match_count
		FROM brain_ltm WHERE (%s) AND (expires_at IS NULL OR expires_at > strftime('%%Y-%%m-%%d %%H:%%M:%%f', 'now'))
		ORDER BY match_count DESC, eff_salience + warmth DESC LIMIT ?`,
		b.EffectiveSalienceSQL(), // conduit-31jg.53: recency at query time
		strings.Join(matchExprs, " + "),
		strings.Join(whereClauses, " OR "),
	)
	args := append(matchArgs, whereArgs...)
	args = append(args, limit)

	rows, err := b.db.Query(sqlQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("recall LTM: %w", err)
	}
	defer rows.Close()
	var ltmHitKeys []string
	for rows.Next() {
		entry := &Entry{Tier: TierLongTerm}
		var matchCount int
		var staleInt int
		var expiresAtNT sql.NullTime
		if err := rows.Scan(&entry.Key, &entry.Value, &entry.CreatedAt, &entry.AccessedAt,
			&entry.AccessCount, &entry.Salience, &entry.Source, &staleInt, &expiresAtNT, &entry.Warmth, &matchCount); err != nil {
			continue
		}
		entry.Stale = staleInt != 0
		if expiresAtNT.Valid {
			t := expiresAtNT.Time
			entry.ExpiresAt = &t
		}
		if !seen[entry.Key] {
			ms := float64(matchCount) / float64(len(terms))
			scored = append(scored, scoredEntry{entry, ms})
			seen[entry.Key] = true
			ltmHitKeys = append(ltmHitKeys, entry.Key)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil { // conduit-31jg.31
		return nil, fmt.Errorf("recall LTM rows: %w", err)
	}

	// Bump access_count/accessed_at for the LTM rows we matched (best-effort).
	// Batched UPDATE keeps lock contention minimal; RetryOnBusy for robustness.
	if len(ltmHitKeys) > 0 {
		placeholders := strings.Repeat("?,", len(ltmHitKeys))
		placeholders = placeholders[:len(placeholders)-1]
		updateSQL := fmt.Sprintf(
			"UPDATE brain_ltm SET access_count = access_count + 1, accessed_at = ? WHERE key IN (%s)",
			placeholders,
		)
		nowStr := time.Now().UTC().Format("2006-01-02 15:04:05")
		updateArgs := make([]interface{}, 0, len(ltmHitKeys)+1)
		updateArgs = append(updateArgs, nowStr)
		for _, k := range ltmHitKeys {
			updateArgs = append(updateArgs, k)
		}
		_ = database.RetryOnBusy(5, func() error {
			_, err := b.db.Exec(updateSQL, updateArgs...)
			return err
		})
	}

	// Cluster expansion: when spreading is enabled and we have LTM hits,
	// expand the result set with namespace-clustered neighbours. These entries
	// don't match the query keywords but share a namespace prefix with direct
	// matches. They get a matchScore of 0 but their warmth (from spreading
	// activation) gives them a natural ranking boost.
	if b.spreadingEnabled && len(ltmHitKeys) > 0 {
		clusterEntries, err := b.clusterNeighbours(ltmHitKeys, seen, defaultClusterConfig)
		if err == nil {
			var added int
			for _, ce := range clusterEntries {
				if !seen[ce.Key] {
					ce.ClusterHit = true
					scored = append(scored, scoredEntry{entry: ce, matchScore: 0.0})
					seen[ce.Key] = true
					added++
				}
			}
			// Metrics: count direct vs cluster hits for the hit-rate denominator.
			if direct := len(ltmHitKeys); direct > 0 || added > 0 {
				b.mu.Lock()
				b.directHitCount += int64(direct)
				b.clusterHitCount += int64(added)
				b.mu.Unlock()
			}
		}
		// Cluster expansion failure is non-fatal — continue with direct results.
	}

	// Sort by blended score. Weights default to 0.5/0.3/0.2 (match/salience/warmth)
	// but are configurable via WithMatchWeight / WithSalienceWeight / WithWarmthWeight.
	// An optional context-overlap boost of (1 + defaultContextWeight) applies last.
	// The access bonus (accessBonusWeight) rewards frequently-accessed entries
	// with exponential time decay, computed dynamically at recall time.
	mw, sw, ww, abw := b.matchWeight, b.salienceWeight, b.warmthWeight, b.accessBonusWeight
	sort.Slice(scored, func(i, j int) bool {
		si := (scored[i].matchScore * mw) + (scored[i].entry.Salience * sw) + (scored[i].entry.Warmth * ww) + (b.computeAccessBonus(scored[i].entry) * abw)
		sj := (scored[j].matchScore * mw) + (scored[j].entry.Salience * sw) + (scored[j].entry.Warmth * ww) + (b.computeAccessBonus(scored[j].entry) * abw)
		if len(contextTerms) > 0 {
			if entryMatchesAnyTerm(scored[i].entry, contextTerms) {
				si *= 1 + defaultContextWeight
			}
			if entryMatchesAnyTerm(scored[j].entry, contextTerms) {
				sj *= 1 + defaultContextWeight
			}
		}
		return si > sj
	})

	if len(scored) > limit {
		scored = scored[:limit]
	}
	results := make([]*Entry, len(scored))
	for i, s := range scored {
		results[i] = s.entry
	}

	// Warmth-floor injection: append up to warmthInjectLimit high-warmth LTM
	// entries that didn't match the query keywords. Tail placement — keyword
	// and cluster hits always outrank injected entries. Only fires when the
	// query already produced results (guard against polluting miss-driven
	// queries, mirroring the cluster-expansion gating).
	results = b.injectWarmEntries(ctx, results, seen, limit)

	// Spread activation from the top results (limit to top 3 to avoid cascade).
	if len(results) > 0 {
		topN := 3
		if len(results) < topN {
			topN = len(results)
		}
		spreadKeys := make([]string, 0, topN)
		for i := 0; i < topN; i++ {
			if results[i].Tier == TierLongTerm {
				spreadKeys = append(spreadKeys, results[i].Key)
			}
		}
		if len(spreadKeys) > 0 {
			_ = b.spreadActivation(spreadKeys)
		}
	}

	// Log recall event for brain_spread reinforcement (best-effort)
	b.logRecallEvent(query, results)

	return results, nil
}

// injectWarmEntries appends up to b.warmthInjectLimit high-warmth LTM entries
// that did not match the query keywords, at the tail of the results. Only
// fires when the query already returned >=1 result (guard against polluting
// miss-driven queries) and when spreading is enabled. Best-effort: any error
// returns the unmodified results — warmth injection must never fail recall.
func (b *Brain) injectWarmEntries(ctx context.Context, results []*Entry, seen map[string]bool, limit int) []*Entry {
	if !b.spreadingEnabled || b.warmthInjectLimit <= 0 || b.warmthInjectFloor <= 0 || len(results) == 0 {
		return results
	}
	// Don't grow the result set beyond the caller's limit: inject only while
	// there's headroom. With the default floor of 0.7 and per-flush decay of
	// 0.85, few entries qualify — the overfetch covers rows lost to `seen`.
	rows, err := b.db.QueryContext(ctx, `
		SELECT key, value, created_at, accessed_at, access_count, `+b.EffectiveSalienceSQL()+`, source, stale, expires_at, warmth
		  FROM brain_ltm
		 WHERE warmth >= ? AND (expires_at IS NULL OR expires_at > strftime('%Y-%m-%d %H:%M:%f','now'))
		 ORDER BY warmth DESC LIMIT ?`, b.warmthInjectFloor, b.warmthInjectLimit+len(results))
	if err != nil {
		return results // best-effort, never fails recall
	}
	defer rows.Close()
	added := 0
	headroom := b.warmthInjectLimit
	if len(results)+added+headroom > limit {
		headroom = limit - len(results)
	}
	for rows.Next() && added < headroom {
		e := &Entry{Tier: TierLongTerm}
		var staleInt int
		var exp sql.NullTime
		if err := rows.Scan(&e.Key, &e.Value, &e.CreatedAt, &e.AccessedAt, &e.AccessCount,
			&e.Salience, &e.Source, &staleInt, &exp, &e.Warmth); err != nil {
			continue
		}
		e.Stale = staleInt != 0
		if exp.Valid {
			t := exp.Time
			e.ExpiresAt = &t
		}
		if seen[e.Key] {
			continue
		}
		e.WarmthHit = true
		results = append(results, e)
		seen[e.Key] = true
		added++
	}
	if added > 0 {
		b.mu.Lock()
		b.warmthHitCount += int64(added)
		b.mu.Unlock()
	}
	return results
}

func (b *Brain) computeSalience(e *Entry) float64 {
	accessScore := math.Min(float64(e.AccessCount)/float64(b.accessCountCap), 1.0)
	hoursSince := time.Since(e.AccessedAt).Hours()
	recencyScore := 1.0 / (1.0 + hoursSince*b.recencyDecayRate)
	var tierScore float64
	switch e.Tier {
	case TierLongTerm:
		tierScore = 0.8
	case TierWorking:
		tierScore = 0.5
	default:
		tierScore = 0.1
	}
	return (accessScore * b.accessWeight) + (recencyScore * b.recencyWeight) + (tierScore * b.tierWeight)
}

// computeAccessBonus calculates a decaying access bonus for an entry at recall
// time. The formula is:
//
//	bonus = min(cap, access_count * alpha * decay^days_since_last_access)
//
// This rewards frequently-accessed entries with exponential time decay, so
// recent accesses matter more than ancient ones. Unlike salience (which bakes
// in access frequency at write time), the access bonus is computed dynamically
// during recall ranking, giving a real-time signal of entry usefulness.
//
// Example with defaults (alpha=0.1, cap=0.3, decay=0.95):
//   - 10 accesses, 0 days ago: min(0.3, 1.0) = 0.3 (capped)
//   - 10 accesses, 7 days ago: min(0.3, 0.697) = 0.297
//   - 5 accesses, 0 days ago: min(0.3, 0.5) = 0.3 (capped)
//   - 3 accesses, 1 day ago: min(0.3, 0.285) = 0.285
//   - 1 access, 30 days ago: min(0.3, 0.022) = 0.022
func (b *Brain) computeAccessBonus(e *Entry) float64 {
	if e.AccessCount <= 0 {
		return 0.0
	}
	daysSince := time.Since(e.AccessedAt).Hours() / 24.0
	if daysSince < 0 {
		daysSince = 0
	}
	raw := float64(e.AccessCount) * b.accessBonusAlpha * math.Pow(b.accessBonusDecay, daysSince)
	return math.Min(b.accessBonusCap, raw)
}

// entryMatchesAnyTerm reports whether any of the given tokens appears in the
// entry's key or value (case-insensitive substring match). Used to decide
// whether to apply the context-overlap rerank boost.
func entryMatchesAnyTerm(e *Entry, terms []string) bool {
	if len(terms) == 0 {
		return false
	}
	keyLower := strings.ToLower(e.Key)
	valueLower := strings.ToLower(e.Value)
	for _, term := range terms {
		if strings.Contains(keyLower, term) || strings.Contains(valueLower, term) {
			return true
		}
	}
	return false
}

// EscapeLike escapes s for use inside a LIKE pattern with ESCAPE '\':
// %, _ and \ become literal. conduit-31jg.31
func EscapeLike(s string) string {
	if !strings.ContainsAny(s, `%_\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 4)
	for _, r := range s {
		if r == '%' || r == '_' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// likeContains returns a LIKE ... ESCAPE '\' pattern matching s anywhere.
func likeContains(s string) string { return "%" + EscapeLike(s) + "%" }

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// queryMatchScore returns the fraction of query terms found in the entry's key or value.
// Returns 0.0 if no terms match, up to 1.0 if all terms match.
func queryMatchScore(e *Entry, terms []string) float64 {
	if len(terms) == 0 {
		return 0
	}
	keyLower := strings.ToLower(e.Key)
	valueLower := strings.ToLower(e.Value)
	matched := 0
	for _, term := range terms {
		if strings.Contains(keyLower, term) || strings.Contains(valueLower, term) {
			matched++
		}
	}
	return float64(matched) / float64(len(terms))
}
