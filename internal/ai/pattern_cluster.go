package ai

import (
	"crypto/sha256"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// Default configuration constants for the pattern analyzer.
const (
	// defaultMaxPatterns is the maximum number of patterns stored (rolling window).
	defaultMaxPatterns = 1000

	// defaultClusterThreshold is the minimum cosine similarity for two patterns
	// to be considered part of the same cluster.
	defaultClusterThreshold = 0.70

	// defaultReclusterInterval is the minimum number of new patterns recorded
	// before triggering an automatic recluster.
	defaultReclusterInterval = 50

	// defaultMinClusterSize is the minimum number of members for a cluster to
	// be considered significant enough to produce recommendations.
	defaultMinClusterSize = 3

	// featureVectorDimensions is the number of features in the pattern vector.
	featureVectorDimensions = 6
)

// RequestPattern captures the characteristics of a completed request for
// historical analysis and cluster membership. Each recorded pattern represents
// one router decision and its outcome.
type RequestPattern struct {
	// ID is a unique identifier derived from the request content hash and timestamp.
	ID string `json:"id"`

	// RequestHash is a SHA-256 prefix of the original request for deduplication.
	RequestHash string `json:"request_hash"`

	// Complexity is the analyzed complexity score (0-100).
	ComplexityScore int `json:"complexity_score"`

	// ComplexityLevel is the derived complexity tier.
	ComplexityLevel ComplexityLevel `json:"complexity_level"`

	// ToolCount is the number of tools that were available for this request.
	ToolCount int `json:"tool_count"`

	// MessageLength is the character count of the user message.
	MessageLength int `json:"message_length"`

	// WordCount is the word count of the user message.
	WordCount int `json:"word_count"`

	// Model is the model that ultimately handled the request.
	Model string `json:"model"`

	// Tier is the model tier that was selected.
	Tier ModelTier `json:"tier"`

	// Success indicates whether the request completed without error.
	Success bool `json:"success"`

	// LatencyMs is the total latency including any fallbacks.
	LatencyMs int64 `json:"latency_ms"`

	// FallbacksAttempted is the number of fallback models tried.
	FallbacksAttempted int `json:"fallbacks_attempted"`

	// ContextInfluenced indicates whether the context engine affected routing.
	ContextInfluenced bool `json:"context_influenced"`

	// RecordedAt is when this pattern was recorded.
	RecordedAt time.Time `json:"recorded_at"`

	// featureVector is the normalized feature vector used for similarity
	// calculations. Computed once at recording time for efficiency.
	featureVector [featureVectorDimensions]float64
}

// PatternCluster is a group of similar request patterns. Clusters are formed
// by grouping patterns whose feature vectors fall within a cosine similarity
// threshold of the cluster centroid.
type PatternCluster struct {
	// ID is a unique identifier for this cluster.
	ID string `json:"id"`

	// Description is a human-readable summary of the cluster.
	Description string `json:"description"`

	// MemberCount is the number of patterns in this cluster.
	MemberCount int `json:"member_count"`

	// DominantModel is the model most frequently used by cluster members.
	DominantModel string `json:"dominant_model"`

	// DominantTier is the tier most frequently used by cluster members.
	DominantTier ModelTier `json:"dominant_tier"`

	// AvgSuccessRate is the average success rate across cluster members (0.0-1.0).
	AvgSuccessRate float64 `json:"avg_success_rate"`

	// AvgLatencyMs is the average latency across cluster members.
	AvgLatencyMs float64 `json:"avg_latency_ms"`

	// AvgComplexity is the average complexity score of members.
	AvgComplexity float64 `json:"avg_complexity"`

	// centroid is the mean feature vector of all cluster members.
	centroid [featureVectorDimensions]float64

	// memberIDs tracks which patterns belong to this cluster.
	memberIDs []string
}

// ClusterRecommendation is a routing recommendation based on which cluster
// a new request is most similar to.
type ClusterRecommendation struct {
	// SuggestedTier is the recommended model tier based on cluster history.
	SuggestedTier ModelTier `json:"suggested_tier"`

	// SuggestedModel is the specific model recommended (the dominant model
	// from the best-matching cluster).
	SuggestedModel string `json:"suggested_model"`

	// Confidence is how confident this recommendation is (0.0-1.0), derived
	// from the similarity score and cluster quality metrics.
	Confidence float64 `json:"confidence"`

	// Reason is a human-readable explanation of the recommendation.
	Reason string `json:"reason"`

	// ClusterID is the ID of the cluster this recommendation is based on.
	ClusterID string `json:"cluster_id"`

	// SimilarPatterns is the number of historical patterns in the matching cluster.
	SimilarPatterns int `json:"similar_patterns"`

	// AvgSuccessRate is the success rate of the matching cluster.
	AvgSuccessRate float64 `json:"avg_success_rate"`
}

// ToRoutingHint converts a ClusterRecommendation into a RoutingHint that
// the context engine can consume.
func (cr *ClusterRecommendation) ToRoutingHint() RoutingHint {
	return RoutingHint{
		SuggestedTier: cr.SuggestedTier,
		Confidence:    cr.Confidence,
		Reason:        cr.Reason,
	}
}

// PatternAnalyzerOption configures a PatternAnalyzer.
type PatternAnalyzerOption func(*PatternAnalyzer)

// WithMaxPatterns sets the maximum number of patterns to retain.
func WithMaxPatterns(n int) PatternAnalyzerOption {
	return func(pa *PatternAnalyzer) {
		if n > 0 {
			pa.maxPatterns = n
		}
	}
}

// WithClusterThreshold sets the cosine similarity threshold for clustering.
func WithClusterThreshold(t float64) PatternAnalyzerOption {
	return func(pa *PatternAnalyzer) {
		if t > 0 && t <= 1.0 {
			pa.clusterThreshold = t
		}
	}
}

// WithReclusterInterval sets how many new patterns trigger a recluster.
func WithReclusterInterval(n int) PatternAnalyzerOption {
	return func(pa *PatternAnalyzer) {
		if n > 0 {
			pa.reclusterInterval = n
		}
	}
}

// WithMinClusterSize sets the minimum number of members for a significant cluster.
func WithMinClusterSize(n int) PatternAnalyzerOption {
	return func(pa *PatternAnalyzer) {
		if n > 0 {
			pa.minClusterSize = n
		}
	}
}

// PatternAnalyzer is the main service for usage pattern clustering. It records
// completed request patterns, groups them into clusters using vector similarity,
// and provides cluster-based routing recommendations.
//
// Thread-safe: all public methods are safe for concurrent use.
type PatternAnalyzer struct {
	mu sync.RWMutex

	// patterns is the rolling window of recorded patterns, ordered by time.
	patterns []RequestPattern

	// clusters is the current set of pattern clusters.
	clusters []PatternCluster

	// patternIndex maps pattern ID to index in the patterns slice for fast lookup.
	patternIndex map[string]int

	// maxPatterns is the rolling window cap.
	maxPatterns int

	// clusterThreshold is the minimum cosine similarity for cluster membership.
	clusterThreshold float64

	// reclusterInterval is how many new patterns since last recluster before
	// triggering automatic reclustering.
	reclusterInterval int

	// minClusterSize is the minimum members for a cluster to produce recommendations.
	minClusterSize int

	// newSinceRecluster counts patterns added since the last recluster.
	newSinceRecluster int

	// featureStats tracks running min/max for feature normalization.
	featureStats featureNormStats
}

// featureNormStats tracks min/max values for each feature dimension to enable
// consistent normalization. Updated as patterns are recorded.
type featureNormStats struct {
	// We track the observed range for each dimension so we can normalize
	// features to [0,1] before computing cosine similarity. This avoids
	// features with large absolute values (like message length) dominating.
	maxValues [featureVectorDimensions]float64
	minValues [featureVectorDimensions]float64
	count     int
}

// NewPatternAnalyzer creates a new pattern analyzer with the given options.
func NewPatternAnalyzer(opts ...PatternAnalyzerOption) *PatternAnalyzer {
	pa := &PatternAnalyzer{
		patterns:          make([]RequestPattern, 0, 256),
		clusters:          make([]PatternCluster, 0),
		patternIndex:      make(map[string]int),
		maxPatterns:       defaultMaxPatterns,
		clusterThreshold:  defaultClusterThreshold,
		reclusterInterval: defaultReclusterInterval,
		minClusterSize:    defaultMinClusterSize,
	}

	// Initialize feature stats with sensible defaults so early patterns
	// get reasonable normalization even before we have enough data.
	pa.featureStats.maxValues = [featureVectorDimensions]float64{
		100,  // complexity score (0-100)
		20,   // tool count
		5000, // message length
		500,  // word count
		2,    // complexity level (0-2)
		1,    // context influenced (0-1)
	}
	// minValues default to 0 which is correct for all features.

	for _, opt := range opts {
		opt(pa)
	}
	return pa
}

// RecordPattern records a completed request's pattern for historical analysis.
// It extracts features from the SmartRoutingResult and the original request
// text, computes a feature vector, and stores the pattern. If the rolling
// window is full, the oldest pattern is evicted.
//
// A nil result is safely ignored (no-op).
func (pa *PatternAnalyzer) RecordPattern(result *SmartRoutingResult, request string, toolCount int, success bool) {
	if result == nil {
		return
	}

	pa.mu.Lock()
	defer pa.mu.Unlock()

	pattern := RequestPattern{
		ID:                 pa.generatePatternID(request),
		RequestHash:        hashRequest(request),
		ComplexityScore:    result.Complexity.Score,
		ComplexityLevel:    result.Complexity.Level,
		ToolCount:          toolCount,
		MessageLength:      len(request),
		WordCount:          len(strings.Fields(request)),
		Model:              result.SelectedModel,
		Tier:               result.Tier,
		Success:            success,
		LatencyMs:          result.TotalLatencyMs,
		FallbacksAttempted: result.FallbacksAttempted,
		ContextInfluenced:  result.ContextInfluenced,
		RecordedAt:         time.Now(),
	}

	// Compute and store feature vector
	pattern.featureVector = pa.computeFeatureVector(&pattern)

	// Update feature normalization stats
	pa.updateFeatureStats(pattern.featureVector)

	// Enforce rolling window capacity
	if len(pa.patterns) >= pa.maxPatterns {
		pa.evictOldest()
	}

	// Append pattern
	pa.patternIndex[pattern.ID] = len(pa.patterns)
	pa.patterns = append(pa.patterns, pattern)
	pa.newSinceRecluster++
}

// FindSimilarPatterns finds the most similar historical patterns to the given
// request characteristics. Returns up to limit patterns sorted by similarity
// (highest first).
func (pa *PatternAnalyzer) FindSimilarPatterns(request string, complexityScore int, complexityLevel ComplexityLevel, toolCount int, limit int) []RequestPattern {
	if limit <= 0 {
		limit = 10
	}

	pa.mu.RLock()
	defer pa.mu.RUnlock()

	if len(pa.patterns) == 0 {
		return nil
	}

	// Build a query feature vector from the request characteristics
	queryVector := pa.computeQueryVector(request, complexityScore, complexityLevel, toolCount)

	// Score all patterns by cosine similarity to the query
	type scored struct {
		pattern    RequestPattern
		similarity float64
	}
	results := make([]scored, 0, len(pa.patterns))

	for _, p := range pa.patterns {
		sim := cosineSimilarity(queryVector, p.featureVector)
		if sim > 0 {
			results = append(results, scored{pattern: p, similarity: sim})
		}
	}

	// Sort by similarity descending
	sort.Slice(results, func(i, j int) bool {
		return results[i].similarity > results[j].similarity
	})

	// Cap at limit
	if len(results) > limit {
		results = results[:limit]
	}

	patterns := make([]RequestPattern, len(results))
	for i, r := range results {
		patterns[i] = r.pattern
	}
	return patterns
}

// GetClusters returns the current cluster state. If clusters have not been
// computed yet (or need refresh), it triggers a recluster first.
func (pa *PatternAnalyzer) GetClusters() []PatternCluster {
	pa.mu.RLock()
	needsRecluster := len(pa.clusters) == 0 && len(pa.patterns) >= pa.minClusterSize
	pa.mu.RUnlock()

	if needsRecluster {
		pa.ReclusterIfNeeded()
	}

	pa.mu.RLock()
	defer pa.mu.RUnlock()

	// Return a copy
	result := make([]PatternCluster, len(pa.clusters))
	copy(result, pa.clusters)
	return result
}

// GetClusterRecommendation returns a routing recommendation based on which
// cluster the given request is most similar to. Returns nil if no clusters
// exist or no sufficiently similar cluster is found.
func (pa *PatternAnalyzer) GetClusterRecommendation(request string, complexityScore int, complexityLevel ComplexityLevel, toolCount int) *ClusterRecommendation {
	// Ensure clusters are fresh
	pa.ReclusterIfNeeded()

	pa.mu.RLock()
	defer pa.mu.RUnlock()

	if len(pa.clusters) == 0 {
		return nil
	}

	queryVector := pa.computeQueryVector(request, complexityScore, complexityLevel, toolCount)

	// Find the most similar cluster by comparing to each cluster's centroid
	var bestCluster *PatternCluster
	bestSimilarity := 0.0

	for i := range pa.clusters {
		sim := cosineSimilarity(queryVector, pa.clusters[i].centroid)
		if sim > bestSimilarity {
			bestSimilarity = sim
			bestCluster = &pa.clusters[i]
		}
	}

	// Require minimum similarity to make a recommendation
	if bestCluster == nil || bestSimilarity < pa.clusterThreshold*0.8 {
		return nil
	}

	// Compute confidence from similarity, cluster size, and success rate
	confidence := pa.computeConfidence(bestSimilarity, bestCluster)

	return &ClusterRecommendation{
		SuggestedTier:   bestCluster.DominantTier,
		SuggestedModel:  bestCluster.DominantModel,
		Confidence:      confidence,
		Reason:          fmt.Sprintf("cluster %q (%d patterns, %.0f%% success) suggests %s tier", bestCluster.Description, bestCluster.MemberCount, bestCluster.AvgSuccessRate*100, bestCluster.DominantTier),
		ClusterID:       bestCluster.ID,
		SimilarPatterns: bestCluster.MemberCount,
		AvgSuccessRate:  bestCluster.AvgSuccessRate,
	}
}

// ReclusterIfNeeded rebuilds clusters if enough new patterns have accumulated
// since the last clustering run. This is safe to call frequently; it short-
// circuits if clustering is not needed.
func (pa *PatternAnalyzer) ReclusterIfNeeded() {
	pa.mu.Lock()
	defer pa.mu.Unlock()

	// Skip if not enough data or not enough new patterns
	if len(pa.patterns) < pa.minClusterSize {
		return
	}
	if len(pa.clusters) > 0 && pa.newSinceRecluster < pa.reclusterInterval {
		return
	}

	pa.reclusterLocked()
	pa.newSinceRecluster = 0
}

// PatternCount returns the number of recorded patterns.
func (pa *PatternAnalyzer) PatternCount() int {
	pa.mu.RLock()
	defer pa.mu.RUnlock()
	return len(pa.patterns)
}

// ClusterCount returns the number of current clusters.
func (pa *PatternAnalyzer) ClusterCount() int {
	pa.mu.RLock()
	defer pa.mu.RUnlock()
	return len(pa.clusters)
}

// Reset clears all patterns and clusters.
func (pa *PatternAnalyzer) Reset() {
	pa.mu.Lock()
	defer pa.mu.Unlock()

	pa.patterns = pa.patterns[:0]
	pa.clusters = pa.clusters[:0]
	pa.patternIndex = make(map[string]int)
	pa.newSinceRecluster = 0
}

// --- Internal methods (must be called with appropriate lock held) ---

// reclusterLocked rebuilds all clusters from the current pattern set.
// Caller must hold pa.mu write lock.
//
// Algorithm: single-pass greedy clustering. For each pattern, find the
// nearest existing cluster centroid. If the similarity exceeds the threshold,
// assign the pattern to that cluster. Otherwise, start a new cluster.
// After assignment, recompute centroids. Finally, prune clusters below
// the minimum size.
func (pa *PatternAnalyzer) reclusterLocked() {
	if len(pa.patterns) == 0 {
		pa.clusters = pa.clusters[:0]
		return
	}

	// Re-normalize all feature vectors using current stats for consistency
	for i := range pa.patterns {
		pa.patterns[i].featureVector = pa.computeFeatureVector(&pa.patterns[i])
	}

	type clusterBuilder struct {
		members  []int // indices into pa.patterns
		centroid [featureVectorDimensions]float64
	}

	var builders []*clusterBuilder

	for i := range pa.patterns {
		vec := pa.patterns[i].featureVector

		// Find nearest cluster
		bestIdx := -1
		bestSim := 0.0
		for ci, cb := range builders {
			sim := cosineSimilarity(vec, cb.centroid)
			if sim > bestSim {
				bestSim = sim
				bestIdx = ci
			}
		}

		if bestIdx >= 0 && bestSim >= pa.clusterThreshold {
			// Add to existing cluster and update centroid incrementally
			cb := builders[bestIdx]
			cb.members = append(cb.members, i)
			cb.centroid = computeCentroid(pa.patterns, cb.members)
		} else {
			// Start a new cluster
			cb := &clusterBuilder{
				members:  []int{i},
				centroid: vec,
			}
			builders = append(builders, cb)
		}
	}

	// Build final clusters, pruning those below minimum size
	pa.clusters = pa.clusters[:0]
	for ci, cb := range builders {
		if len(cb.members) < pa.minClusterSize {
			continue
		}

		cluster := pa.buildCluster(ci, cb.members, cb.centroid)
		pa.clusters = append(pa.clusters, cluster)
	}

	// Sort clusters by member count descending
	sort.Slice(pa.clusters, func(i, j int) bool {
		return pa.clusters[i].MemberCount > pa.clusters[j].MemberCount
	})
}

// buildCluster constructs a PatternCluster from a set of member indices.
func (pa *PatternAnalyzer) buildCluster(idx int, memberIndices []int, centroid [featureVectorDimensions]float64) PatternCluster {
	// Count models and tiers
	modelCounts := make(map[string]int)
	tierCounts := make(map[ModelTier]int)
	var totalLatency int64
	var successCount int
	var totalComplexity int

	memberIDs := make([]string, len(memberIndices))
	for i, mi := range memberIndices {
		p := pa.patterns[mi]
		memberIDs[i] = p.ID
		modelCounts[p.Model]++
		tierCounts[p.Tier]++
		totalLatency += p.LatencyMs
		totalComplexity += p.ComplexityScore
		if p.Success {
			successCount++
		}
	}

	n := len(memberIndices)

	// Find dominant model
	dominantModel := ""
	maxModelCount := 0
	for model, count := range modelCounts {
		if count > maxModelCount {
			maxModelCount = count
			dominantModel = model
		}
	}

	// Find dominant tier
	dominantTier := TierSonnet
	maxTierCount := 0
	for tier, count := range tierCounts {
		if count > maxTierCount {
			maxTierCount = count
			dominantTier = tier
		}
	}

	// Generate description from centroid characteristics
	avgComplexity := float64(totalComplexity) / float64(n)
	description := pa.clusterDescription(avgComplexity, dominantTier, n)

	return PatternCluster{
		ID:             fmt.Sprintf("cluster-%d", idx),
		Description:    description,
		MemberCount:    n,
		DominantModel:  dominantModel,
		DominantTier:   dominantTier,
		AvgSuccessRate: float64(successCount) / float64(n),
		AvgLatencyMs:   float64(totalLatency) / float64(n),
		AvgComplexity:  avgComplexity,
		centroid:       centroid,
		memberIDs:      memberIDs,
	}
}

// clusterDescription generates a human-readable description for a cluster.
func (pa *PatternAnalyzer) clusterDescription(avgComplexity float64, tier ModelTier, memberCount int) string {
	var complexityLabel string
	switch {
	case avgComplexity >= 40:
		complexityLabel = "complex"
	case avgComplexity >= 15:
		complexityLabel = "standard"
	default:
		complexityLabel = "simple"
	}
	return fmt.Sprintf("%s requests routed to %s (%d patterns)", complexityLabel, tier, memberCount)
}

// featureWeights controls the relative importance of each dimension in the
// feature vector. Higher weights make that dimension more influential in
// similarity calculations. The complexity score and complexity level are
// weighted most heavily since they are the strongest signals for routing.
var featureWeights = [featureVectorDimensions]float64{
	3.0, // complexity score — strongest routing signal
	1.5, // tool count — moderate signal
	1.0, // message length — weak signal
	1.0, // word count — weak signal
	2.5, // complexity level — strong categorical signal
	0.5, // context influenced — weak binary signal
}

// computeFeatureVector builds a normalized, weighted feature vector from a
// pattern's characteristics. Each dimension is normalized to [0,1] using
// fixed ranges and then scaled by its feature weight.
func (pa *PatternAnalyzer) computeFeatureVector(p *RequestPattern) [featureVectorDimensions]float64 {
	raw := [featureVectorDimensions]float64{
		float64(p.ComplexityScore),
		float64(p.ToolCount),
		float64(p.MessageLength),
		float64(p.WordCount),
		float64(p.ComplexityLevel),
		boolToFloat(p.ContextInfluenced),
	}

	// Normalize each dimension to [0,1] using fixed min/max, then apply weight
	var vec [featureVectorDimensions]float64
	for i := range vec {
		rangeVal := pa.featureStats.maxValues[i] - pa.featureStats.minValues[i]
		if rangeVal > 0 {
			vec[i] = (raw[i] - pa.featureStats.minValues[i]) / rangeVal
		} else {
			vec[i] = 0
		}
		// Clamp to [0,1]
		if vec[i] < 0 {
			vec[i] = 0
		}
		if vec[i] > 1 {
			vec[i] = 1
		}
		// Apply feature weight
		vec[i] *= featureWeights[i]
	}
	return vec
}

// computeQueryVector builds a feature vector for an incoming request (before
// routing) so it can be compared against stored patterns.
func (pa *PatternAnalyzer) computeQueryVector(request string, complexityScore int, complexityLevel ComplexityLevel, toolCount int) [featureVectorDimensions]float64 {
	p := &RequestPattern{
		ComplexityScore: complexityScore,
		ComplexityLevel: complexityLevel,
		ToolCount:       toolCount,
		MessageLength:   len(request),
		WordCount:       len(strings.Fields(request)),
	}
	return pa.computeFeatureVector(p)
}

// updateFeatureStats updates the observation count for feature normalization.
// The min/max ranges are initialized with sensible defaults at construction
// time and remain fixed to ensure consistent normalization across the
// lifetime of the analyzer.
func (pa *PatternAnalyzer) updateFeatureStats(_ [featureVectorDimensions]float64) {
	pa.featureStats.count++
}

// evictOldest removes the oldest pattern to maintain the rolling window.
func (pa *PatternAnalyzer) evictOldest() {
	if len(pa.patterns) == 0 {
		return
	}

	// Remove the oldest (first) pattern
	oldID := pa.patterns[0].ID
	delete(pa.patternIndex, oldID)

	// Shift patterns
	pa.patterns = pa.patterns[1:]

	// Rebuild the index (necessary after shift)
	pa.patternIndex = make(map[string]int, len(pa.patterns))
	for i, p := range pa.patterns {
		pa.patternIndex[p.ID] = i
	}
}

// generatePatternID creates a unique pattern ID from the request and current time.
func (pa *PatternAnalyzer) generatePatternID(request string) string {
	h := hashRequest(request)
	return fmt.Sprintf("pat-%s-%d", h[:8], time.Now().UnixNano())
}

// computeConfidence calculates a confidence score (0.0-1.0) for a cluster
// recommendation based on similarity, cluster quality, and cluster size.
func (pa *PatternAnalyzer) computeConfidence(similarity float64, cluster *PatternCluster) float64 {
	// Base confidence from similarity (50% weight)
	conf := similarity * 0.5

	// Boost from cluster success rate (25% weight)
	conf += cluster.AvgSuccessRate * 0.25

	// Boost from cluster size relative to minimum (25% weight)
	sizeBoost := float64(cluster.MemberCount) / float64(pa.maxPatterns)
	if sizeBoost > 1.0 {
		sizeBoost = 1.0
	}
	// Scale: 3 members = low boost, 50+ members = high boost
	sizeBoost = math.Min(float64(cluster.MemberCount)/50.0, 1.0)
	conf += sizeBoost * 0.25

	// Clamp to [0,1]
	if conf > 1.0 {
		conf = 1.0
	}
	if conf < 0 {
		conf = 0
	}

	return conf
}

// --- Pure helper functions ---

// hashRequest produces a short SHA-256 hex digest of a request string.
func hashRequest(request string) string {
	h := sha256.Sum256([]byte(request))
	return fmt.Sprintf("%x", h[:8])
}

// cosineSimilarity computes the cosine similarity between two vectors.
// Returns 0 if either vector is zero-length.
func cosineSimilarity(a, b [featureVectorDimensions]float64) float64 {
	var dotProduct, normA, normB float64
	for i := 0; i < featureVectorDimensions; i++ {
		dotProduct += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dotProduct / (math.Sqrt(normA) * math.Sqrt(normB))
}

// computeCentroid calculates the mean feature vector for a set of patterns.
func computeCentroid(patterns []RequestPattern, indices []int) [featureVectorDimensions]float64 {
	var centroid [featureVectorDimensions]float64
	if len(indices) == 0 {
		return centroid
	}

	for _, idx := range indices {
		for d := 0; d < featureVectorDimensions; d++ {
			centroid[d] += patterns[idx].featureVector[d]
		}
	}

	n := float64(len(indices))
	for d := 0; d < featureVectorDimensions; d++ {
		centroid[d] /= n
	}
	return centroid
}

// boolToFloat converts a bool to 0.0 or 1.0.
func boolToFloat(b bool) float64 {
	if b {
		return 1.0
	}
	return 0.0
}
