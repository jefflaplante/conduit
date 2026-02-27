// Package learning provides a proactive learning system that recognizes user
// behavior patterns and anticipates needs. It observes interactions (tool usage,
// message characteristics, timing) and builds predictive models to suggest
// next actions, prefetch data, and surface relevant context.
//
// Privacy-conscious: no raw message content is stored. Only statistical
// patterns, tool names, and timing data are retained. All structures are
// bounded to prevent unbounded memory growth.
//
// Thread-safe: all exported methods are safe for concurrent use.
package learning

import (
	"crypto/sha256"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// Default configuration constants.
const (
	// defaultMaxInteractions is the per-session rolling window cap for
	// recorded interactions.
	defaultMaxInteractions = 500

	// defaultMaxSessions is the maximum number of sessions tracked
	// concurrently. Least-recently-used sessions are evicted when exceeded.
	defaultMaxSessions = 100

	// defaultMinPatternOccurrences is the minimum number of times a sequence
	// must occur before it is surfaced as a recognized pattern.
	defaultMinPatternOccurrences = 3

	// defaultMinConfidence is the minimum confidence threshold for
	// predictions to be returned.
	defaultMinConfidence = 0.3

	// defaultMaxPredictions is the maximum number of predictions returned
	// by PredictNextAction.
	defaultMaxPredictions = 5

	// defaultMaxPatterns is the maximum number of patterns tracked per session.
	defaultMaxPatterns = 200

	// defaultMaxPrefetchSuggestions is the maximum number of prefetch
	// suggestions returned.
	defaultMaxPrefetchSuggestions = 5
)

// Prediction represents a predicted next action with confidence and reasoning.
type Prediction struct {
	// Action is the predicted next action (typically a tool name or action category).
	Action string `json:"action"`

	// Confidence is the prediction confidence (0.0-1.0).
	Confidence float64 `json:"confidence"`

	// Reasoning explains why this prediction was made.
	Reasoning string `json:"reasoning"`

	// Source indicates which learning signal produced this prediction.
	Source string `json:"source"`
}

// PrefetchSuggestion suggests data that should be prefetched based on
// observed user patterns.
type PrefetchSuggestion struct {
	// Resource is the resource identifier to prefetch (e.g., tool name, file path).
	Resource string `json:"resource"`

	// Reason explains why this resource should be prefetched.
	Reason string `json:"reason"`

	// ExpectedBenefit describes the anticipated benefit of prefetching.
	ExpectedBenefit string `json:"expected_benefit"`

	// Confidence is how confident we are that this prefetch will be useful.
	Confidence float64 `json:"confidence"`
}

// UserPattern describes a recognized behavioral pattern for a session.
type UserPattern struct {
	// Name is a human-readable name for the pattern (e.g., "edit-test-commit").
	Name string `json:"name"`

	// Frequency is how many times this pattern has been observed.
	Frequency int `json:"frequency"`

	// Confidence is the pattern's reliability score (0.0-1.0).
	Confidence float64 `json:"confidence"`

	// Sequence is the typical action sequence for this pattern.
	Sequence []string `json:"sequence"`

	// TimeOfDay indicates the time-of-day preference if any (empty if none).
	TimeOfDay string `json:"time_of_day,omitempty"`

	// LastSeen is when this pattern was last observed.
	LastSeen time.Time `json:"last_seen"`
}

// interaction is an internal record of a single observed interaction.
// It stores only statistical/categorical data, never raw message content.
type interaction struct {
	// sessionID is the session this interaction belongs to.
	sessionID string

	// tools is the list of tools used in this interaction.
	tools []string

	// outcome records the outcome category (e.g., "success", "error", "partial").
	outcome string

	// messageWordCount is the word count of the user message.
	messageWordCount int

	// messageHash is a truncated SHA-256 for deduplication (not reversible).
	messageHash string

	// timestamp is when this interaction occurred.
	timestamp time.Time

	// hour is the hour of day (0-23) for time-based pattern detection.
	hour int
}

// sessionData holds all learning state for a single session.
type sessionData struct {
	interactions []interaction
	lastAccess   time.Time
}

// LearnerOption configures a Learner.
type LearnerOption func(*Learner)

// WithMaxInteractions sets the per-session interaction window cap.
func WithMaxInteractions(n int) LearnerOption {
	return func(l *Learner) {
		if n > 0 {
			l.maxInteractions = n
		}
	}
}

// WithMaxSessions sets the maximum number of tracked sessions.
func WithMaxSessions(n int) LearnerOption {
	return func(l *Learner) {
		if n > 0 {
			l.maxSessions = n
		}
	}
}

// WithMinPatternOccurrences sets the minimum occurrences for a pattern to
// be recognized.
func WithMinPatternOccurrences(n int) LearnerOption {
	return func(l *Learner) {
		if n > 0 {
			l.minPatternOccurrences = n
		}
	}
}

// WithMinConfidence sets the minimum confidence for predictions.
func WithMinConfidence(c float64) LearnerOption {
	return func(l *Learner) {
		if c > 0 && c <= 1.0 {
			l.minConfidence = c
		}
	}
}

// Learner is the proactive learning engine. It observes user behavior,
// detects patterns, and generates predictions about future actions.
type Learner struct {
	mu sync.RWMutex

	// sessions maps session ID to its learning data.
	sessions map[string]*sessionData

	// sequenceAnalyzer handles n-gram and sequence analysis.
	sequenceAnalyzer *SequenceAnalyzer

	// preferences tracks per-session user preference learning.
	preferences *PreferenceTracker

	// Configuration
	maxInteractions       int
	maxSessions           int
	minPatternOccurrences int
	minConfidence         float64
}

// NewLearner creates a new proactive learning engine with the given options.
func NewLearner(opts ...LearnerOption) *Learner {
	l := &Learner{
		sessions:              make(map[string]*sessionData),
		sequenceAnalyzer:      NewSequenceAnalyzer(),
		preferences:           NewPreferenceTracker(),
		maxInteractions:       defaultMaxInteractions,
		maxSessions:           defaultMaxSessions,
		minPatternOccurrences: defaultMinPatternOccurrences,
		minConfidence:         defaultMinConfidence,
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// ObserveInteraction records an interaction for learning. The message content
// is hashed immediately and never stored in plaintext.
func (l *Learner) ObserveInteraction(session, message string, tools []string, outcome string) {
	if session == "" {
		return
	}

	now := time.Now()
	ix := interaction{
		sessionID:        session,
		tools:            tools,
		outcome:          outcome,
		messageWordCount: len(strings.Fields(message)),
		messageHash:      hashContent(message),
		timestamp:        now,
		hour:             now.Hour(),
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// Ensure session exists, evict LRU if at capacity
	sd, ok := l.sessions[session]
	if !ok {
		if len(l.sessions) >= l.maxSessions {
			l.evictLRUSession()
		}
		sd = &sessionData{
			interactions: make([]interaction, 0, 64),
		}
		l.sessions[session] = sd
	}
	sd.lastAccess = now

	// Enforce per-session rolling window
	if len(sd.interactions) >= l.maxInteractions {
		sd.interactions = sd.interactions[1:]
	}
	sd.interactions = append(sd.interactions, ix)

	// Feed the sequence analyzer and preference tracker
	l.sequenceAnalyzer.RecordAction(session, toolsToAction(tools), now)
	l.preferences.RecordInteraction(session, tools, outcome, ix.messageWordCount, now)
}

// PredictNextAction predicts what the user likely wants next based on
// observed patterns. Returns up to defaultMaxPredictions predictions
// sorted by confidence (highest first).
func (l *Learner) PredictNextAction(session, currentMessage string) []Prediction {
	l.mu.RLock()
	defer l.mu.RUnlock()

	sd, ok := l.sessions[session]
	if !ok || len(sd.interactions) == 0 {
		return nil
	}

	var predictions []Prediction

	// 1. Sequence-based predictions (n-gram model)
	seqPreds := l.sequenceAnalyzer.Predict(session, defaultMaxPredictions)
	for _, sp := range seqPreds {
		if sp.Confidence >= l.minConfidence {
			predictions = append(predictions, Prediction{
				Action:     sp.Action,
				Confidence: sp.Confidence,
				Reasoning:  sp.Reasoning,
				Source:     "sequence",
			})
		}
	}

	// 2. Time-of-day predictions
	hour := time.Now().Hour()
	timePreds := l.sequenceAnalyzer.PredictByTimeOfDay(session, hour, 3)
	for _, tp := range timePreds {
		if tp.Confidence >= l.minConfidence {
			predictions = append(predictions, Prediction{
				Action:     tp.Action,
				Confidence: tp.Confidence * 0.8, // slightly lower weight for time-based
				Reasoning:  tp.Reasoning,
				Source:     "time_of_day",
			})
		}
	}

	// 3. Preference-based predictions
	prefPreds := l.preferences.PredictFromPreferences(session, currentMessage)
	for _, pp := range prefPreds {
		if pp.Confidence >= l.minConfidence {
			predictions = append(predictions, Prediction{
				Action:     pp.Action,
				Confidence: pp.Confidence * 0.7, // lower weight for preference-only
				Reasoning:  pp.Reasoning,
				Source:     "preference",
			})
		}
	}

	// Deduplicate by action (keep highest confidence)
	predictions = deduplicatePredictions(predictions)

	// Sort by confidence descending
	sort.Slice(predictions, func(i, j int) bool {
		return predictions[i].Confidence > predictions[j].Confidence
	})

	// Cap results
	if len(predictions) > defaultMaxPredictions {
		predictions = predictions[:defaultMaxPredictions]
	}

	return predictions
}

// GetUserPatterns returns the recognized behavioral patterns for a session.
func (l *Learner) GetUserPatterns(session string) []UserPattern {
	l.mu.RLock()
	defer l.mu.RUnlock()

	_, ok := l.sessions[session]
	if !ok {
		return nil
	}

	var patterns []UserPattern

	// Get sequence patterns
	seqPatterns := l.sequenceAnalyzer.GetPatterns(session, l.minPatternOccurrences)
	for _, sp := range seqPatterns {
		patterns = append(patterns, UserPattern{
			Name:       sp.Name,
			Frequency:  sp.Frequency,
			Confidence: sp.Confidence,
			Sequence:   sp.Sequence,
			TimeOfDay:  sp.TimeOfDay,
			LastSeen:   sp.LastSeen,
		})
	}

	// Get preference patterns
	prefPatterns := l.preferences.GetPreferencePatterns(session)
	for _, pp := range prefPatterns {
		patterns = append(patterns, UserPattern{
			Name:       pp.Name,
			Frequency:  pp.Frequency,
			Confidence: pp.Confidence,
			Sequence:   pp.Sequence,
			LastSeen:   pp.LastSeen,
		})
	}

	// Sort by frequency descending
	sort.Slice(patterns, func(i, j int) bool {
		return patterns[i].Frequency > patterns[j].Frequency
	})

	if len(patterns) > defaultMaxPatterns {
		patterns = patterns[:defaultMaxPatterns]
	}

	return patterns
}

// SuggestPrefetch suggests data to prefetch based on patterns for a session.
func (l *Learner) SuggestPrefetch(session string) []PrefetchSuggestion {
	l.mu.RLock()
	defer l.mu.RUnlock()

	sd, ok := l.sessions[session]
	if !ok || len(sd.interactions) == 0 {
		return nil
	}

	var suggestions []PrefetchSuggestion

	// Predict next actions and suggest prefetching their resources
	seqPreds := l.sequenceAnalyzer.Predict(session, defaultMaxPrefetchSuggestions)
	for _, pred := range seqPreds {
		if pred.Confidence >= l.minConfidence {
			suggestions = append(suggestions, PrefetchSuggestion{
				Resource:        pred.Action,
				Reason:          fmt.Sprintf("Predicted next action: %s", pred.Reasoning),
				ExpectedBenefit: fmt.Sprintf("Reduce latency for %s by having results ready", pred.Action),
				Confidence:      pred.Confidence,
			})
		}
	}

	// Suggest prefetching commonly used tools for the current time of day
	hour := time.Now().Hour()
	timePreds := l.sequenceAnalyzer.PredictByTimeOfDay(session, hour, 3)
	for _, tp := range timePreds {
		if tp.Confidence >= l.minConfidence {
			// Only add if not already suggested
			found := false
			for _, s := range suggestions {
				if s.Resource == tp.Action {
					found = true
					break
				}
			}
			if !found {
				suggestions = append(suggestions, PrefetchSuggestion{
					Resource:        tp.Action,
					Reason:          fmt.Sprintf("Frequently used at this time: %s", tp.Reasoning),
					ExpectedBenefit: fmt.Sprintf("Proactive preparation for typical %s usage", timeOfDayLabel(hour)),
					Confidence:      tp.Confidence * 0.8,
				})
			}
		}
	}

	// Sort by confidence descending
	sort.Slice(suggestions, func(i, j int) bool {
		return suggestions[i].Confidence > suggestions[j].Confidence
	})

	if len(suggestions) > defaultMaxPrefetchSuggestions {
		suggestions = suggestions[:defaultMaxPrefetchSuggestions]
	}

	return suggestions
}

// SessionCount returns the number of sessions currently tracked.
func (l *Learner) SessionCount() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.sessions)
}

// InteractionCount returns the number of interactions recorded for a session.
func (l *Learner) InteractionCount(session string) int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	sd, ok := l.sessions[session]
	if !ok {
		return 0
	}
	return len(sd.interactions)
}

// Reset clears all learning data.
func (l *Learner) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.sessions = make(map[string]*sessionData)
	l.sequenceAnalyzer.Reset()
	l.preferences.Reset()
}

// --- Internal helpers ---

// evictLRUSession removes the least recently used session. Caller must hold mu write lock.
func (l *Learner) evictLRUSession() {
	var oldestKey string
	var oldestTime time.Time

	first := true
	for key, sd := range l.sessions {
		if first || sd.lastAccess.Before(oldestTime) {
			oldestKey = key
			oldestTime = sd.lastAccess
			first = false
		}
	}

	if oldestKey != "" {
		delete(l.sessions, oldestKey)
		l.sequenceAnalyzer.RemoveSession(oldestKey)
		l.preferences.RemoveSession(oldestKey)
	}
}

// hashContent produces a short, non-reversible hash of content for deduplication.
func hashContent(content string) string {
	h := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", h[:8])
}

// toolsToAction converts a tool list to a single action string.
// If multiple tools were used, they are joined with "+".
// If no tools were used, returns "chat" as the action.
func toolsToAction(tools []string) string {
	if len(tools) == 0 {
		return "chat"
	}
	if len(tools) == 1 {
		return tools[0]
	}
	sort.Strings(tools)
	return strings.Join(tools, "+")
}

// timeOfDayLabel returns a human-readable label for a time period.
func timeOfDayLabel(hour int) string {
	switch {
	case hour >= 5 && hour < 12:
		return "morning"
	case hour >= 12 && hour < 17:
		return "afternoon"
	case hour >= 17 && hour < 21:
		return "evening"
	default:
		return "night"
	}
}

// deduplicatePredictions removes duplicate actions, keeping the highest confidence.
func deduplicatePredictions(preds []Prediction) []Prediction {
	seen := make(map[string]int) // action -> index in result
	var result []Prediction

	for _, p := range preds {
		if idx, ok := seen[p.Action]; ok {
			// Keep the one with higher confidence
			if p.Confidence > result[idx].Confidence {
				result[idx] = p
			}
		} else {
			seen[p.Action] = len(result)
			result = append(result, p)
		}
	}
	return result
}

// clampConfidence clamps a confidence value to [0.0, 1.0].
func clampConfidence(c float64) float64 {
	if c < 0 {
		return 0
	}
	if c > 1.0 {
		return 1.0
	}
	return c
}

// decayFactor computes an exponential time decay factor. Events older than
// maxAge receive a factor of 0. Recent events approach 1.0.
func decayFactor(eventTime time.Time, now time.Time, maxAge time.Duration) float64 {
	age := now.Sub(eventTime)
	if age >= maxAge || age < 0 {
		return 0
	}
	// Exponential decay: e^(-3 * age/maxAge) — drops to ~5% at maxAge
	ratio := float64(age) / float64(maxAge)
	return math.Exp(-3 * ratio)
}
