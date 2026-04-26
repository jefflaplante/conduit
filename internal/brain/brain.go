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

// Spreading activation tuning constants.
//
// The per-flush warmth decay controls how quickly activated entries cool down
// between autoFlush cycles. Lower values = faster cooling = less cross-domain
// noise from accumulated neighbour-of-neighbour warmth. The previous value of
// 0.95 caused warmth to persist too long (85.7% at 3 flushes), pulling
// unrelated entries into recall results (e.g., Paris trip recall returning
// solar net metering info). At 0.85, warmth drops to 61.4% after 3 flushes
// and 44.4% after 5 — aggressive enough to suppress cross-domain noise.
//
// The spreading decay controls how much warmth boost a direct neighbour
// receives. Combined with faster warmth cooling, this keeps activation focused
// on truly related entries.
const (
	DefaultSpreadingDecay         = 0.85 // neighbour warmth boost multiplier (was 0.35, then 0.5; 0.85 gives 61% at 3 hops, good domain isolation)
	DefaultWarmthDecay            = 0.85 // per-flush warmth decay (was 0.95)
	DefaultEdgeDecay              = 0.85 // per-flush edge confidence decay (was 0.95)
	DefaultMinConfidenceThreshold = 0.3  // min edge confidence to participate in spreading; prevents low-confidence edges from polluting activation
	DefaultEdgeAccessAlpha        = 0.1  // usage-weighted boost: effective_conf = base * (1 + alpha * log1p(access_count))
	DefaultEdgeAccessDecay        = 0.95 // per-flush decay of edge access_count (prevents stale activity from permanently inflating confidence)
)

type Entry struct {
	Key         string     `json:"key"`
	Value       string     `json:"value"`
	Tier        Tier       `json:"tier"`
	CreatedAt   time.Time  `json:"created_at"`
	AccessedAt  time.Time  `json:"accessed_at"`
	AccessCount int        `json:"access_count"`
	Salience    float64    `json:"salience"`
	Warmth      float64    `json:"warmth,omitempty"`
	Source      string     `json:"source,omitempty"`
	Stale          bool       `json:"stale,omitempty"`
	ClusterHit      bool       `json:"cluster_hit,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
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
	ColdestKeys  []string `json:"coldest_keys,omitempty"`
	ExpiringSoon int      `json:"expiring_soon,omitempty"`

	// Spreading activation metrics (session-lifetime counters).
	SpreadEvents     int64          `json:"spread_events,omitempty"`
	AvgWarmthBoost   float64        `json:"avg_warmth_boost,omitempty"`
	ClusterHitRate   float64        `json:"cluster_hit_rate,omitempty"`
	EdgeCountByType  map[string]int `json:"edge_count_by_type,omitempty"`
}

// storeOpts holds options for Store calls, populated by StoreOption funcs.
type storeOpts struct {
	ttl time.Duration
}

// StoreOption configures an individual Store call.
type StoreOption func(*storeOpts)

// WithTTL sets a time-to-live on the stored entry. After ttl elapses, the
// entry is no longer returned from Get/Recall and is eligible for deletion
// by the next pruneExpired/Consolidate/rem_cycle sweep. A zero duration
// means no expiry.
func WithTTL(d time.Duration) StoreOption {
	return func(o *storeOpts) { o.ttl = d }
}

// ParseDuration parses a duration string with Go's standard units plus
// the convenience suffixes "d" (days) and "w" (weeks), which time.ParseDuration
// does not natively support. Example: "24h", "7d", "2w", "90m".
func ParseDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	// Check for trailing d or w (days/weeks); otherwise defer to stdlib.
	if n := len(s); n >= 2 {
		last := s[n-1]
		if last == 'd' || last == 'w' {
			var count int
			if _, err := fmt.Sscanf(s[:n-1], "%d", &count); err != nil {
				return 0, fmt.Errorf("parse duration %q: %w", s, err)
			}
			mult := 24 * time.Hour
			if last == 'w' {
				mult = 7 * 24 * time.Hour
			}
			return time.Duration(count) * mult, nil
		}
	}
	return time.ParseDuration(s)
}

type Option func(*Brain)

func WithMaxLTMEntries(n int) Option               { return func(b *Brain) { b.maxLTMEntries = n } }
func WithAutoFlushInterval(d time.Duration) Option   { return func(b *Brain) { b.autoFlushInterval = d } }
func WithConsolidateThreshold(t float64) Option      { return func(b *Brain) { b.consolidateThreshold = t } }
func WithEvictThreshold(t float64) Option            { return func(b *Brain) { b.evictThreshold = t } }
func WithAutoPromote(v bool) Option                  { return func(b *Brain) { b.autoPromote = v } }
func WithWMGracePeriod(d time.Duration) Option       { return func(b *Brain) { b.wmGracePeriod = d } }
func WithAccessWeight(w float64) Option              { return func(b *Brain) { b.accessWeight = w } }
func WithRecencyWeight(w float64) Option             { return func(b *Brain) { b.recencyWeight = w } }
func WithTierWeight(w float64) Option                { return func(b *Brain) { b.tierWeight = w } }
func WithRecencyDecayRate(r float64) Option          { return func(b *Brain) { b.recencyDecayRate = r } }
func WithAccessCountCap(n int) Option                { return func(b *Brain) { b.accessCountCap = n } }
func WithHeatPromotionThreshold(n int) Option        { return func(b *Brain) { b.heatPromotionThreshold = n } }

// WithSpreadingDecay sets the activation decay factor (distance-1 multiplier).
// For each edge traversal the boost is multiplied by d. Default: 0.5.
func WithSpreadingDecay(d float64) Option { return func(b *Brain) { b.spreadingDecay = d } }

// WithSpreadingEnabled enables or disables spreading activation globally.
// Default: true.
func WithSpreadingEnabled(enabled bool) Option { return func(b *Brain) { b.spreadingEnabled = enabled } }

// WithMinConfidenceThreshold sets the minimum edge confidence required for an
// edge to participate in spreading activation. Edges below this threshold will
// still be searchable but won't propagate warmth. Default: 0.3.
func WithMinConfidenceThreshold(t float64) Option { return func(b *Brain) { b.minConfidenceThreshold = t } }

// WithMatchWeight sets the weight applied to per-entry keyword match score
// during recall ranking. Default: 0.5.
func WithMatchWeight(w float64) Option { return func(b *Brain) { b.matchWeight = w } }

// WithSalienceWeight sets the weight applied to entry salience during recall
// ranking. Default: 0.3.
func WithSalienceWeight(w float64) Option { return func(b *Brain) { b.salienceWeight = w } }

// WithWarmthWeight sets the weight applied to spreading-activation warmth
// during recall ranking. Default: 0.2.
func WithWarmthWeight(w float64) Option { return func(b *Brain) { b.warmthWeight = w } }

type Brain struct {
	mu      sync.RWMutex
	working map[string]map[string]*Entry // userID -> key -> entry
	scratch map[string][]string          // userID -> LIFO stack
	db      *sql.DB

	maxLTMEntries          int
	autoFlushInterval      time.Duration
	consolidateThreshold   float64
	evictThreshold         float64
	autoPromote            bool
	wmGracePeriod          time.Duration
	accessWeight           float64
	recencyWeight          float64
	tierWeight             float64
	recencyDecayRate       float64
	accessCountCap         int
	heatPromotionThreshold int

	// Spreading activation
	spreadingEnabled   bool
	spreadingDecay     float64 // neighbour boost multiplier
	warmthDecay        float64 // per-flush warmth cooling (was 0.95, now 0.85)
	edgeDecay          float64 // per-flush edge confidence cooling (was 0.95, now 0.85)
	minConfidenceThreshold float64 // min edge confidence to participate in spreading (default 0.3)
	edgeAccessAlpha    float64 // usage-weighted boost factor (default 0.1)
	edgeAccessDecay    float64 // per-flush decay of edge access_count (default 0.95)

	// Recall blended-score weights. Defaults sum to 1.0 (match 0.5, salience 0.3,
	// warmth 0.2) but aren't forced to — callers can overweight a signal if
	// metrics show it correlates with perceived usefulness.
	matchWeight    float64
	salienceWeight float64
	warmthWeight   float64

	// pendingEdgeKeys accumulates LTM keys stored since the last autoFlush so
	// namespace edges can be materialized in batch rather than per-store.
	// Protected by mu.
	pendingEdgeKeys map[string]struct{}

	// Spreading metrics (mu-protected). Session-lifetime counters; reset on
	// Brain restart. Used by Status() to report effectiveness.
	spreadEvents     int64
	totalBoost       float64
	totalBoostCount  int64
	clusterHitCount  int64
	directHitCount   int64

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
		working:                make(map[string]map[string]*Entry),
		scratch:                make(map[string][]string),
		db:                     db,
		maxLTMEntries:          10000,
		autoFlushInterval:      10 * time.Minute,
		consolidateThreshold:   0.6,
		evictThreshold:         0.1,
		autoPromote:            true,
		wmGracePeriod:          5 * time.Minute,
		accessWeight:           0.4,
		recencyWeight:          0.4,
		tierWeight:             0.2,
		recencyDecayRate:       1.0,
		accessCountCap:         100,
		heatPromotionThreshold: 3,
		spreadingEnabled:       true,
		spreadingDecay:         DefaultSpreadingDecay,
		warmthDecay:            DefaultWarmthDecay,
		edgeDecay:              DefaultEdgeDecay,
		minConfidenceThreshold: DefaultMinConfidenceThreshold,
		edgeAccessAlpha:        DefaultEdgeAccessAlpha,
		edgeAccessDecay:        DefaultEdgeAccessDecay,
		matchWeight:            0.5,
		salienceWeight:         0.3,
		warmthWeight:           0.2,
		pendingEdgeKeys:        make(map[string]struct{}),
		stopCh:                 make(chan struct{}),
	}
	for _, opt := range opts {
		opt(b)
	}
	b.startAutoFlush()
	log.Printf("Brain initialized at %s", dbPath)
	return b, nil
}

func (b *Brain) Store(ctx context.Context, key, value string, tier Tier, source string, opts ...StoreOption) error {
	if err := ValidateSource(source); err != nil {
		log.Printf("Brain: warning: %v (key=%q)", err, key)
	}
	o := &storeOpts{}
	for _, opt := range opts {
		opt(o)
	}
	now := time.Now()
	var expiresAt *time.Time
	if o.ttl > 0 {
		t := now.Add(o.ttl)
		expiresAt = &t
	}
	switch tier {
	case TierLongTerm:
		return b.storeLTM(key, value, source, now, expiresAt)
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
			existing.ExpiresAt = expiresAt
		} else {
			b.working[userID][key] = &Entry{
				Key: key, Value: value, Tier: TierWorking,
				CreatedAt: now, AccessedAt: now,
				AccessCount: 1, Salience: 0.5, Source: source,
				ExpiresAt: expiresAt,
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
		for _, e := range ltmBatch {
			b.markPendingEdge(e.Key)
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

func (b *Brain) storeLTM(key, value, source string, now time.Time, expiresAt *time.Time) error {
	nowStr := now.UTC().Format("2006-01-02 15:04:05")
	var expiresStr interface{}
	if expiresAt != nil {
		// Use sub-second precision so TTLs shorter than 1s still expire correctly
		// when compared against strftime('%Y-%m-%d %H:%M:%f','now').
		expiresStr = expiresAt.UTC().Format("2006-01-02 15:04:05.000")
	} else {
		expiresStr = nil
	}
	// Use a simple default salience for upsert; the exact salience is recomputed on access.
	// The ON CONFLICT UPDATE preserves the existing salience bumped slightly for the access.
	err := database.RetryOnBusy(5, func() error {
		_, err := b.db.Exec(`
			INSERT INTO brain_ltm (key, value, source, created_at, accessed_at, access_count, salience, expires_at)
			VALUES (?, ?, ?, ?, ?, 1, 0.5, ?)
			ON CONFLICT(key) DO UPDATE SET
				value = excluded.value,
				source = excluded.source,
				accessed_at = excluded.accessed_at,
				access_count = access_count + 1,
				expires_at = excluded.expires_at,
				salience = COALESCE(
					(MIN(CAST(access_count + 1 AS REAL) / CAST(? AS REAL), 1.0) * ?) +
					(1.0 / (1.0 + 0.0)) * ? +
					(0.8 * ?),
					salience, 0.5)
		`, key, value, source, nowStr, nowStr, expiresStr,
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
	b.markPendingEdge(key)
	return nil
}

// markPendingEdge queues an LTM key for namespace edge materialization on the
// next autoFlush. No-op when spreading is disabled.
func (b *Brain) markPendingEdge(key string) {
	if !b.spreadingEnabled {
		return
	}
	b.mu.Lock()
	if b.pendingEdgeKeys == nil {
		b.pendingEdgeKeys = make(map[string]struct{})
	}
	b.pendingEdgeKeys[key] = struct{}{}
	b.mu.Unlock()
}

func (b *Brain) Get(ctx context.Context, key string) (*Entry, error) {
	userID := userIDFromCtx(ctx)
	now := time.Now()
	b.mu.RLock()
	if wm, ok := b.working[userID]; ok {
		if entry, ok := wm[key]; ok {
			if entry.ExpiresAt != nil && !entry.ExpiresAt.After(now) {
				// Expired — delete and fall through to LTM lookup.
				b.mu.RUnlock()
				b.mu.Lock()
				delete(wm, key)
				b.mu.Unlock()
				return b.getLTM(key)
			}
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
				if entry.ExpiresAt != nil && !entry.ExpiresAt.After(now) {
					// Expired parent entry — skip, fall through to LTM.
					b.mu.RUnlock()
					return b.getLTM(key)
				}
				b.mu.RUnlock()
				copied := *entry
				return &copied, nil
			}
		}
	}
	b.mu.RUnlock()
	entry, err := b.getLTM(key)
	if err == nil && entry != nil {
		// Fire-and-forget: spread activation to neighbours. Errors are non-fatal.
		_ = b.spreadActivation([]string{key})
	}
	return entry, err
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
		WHERE key = ? AND (expires_at IS NULL OR expires_at > strftime('%Y-%m-%d %H:%M:%f', 'now'))
		RETURNING key, value, created_at, accessed_at, access_count, salience, source, stale, expires_at, warmth
	`, b.accessCountCap, b.accessWeight, b.recencyWeight, b.tierWeight, key)
	entry := &Entry{Tier: TierLongTerm}
	var staleInt int
	var expiresAt sql.NullTime
	err := row.Scan(&entry.Key, &entry.Value, &entry.CreatedAt, &entry.AccessedAt,
		&entry.AccessCount, &entry.Salience, &entry.Source, &staleInt, &expiresAt, &entry.Warmth)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get LTM: %w", err)
	}
	entry.Stale = staleInt != 0
	if expiresAt.Valid {
		t := expiresAt.Time
		entry.ExpiresAt = &t
	}
	return entry, nil
}

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

	// First pass: identify matching WM entries under RLock.
	var wmHits []*Entry
	b.mu.RLock()
	if wm, ok := b.working[userID]; ok {
		for _, entry := range wm {
			if entry.ExpiresAt != nil && !entry.ExpiresAt.After(now) {
				continue
			}
			if ms := queryMatchScore(entry, terms); ms > 0 {
				wmHits = append(wmHits, entry)
				scored = append(scored, scoredEntry{entry, ms})
				seen[entry.Key] = true
			}
		}
	}
	// Include parent's WM entries (read-only copies, deduped by key)
	if parentID != "" && parentID != userID {
		if parentWM, ok := b.working[parentID]; ok {
			for _, entry := range parentWM {
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
	}
	b.mu.RUnlock()

	// Second pass: bump AccessCount/AccessedAt on our own WM hits under write lock.
	if len(wmHits) > 0 {
		now := time.Now()
		b.mu.Lock()
		for _, entry := range wmHits {
			entry.AccessedAt = now
			entry.AccessCount++
			entry.Salience = b.computeSalience(entry)
		}
		b.mu.Unlock()
	}

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

	sqlQuery := fmt.Sprintf(
		`SELECT key, value, created_at, accessed_at, access_count, salience, source, stale, expires_at, warmth,
		(%s) AS match_count
		FROM brain_ltm WHERE (%s) AND (expires_at IS NULL OR expires_at > strftime('%%Y-%%m-%%d %%H:%%M:%%f', 'now'))
		ORDER BY match_count DESC, salience + warmth DESC LIMIT ?`,
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
	mw, sw, ww := b.matchWeight, b.salienceWeight, b.warmthWeight
	sort.Slice(scored, func(i, j int) bool {
		si := (scored[i].matchScore * mw) + (scored[i].entry.Salience * sw) + (scored[i].entry.Warmth * ww)
		sj := (scored[j].matchScore * mw) + (scored[j].entry.Salience * sw) + (scored[j].entry.Warmth * ww)
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

	return results, nil
}

func (b *Brain) List(ctx context.Context, prefix string, sourcePrefix string) ([]*Entry, error) {
	var results []*Entry
	seen := make(map[string]bool)
	userID := userIDFromCtx(ctx)
	parentID := parentUserIDFromCtx(ctx)
	now := time.Now()

	b.mu.RLock()
	if wm, ok := b.working[userID]; ok {
		for _, entry := range wm {
			if entry.ExpiresAt != nil && !entry.ExpiresAt.After(now) {
				continue
			}
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
				if entry.ExpiresAt != nil && !entry.ExpiresAt.After(now) {
					continue
				}
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

	query := `SELECT key, value, created_at, accessed_at, access_count, salience, source, stale, expires_at
		FROM brain_ltm WHERE key LIKE ? AND (expires_at IS NULL OR expires_at > strftime('%Y-%m-%d %H:%M:%f', 'now'))`
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
		var expiresAtNT sql.NullTime
		if err := rows.Scan(&entry.Key, &entry.Value, &entry.CreatedAt, &entry.AccessedAt,
			&entry.AccessCount, &entry.Salience, &entry.Source, &staleInt, &expiresAtNT); err != nil {
			continue
		}
		entry.Stale = staleInt != 0
		if expiresAtNT.Valid {
			t := expiresAtNT.Time
			entry.ExpiresAt = &t
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
	return b.storeLTM(key, entry.Value, entry.Source, time.Now(), entry.ExpiresAt)
}

// WorkingMemoryEntries returns a snapshot of all WM entries for the given user.
// Expired entries are filtered out.
func (b *Brain) WorkingMemoryEntries(ctx context.Context) []*Entry {
	userID := userIDFromCtx(ctx)
	now := time.Now()
	b.mu.RLock()
	defer b.mu.RUnlock()
	wm, ok := b.working[userID]
	if !ok {
		return nil
	}
	entries := make([]*Entry, 0, len(wm))
	for _, e := range wm {
		if e.ExpiresAt != nil && !e.ExpiresAt.After(now) {
			continue
		}
		copied := *e
		entries = append(entries, &copied)
	}
	return entries
}

func (b *Brain) Consolidate(ctx context.Context, autoPromote bool) (*ConsolidationReport, error) {
	userID := userIDFromCtx(ctx)
	report := &ConsolidationReport{}

	// Prune expired entries first so we never promote stale data.
	if _, err := b.pruneExpired(ctx); err != nil {
		log.Printf("Brain.Consolidate: pruneExpired failed: %v", err)
	}

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

	var promotedKeys []string
	for _, entry := range toPromote {
		if err := b.storeLTM(entry.Key, entry.Value, entry.Source, time.Now(), entry.ExpiresAt); err != nil {
			log.Printf("Brain: failed to promote %q: %v", entry.Key, err)
			continue
		}
		promotedKeys = append(promotedKeys, entry.Key)
		report.PromotedKeys = append(report.PromotedKeys, entry.Key)
		report.PromotedCount++
	}
	if len(promotedKeys) > 0 {
		b.mu.Lock()
		if wm, ok := b.working[userID]; ok {
			for _, k := range promotedKeys {
				delete(wm, k)
			}
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
	now := time.Now()
	soonCutoff := now.Add(24 * time.Hour)
	b.mu.RLock()
	wmCount := 0
	scratchDepth := 0
	var totalSalience float64
	expiringSoon := 0
	if wm, ok := b.working[userID]; ok {
		for _, e := range wm {
			if e.ExpiresAt != nil && !e.ExpiresAt.After(now) {
				continue // already-expired entries don't count as "active"
			}
			wmCount++
			totalSalience += e.Salience
			if e.ExpiresAt != nil && !e.ExpiresAt.After(soonCutoff) {
				expiringSoon++
			}
		}
	}
	if stack, ok := b.scratch[userID]; ok {
		scratchDepth = len(stack)
	}
	b.mu.RUnlock()

	var ltmCount int
	b.db.QueryRow(`SELECT COUNT(*) FROM brain_ltm WHERE expires_at IS NULL OR expires_at > strftime('%Y-%m-%d %H:%M:%f', 'now')`).Scan(&ltmCount)
	var hottestKeys []string
	rows, err := b.db.Query(`SELECT key FROM brain_ltm WHERE expires_at IS NULL OR expires_at > strftime('%Y-%m-%d %H:%M:%f', 'now') ORDER BY salience DESC LIMIT 5`)
	if err == nil {
		for rows.Next() {
			var key string
			if rows.Scan(&key) == nil {
				hottestKeys = append(hottestKeys, key)
			}
		}
		rows.Close()
	}
	var coldestKeys []string
	coldRows, err := b.db.Query("SELECT key FROM brain_ltm ORDER BY access_count ASC, accessed_at ASC LIMIT 5")
	if err == nil {
		for coldRows.Next() {
			var key string
			if coldRows.Scan(&key) == nil {
				coldestKeys = append(coldestKeys, key)
			}
		}
		coldRows.Close()
	}
	// Count LTM entries expiring within the next 24h.
	var ltmExpiringSoon int
	b.db.QueryRow(
		`SELECT COUNT(*) FROM brain_ltm WHERE expires_at IS NOT NULL AND expires_at > strftime('%Y-%m-%d %H:%M:%f', 'now') AND expires_at <= strftime('%Y-%m-%d %H:%M:%f', 'now', '+24 hours')`,
	).Scan(&ltmExpiringSoon)
	expiringSoon += ltmExpiringSoon

	avgSalience := 0.0
	if wmCount > 0 {
		avgSalience = totalSalience / float64(wmCount)
	}

	// Spreading activation metrics. Counter snapshots under mu; edge-count query
	// outside the lock.
	b.mu.RLock()
	spreadEvents := b.spreadEvents
	totalBoost := b.totalBoost
	totalBoostCount := b.totalBoostCount
	clusterHits := b.clusterHitCount
	directHits := b.directHitCount
	b.mu.RUnlock()

	var avgBoost float64
	if totalBoostCount > 0 {
		avgBoost = totalBoost / float64(totalBoostCount)
	}
	var clusterHitRate float64
	if total := clusterHits + directHits; total > 0 {
		clusterHitRate = float64(clusterHits) / float64(total)
	}

	var edgeCounts map[string]int
	if edgeRows, err := b.db.Query(
		`SELECT COALESCE(relationship, ''), COUNT(*) FROM brain_relationships GROUP BY relationship`,
	); err == nil {
		edgeCounts = make(map[string]int)
		for edgeRows.Next() {
			var rel string
			var n int
			if edgeRows.Scan(&rel, &n) == nil {
				if rel == "" {
					rel = "unknown"
				}
				edgeCounts[rel] = n
			}
		}
		edgeRows.Close()
	}

	return &Status{
		LTMEntries: ltmCount, WMEntries: wmCount, ScratchDepth: scratchDepth,
		AvgSalience: avgSalience, HottestKeys: hottestKeys, ColdestKeys: coldestKeys,
		ExpiringSoon:    expiringSoon,
		SpreadEvents:    spreadEvents,
		AvgWarmthBoost:  avgBoost,
		ClusterHitRate:  clusterHitRate,
		EdgeCountByType: edgeCounts,
	}, nil
}

// pruneExpired deletes all brain_ltm rows whose expires_at has passed, and
// also removes expired entries from in-memory working memory. Returns the
// total count of deleted entries.
func (b *Brain) pruneExpired(ctx context.Context) (int, error) {
	var total int
	// LTM: single DELETE for all expired rows.
	var n int64
	err := database.RetryOnBusy(5, func() error {
		res, err := b.db.ExecContext(ctx,
			`DELETE FROM brain_ltm WHERE expires_at IS NOT NULL AND expires_at <= strftime('%Y-%m-%d %H:%M:%f', 'now')`)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("prune expired LTM: %w", err)
	}
	total += int(n)

	// WM: walk every user's working map and drop expired entries.
	now := time.Now()
	b.mu.Lock()
	for _, wm := range b.working {
		for key, entry := range wm {
			if entry.ExpiresAt != nil && !entry.ExpiresAt.After(now) {
				delete(wm, key)
				total++
			}
		}
	}
	b.mu.Unlock()
	return total, nil
}

// PruneExpired is the exported wrapper for pruneExpired; used by REM cycle
// phases (prune, consolidate) to delete time-expired entries before other work.
func (b *Brain) PruneExpired(ctx context.Context) (int, error) {
	return b.pruneExpired(ctx)
}

// StoreWithTTL is a convenience wrapper around Store that applies WithTTL(ttl).
// A zero ttl is equivalent to calling Store with no options.
func (b *Brain) StoreWithTTL(ctx context.Context, key, value string, tier Tier, source string, ttl time.Duration) error {
	if ttl <= 0 {
		return b.Store(ctx, key, value, tier, source)
	}
	return b.Store(ctx, key, value, tier, source, WithTTL(ttl))
}

// DB returns the underlying database connection for external indexers.
func (b *Brain) DB() *sql.DB { return b.db }

// HeatPromotionThreshold returns the minimum AccessCount at which a working-memory
// entry should be promoted to LTM regardless of its salience score.
func (b *Brain) HeatPromotionThreshold() int { return b.heatPromotionThreshold }

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
	b.mu.Unlock()

	// The edge/warmth maintenance below must run without b.mu held:
	// flushPendingEdges re-acquires b.mu, and holding it across DB work blocks
	// every Store/Get/Recall in the gateway for the duration of the flush.
	if b.spreadingEnabled {
		// Warmth decay: cools activated entries each flush cycle.
		// 0.85^3 = 61.4% warmth at 3 flushes (good isolation), 0.85^5 = 44.4%
		// Previously 0.95 gave 85.7% at 3 flushes, causing cross-domain noise.
		wd := b.warmthDecay
		_ = database.RetryOnBusy(3, func() error {
			_, err := b.db.Exec(`UPDATE brain_ltm SET warmth = CASE WHEN warmth * ? < 0.01 THEN 0.0 ELSE warmth * ? END WHERE warmth > 0.0`, wd, wd)
			return err
		})
		_ = b.flushPendingEdges()
		_ = b.DecayEdgeConfidence(b.edgeDecay, 0.1, 6*time.Hour)
	}
}
