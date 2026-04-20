package brain

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"conduit/internal/database"
	_ "modernc.org/sqlite"
)

type Tier string

const (
	TierLongTerm Tier = "longterm"
	TierWorking  Tier = "working"
	TierScratch  Tier = "scratch"
)

type Entry struct {
	Key         string    `json:"key"`
	Value       string    `json:"value"`
	Tier        Tier      `json:"tier"`
	CreatedAt   time.Time `json:"created_at"`
	AccessedAt  time.Time `json:"accessed_at"`
	AccessCount int       `json:"access_count"`
	Salience    float64   `json:"salience"`
	Source      string    `json:"source,omitempty"`
	Stale       bool      `json:"stale,omitempty"`
}

type ConsolidationReport struct {
	PromotedCount int      `json:"promoted_count"`
	EvictedCount  int      `json:"evicted_count"`
	LTMSize       int      `json:"ltm_size"`
	PromotedKeys  []string `json:"promoted_keys,omitempty"`
	EvictedKeys   []string `json:"evicted_keys,omitempty"`
}

type Status struct {
	LTMEntries   int      `json:"ltm_entries"`
	WMEntries    int      `json:"wm_entries"`
	ScratchDepth int      `json:"scratch_depth"`
	AvgSalience  float64  `json:"avg_salience,omitempty"`
	HottestKeys  []string `json:"hottest_keys,omitempty"`
}

type Option func(*Brain)

func WithMaxLTMEntries(n int) Option               { return func(b *Brain) { b.maxLTMEntries = n } }
func WithAutoFlushInterval(d time.Duration) Option { return func(b *Brain) { b.autoFlushInterval = d } }
func WithConsolidateThreshold(t float64) Option    { return func(b *Brain) { b.consolidateThreshold = t } }
func WithEvictThreshold(t float64) Option          { return func(b *Brain) { b.evictThreshold = t } }
func WithAutoPromote(v bool) Option                { return func(b *Brain) { b.autoPromote = v } }
func WithWMGracePeriod(d time.Duration) Option     { return func(b *Brain) { b.wmGracePeriod = d } }
func WithAccessWeight(w float64) Option            { return func(b *Brain) { b.accessWeight = w } }
func WithRecencyWeight(w float64) Option           { return func(b *Brain) { b.recencyWeight = w } }
func WithTierWeight(w float64) Option              { return func(b *Brain) { b.tierWeight = w } }
func WithRecencyDecayRate(r float64) Option        { return func(b *Brain) { b.recencyDecayRate = r } }
func WithAccessCountCap(n int) Option              { return func(b *Brain) { b.accessCountCap = n } }

type Brain struct {
	mu      sync.RWMutex
	working map[string]map[string]*Entry // userID -> key -> entry
	scratch map[string][]string          // userID -> LIFO stack
	db      *sql.DB

	maxLTMEntries        int
	autoFlushInterval    time.Duration
	consolidateThreshold float64
	evictThreshold       float64
	autoPromote          bool
	wmGracePeriod        time.Duration
	accessWeight         float64
	recencyWeight        float64
	tierWeight           float64
	recencyDecayRate     float64
	accessCountCap       int

	stopCh chan struct{}
	wg     sync.WaitGroup
}

func New(dbPath string, opts ...Option) (*Brain, error) {
	db, err := sql.Open("sqlite", database.BuildDSN(dbPath))
	if err != nil {
		return nil, fmt.Errorf("open brain database: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(0)

	if err := runMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("brain migrations: %w", err)
	}

	b := &Brain{
		working:              make(map[string]map[string]*Entry),
		scratch:              make(map[string][]string),
		db:                   db,
		maxLTMEntries:        10000,
		autoFlushInterval:    10 * time.Minute,
		consolidateThreshold: 0.6,
		evictThreshold:       0.1,
		autoPromote:          true,
		wmGracePeriod:        5 * time.Minute,
		accessWeight:         0.4,
		recencyWeight:        0.4,
		tierWeight:           0.2,
		recencyDecayRate:     1.0,
		accessCountCap:       100,
		stopCh:               make(chan struct{}),
	}
	for _, opt := range opts {
		opt(b)
	}
	b.startAutoFlush()
	log.Printf("Brain initialized at %s", dbPath)
	return b, nil
}

func (b *Brain) Store(ctx context.Context, key, value string, tier Tier, source string) error {
	if err := ValidateSource(source); err != nil {
		log.Printf("Brain: warning: %v (key=%q)", err, key)
	}
	now := time.Now()
	switch tier {
	case TierLongTerm:
		return b.storeLTM(key, value, source, now)
	case TierWorking:
		userID := userIDFromCtx(ctx)
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.working[userID] == nil {
			b.working[userID] = make(map[string]*Entry)
		}
		if existing, ok := b.working[userID][key]; ok {
			existing.Value = value
			existing.AccessedAt = now
			existing.AccessCount++
			existing.Source = source
			existing.Salience = b.computeSalience(existing)
		} else {
			b.working[userID][key] = &Entry{
				Key: key, Value: value, Tier: TierWorking,
				CreatedAt: now, AccessedAt: now,
				AccessCount: 1, Salience: 0.5, Source: source,
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported tier for store: %s (use Push for scratch)", tier)
	}
}

// BulkEntry is a single key/value entry for StoreBulk. Tier defaults to
// TierWorking when empty; TierScratch is rejected. Source is optional.
type BulkEntry struct {
	Key    string
	Value  string
	Tier   Tier
	Source string
}

// StoreBulk stores many entries atomically. Long-term entries are written in
// a single SQL transaction (so a failure on any entry rolls back all of the
// LTM writes in the batch); working-memory entries are then applied under the
// brain lock. If the LTM transaction fails, no entries — LTM or WM — are
// persisted. TierScratch is not a valid bulk target and causes an error.
func (b *Brain) StoreBulk(ctx context.Context, entries []BulkEntry) error {
	if len(entries) == 0 {
		return nil
	}

	// Normalize + validate up-front so we never partially apply.
	normalized := make([]BulkEntry, len(entries))
	for i, e := range entries {
		if e.Key == "" {
			return fmt.Errorf("bulk entry %d: key is required", i)
		}
		tier := e.Tier
		if tier == "" {
			tier = TierWorking
		}
		switch tier {
		case TierLongTerm, TierWorking:
			// ok
		case TierScratch:
			return fmt.Errorf("bulk entry %d: scratch tier is not a valid bulk target", i)
		default:
			return fmt.Errorf("bulk entry %d: unsupported tier %q", i, tier)
		}
		if err := ValidateSource(e.Source); err != nil {
			// Match Store(): warn but do not fail on weird sources.
			log.Printf("Brain.StoreBulk: warning: %v (key=%q)", err, e.Key)
		}
		normalized[i] = BulkEntry{Key: e.Key, Value: e.Value, Tier: tier, Source: e.Source}
	}

	now := time.Now()
	nowStr := now.UTC().Format("2006-01-02 15:04:05")

	// Partition so we can commit LTM atomically inside a single tx.
	var ltmBatch []BulkEntry
	var wmBatch []BulkEntry
	for _, e := range normalized {
		if e.Tier == TierLongTerm {
			ltmBatch = append(ltmBatch, e)
		} else {
			wmBatch = append(wmBatch, e)
		}
	}

	if len(ltmBatch) > 0 {
		err := database.RetryOnBusy(5, func() error {
			tx, err := b.db.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			stmt, err := tx.PrepareContext(ctx, `
				INSERT INTO brain_ltm (key, value, source, created_at, accessed_at, access_count, salience)
				VALUES (?, ?, ?, ?, ?, 1, 0.5)
				ON CONFLICT(key) DO UPDATE SET
					value = excluded.value,
					source = excluded.source,
					accessed_at = excluded.accessed_at,
					access_count = access_count + 1,
					salience = COALESCE(
						(MIN(CAST(access_count + 1 AS REAL) / CAST(? AS REAL), 1.0) * ?) +
						(1.0 / (1.0 + 0.0)) * ? +
						(0.8 * ?),
						salience, 0.5)
			`)
			if err != nil {
				tx.Rollback()
				return err
			}
			defer stmt.Close()
			for _, e := range ltmBatch {
				if _, err := stmt.ExecContext(ctx, e.Key, e.Value, e.Source, nowStr, nowStr,
					b.accessCountCap, b.accessWeight, b.recencyWeight, b.tierWeight); err != nil {
					tx.Rollback()
					return err
				}
			}
			return tx.Commit()
		})
		if err != nil {
			return fmt.Errorf("store bulk LTM: %w", err)
		}

		// Best-effort eviction — same pattern as storeLTM.
		if b.maxLTMEntries > 0 {
			_ = database.RetryOnBusy(5, func() error {
				_, err := b.db.Exec(`DELETE FROM brain_ltm WHERE key IN (
					SELECT key FROM brain_ltm ORDER BY salience ASC
					LIMIT MAX(0, (SELECT COUNT(*) FROM brain_ltm) - ?))`, b.maxLTMEntries)
				return err
			})
		}
	}

	if len(wmBatch) > 0 {
		userID := userIDFromCtx(ctx)
		b.mu.Lock()
		if b.working[userID] == nil {
			b.working[userID] = make(map[string]*Entry)
		}
		for _, e := range wmBatch {
			if existing, ok := b.working[userID][e.Key]; ok {
				existing.Value = e.Value
				existing.AccessedAt = now
				existing.AccessCount++
				existing.Source = e.Source
				existing.Salience = b.computeSalience(existing)
			} else {
				b.working[userID][e.Key] = &Entry{
					Key: e.Key, Value: e.Value, Tier: TierWorking,
					CreatedAt: now, AccessedAt: now,
					AccessCount: 1, Salience: 0.5, Source: e.Source,
				}
			}
		}
		b.mu.Unlock()
	}

	return nil
}

func (b *Brain) storeLTM(key, value, source string, now time.Time) error {
	nowStr := now.UTC().Format("2006-01-02 15:04:05")
	// Use a simple default salience for upsert; the exact salience is recomputed on access.
	// The ON CONFLICT UPDATE preserves the existing salience bumped slightly for the access.
	err := database.RetryOnBusy(5, func() error {
		_, err := b.db.Exec(`
			INSERT INTO brain_ltm (key, value, source, created_at, accessed_at, access_count, salience)
			VALUES (?, ?, ?, ?, ?, 1, 0.5)
			ON CONFLICT(key) DO UPDATE SET
				value = excluded.value,
				source = excluded.source,
				accessed_at = excluded.accessed_at,
				access_count = access_count + 1,
				salience = COALESCE(
					(MIN(CAST(access_count + 1 AS REAL) / CAST(? AS REAL), 1.0) * ?) +
					(1.0 / (1.0 + 0.0)) * ? +
					(0.8 * ?),
					salience, 0.5)
		`, key, value, source, nowStr, nowStr,
			b.accessCountCap, b.accessWeight, b.recencyWeight, b.tierWeight)
		return err
	})
	if err != nil {
		return fmt.Errorf("store LTM: %w", err)
	}
	if b.maxLTMEntries > 0 {
		// Best-effort eviction — retry on BUSY so heartbeat-paced writers don't hit the 5s cap.
		_ = database.RetryOnBusy(5, func() error {
			_, err := b.db.Exec(`DELETE FROM brain_ltm WHERE key IN (
				SELECT key FROM brain_ltm ORDER BY salience ASC
				LIMIT MAX(0, (SELECT COUNT(*) FROM brain_ltm) - ?))`, b.maxLTMEntries)
			return err
		})
	}
	return nil
}

func (b *Brain) Get(ctx context.Context, key string) (*Entry, error) {
	userID := userIDFromCtx(ctx)
	b.mu.RLock()
	if wm, ok := b.working[userID]; ok {
		if entry, ok := wm[key]; ok {
			b.mu.RUnlock()
			b.mu.Lock()
			entry.AccessedAt = time.Now()
			entry.AccessCount++
			entry.Salience = b.computeSalience(entry)
			b.mu.Unlock()
			return entry, nil
		}
	}
	// Check parent's WM (read-only — return a copy, no access bump)
	parentID := parentUserIDFromCtx(ctx)
	if parentID != "" && parentID != userID {
		if parentWM, ok := b.working[parentID]; ok {
			if entry, ok := parentWM[key]; ok {
				b.mu.RUnlock()
				copied := *entry
				return &copied, nil
			}
		}
	}
	b.mu.RUnlock()
	return b.getLTM(key)
}

func (b *Brain) getLTM(key string) (*Entry, error) {
	row := b.db.QueryRow(`
		UPDATE brain_ltm SET
			accessed_at = datetime('now'),
			access_count = access_count + 1,
			salience = COALESCE(
				(MIN(CAST(access_count + 1 AS REAL) / CAST(? AS REAL), 1.0) * ?) +
				(1.0 / (1.0 + 0.0)) * ? +
				(0.8 * ?),
				salience, 0.5)
		WHERE key = ?
		RETURNING key, value, created_at, accessed_at, access_count, salience, source, stale
	`, b.accessCountCap, b.accessWeight, b.recencyWeight, b.tierWeight, key)
	entry := &Entry{Tier: TierLongTerm}
	var staleInt int
	err := row.Scan(&entry.Key, &entry.Value, &entry.CreatedAt, &entry.AccessedAt,
		&entry.AccessCount, &entry.Salience, &entry.Source, &staleInt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get LTM: %w", err)
	}
	entry.Stale = staleInt != 0
	return entry, nil
}

func (b *Brain) Recall(ctx context.Context, query string, limit int) ([]*Entry, error) {
	if limit <= 0 {
		limit = 20
	}

	terms := TokenizeQuery(query)
	if len(terms) == 0 {
		return nil, nil
	}

	type scoredEntry struct {
		entry      *Entry
		matchScore float64
	}

	var scored []scoredEntry
	seen := make(map[string]bool)

	userID := userIDFromCtx(ctx)
	parentID := parentUserIDFromCtx(ctx)

	b.mu.RLock()
	if wm, ok := b.working[userID]; ok {
		for _, entry := range wm {
			if ms := queryMatchScore(entry, terms); ms > 0 {
				scored = append(scored, scoredEntry{entry, ms})
				seen[entry.Key] = true
			}
		}
	}
	// Include parent's WM entries (read-only copies, deduped by key)
	if parentID != "" && parentID != userID {
		if parentWM, ok := b.working[parentID]; ok {
			for _, entry := range parentWM {
				if !seen[entry.Key] {
					if ms := queryMatchScore(entry, terms); ms > 0 {
						copied := *entry
						scored = append(scored, scoredEntry{&copied, ms})
						seen[entry.Key] = true
					}
				}
			}
		}
	}
	b.mu.RUnlock()

	// Build OR-joined SQL query with per-term match counting.
	var whereClauses []string
	var matchExprs []string
	var whereArgs []interface{}
	var matchArgs []interface{}
	for _, term := range terms {
		whereClauses = append(whereClauses, "(LOWER(key) LIKE ? OR LOWER(value) LIKE ?)")
		whereArgs = append(whereArgs, "%"+term+"%", "%"+term+"%")
		matchExprs = append(matchExprs, "(CASE WHEN LOWER(key) LIKE ? OR LOWER(value) LIKE ? THEN 1 ELSE 0 END)")
		matchArgs = append(matchArgs, "%"+term+"%", "%"+term+"%")
	}

	sql := fmt.Sprintf(
		`SELECT key, value, created_at, accessed_at, access_count, salience, source, stale,
		(%s) AS match_count
		FROM brain_ltm WHERE %s
		ORDER BY match_count DESC, salience DESC LIMIT ?`,
		strings.Join(matchExprs, " + "),
		strings.Join(whereClauses, " OR "),
	)
	args := append(matchArgs, whereArgs...)
	args = append(args, limit)

	rows, err := b.db.Query(sql, args...)
	if err != nil {
		return nil, fmt.Errorf("recall LTM: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		entry := &Entry{Tier: TierLongTerm}
		var matchCount int
		var staleInt int
		if err := rows.Scan(&entry.Key, &entry.Value, &entry.CreatedAt, &entry.AccessedAt,
			&entry.AccessCount, &entry.Salience, &entry.Source, &staleInt, &matchCount); err != nil {
			continue
		}
		entry.Stale = staleInt != 0
		if !seen[entry.Key] {
			ms := float64(matchCount) / float64(len(terms))
			scored = append(scored, scoredEntry{entry, ms})
			seen[entry.Key] = true
		}
	}

	// Sort by blended score: match relevance (60%) + salience (40%).
	sort.Slice(scored, func(i, j int) bool {
		si := (scored[i].matchScore * 0.6) + (scored[i].entry.Salience * 0.4)
		sj := (scored[j].matchScore * 0.6) + (scored[j].entry.Salience * 0.4)
		return si > sj
	})

	if len(scored) > limit {
		scored = scored[:limit]
	}
	results := make([]*Entry, len(scored))
	for i, s := range scored {
		results[i] = s.entry
	}
	return results, nil
}

func (b *Brain) List(ctx context.Context, prefix string, sourcePrefix string) ([]*Entry, error) {
	var results []*Entry
	seen := make(map[string]bool)
	userID := userIDFromCtx(ctx)
	parentID := parentUserIDFromCtx(ctx)

	b.mu.RLock()
	if wm, ok := b.working[userID]; ok {
		for _, entry := range wm {
			if strings.HasPrefix(entry.Key, prefix) {
				if sourcePrefix == "" || strings.HasPrefix(entry.Source, sourcePrefix) {
					results = append(results, entry)
					seen[entry.Key] = true
				}
			}
		}
	}
	// Include parent's WM entries (read-only copies, deduped by key)
	if parentID != "" && parentID != userID {
		if parentWM, ok := b.working[parentID]; ok {
			for _, entry := range parentWM {
				if !seen[entry.Key] && strings.HasPrefix(entry.Key, prefix) {
					if sourcePrefix == "" || strings.HasPrefix(entry.Source, sourcePrefix) {
						copied := *entry
						results = append(results, &copied)
					}
				}
			}
		}
	}
	b.mu.RUnlock()

	query := `SELECT key, value, created_at, accessed_at, access_count, salience, source, stale
		FROM brain_ltm WHERE key LIKE ?`
	args := []interface{}{prefix + "%"}
	if sourcePrefix != "" {
		query += " AND source LIKE ?"
		args = append(args, sourcePrefix+"%")
	}
	query += " ORDER BY key"

	rows, err := b.db.Query(query, args...)
	if err != nil {
		return results, fmt.Errorf("list LTM: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		entry := &Entry{Tier: TierLongTerm}
		var staleInt int
		if err := rows.Scan(&entry.Key, &entry.Value, &entry.CreatedAt, &entry.AccessedAt,
			&entry.AccessCount, &entry.Salience, &entry.Source, &staleInt); err != nil {
			continue
		}
		entry.Stale = staleInt != 0
		results = append(results, entry)
	}
	return results, nil
}

func (b *Brain) Delete(ctx context.Context, key string) error {
	userID := userIDFromCtx(ctx)
	b.mu.Lock()
	if wm, ok := b.working[userID]; ok {
		delete(wm, key)
	}
	b.mu.Unlock()
	err := database.RetryOnBusy(5, func() error {
		_, err := b.db.Exec("DELETE FROM brain_ltm WHERE key = ?", key)
		return err
	})
	if err != nil {
		return fmt.Errorf("delete LTM: %w", err)
	}
	return nil
}

func (b *Brain) Push(ctx context.Context, userID, value string) error {
	if userID == "" {
		userID = userIDFromCtx(ctx)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.scratch[userID] = append(b.scratch[userID], value)
	return nil
}

func (b *Brain) Pop(ctx context.Context, userID string) (string, error) {
	if userID == "" {
		userID = userIDFromCtx(ctx)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	stack := b.scratch[userID]
	if len(stack) == 0 {
		return "", fmt.Errorf("scratchpad is empty")
	}
	val := stack[len(stack)-1]
	b.scratch[userID] = stack[:len(stack)-1]
	return val, nil
}

func (b *Brain) Peek(ctx context.Context, userID string) (string, error) {
	if userID == "" {
		userID = userIDFromCtx(ctx)
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	stack := b.scratch[userID]
	if len(stack) == 0 {
		return "", fmt.Errorf("scratchpad is empty")
	}
	return stack[len(stack)-1], nil
}

func (b *Brain) Promote(ctx context.Context, key string) error {
	userID := userIDFromCtx(ctx)
	b.mu.Lock()
	var entry *Entry
	if wm, ok := b.working[userID]; ok {
		if e, ok := wm[key]; ok {
			entry = e
			delete(wm, key)
		}
	}
	b.mu.Unlock()
	if entry == nil {
		return fmt.Errorf("key %q not found in working memory", key)
	}
	return b.storeLTM(key, entry.Value, entry.Source, time.Now())
}

// WorkingMemoryEntries returns a snapshot of all WM entries for the given user.
func (b *Brain) WorkingMemoryEntries(ctx context.Context) []*Entry {
	userID := userIDFromCtx(ctx)
	b.mu.RLock()
	defer b.mu.RUnlock()
	wm, ok := b.working[userID]
	if !ok {
		return nil
	}
	entries := make([]*Entry, 0, len(wm))
	for _, e := range wm {
		copied := *e
		entries = append(entries, &copied)
	}
	return entries
}

func (b *Brain) Consolidate(ctx context.Context, autoPromote bool) (*ConsolidationReport, error) {
	userID := userIDFromCtx(ctx)
	report := &ConsolidationReport{}

	b.mu.Lock()
	wm, ok := b.working[userID]
	if !ok || len(wm) == 0 {
		b.mu.Unlock()
		var ltmSize int
		b.db.QueryRow("SELECT COUNT(*) FROM brain_ltm").Scan(&ltmSize)
		report.LTMSize = ltmSize
		return report, nil
	}

	var toPromote []*Entry
	var toEvict []string
	for key, entry := range wm {
		entry.Salience = b.computeSalience(entry)
		if autoPromote && entry.Salience >= b.consolidateThreshold {
			toPromote = append(toPromote, entry)
		} else if entry.Salience < b.evictThreshold {
			toEvict = append(toEvict, key)
		}
	}
	for _, key := range toEvict {
		delete(wm, key)
	}
	b.mu.Unlock()

	for _, entry := range toPromote {
		if err := b.storeLTM(entry.Key, entry.Value, entry.Source, time.Now()); err != nil {
			log.Printf("Brain: failed to promote %q: %v", entry.Key, err)
			continue
		}
		report.PromotedKeys = append(report.PromotedKeys, entry.Key)
		report.PromotedCount++
		b.mu.Lock()
		if wm, ok := b.working[userID]; ok {
			delete(wm, entry.Key)
		}
		b.mu.Unlock()
	}
	report.EvictedCount = len(toEvict)
	report.EvictedKeys = toEvict

	var ltmSize int
	b.db.QueryRow("SELECT COUNT(*) FROM brain_ltm").Scan(&ltmSize)
	report.LTMSize = ltmSize
	log.Printf("Brain: consolidation — promoted=%d evicted=%d ltm=%d", report.PromotedCount, report.EvictedCount, report.LTMSize)
	return report, nil
}

func (b *Brain) Status(ctx context.Context) (*Status, error) {
	userID := userIDFromCtx(ctx)
	b.mu.RLock()
	wmCount := 0
	scratchDepth := 0
	var totalSalience float64
	if wm, ok := b.working[userID]; ok {
		wmCount = len(wm)
		for _, e := range wm {
			totalSalience += e.Salience
		}
	}
	if stack, ok := b.scratch[userID]; ok {
		scratchDepth = len(stack)
	}
	b.mu.RUnlock()

	var ltmCount int
	b.db.QueryRow("SELECT COUNT(*) FROM brain_ltm").Scan(&ltmCount)
	var hottestKeys []string
	rows, err := b.db.Query("SELECT key FROM brain_ltm ORDER BY salience DESC LIMIT 5")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var key string
			if rows.Scan(&key) == nil {
				hottestKeys = append(hottestKeys, key)
			}
		}
	}
	avgSalience := 0.0
	if wmCount > 0 {
		avgSalience = totalSalience / float64(wmCount)
	}
	return &Status{
		LTMEntries: ltmCount, WMEntries: wmCount, ScratchDepth: scratchDepth,
		AvgSalience: avgSalience, HottestKeys: hottestKeys,
	}, nil
}

// DB returns the underlying database connection for external indexers.
func (b *Brain) DB() *sql.DB { return b.db }

func (b *Brain) Close() error {
	close(b.stopCh)
	b.wg.Wait()
	if b.db != nil {
		return b.db.Close()
	}
	return nil
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

type brainUserIDKey struct{}

func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, brainUserIDKey{}, userID)
}

func userIDFromCtx(ctx context.Context) string {
	if uid, ok := ctx.Value(brainUserIDKey{}).(string); ok && uid != "" {
		return uid
	}
	return "default"
}

type brainParentUserIDKey struct{}

// WithParentUserID attaches a parent brain user ID to the context, enabling
// read-only fallback to the parent's working memory for sub-agent sessions.
func WithParentUserID(ctx context.Context, parentUserID string) context.Context {
	return context.WithValue(ctx, brainParentUserIDKey{}, parentUserID)
}

func parentUserIDFromCtx(ctx context.Context) string {
	if uid, ok := ctx.Value(brainParentUserIDKey{}).(string); ok {
		return uid
	}
	return ""
}

func (b *Brain) startAutoFlush() {
	if b.autoFlushInterval <= 0 {
		return
	}
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		ticker := time.NewTicker(b.autoFlushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-b.stopCh:
				return
			case <-ticker.C:
				b.autoFlush()
			}
		}
	}()
}

func (b *Brain) autoFlush() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for userID, wm := range b.working {
		for key, entry := range wm {
			entry.Salience = b.computeSalience(entry)
			if time.Since(entry.AccessedAt) > time.Hour && entry.Salience < b.evictThreshold {
				delete(wm, key)
			}
		}
		if len(wm) == 0 {
			delete(b.working, userID)
		}
	}
}
