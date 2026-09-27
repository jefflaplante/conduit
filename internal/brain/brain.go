package brain

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
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
// unrelated entries into recall results (e.g., a travel query returning
// energy-billing entries). At 0.85, warmth drops to 61.4% after 3 flushes
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
	Stale       bool       `json:"stale,omitempty"`
	ClusterHit  bool       `json:"cluster_hit,omitempty"`
	WarmthHit   bool       `json:"warmth_hit,omitempty"` // injected by warmth-floor, not a keyword/cluster match
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
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
	SpreadEvents    int64          `json:"spread_events,omitempty"`
	AvgWarmthBoost  float64        `json:"avg_warmth_boost,omitempty"`
	ClusterHitRate  float64        `json:"cluster_hit_rate,omitempty"`
	WarmthHitCount  int64          `json:"warmth_hit_count,omitempty"`
	EdgeCountByType map[string]int `json:"edge_count_by_type,omitempty"`

	// Access bonus metrics.
	AvgAccessBonus float64  `json:"avg_access_bonus,omitempty"`
	TopAccessBonus []string `json:"top_access_bonus_keys,omitempty"`
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
func WithHeatPromotionThreshold(n int) Option      { return func(b *Brain) { b.heatPromotionThreshold = n } }

// WithMaxWMEntriesPerUser caps each user's working-memory bucket. When a
// Store/StoreBulk pushes a bucket over the cap, the least valuable entries
// (lowest access+recency score) are evicted — hot ones are promoted to LTM
// first when auto-promote is on. <= 0 disables the cap. conduit-31jg.29
func WithMaxWMEntriesPerUser(n int) Option { return func(b *Brain) { b.maxWMEntriesPerUser = n } }

// WithLTMEvictionGrace sets how long a freshly written or accessed LTM row is
// immune from capacity eviction. Default: DefaultLTMEvictionGrace. Negative
// values are treated as 0 (rows written in the current second stay protected).
// conduit-31jg.28
func WithLTMEvictionGrace(d time.Duration) Option { return func(b *Brain) { b.ltmEvictionGrace = d } }

// WithSpreadingDecay sets the activation decay factor (distance-1 multiplier).
// For each edge traversal the boost is multiplied by d. Default: 0.5.
func WithSpreadingDecay(d float64) Option { return func(b *Brain) { b.spreadingDecay = d } }

// WithSpreadingEnabled enables or disables spreading activation globally.
// Default: true.
func WithSpreadingEnabled(enabled bool) Option {
	return func(b *Brain) { b.spreadingEnabled = enabled }
}

// WithMinConfidenceThreshold sets the minimum edge confidence required for an
// edge to participate in spreading activation. Edges below this threshold will
// still be searchable but won't propagate warmth. Default: 0.3.
func WithMinConfidenceThreshold(t float64) Option {
	return func(b *Brain) { b.minConfidenceThreshold = t }
}

// WithMatchWeight sets the weight applied to per-entry keyword match score
// during recall ranking. Default: 0.5.
func WithMatchWeight(w float64) Option { return func(b *Brain) { b.matchWeight = w } }

// WithSalienceWeight sets the weight applied to entry salience during recall
// ranking. Default: 0.3.
func WithSalienceWeight(w float64) Option { return func(b *Brain) { b.salienceWeight = w } }

// WithWarmthWeight sets the weight applied to spreading-activation warmth
// during recall ranking. Default: 0.2.
func WithWarmthWeight(w float64) Option { return func(b *Brain) { b.warmthWeight = w } }

// WithAccessBonusWeight sets the weight applied to the decaying access bonus
// during recall ranking. The access bonus rewards frequently-accessed entries
// with a decaying curve: min(cap, access_count * alpha * decay^days_since_access).
// Default: 0.15.
func WithAccessBonusWeight(w float64) Option { return func(b *Brain) { b.accessBonusWeight = w } }

// WithAccessBonusAlpha sets the per-access contribution to the access bonus.
// Default: 0.1 (each access adds 0.1 to the bonus, subject to decay and cap).
func WithAccessBonusAlpha(a float64) Option { return func(b *Brain) { b.accessBonusAlpha = a } }

// WithAccessBonusCap sets the maximum access bonus value. Default: 0.3.
func WithAccessBonusCap(c float64) Option { return func(b *Brain) { b.accessBonusCap = c } }

// WithAccessBonusDecay sets the daily decay factor for the access bonus.
// 0.95 means the bonus halves every ~13.5 days without access. Default: 0.95.
func WithAccessBonusDecay(d float64) Option { return func(b *Brain) { b.accessBonusDecay = d } }

// WithWarmthInjectFloor sets the minimum warmth an LTM entry must have to be
// eligible for warmth-floor injection during recall. Default: 0.7.
func WithWarmthInjectFloor(f float64) Option { return func(b *Brain) { b.warmthInjectFloor = f } }

// WithWarmthInjectLimit sets the maximum number of high-warmth non-matching
// LTM entries appended to recall results. 0 disables injection entirely.
// Default: 2.
func WithWarmthInjectLimit(n int) Option { return func(b *Brain) { b.warmthInjectLimit = n } }

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
	maxWMEntriesPerUser    int           // conduit-31jg.29: per-user WM cap (<=0 = unbounded)
	ltmEvictionGrace       time.Duration // conduit-31jg.28: capacity-eviction immunity window

	// Spreading activation
	spreadingEnabled       bool
	spreadingDecay         float64 // neighbour boost multiplier
	warmthDecay            float64 // per-flush warmth cooling (was 0.95, now 0.85)
	edgeDecay              float64 // per-flush edge confidence cooling (was 0.95, now 0.85)
	minConfidenceThreshold float64 // min edge confidence to participate in spreading (default 0.3)
	edgeAccessAlpha        float64 // usage-weighted boost factor (default 0.1)
	edgeAccessDecay        float64 // per-flush decay of edge access_count (default 0.95)

	// Recall blended-score weights. Defaults sum to 1.0 (match 0.5, salience 0.3,
	// warmth 0.2) but aren't forced to — callers can overweight a signal if
	// metrics show it correlates with perceived usefulness.
	matchWeight    float64
	salienceWeight float64
	warmthWeight   float64

	// Access bonus: decaying score reward for frequently-accessed entries.
	// Computed at recall time as: min(cap, access_count * alpha * decay^days)
	// This is separate from salience (which bakes in access at write time) and
	// warmth (which is transient spreading activation). Access bonus captures
	// "this entry has been useful repeatedly" with exponential time decay.
	accessBonusWeight float64 // weight in blended score (default 0.15)
	accessBonusAlpha  float64 // per-access contribution (default 0.1)
	accessBonusCap    float64 // maximum bonus value (default 0.3)
	accessBonusDecay  float64 // daily decay factor (default 0.95)

	// Warmth-floor injection: append up to warmthInjectLimit high-warmth LTM
	// entries that didn't match the query keywords, at the tail of recall
	// results. Kill-switch: warmthInjectLimit <= 0.
	warmthInjectFloor float64 // min warmth to qualify (default 0.7)
	warmthInjectLimit int     // max injected per recall (default 2; <=0 disables)

	// pendingEdgeKeys accumulates LTM keys stored since the last autoFlush so
	// namespace edges can be materialized in batch rather than per-store.
	// Protected by mu.
	pendingEdgeKeys map[string]struct{}

	// Spreading metrics (mu-protected). Session-lifetime counters; reset on
	// Brain restart. Used by Status() to report effectiveness.
	spreadEvents    int64
	totalBoost      float64
	totalBoostCount int64
	clusterHitCount int64
	directHitCount  int64
	warmthHitCount  int64

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
		maxWMEntriesPerUser:    DefaultMaxWMEntriesPerUser,
		ltmEvictionGrace:       DefaultLTMEvictionGrace,
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
		accessBonusWeight:      0.15,
		accessBonusAlpha:       0.1,
		accessBonusCap:         0.3,
		accessBonusDecay:       0.95,
		warmthInjectFloor:      0.7,
		warmthInjectLimit:      2,
		pendingEdgeKeys:        make(map[string]struct{}),
		stopCh:                 make(chan struct{}),
	}
	for _, opt := range opts {
		opt(b)
	}
	// Migrations run after options: migration 9 depends on the configured
	// recency weight (conduit-31jg.53).
	if err := runMigrations(db, migrationParams{recencyWeight: b.recencyWeight}); err != nil {
		db.Close()
		return nil, fmt.Errorf("brain migrations: %w", err)
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
		rescue := b.enforceWMCapLocked(userID, map[string]bool{key: true}, now)
		b.mu.Unlock()
		b.promoteEvicted(rescue)
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
			// conduit-31jg.53: store base salience only (access + tier);
			// recency is computed at query time.
			stmt, err := tx.PrepareContext(ctx, `
				INSERT INTO brain_ltm (key, value, source, created_at, accessed_at, access_count, salience)
				VALUES (?, ?, ?, ?, ?, 1, ?)
				ON CONFLICT(key) DO UPDATE SET
					value = excluded.value,
					source = excluded.source,
					accessed_at = excluded.accessed_at,
					access_count = access_count + 1,
					salience = `+b.ltmBaseSalienceSQL("access_count + 1")+`
			`)
			if err != nil {
				tx.Rollback()
				return err
			}
			defer stmt.Close()
			for _, e := range ltmBatch {
				if _, err := stmt.ExecContext(ctx, e.Key, e.Value, e.Source, nowStr, nowStr,
					b.ltmBaseSalience(1)); err != nil {
					tx.Rollback()
					return err
				}
			}
			return tx.Commit()
		})
		if err != nil {
			return fmt.Errorf("store bulk LTM: %w", err)
		}

		// Best-effort eviction — same pattern as storeLTM. Every row in this
		// batch has accessed_at = nowStr, so it is inside the grace window.
		// conduit-31jg.28
		b.evictLTMOverCapacity(ctx, now)
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
		protect := make(map[string]bool, len(wmBatch))
		for _, e := range wmBatch {
			protect[e.Key] = true
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
		// A bulk larger than the cap can't protect every key it just wrote;
		// in that case the batch's own writes are fair game too.
		if b.maxWMEntriesPerUser > 0 && len(protect) >= b.maxWMEntriesPerUser {
			protect = nil
		}
		rescue := b.enforceWMCapLocked(userID, protect, now)
		b.mu.Unlock()
		b.promoteEvicted(rescue)
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
	// conduit-31jg.53: the column holds base salience (access + tier) only;
	// recency is computed at query time (see salience.go).
	err := database.RetryOnBusy(5, func() error {
		_, err := b.db.Exec(`
			INSERT INTO brain_ltm (key, value, source, created_at, accessed_at, access_count, salience, expires_at)
			VALUES (?, ?, ?, ?, ?, 1, ?, ?)
			ON CONFLICT(key) DO UPDATE SET
				value = excluded.value,
				source = excluded.source,
				accessed_at = excluded.accessed_at,
				access_count = access_count + 1,
				expires_at = excluded.expires_at,
				salience = `+b.ltmBaseSalienceSQL("access_count + 1")+`
		`, key, value, source, nowStr, nowStr, b.ltmBaseSalience(1), expiresStr)
		return err
	})
	if err != nil {
		return fmt.Errorf("store LTM: %w", err)
	}
	// Best-effort eviction. The row just written has accessed_at = nowStr and
	// is therefore inside the grace window. conduit-31jg.28
	b.evictLTMOverCapacity(context.Background(), now)
	b.markPendingEdge(key)
	return nil
}

// DefaultLTMEvictionGrace is how long a newly written/accessed LTM row is
// exempt from capacity eviction. conduit-31jg.28
const DefaultLTMEvictionGrace = time.Hour

// ltmEvictionScoreSQL ranks LTM rows for capacity eviction using the
// query-independent part of Recall's blended score:
//
//	salience*salienceWeight + warmth*warmthWeight + accessBonus*accessBonusWeight
//
// where accessBonus mirrors computeAccessBonus (recency computed at query time
// from accessed_at, never baked in). Lowest score is evicted first.
// Placeholders: salienceWeight, warmthWeight, accessBonusCap, accessBonusAlpha,
// accessBonusDecay, accessBonusWeight. conduit-31jg.28
//
// conduit-31jg.53: salience here is the query-time effective salience (base +
// recency computed from accessed_at), matching what Recall ranks on.
func (b *Brain) ltmEvictionScoreSQL() string {
	return `(
	` + b.EffectiveSalienceSQL() + ` * ? +
	COALESCE(warmth, 0) * ? +
	COALESCE(MIN(?, MAX(access_count, 0) * ? *
		pow(?, MAX(0.0, julianday('now') - julianday(accessed_at)))), 0) * ?
)`
}

// evictLTMOverCapacity trims brain_ltm down to maxLTMEntries.
//
// conduit-31jg.28: the previous implementation deleted ORDER BY salience ASC,
// and because the upsert bakes a recency of 1.0 into salience for any touched
// row (~0.564) while fresh inserts get 0.5, the row just inserted was almost
// always the one deleted. Now:
//   - rows accessed/written within ltmEvictionGrace of now are never
//     candidates, so the current Store/StoreBulk write can't be evicted (the
//     table may briefly exceed the cap if everything is that fresh);
//   - candidates are ranked by the same salience/warmth/access-bonus signal
//     Recall uses, so old-but-valuable facts are not evicted merely for age;
//   - evicted rows are archived to brain_archive (reason 'capacity') in the
//     same transaction as the delete, and each key is logged.
//
// Best-effort: errors are logged, never returned, matching prior behaviour.
func (b *Brain) evictLTMOverCapacity(ctx context.Context, now time.Time) {
	if b.maxLTMEntries <= 0 {
		return
	}
	grace := b.ltmEvictionGrace
	if grace < 0 {
		grace = 0
	}
	// Strict "<" against cutoff: with grace == 0 the cutoff equals the
	// write's own timestamp, so the current write is still protected.
	cutoff := now.Add(-grace).UTC().Format("2006-01-02 15:04:05")

	// Fast path: a single read, no transaction, when under capacity (the
	// common case). The count is re-checked inside the tx below.
	var total int
	if err := b.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM brain_ltm`).Scan(&total); err != nil {
		log.Printf("Brain: LTM capacity check failed: %v", err)
		return
	}
	if total <= b.maxLTMEntries {
		return
	}

	var evicted []string
	var shortfall int
	err := database.RetryOnBusy(5, func() error {
		evicted, shortfall = nil, 0
		tx, err := b.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()

		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM brain_ltm`).Scan(&count); err != nil {
			return err
		}
		excess := count - b.maxLTMEntries
		if excess <= 0 {
			return nil
		}

		rows, err := tx.QueryContext(ctx, `
			SELECT key, value, COALESCE(source, ''), `+b.PeakSalienceSQL()+`
			FROM brain_ltm
			WHERE accessed_at < ? AND created_at < ?
			ORDER BY `+b.ltmEvictionScoreSQL()+` ASC, accessed_at ASC
			LIMIT ?`,
			cutoff, cutoff,
			b.salienceWeight, b.warmthWeight,
			b.accessBonusCap, b.accessBonusAlpha, b.accessBonusDecay, b.accessBonusWeight,
			excess)
		if err != nil {
			return err
		}
		type victim struct {
			key, value, source string
			salience           float64
		}
		var victims []victim
		for rows.Next() {
			var v victim
			if err := rows.Scan(&v.key, &v.value, &v.source, &v.salience); err != nil {
				rows.Close()
				return err
			}
			victims = append(victims, v)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		for _, v := range victims {
			if _, err := tx.ExecContext(ctx, `
				INSERT OR REPLACE INTO brain_archive (key, value, source, tier, salience, reason, archived_at)
				VALUES (?, ?, ?, 'longterm', ?, 'capacity', datetime('now'))`,
				v.key, v.value, v.source, v.salience); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM brain_ltm WHERE key = ?`, v.key); err != nil {
				return err
			}
			evicted = append(evicted, v.key)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		shortfall = excess - len(victims)
		return nil
	})
	if err != nil {
		log.Printf("Brain: LTM capacity eviction failed: %v", err)
		return
	}
	for _, k := range evicted {
		log.Printf("Brain: LTM at capacity (%d) — archived and evicted key=%q", b.maxLTMEntries, k)
	}
	if shortfall > 0 {
		log.Printf("Brain: LTM over capacity by %d but all remaining rows are within the %s eviction grace window; deferring",
			shortfall, grace)
	}
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
	// conduit-31jg.32: one write-locked section for the WM lookup + access
	// bump, and callers always get a snapshot copy, never the live *Entry
	// (autoFlush/Consolidate/Store mutate live entries under b.mu).
	b.mu.Lock()
	if wm, ok := b.working[userID]; ok {
		if entry, ok := wm[key]; ok {
			if entry.ExpiresAt != nil && !entry.ExpiresAt.After(now) {
				// Expired — delete and fall through to LTM lookup.
				delete(wm, key)
				b.mu.Unlock()
				return b.getLTM(key)
			}
			entry.AccessedAt = now
			entry.AccessCount++
			entry.Salience = b.computeSalience(entry)
			copied := *entry
			b.mu.Unlock()
			return &copied, nil
		}
	}
	// Parent's WM, then the shared bucket (read-only — copies, no access
	// bump). conduit-31jg.30
	for _, bucket := range readOnlyBuckets(userID, parentUserIDFromCtx(ctx)) {
		if entry, ok := b.working[bucket][key]; ok {
			if entry.ExpiresAt != nil && !entry.ExpiresAt.After(now) {
				continue // expired — keep looking, then LTM
			}
			copied := *entry
			b.mu.Unlock()
			return &copied, nil
		}
	}
	b.mu.Unlock()
	entry, err := b.getLTM(key)
	if err == nil && entry != nil {
		// Fire-and-forget: spread activation to neighbours. Errors are non-fatal.
		_ = b.spreadActivation([]string{key})
	}
	return entry, err
}

func (b *Brain) getLTM(key string) (*Entry, error) {
	// conduit-31jg.53: store base salience; return the effective value (just
	// accessed, so recency = 1).
	row := b.db.QueryRow(`
		UPDATE brain_ltm SET
			accessed_at = datetime('now'),
			access_count = access_count + 1,
			salience = `+b.ltmBaseSalienceSQL("access_count + 1")+`
		WHERE key = ? AND (expires_at IS NULL OR expires_at > strftime('%Y-%m-%d %H:%M:%f', 'now'))
		RETURNING key, value, created_at, accessed_at, access_count, `+b.EffectiveSalienceSQL()+`, source, stale, expires_at, warmth
	`, key)
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

// defaultRecallEventsPath is the production location of the recall-event log
// consumed by brain_spread reinforcement.
const defaultRecallEventsPath = "/home/jules/ocgo/workspace/memory/recall-events.jsonl"

// recallEventsPath is where recall events are logged for brain_spread
// reinforcement. Package var so tests can redirect to a temp dir.
var recallEventsPath = defaultRecallEventsPath

func (b *Brain) logRecallEvent(query string, results []*Entry) {
	// Best-effort logging: failure must not fail recall
	type recallEvent struct {
		Timestamp string   `json:"ts"`
		Query     string   `json:"query"`
		Keys      []string `json:"keys"`
		Tiers     []string `json:"tiers"`
	}

	if len(results) == 0 {
		return
	}
	// Test binaries (this package and every package that drives a real Brain,
	// e.g. tools/core, gateway, rem) must never append synthetic queries to
	// the live production log. Tests that want events redirect
	// recallEventsPath to a temp file first.
	if recallEventsPath == defaultRecallEventsPath && testing.Testing() {
		return
	}

	event := recallEvent{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Query:     query,
		Keys:      make([]string, 0, len(results)),
		Tiers:     make([]string, 0, len(results)),
	}

	for _, entry := range results {
		event.Keys = append(event.Keys, entry.Key)
		event.Tiers = append(event.Tiers, string(entry.Tier))
	}

	data, err := json.Marshal(event)
	if err != nil {
		log.Printf("Brain: failed to marshal recall event: %v", err)
		return
	}

	file, err := os.OpenFile(recallEventsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("Brain: failed to open recall-events.jsonl: %v", err)
		return
	}
	defer file.Close()

	if _, err := file.Write(append(data, '\n')); err != nil {
		log.Printf("Brain: failed to write recall event: %v", err)
	}
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
					copied := *entry // conduit-31jg.32: snapshot, never the live entry
					results = append(results, &copied)
					seen[entry.Key] = true
				}
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
			if !seen[entry.Key] && strings.HasPrefix(entry.Key, prefix) {
				if sourcePrefix == "" || strings.HasPrefix(entry.Source, sourcePrefix) {
					copied := *entry
					results = append(results, &copied)
					seen[entry.Key] = true
				}
			}
		}
	}
	b.mu.RUnlock()

	query := `SELECT key, value, created_at, accessed_at, access_count, ` + b.EffectiveSalienceSQL() + `, source, stale, expires_at
		FROM brain_ltm WHERE key LIKE ? ESCAPE '\' AND (expires_at IS NULL OR expires_at > strftime('%Y-%m-%d %H:%M:%f', 'now'))`
	args := []interface{}{EscapeLike(prefix) + "%"}
	if sourcePrefix != "" {
		query += ` AND source LIKE ? ESCAPE '\'`
		args = append(args, EscapeLike(sourcePrefix)+"%")
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
	if err := rows.Err(); err != nil {
		return results, fmt.Errorf("list LTM rows: %w", err)
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

	// conduit-31jg.32: toPromote holds snapshots — storeLTM runs outside the
	// lock while Store/Get/autoFlush keep mutating the live entries.
	var toPromote []Entry
	var toEvict []string
	now := time.Now()
	for key, entry := range wm {
		entry.Salience = b.computeSalience(entry)
		if autoPromote && entry.Salience >= b.consolidateThreshold {
			toPromote = append(toPromote, *entry)
		} else if b.wmEvictable(entry, now) {
			// conduit-31jg.29: hot entries are promoted (or kept, when
			// promotion is off) rather than evicted.
			if b.wmIsHot(entry) {
				if autoPromote {
					toPromote = append(toPromote, *entry)
				}
				continue
			}
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
			for _, snap := range toPromote {
				// Only drop the WM copy if it still holds the value we
				// promoted; a concurrent Store of a newer value stays in WM.
				if live, ok := wm[snap.Key]; ok && live.Value == snap.Value && containsString(promotedKeys, snap.Key) {
					delete(wm, snap.Key)
				}
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
	rows, err := b.db.Query(`SELECT key FROM brain_ltm WHERE expires_at IS NULL OR expires_at > strftime('%Y-%m-%d %H:%M:%f', 'now') ORDER BY ` + b.EffectiveSalienceSQL() + ` DESC LIMIT 5`)
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
	warmthHits := b.warmthHitCount
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

	// Access bonus metrics: average and top-5 keys by access bonus.
	var avgAccessBonus float64
	var topAccessBonusKeys []string
	if bonusRows, err := b.db.Query(`
		SELECT key, access_count, accessed_at
		  FROM brain_ltm
		 WHERE access_count > 0 AND (expires_at IS NULL OR expires_at > strftime('%Y-%m-%d %H:%M:%f', 'now'))
		 ORDER BY accessed_at DESC
		 LIMIT 100
	`); err == nil {
		var totalBonus float64
		var count int
		type bonusEntry struct {
			key   string
			bonus float64
		}
		var entries []bonusEntry
		for bonusRows.Next() {
			var key string
			var ac int
			var aa sql.NullTime
			if bonusRows.Scan(&key, &ac, &aa) != nil {
				continue
			}
			e := &Entry{AccessCount: ac, Tier: TierLongTerm}
			if aa.Valid {
				e.AccessedAt = aa.Time
			}
			bonus := b.computeAccessBonus(e)
			if bonus > 0 {
				totalBonus += bonus
				count++
				entries = append(entries, bonusEntry{key: key, bonus: bonus})
			}
		}
		bonusRows.Close()
		if count > 0 {
			avgAccessBonus = totalBonus / float64(count)
		}
		// Sort by bonus descending and take top 5.
		sort.Slice(entries, func(i, j int) bool { return entries[i].bonus > entries[j].bonus })
		limit := 5
		if len(entries) < limit {
			limit = len(entries)
		}
		topAccessBonusKeys = make([]string, limit)
		for i := 0; i < limit; i++ {
			topAccessBonusKeys[i] = entries[i].key
		}
	}

	return &Status{
		LTMEntries: ltmCount, WMEntries: wmCount, ScratchDepth: scratchDepth,
		AvgSalience: avgSalience, HottestKeys: hottestKeys, ColdestKeys: coldestKeys,
		ExpiringSoon:    expiringSoon,
		SpreadEvents:    spreadEvents,
		AvgWarmthBoost:  avgBoost,
		ClusterHitRate:  clusterHitRate,
		WarmthHitCount:  warmthHits,
		EdgeCountByType: edgeCounts,
		AvgAccessBonus:  avgAccessBonus,
		TopAccessBonus:  topAccessBonusKeys,
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

// DefaultMaxWMEntriesPerUser is the default per-user working-memory cap.
// conduit-31jg.29
const DefaultMaxWMEntriesPerUser = 1000

// wmEvictMinIdle is how long a WM entry must go untouched before it can be
// evicted by score (autoFlush / Consolidate). conduit-31jg.29
const wmEvictMinIdle = time.Hour

// wmRef is a snapshot of a WM entry plus the bucket it lives in.
type wmRef struct {
	userID string
	entry  Entry
}

// wmEvictScore is the part of a WM entry's salience that actually varies
// between WM entries: access*accessWeight + recency*recencyWeight.
//
// conduit-31jg.29: eviction used to compare full salience against
// evictThreshold, but full salience includes the constant tier term
// (0.5*tierWeight = 0.1 by default), so with the default threshold of 0.1
// `salience < evictThreshold` was unreachable and WM only drained via TTL or
// promotion. The tier term carries no information when comparing WM entries
// with each other, so it is excluded here. With defaults, an entry touched
// once is evictable after ~3.2h idle; one touched 10 times after ~5.7h;
// 25+ touches keep it indefinitely (it will have been promoted as hot).
func (b *Brain) wmEvictScore(e *Entry, now time.Time) float64 {
	accessScore := 0.0
	if b.accessCountCap > 0 {
		accessScore = math.Min(float64(e.AccessCount)/float64(b.accessCountCap), 1.0)
	}
	hoursSince := now.Sub(e.AccessedAt).Hours()
	if hoursSince < 0 {
		hoursSince = 0
	}
	recencyScore := 1.0 / (1.0 + hoursSince*b.recencyDecayRate)
	return accessScore*b.accessWeight + recencyScore*b.recencyWeight
}

// wmEvictable reports whether a WM entry is cold enough to leave WM.
func (b *Brain) wmEvictable(e *Entry, now time.Time) bool {
	return now.Sub(e.AccessedAt) > wmEvictMinIdle && b.wmEvictScore(e, now) < b.evictThreshold
}

// wmIsHot mirrors REM's heat-promotion rule: an entry accessed at least
// heatPromotionThreshold times is worth keeping in LTM regardless of salience.
func (b *Brain) wmIsHot(e *Entry) bool {
	return b.heatPromotionThreshold > 0 && e.AccessCount >= b.heatPromotionThreshold
}

// enforceWMCapLocked trims userID's WM bucket to maxWMEntriesPerUser, never
// choosing a key in protect. Victims are the lowest wmEvictScore (oldest
// AccessedAt breaks ties). Hot victims are returned for promotion to LTM when
// autoPromote is on; everything else is dropped. Caller holds b.mu.
// conduit-31jg.29
func (b *Brain) enforceWMCapLocked(userID string, protect map[string]bool, now time.Time) []Entry {
	if b.maxWMEntriesPerUser <= 0 {
		return nil
	}
	wm := b.working[userID]
	excess := len(wm) - b.maxWMEntriesPerUser
	if excess <= 0 {
		return nil
	}
	type cand struct {
		e     *Entry
		score float64
	}
	cands := make([]cand, 0, len(wm))
	for k, e := range wm {
		if protect[k] {
			continue
		}
		cands = append(cands, cand{e, b.wmEvictScore(e, now)})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score < cands[j].score
		}
		return cands[i].e.AccessedAt.Before(cands[j].e.AccessedAt)
	})
	if excess > len(cands) {
		excess = len(cands)
	}
	var rescue []Entry
	for _, c := range cands[:excess] {
		if b.autoPromote && b.wmIsHot(c.e) {
			rescue = append(rescue, *c.e)
		}
		delete(wm, c.e.Key)
	}
	log.Printf("Brain: WM cap (%d) reached for user %q — evicted %d entries (%d promoted to LTM)",
		b.maxWMEntriesPerUser, userID, excess, len(rescue))
	return rescue
}

// promoteEvicted writes cap-evicted hot WM entries to LTM. Must be called
// without b.mu held. Best-effort: failures are logged.
func (b *Brain) promoteEvicted(entries []Entry) {
	for _, e := range entries {
		if err := b.storeLTM(e.Key, e.Value, e.Source, time.Now(), e.ExpiresAt); err != nil {
			log.Printf("Brain: failed to promote cap-evicted WM key %q: %v", e.Key, err)
		}
	}
}

// promoteThenDrop promotes each snapshot to LTM and, only on success, removes
// the WM entry if it is unchanged since the snapshot (a concurrent write keeps
// it in WM). Must be called without b.mu held.
func (b *Brain) promoteThenDrop(refs []wmRef) {
	for _, r := range refs {
		if err := b.storeLTM(r.entry.Key, r.entry.Value, r.entry.Source, time.Now(), r.entry.ExpiresAt); err != nil {
			log.Printf("Brain: failed to promote idle hot WM key %q (kept in WM): %v", r.entry.Key, err)
			continue
		}
		b.mu.Lock()
		if wm, ok := b.working[r.userID]; ok {
			if live, ok := wm[r.entry.Key]; ok && live.Value == r.entry.Value && live.AccessedAt.Equal(r.entry.AccessedAt) {
				delete(wm, r.entry.Key)
				if len(wm) == 0 {
					delete(b.working, r.userID)
				}
			}
		}
		b.mu.Unlock()
	}
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

type brainUserIDKey struct{}

// SharedUserID is the working-memory bucket used by callers that carry no
// user (system writers such as heartbeat alerts under sense.alerts.*). Every
// user can read it (read-only, after their own and their parent's WM); only
// unscoped callers write to it. conduit-31jg.30
const SharedUserID = "default"

// WithUserID scopes working memory and the scratchpad to userID.
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, brainUserIDKey{}, userID)
}

// UserIDFromContext returns the brain user ID explicitly attached with
// WithUserID, and whether one was set.
func UserIDFromContext(ctx context.Context) (string, bool) {
	uid, ok := ctx.Value(brainUserIDKey{}).(string)
	return uid, ok && uid != ""
}

func userIDFromCtx(ctx context.Context) string {
	if uid, ok := UserIDFromContext(ctx); ok {
		return uid
	}
	return SharedUserID
}

// readOnlyBuckets lists the WM buckets a caller may read but not write, in
// lookup order: the parent's (sub-agent sharing), then the shared bucket.
func readOnlyBuckets(userID, parentID string) []string {
	var out []string
	if parentID != "" && parentID != userID {
		out = append(out, parentID)
	}
	if userID != SharedUserID && parentID != SharedUserID {
		out = append(out, SharedUserID)
	}
	return out
}

// WorkingMemoryUserIDs returns the IDs of every user with a non-empty WM
// bucket (used by REM to promote across all users). conduit-31jg.30
func (b *Brain) WorkingMemoryUserIDs() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	ids := make([]string, 0, len(b.working))
	for id, wm := range b.working {
		if len(wm) > 0 {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
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
	// conduit-31jg.29: eviction is decided by wmEvictable (access+recency
	// score, idle > wmEvictMinIdle), which is actually reachable. Hot entries
	// that go idle are promoted to LTM first (promote-then-delete, so a
	// failed promotion loses nothing) instead of silently disappearing
	// before the nightly REM cycle can see them.
	now := time.Now()
	var rescue []wmRef
	b.mu.Lock()
	for userID, wm := range b.working {
		for key, entry := range wm {
			entry.Salience = b.computeSalience(entry)
			if !b.wmEvictable(entry, now) {
				continue
			}
			if b.wmIsHot(entry) {
				if b.autoPromote {
					rescue = append(rescue, wmRef{userID: userID, entry: *entry})
				}
				continue
			}
			delete(wm, key)
		}
		if len(wm) == 0 {
			delete(b.working, userID)
		}
	}
	b.mu.Unlock()
	b.promoteThenDrop(rescue)

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

// ExportGraph returns the complete LTM graph for JSON serialization.
func (b *Brain) ExportGraph() (*Graph, error) {
	ctx := context.Background()
	opts := GraphOptions{
		MinSalience:   0.0, // Include all nodes
		MinConfidence: 0.0, // Include all edges
		ValueTruncate: 0,   // Full values
		NodeLimit:     0,   // No limit
	}
	return b.ListGraph(ctx, opts)
}

// ExportGraphFile exports the LTM graph to a JSON file.
func (b *Brain) ExportGraphFile(path string) error {
	graph, err := b.ExportGraph()
	if err != nil {
		return fmt.Errorf("export graph: %w", err)
	}
	data, err := json.MarshalIndent(graph, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal graph: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	return nil
}
