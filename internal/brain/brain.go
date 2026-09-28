package brain

import (
	"database/sql"
	"fmt"
	"log"
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

type Option func(*Brain)

func WithMaxLTMEntries(n int) Option { return func(b *Brain) { b.maxLTMEntries = n } }

func WithAutoFlushInterval(d time.Duration) Option { return func(b *Brain) { b.autoFlushInterval = d } }

func WithConsolidateThreshold(t float64) Option { return func(b *Brain) { b.consolidateThreshold = t } }

func WithEvictThreshold(t float64) Option { return func(b *Brain) { b.evictThreshold = t } }

func WithAutoPromote(v bool) Option { return func(b *Brain) { b.autoPromote = v } }

func WithWMGracePeriod(d time.Duration) Option { return func(b *Brain) { b.wmGracePeriod = d } }

func WithAccessWeight(w float64) Option { return func(b *Brain) { b.accessWeight = w } }

func WithRecencyWeight(w float64) Option { return func(b *Brain) { b.recencyWeight = w } }

func WithTierWeight(w float64) Option { return func(b *Brain) { b.tierWeight = w } }

func WithRecencyDecayRate(r float64) Option { return func(b *Brain) { b.recencyDecayRate = r } }

func WithAccessCountCap(n int) Option { return func(b *Brain) { b.accessCountCap = n } }

func WithHeatPromotionThreshold(n int) Option { return func(b *Brain) { b.heatPromotionThreshold = n } }

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

	// recallEvents logs recalls for brain_spread (recall_events.go);
	// nil => not logged. conduit-31jg.40
	recallEvents *recallEventLog

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
