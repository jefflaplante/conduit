package types

import (
	"context"
	"time"
)

// BrainTier represents a memory tier in the cognitive architecture.
type BrainTier string

const (
	BrainTierLongTerm BrainTier = "longterm"
	BrainTierWorking  BrainTier = "working"
	BrainTierScratch  BrainTier = "scratch"
)

// BrainEntry represents a single fact stored in the brain.
type BrainEntry struct {
	Key         string     `json:"key"`
	Value       string     `json:"value"`
	Tier        BrainTier  `json:"tier"`
	CreatedAt   time.Time  `json:"created_at"`
	AccessedAt  time.Time  `json:"accessed_at"`
	AccessCount int        `json:"access_count"`
	Salience    float64    `json:"salience"`
	Warmth      float64    `json:"warmth,omitempty"`
	Source      string     `json:"source,omitempty"`
	Stale       bool       `json:"stale,omitempty"`
	ClusterHit  bool       `json:"cluster_hit,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}

// BrainStatus reports the current state of the brain service.
type BrainStatus struct {
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
	EdgeCountByType map[string]int `json:"edge_count_by_type,omitempty"`
}

// ConsolidationReport summarizes a consolidation sweep.
type ConsolidationReport struct {
	PromotedCount int      `json:"promoted_count"`
	EvictedCount  int      `json:"evicted_count"`
	LTMSize       int      `json:"ltm_size"`
	PromotedKeys  []string `json:"promoted_keys,omitempty"`
	EvictedKeys   []string `json:"evicted_keys,omitempty"`
}

// BrainClusterResult holds the result of a namespace-clustered recall.
// Direct entries matched the query keywords; Cluster entries share a namespace
// prefix with a direct match but didn't match the keywords themselves.
type BrainClusterResult struct {
	Direct  []*BrainEntry `json:"direct"`
	Cluster []*BrainEntry `json:"cluster"`
}

// BrainBulkEntry is a single entry passed to BrainService.StoreBulk. Tier
// defaults to BrainTierWorking when empty; BrainTierScratch is rejected.
type BrainBulkEntry struct {
	Key    string    `json:"key"`
	Value  string    `json:"value"`
	Tier   BrainTier `json:"tier,omitempty"`
	Source string    `json:"source,omitempty"`
}

// BrainGraphNode is a single node in the brain graph payload.
type BrainGraphNode struct {
	Key         string    `json:"key"`
	Value       string    `json:"value"`
	Source      string    `json:"source,omitempty"`
	Salience    float64   `json:"salience"`
	Warmth      float64   `json:"warmth,omitempty"`
	AccessCount int       `json:"access_count"`
	CreatedAt   time.Time `json:"created_at"`
	Truncated   bool      `json:"truncated,omitempty"`
}

// BrainGraphEdge is a single edge in the brain graph payload.
type BrainGraphEdge struct {
	KeyA            string     `json:"key_a"`
	KeyB            string     `json:"key_b"`
	Relationship    string     `json:"relationship"`
	Confidence      float64    `json:"confidence"`
	LastTraversedAt *time.Time `json:"last_traversed_at,omitempty"`
}

// BrainGraph is the full node+edge payload for the dashboard.
type BrainGraph struct {
	Nodes []*BrainGraphNode `json:"nodes"`
	Edges []*BrainGraphEdge `json:"edges"`
}

// BrainGraphOptions filters and shapes the graph returned by ListGraph.
type BrainGraphOptions struct {
	SourcePrefix  string
	MinSalience   float64
	MinConfidence float64
	ValueTruncate int
	NodeLimit     int
}

// BrainService provides tiered memory (LTM + working + scratchpad) to tools.
type BrainService interface {
	Store(ctx context.Context, key, value string, tier BrainTier, source string) error
	// StoreWithTTL stores an entry that expires after the given duration.
	// A zero ttl means no expiry (equivalent to Store).
	StoreWithTTL(ctx context.Context, key, value string, tier BrainTier, source string, ttl time.Duration) error
	StoreBulk(ctx context.Context, entries []BrainBulkEntry) error
	Get(ctx context.Context, key string) (*BrainEntry, error)
	Recall(ctx context.Context, query string, limit int) ([]*BrainEntry, error)
	// RecallWithContext performs fuzzy recall with an optional context string.
	// If contextStr is non-empty, entries whose key or value contain any context
	// token get a ranking boost; it never filters results. An empty contextStr
	// is equivalent to Recall.
	RecallWithContext(ctx context.Context, query string, limit int, contextStr string) ([]*BrainEntry, error)
	// RecallWithCluster performs a recall augmented by namespace clustering.
	// Returns both direct keyword matches and neighbouring entries that share
	// a namespace prefix with direct matches (BFS expansion).
	RecallWithCluster(ctx context.Context, query string, limit int) (*BrainClusterResult, error)
	List(ctx context.Context, prefix string, sourcePrefix string) ([]*BrainEntry, error)
	// ListGraph returns the LTM graph (nodes + edges), filtered by opts.
	ListGraph(ctx context.Context, opts BrainGraphOptions) (*BrainGraph, error)
	Delete(ctx context.Context, key string) error
	Push(ctx context.Context, userID, value string) error
	Pop(ctx context.Context, userID string) (string, error)
	Peek(ctx context.Context, userID string) (string, error)
	Promote(ctx context.Context, key string) error
	Consolidate(ctx context.Context, autoPromote bool) (*ConsolidationReport, error)
	WorkingMemoryEntries(ctx context.Context) []*BrainEntry
	Status(ctx context.Context) (*BrainStatus, error)
	Close() error
}

// BrainFTSResult represents a single FTS5 search result from brain LTM.
type BrainFTSResult struct {
	Key    string  `json:"key"`
	Value  string  `json:"value"`
	Source string  `json:"source"`
	Rank   float64 `json:"rank"`
}

// BrainFTSSearcher provides FTS5-backed search over brain LTM entries.
type BrainFTSSearcher interface {
	SearchBrain(ctx context.Context, query string, limit int) ([]BrainFTSResult, error)
}

// REMCycleReport is the tool-layer representation of a REM cycle execution report.
type REMCycleReport struct {
	Date          string                 `json:"date"`
	DryRun        bool                   `json:"dry_run"`
	Triage        map[string]interface{} `json:"triage,omitempty"`
	Reflect       map[string]interface{} `json:"reflect,omitempty"`
	Consolidation map[string]interface{} `json:"consolidation,omitempty"`
	Pruning       map[string]interface{} `json:"pruning,omitempty"`
	Integration   map[string]interface{} `json:"integration,omitempty"`
	Grooming      map[string]interface{} `json:"grooming,omitempty"`
}

// REMCycleRunner executes the REM sleep consolidation cycle.
type REMCycleRunner interface {
	RunREMCycle(ctx context.Context, phases []string, dryRun bool) (*REMCycleReport, error)
}
