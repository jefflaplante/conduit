package ai

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"conduit/internal/fts"
	"conduit/internal/tools/types"
)

// ContextEngine retrieves semantically relevant context to inform model
// routing decisions. It queries search services (FTS5 and/or vector) for
// similar past interactions and extracts routing hints based on historical
// patterns.
//
// The engine is optional — the router works fine without it. When search
// services are unavailable, methods return empty context (no error).
type ContextEngine interface {
	// RetrieveContext returns routing-relevant context for a given request.
	// Returns empty context (not an error) if search services are unavailable.
	RetrieveContext(ctx context.Context, request string) *RoutingContext
}

// RoutingContext holds retrieved context that can inform model selection.
type RoutingContext struct {
	// SimilarRequests are past interactions that resemble the current request.
	SimilarRequests []SimilarRequest `json:"similar_requests,omitempty"`

	// Hints are routing recommendations derived from historical patterns.
	Hints []RoutingHint `json:"hints,omitempty"`

	// SearchLatencyMs is the time taken to retrieve context, for monitoring.
	SearchLatencyMs int64 `json:"search_latency_ms"`

	// Source indicates which search backends contributed results.
	Source string `json:"source"` // "fts5", "vector", "hybrid", "none"
}

// IsEmpty returns true if no context was retrieved.
func (rc *RoutingContext) IsEmpty() bool {
	return len(rc.SimilarRequests) == 0 && len(rc.Hints) == 0
}

// SuggestedTier returns the most frequently suggested model tier from hints,
// or -1 if no hints are available.
func (rc *RoutingContext) SuggestedTier() ModelTier {
	if len(rc.Hints) == 0 {
		return -1
	}
	// Count votes per tier
	votes := make(map[ModelTier]int)
	for _, h := range rc.Hints {
		votes[h.SuggestedTier]++
	}
	// Return tier with most votes
	bestTier := ModelTier(-1)
	bestCount := 0
	for tier, count := range votes {
		if count > bestCount {
			bestTier = tier
			bestCount = count
		}
	}
	return bestTier
}

// SimilarRequest represents a past interaction that resembles the current request.
type SimilarRequest struct {
	// Content is the matched content (truncated for efficiency).
	Content string `json:"content"`

	// Score is the relevance score (higher = more relevant for vector,
	// more negative = more relevant for FTS5 BM25).
	Score float64 `json:"score"`

	// SessionKey identifies the session this came from.
	SessionKey string `json:"session_key,omitempty"`

	// Role is the message role ("user", "assistant").
	Role string `json:"role,omitempty"`

	// Source is which search backend found this ("fts5" or "vector").
	Source string `json:"source"`
}

// RoutingHint is a recommendation for model selection derived from patterns
// observed in similar past requests.
type RoutingHint struct {
	// SuggestedTier is the recommended model tier.
	SuggestedTier ModelTier `json:"suggested_tier"`

	// Confidence is a 0.0-1.0 score indicating how confident this hint is.
	Confidence float64 `json:"confidence"`

	// Reason explains why this tier is suggested.
	Reason string `json:"reason"`
}

// contextCacheEntry stores a cached routing context with expiry.
type contextCacheEntry struct {
	context   *RoutingContext
	expiresAt time.Time
}

// DefaultContextEngine is the standard implementation of ContextEngine.
// It uses FTS5 and/or vector search to find similar past interactions
// and derives routing hints from the results.
type DefaultContextEngine struct {
	// searchService provides FTS5 full-text search (optional).
	searchService types.SearchService

	// vectorService provides vector/semantic search (optional).
	vectorService types.VectorService

	// usageTracker provides model usage data for hint derivation.
	usageTracker *UsageTracker

	// cache stores recent context lookups to avoid redundant searches.
	cache   map[string]*contextCacheEntry
	cacheMu sync.RWMutex

	// cacheTTL is how long cached contexts remain valid.
	cacheTTL time.Duration

	// maxResults is the maximum number of similar requests to retrieve.
	maxResults int

	// searchTimeout is the maximum time to spend on search operations.
	searchTimeout time.Duration

	// maxContentLen is the maximum length of content stored per result.
	maxContentLen int
}

// ContextEngineOption configures a DefaultContextEngine.
type ContextEngineOption func(*DefaultContextEngine)

// WithSearchService sets the FTS5 search service.
func WithSearchService(s types.SearchService) ContextEngineOption {
	return func(e *DefaultContextEngine) {
		e.searchService = s
	}
}

// WithVectorService sets the vector/semantic search service.
func WithVectorService(v types.VectorService) ContextEngineOption {
	return func(e *DefaultContextEngine) {
		e.vectorService = v
	}
}

// WithUsageTracker sets the usage tracker for model usage context.
func WithUsageTracker(ut *UsageTracker) ContextEngineOption {
	return func(e *DefaultContextEngine) {
		e.usageTracker = ut
	}
}

// WithCacheTTL sets the duration for which cached contexts remain valid.
func WithCacheTTL(ttl time.Duration) ContextEngineOption {
	return func(e *DefaultContextEngine) {
		e.cacheTTL = ttl
	}
}

// WithMaxResults sets the maximum number of similar requests to retrieve.
func WithMaxResults(n int) ContextEngineOption {
	return func(e *DefaultContextEngine) {
		if n > 0 {
			e.maxResults = n
		}
	}
}

// WithSearchTimeout sets the maximum time for search operations.
func WithSearchTimeout(d time.Duration) ContextEngineOption {
	return func(e *DefaultContextEngine) {
		e.searchTimeout = d
	}
}

// NewContextEngine creates a new DefaultContextEngine with the given options.
// All services are optional — the engine degrades gracefully when they are nil.
func NewContextEngine(opts ...ContextEngineOption) *DefaultContextEngine {
	e := &DefaultContextEngine{
		cache:         make(map[string]*contextCacheEntry),
		cacheTTL:      30 * time.Second,
		maxResults:    5,
		searchTimeout: 2 * time.Second,
		maxContentLen: 200,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// RetrieveContext implements ContextEngine. It searches for similar past
// interactions and derives routing hints. Returns empty context (never nil,
// never an error) if services are unavailable or search fails.
func (e *DefaultContextEngine) RetrieveContext(ctx context.Context, request string) *RoutingContext {
	if request == "" {
		return &RoutingContext{Source: "none"}
	}

	// Check cache first
	if cached := e.getCached(request); cached != nil {
		return cached
	}

	start := time.Now()

	// Create a timeout context for the search operations
	searchCtx, cancel := context.WithTimeout(ctx, e.searchTimeout)
	defer cancel()

	var (
		ftsResults    []SimilarRequest
		vectorResults []SimilarRequest
		wg            sync.WaitGroup
		ftsMu         sync.Mutex
		vectorMu      sync.Mutex
	)

	// Run FTS5 search in parallel
	if e.searchService != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results := e.searchFTS(searchCtx, request)
			ftsMu.Lock()
			ftsResults = results
			ftsMu.Unlock()
		}()
	}

	// Run vector search in parallel
	if e.vectorService != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results := e.searchVector(searchCtx, request)
			vectorMu.Lock()
			vectorResults = results
			vectorMu.Unlock()
		}()
	}

	wg.Wait()

	// Merge results
	source := e.determineSource(ftsResults, vectorResults)
	merged := e.mergeResults(ftsResults, vectorResults)

	// Derive routing hints from the similar requests
	hints := e.deriveHints(merged)

	rc := &RoutingContext{
		SimilarRequests: merged,
		Hints:           hints,
		SearchLatencyMs: time.Since(start).Milliseconds(),
		Source:          source,
	}

	// Cache the result
	e.putCached(request, rc)

	return rc
}

// searchFTS queries FTS5 for similar messages. Returns empty slice on any error.
func (e *DefaultContextEngine) searchFTS(ctx context.Context, query string) []SimilarRequest {
	results, err := e.searchService.SearchMessages(ctx, query, e.maxResults)
	if err != nil {
		log.Printf("[ContextEngine] FTS5 search failed (degrading gracefully): %v", err)
		return nil
	}

	similar := make([]SimilarRequest, 0, len(results))
	for _, r := range results {
		similar = append(similar, SimilarRequest{
			Content:    e.truncateContent(r.Content),
			Score:      r.Rank,
			SessionKey: r.SessionKey,
			Role:       r.Role,
			Source:     "fts5",
		})
	}
	return similar
}

// searchVector queries vector search for semantically similar content.
// Returns empty slice on any error.
func (e *DefaultContextEngine) searchVector(ctx context.Context, query string) []SimilarRequest {
	results, err := e.vectorService.Search(ctx, query, e.maxResults)
	if err != nil {
		log.Printf("[ContextEngine] Vector search failed (degrading gracefully): %v", err)
		return nil
	}

	similar := make([]SimilarRequest, 0, len(results))
	for _, r := range results {
		sr := SimilarRequest{
			Content: e.truncateContent(r.Content),
			Score:   r.Score,
			Source:  "vector",
		}
		// Extract session key from metadata if available
		if sk, ok := r.Metadata["session_key"]; ok {
			sr.SessionKey = sk
		}
		if role, ok := r.Metadata["role"]; ok {
			sr.Role = role
		}
		similar = append(similar, sr)
	}
	return similar
}

// mergeResults combines FTS5 and vector search results, deduplicating
// and limiting to maxResults. Vector results are preferred for overlap
// since they capture semantic similarity.
func (e *DefaultContextEngine) mergeResults(ftsResults, vectorResults []SimilarRequest) []SimilarRequest {
	if len(ftsResults) == 0 && len(vectorResults) == 0 {
		return nil
	}

	// Use a simple approach: vector results first (better semantic match),
	// then FTS5 results that don't overlap.
	seen := make(map[string]bool)
	var merged []SimilarRequest

	for _, r := range vectorResults {
		key := r.SessionKey + "|" + r.Content
		if !seen[key] {
			seen[key] = true
			merged = append(merged, r)
		}
	}

	for _, r := range ftsResults {
		key := r.SessionKey + "|" + r.Content
		if !seen[key] {
			seen[key] = true
			merged = append(merged, r)
		}
	}

	if len(merged) > e.maxResults {
		merged = merged[:e.maxResults]
	}

	return merged
}

// deriveHints analyzes similar requests to produce routing recommendations.
func (e *DefaultContextEngine) deriveHints(similar []SimilarRequest) []RoutingHint {
	if len(similar) == 0 {
		return nil
	}

	var hints []RoutingHint

	// Hint 1: Complexity inference from similar request patterns
	if hint := e.deriveComplexityHint(similar); hint != nil {
		hints = append(hints, *hint)
	}

	// Hint 2: Model usage patterns from the usage tracker
	if hint := e.deriveUsageHint(); hint != nil {
		hints = append(hints, *hint)
	}

	return hints
}

// deriveComplexityHint analyzes the content of similar requests to infer
// what complexity level (and thus model tier) worked well.
func (e *DefaultContextEngine) deriveComplexityHint(similar []SimilarRequest) *RoutingHint {
	if len(similar) == 0 {
		return nil
	}

	// Analyze the content patterns of similar requests.
	// If similar requests tend to have long assistant responses, they likely
	// required a more capable model.
	var totalLen int
	var assistantCount int
	for _, r := range similar {
		if r.Role == "assistant" {
			totalLen += len(r.Content)
			assistantCount++
		}
	}

	if assistantCount == 0 {
		return nil
	}

	avgLen := totalLen / assistantCount

	// Heuristic: longer assistant responses suggest more complex tasks
	// that benefited from a higher-tier model.
	var tier ModelTier
	var reason string
	var confidence float64

	switch {
	case avgLen > 150:
		tier = TierOpus
		reason = "similar past requests produced long responses, suggesting complex tasks"
		confidence = 0.6
	case avgLen > 80:
		tier = TierSonnet
		reason = "similar past requests produced moderate responses"
		confidence = 0.5
	default:
		tier = TierHaiku
		reason = "similar past requests produced short responses, suggesting simple tasks"
		confidence = 0.4
	}

	// Scale confidence by the number of similar results found
	if len(similar) >= 4 {
		confidence += 0.1
	}
	if confidence > 1.0 {
		confidence = 1.0
	}

	return &RoutingHint{
		SuggestedTier: tier,
		Confidence:    confidence,
		Reason:        reason,
	}
}

// deriveUsageHint produces a hint based on current model usage patterns.
// If the usage tracker shows that a particular model has a high success rate,
// we can recommend it.
func (e *DefaultContextEngine) deriveUsageHint() *RoutingHint {
	if e.usageTracker == nil {
		return nil
	}

	snapshot := e.usageTracker.GetSnapshot()
	if len(snapshot.Models) == 0 {
		return nil
	}

	// Find the model with the best success rate and reasonable usage
	var bestModel string
	var bestSuccessRate float64
	var bestRequests int64

	for _, m := range snapshot.Models {
		if m.TotalRequests < 3 {
			continue // Not enough data
		}
		successRate := 1.0 - (float64(m.ErrorCount) / float64(m.TotalRequests))
		if successRate > bestSuccessRate || (successRate == bestSuccessRate && m.TotalRequests > bestRequests) {
			bestSuccessRate = successRate
			bestRequests = m.TotalRequests
			bestModel = m.Model
		}
	}

	if bestModel == "" {
		return nil
	}

	// Map the best-performing model to a tier
	tier := inferTierFromModel(bestModel)

	return &RoutingHint{
		SuggestedTier: tier,
		Confidence:    bestSuccessRate * 0.5, // Scale down: usage patterns are a weak signal
		Reason:        "model " + bestModel + " has highest success rate in recent usage",
	}
}

// inferTierFromModel guesses the model tier from a model ID string.
func inferTierFromModel(model string) ModelTier {
	lower := strings.ToLower(model)
	if strings.Contains(lower, "opus") {
		return TierOpus
	}
	if strings.Contains(lower, "haiku") {
		return TierHaiku
	}
	return TierSonnet
}

// determineSource returns a label for which search backends contributed results.
func (e *DefaultContextEngine) determineSource(fts, vector []SimilarRequest) string {
	hasFTS := len(fts) > 0
	hasVector := len(vector) > 0

	switch {
	case hasFTS && hasVector:
		return "hybrid"
	case hasFTS:
		return "fts5"
	case hasVector:
		return "vector"
	default:
		return "none"
	}
}

// truncateContent shortens content to maxContentLen for storage efficiency.
func (e *DefaultContextEngine) truncateContent(content string) string {
	if len(content) <= e.maxContentLen {
		return content
	}
	return content[:e.maxContentLen] + "..."
}

// getCached returns a cached context for the given request, or nil if not cached
// or expired.
func (e *DefaultContextEngine) getCached(request string) *RoutingContext {
	key := e.cacheKey(request)

	e.cacheMu.RLock()
	entry, ok := e.cache[key]
	e.cacheMu.RUnlock()

	if !ok || time.Now().After(entry.expiresAt) {
		return nil
	}

	return entry.context
}

// putCached stores a routing context in the cache.
func (e *DefaultContextEngine) putCached(request string, rc *RoutingContext) {
	key := e.cacheKey(request)

	e.cacheMu.Lock()
	defer e.cacheMu.Unlock()

	// Evict expired entries periodically (simple approach: check all on write)
	if len(e.cache) > 100 {
		now := time.Now()
		for k, v := range e.cache {
			if now.After(v.expiresAt) {
				delete(e.cache, k)
			}
		}
	}

	e.cache[key] = &contextCacheEntry{
		context:   rc,
		expiresAt: time.Now().Add(e.cacheTTL),
	}
}

// cacheKey normalizes a request string into a cache key.
// Uses the first 100 characters (lowercased, trimmed) to group similar requests.
func (e *DefaultContextEngine) cacheKey(request string) string {
	key := strings.ToLower(strings.TrimSpace(request))
	if len(key) > 100 {
		key = key[:100]
	}
	return key
}

// ClearCache removes all cached contexts.
func (e *DefaultContextEngine) ClearCache() {
	e.cacheMu.Lock()
	defer e.cacheMu.Unlock()
	e.cache = make(map[string]*contextCacheEntry)
}

// --- Mock implementations for testing ---

// mockSearchService implements types.SearchService for testing.
type mockSearchService struct {
	messages []fts.MessageResult
	err      error
}

func (m *mockSearchService) SearchDocuments(_ context.Context, _ string, _ int) ([]fts.DocumentResult, error) {
	return nil, m.err
}

func (m *mockSearchService) SearchMessages(_ context.Context, _ string, _ int) ([]fts.MessageResult, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.messages, nil
}

func (m *mockSearchService) SearchBeads(_ context.Context, _ string, _ int, _ string) ([]fts.BeadsResult, error) {
	return nil, m.err
}

func (m *mockSearchService) Search(_ context.Context, _ string, _ int) ([]fts.SearchResult, error) {
	return nil, m.err
}

// mockVectorService implements types.VectorService for testing.
type mockVectorService struct {
	results []types.VectorSearchResult
	err     error
}

func (m *mockVectorService) Search(_ context.Context, _ string, _ int) ([]types.VectorSearchResult, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.results, nil
}

func (m *mockVectorService) Index(_ context.Context, _, _ string, _ map[string]string) error {
	return nil
}

func (m *mockVectorService) Remove(_ context.Context, _ string) error {
	return nil
}

func (m *mockVectorService) Close() error {
	return nil
}
