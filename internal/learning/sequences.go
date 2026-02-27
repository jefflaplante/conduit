package learning

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Default sequence analysis constants.
const (
	// defaultMaxNgramOrder is the maximum n-gram length tracked. We track
	// bigrams (2) and trigrams (3) for action sequence prediction.
	defaultMaxNgramOrder = 3

	// defaultMaxActionsPerSession is the maximum number of recent actions
	// retained per session for sequence analysis.
	defaultMaxActionsPerSession = 200

	// defaultMaxNgrams is the maximum number of distinct n-grams stored
	// per session before pruning low-frequency entries.
	defaultMaxNgrams = 500

	// defaultTimeWindowHours groups interactions into time-of-day buckets.
	// We use 4-hour windows: [0-4), [4-8), [8-12), [12-16), [16-20), [20-24).
	defaultTimeWindowHours = 4

	// maxAgeForDecay is the maximum age of an observation before its
	// influence decays to zero.
	maxAgeForDecay = 30 * 24 * time.Hour // 30 days
)

// sequencePrediction is an internal prediction from the sequence analyzer.
type sequencePrediction struct {
	Action     string
	Confidence float64
	Reasoning  string
}

// sequencePattern is an internal detected pattern from the sequence analyzer.
type sequencePattern struct {
	Name       string
	Frequency  int
	Confidence float64
	Sequence   []string
	TimeOfDay  string
	LastSeen   time.Time
}

// timedAction records an action with its timestamp for sequence tracking.
type timedAction struct {
	action    string
	timestamp time.Time
	hour      int
}

// ngramEntry tracks an n-gram's frequency and recency.
type ngramEntry struct {
	count    int
	lastSeen time.Time
}

// sessionSequences holds sequence analysis data for a single session.
type sessionSequences struct {
	// recentActions is a rolling window of recent actions.
	recentActions []timedAction

	// ngrams maps n-gram keys to their frequency data.
	// Key format: "action1|action2|action3" for a trigram.
	ngrams map[string]*ngramEntry

	// hourBuckets tracks action frequencies by time-of-day bucket.
	// Key: bucket index (0-5 for 4-hour windows), Value: action -> count.
	hourBuckets [6]map[string]int

	// totalActions is the total number of actions ever recorded (not capped).
	totalActions int
}

// SequenceAnalyzer detects common action sequences and time-based patterns
// using an n-gram model. Thread-safe via its own mutex.
type SequenceAnalyzer struct {
	mu sync.RWMutex

	// sessions maps session ID to its sequence data.
	sessions map[string]*sessionSequences
}

// NewSequenceAnalyzer creates a new sequence analyzer.
func NewSequenceAnalyzer() *SequenceAnalyzer {
	return &SequenceAnalyzer{
		sessions: make(map[string]*sessionSequences),
	}
}

// RecordAction records a new action for sequence analysis.
func (sa *SequenceAnalyzer) RecordAction(session, action string, ts time.Time) {
	if session == "" || action == "" {
		return
	}

	sa.mu.Lock()
	defer sa.mu.Unlock()

	ss := sa.getOrCreateSession(session)

	ta := timedAction{
		action:    action,
		timestamp: ts,
		hour:      ts.Hour(),
	}

	// Enforce rolling window
	if len(ss.recentActions) >= defaultMaxActionsPerSession {
		ss.recentActions = ss.recentActions[1:]
	}
	ss.recentActions = append(ss.recentActions, ta)
	ss.totalActions++

	// Update n-grams for all orders (bigram, trigram)
	sa.updateNgrams(ss)

	// Update time-of-day buckets
	bucket := ta.hour / defaultTimeWindowHours
	if bucket >= len(ss.hourBuckets) {
		bucket = len(ss.hourBuckets) - 1
	}
	if ss.hourBuckets[bucket] == nil {
		ss.hourBuckets[bucket] = make(map[string]int)
	}
	ss.hourBuckets[bucket][action]++

	// Prune n-grams if too many
	if len(ss.ngrams) > defaultMaxNgrams {
		sa.pruneNgrams(ss)
	}
}

// Predict returns the most likely next actions based on the recent action
// sequence for a session, using the n-gram model.
func (sa *SequenceAnalyzer) Predict(session string, limit int) []sequencePrediction {
	if limit <= 0 {
		limit = defaultMaxPredictions
	}

	sa.mu.RLock()
	defer sa.mu.RUnlock()

	ss, ok := sa.sessions[session]
	if !ok || len(ss.recentActions) == 0 {
		return nil
	}

	candidates := make(map[string]float64) // action -> aggregated score

	// Use the most recent action's timestamp as reference for decay
	// so predictions work correctly regardless of wall clock.
	refTime := ss.recentActions[len(ss.recentActions)-1].timestamp

	// Try trigram prediction first (strongest signal)
	if len(ss.recentActions) >= 2 {
		last2 := ss.recentActions[len(ss.recentActions)-2:]
		prefix := last2[0].action + "|" + last2[1].action + "|"
		sa.collectCandidates(ss, prefix, 3, candidates, 1.0, refTime)
	}

	// Then bigram prediction
	if len(ss.recentActions) >= 1 {
		last := ss.recentActions[len(ss.recentActions)-1]
		prefix := last.action + "|"
		sa.collectCandidates(ss, prefix, 2, candidates, 0.6, refTime)
	}

	if len(candidates) == 0 {
		return nil
	}

	// Convert to predictions
	var preds []sequencePrediction
	for action, score := range candidates {
		conf := clampConfidence(score)
		if conf > 0 {
			preds = append(preds, sequencePrediction{
				Action:     action,
				Confidence: conf,
				Reasoning:  fmt.Sprintf("Follows from recent action sequence (score: %.2f)", score),
			})
		}
	}

	sort.Slice(preds, func(i, j int) bool {
		return preds[i].Confidence > preds[j].Confidence
	})

	if len(preds) > limit {
		preds = preds[:limit]
	}

	return preds
}

// PredictByTimeOfDay returns actions that are commonly performed at the
// given hour of day for a session.
func (sa *SequenceAnalyzer) PredictByTimeOfDay(session string, hour int, limit int) []sequencePrediction {
	if limit <= 0 {
		limit = 3
	}

	sa.mu.RLock()
	defer sa.mu.RUnlock()

	ss, ok := sa.sessions[session]
	if !ok {
		return nil
	}

	bucket := hour / defaultTimeWindowHours
	if bucket >= len(ss.hourBuckets) {
		bucket = len(ss.hourBuckets) - 1
	}

	bucketMap := ss.hourBuckets[bucket]
	if len(bucketMap) == 0 {
		return nil
	}

	// Sum total actions in this bucket for normalization
	var total int
	for _, count := range bucketMap {
		total += count
	}
	if total == 0 {
		return nil
	}

	// Build predictions from bucket frequencies
	type scored struct {
		action string
		count  int
	}
	var items []scored
	for action, count := range bucketMap {
		items = append(items, scored{action: action, count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].count > items[j].count
	})

	if len(items) > limit {
		items = items[:limit]
	}

	label := timeOfDayLabel(hour)
	var preds []sequencePrediction
	for _, item := range items {
		conf := clampConfidence(float64(item.count) / float64(total))
		if conf > 0 {
			preds = append(preds, sequencePrediction{
				Action:     item.action,
				Confidence: conf,
				Reasoning:  fmt.Sprintf("Used %d times during %s hours", item.count, label),
			})
		}
	}

	return preds
}

// GetPatterns returns recognized action sequence patterns for a session that
// meet the minimum occurrence threshold.
func (sa *SequenceAnalyzer) GetPatterns(session string, minOccurrences int) []sequencePattern {
	sa.mu.RLock()
	defer sa.mu.RUnlock()

	ss, ok := sa.sessions[session]
	if !ok {
		return nil
	}

	var patterns []sequencePattern

	// Extract significant n-grams as patterns
	for key, entry := range ss.ngrams {
		if entry.count < minOccurrences {
			continue
		}

		parts := strings.Split(key, "|")
		if len(parts) < 2 {
			continue
		}

		// Calculate confidence based on frequency relative to total possible
		// The more frequently this pattern appears relative to total actions,
		// the higher the confidence.
		confidence := clampConfidence(float64(entry.count) / float64(max(ss.totalActions/len(parts), 1)))

		name := strings.Join(parts, " -> ")
		patterns = append(patterns, sequencePattern{
			Name:       name,
			Frequency:  entry.count,
			Confidence: confidence,
			Sequence:   parts,
			TimeOfDay:  sa.dominantTimeOfDay(ss, parts[0]),
			LastSeen:   entry.lastSeen,
		})
	}

	// Sort by frequency descending
	sort.Slice(patterns, func(i, j int) bool {
		return patterns[i].Frequency > patterns[j].Frequency
	})

	return patterns
}

// RemoveSession removes all sequence data for a session.
func (sa *SequenceAnalyzer) RemoveSession(session string) {
	sa.mu.Lock()
	defer sa.mu.Unlock()
	delete(sa.sessions, session)
}

// Reset clears all sequence analysis data.
func (sa *SequenceAnalyzer) Reset() {
	sa.mu.Lock()
	defer sa.mu.Unlock()
	sa.sessions = make(map[string]*sessionSequences)
}

// --- Internal helpers ---

// getOrCreateSession returns or creates sequence data for a session.
// Caller must hold sa.mu write lock.
func (sa *SequenceAnalyzer) getOrCreateSession(session string) *sessionSequences {
	ss, ok := sa.sessions[session]
	if !ok {
		ss = &sessionSequences{
			recentActions: make([]timedAction, 0, 64),
			ngrams:        make(map[string]*ngramEntry),
		}
		sa.sessions[session] = ss
	}
	return ss
}

// updateNgrams updates bigram and trigram counts based on the most recent
// actions. Caller must hold sa.mu write lock.
func (sa *SequenceAnalyzer) updateNgrams(ss *sessionSequences) {
	n := len(ss.recentActions)
	if n < 2 {
		return
	}

	now := ss.recentActions[n-1].timestamp

	// Update bigram (last 2 actions)
	bigram := ss.recentActions[n-2].action + "|" + ss.recentActions[n-1].action
	if entry, ok := ss.ngrams[bigram]; ok {
		entry.count++
		entry.lastSeen = now
	} else {
		ss.ngrams[bigram] = &ngramEntry{count: 1, lastSeen: now}
	}

	// Update trigram (last 3 actions) if possible
	if n >= 3 {
		trigram := ss.recentActions[n-3].action + "|" + ss.recentActions[n-2].action + "|" + ss.recentActions[n-1].action
		if entry, ok := ss.ngrams[trigram]; ok {
			entry.count++
			entry.lastSeen = now
		} else {
			ss.ngrams[trigram] = &ngramEntry{count: 1, lastSeen: now}
		}
	}
}

// collectCandidates gathers prediction candidates from n-grams matching a prefix.
// The weight parameter scales the contribution of these matches. refTime is the
// reference timestamp for decay calculation (typically the most recent action).
func (sa *SequenceAnalyzer) collectCandidates(ss *sessionSequences, prefix string, order int, candidates map[string]float64, weight float64, refTime time.Time) {
	for key, entry := range ss.ngrams {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		parts := strings.Split(key, "|")
		if len(parts) != order {
			continue
		}
		// The predicted action is the last part of the n-gram
		predicted := parts[len(parts)-1]

		// Score = frequency * time decay * weight
		decay := decayFactor(entry.lastSeen, refTime, maxAgeForDecay)
		score := float64(entry.count) * decay * weight

		// Normalize by total actions to get a probability-like score
		if ss.totalActions > 0 {
			score = score / float64(ss.totalActions) * 10.0 // scale up for readability
		}

		if existing, ok := candidates[predicted]; ok {
			candidates[predicted] = existing + score
		} else {
			candidates[predicted] = score
		}
	}
}

// pruneNgrams removes the least frequent n-grams to stay under the cap.
// Caller must hold sa.mu write lock.
func (sa *SequenceAnalyzer) pruneNgrams(ss *sessionSequences) {
	// Collect all entries with their keys
	type entry struct {
		key   string
		count int
	}
	var entries []entry
	for k, v := range ss.ngrams {
		entries = append(entries, entry{key: k, count: v.count})
	}

	// Sort by count ascending (least frequent first)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].count < entries[j].count
	})

	// Remove the bottom half
	removeCount := len(entries) / 2
	for i := 0; i < removeCount; i++ {
		delete(ss.ngrams, entries[i].key)
	}
}

// dominantTimeOfDay returns the time-of-day label where an action is most
// frequently performed, or "" if no clear pattern.
func (sa *SequenceAnalyzer) dominantTimeOfDay(ss *sessionSequences, action string) string {
	var maxCount int
	var maxBucket int = -1

	for bucket := range ss.hourBuckets {
		if ss.hourBuckets[bucket] == nil {
			continue
		}
		count := ss.hourBuckets[bucket][action]
		if count > maxCount {
			maxCount = count
			maxBucket = bucket
		}
	}

	if maxBucket < 0 || maxCount < 2 {
		return ""
	}

	// Convert bucket to representative hour
	hour := maxBucket * defaultTimeWindowHours
	return timeOfDayLabel(hour)
}

// max returns the larger of a and b.
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
