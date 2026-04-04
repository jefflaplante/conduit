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
func WithAutoFlushInterval(d time.Duration) Option  { return func(b *Brain) { b.autoFlushInterval = d } }
func WithConsolidateThreshold(t float64) Option     { return func(b *Brain) { b.consolidateThreshold = t } }
func WithEvictThreshold(t float64) Option           { return func(b *Brain) { b.evictThreshold = t } }
func WithAutoPromote(v bool) Option                 { return func(b *Brain) { b.autoPromote = v } }
func WithWMGracePeriod(d time.Duration) Option      { return func(b *Brain) { b.wmGracePeriod = d } }

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

func (b *Brain) storeLTM(key, value, source string, now time.Time) error {
	_, err := b.db.Exec(`
		INSERT INTO brain_ltm (key, value, source, created_at, accessed_at, access_count, salience)
		VALUES (?, ?, ?, ?, ?, 1, 0.5)
		ON CONFLICT(key) DO UPDATE SET
			value = excluded.value,
			source = excluded.source,
			accessed_at = excluded.accessed_at,
			access_count = access_count + 1,
			salience = (CAST(access_count + 1 AS REAL) * 0.4) +
				(1.0 / (1.0 + (julianday('now') - julianday(excluded.accessed_at)) * 24.0)) * 0.4 +
				0.16
	`, key, value, source, now, now)
	if err != nil {
		return fmt.Errorf("store LTM: %w", err)
	}
	if b.maxLTMEntries > 0 {
		b.db.Exec(`DELETE FROM brain_ltm WHERE key IN (
			SELECT key FROM brain_ltm ORDER BY salience ASC
			LIMIT MAX(0, (SELECT COUNT(*) FROM brain_ltm) - ?))`, b.maxLTMEntries)
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
	b.mu.RUnlock()
	return b.getLTM(key)
}

func (b *Brain) getLTM(key string) (*Entry, error) {
	row := b.db.QueryRow(`
		UPDATE brain_ltm SET
			accessed_at = datetime('now'),
			access_count = access_count + 1,
			salience = (CAST(access_count + 1 AS REAL) * 0.4) + (1.0 / (1.0 + 0.0)) * 0.4 + 0.16
		WHERE key = ?
		RETURNING key, value, created_at, accessed_at, access_count, salience, source
	`, key)
	entry := &Entry{Tier: TierLongTerm}
	err := row.Scan(&entry.Key, &entry.Value, &entry.CreatedAt, &entry.AccessedAt,
		&entry.AccessCount, &entry.Salience, &entry.Source)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get LTM: %w", err)
	}
	return entry, nil
}

func (b *Brain) Recall(ctx context.Context, query string, limit int) ([]*Entry, error) {
	if limit <= 0 {
		limit = 20
	}
	queryLower := strings.ToLower(query)
	var results []*Entry

	userID := userIDFromCtx(ctx)
	b.mu.RLock()
	if wm, ok := b.working[userID]; ok {
		for _, entry := range wm {
			if matchesQuery(entry, queryLower) {
				results = append(results, entry)
			}
		}
	}
	b.mu.RUnlock()

	rows, err := b.db.Query(`
		SELECT key, value, created_at, accessed_at, access_count, salience, source
		FROM brain_ltm WHERE key LIKE ? OR value LIKE ?
		ORDER BY salience DESC LIMIT ?
	`, "%"+query+"%", "%"+query+"%", limit)
	if err != nil {
		return results, fmt.Errorf("recall LTM: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		entry := &Entry{Tier: TierLongTerm}
		if err := rows.Scan(&entry.Key, &entry.Value, &entry.CreatedAt, &entry.AccessedAt,
			&entry.AccessCount, &entry.Salience, &entry.Source); err != nil {
			continue
		}
		results = append(results, entry)
	}

	sort.Slice(results, func(i, j int) bool { return results[i].Salience > results[j].Salience })
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func (b *Brain) List(ctx context.Context, prefix string) ([]*Entry, error) {
	var results []*Entry
	userID := userIDFromCtx(ctx)
	b.mu.RLock()
	if wm, ok := b.working[userID]; ok {
		for _, entry := range wm {
			if strings.HasPrefix(entry.Key, prefix) {
				results = append(results, entry)
			}
		}
	}
	b.mu.RUnlock()

	rows, err := b.db.Query(`
		SELECT key, value, created_at, accessed_at, access_count, salience, source
		FROM brain_ltm WHERE key LIKE ? ORDER BY key`, prefix+"%")
	if err != nil {
		return results, fmt.Errorf("list LTM: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		entry := &Entry{Tier: TierLongTerm}
		if err := rows.Scan(&entry.Key, &entry.Value, &entry.CreatedAt, &entry.AccessedAt,
			&entry.AccessCount, &entry.Salience, &entry.Source); err != nil {
			continue
		}
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
	_, err := b.db.Exec("DELETE FROM brain_ltm WHERE key = ?", key)
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

func (b *Brain) Close() error {
	close(b.stopCh)
	b.wg.Wait()
	if b.db != nil {
		return b.db.Close()
	}
	return nil
}

func (b *Brain) computeSalience(e *Entry) float64 {
	accessScore := math.Min(float64(e.AccessCount)/100.0, 1.0)
	hoursSince := time.Since(e.AccessedAt).Hours()
	recencyScore := 1.0 / (1.0 + hoursSince)
	var tierWeight float64
	switch e.Tier {
	case TierLongTerm:
		tierWeight = 0.8
	case TierWorking:
		tierWeight = 0.5
	default:
		tierWeight = 0.1
	}
	return (accessScore * 0.4) + (recencyScore * 0.4) + (tierWeight * 0.2)
}

func matchesQuery(e *Entry, queryLower string) bool {
	return strings.Contains(strings.ToLower(e.Key), queryLower) ||
		strings.Contains(strings.ToLower(e.Value), queryLower)
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
