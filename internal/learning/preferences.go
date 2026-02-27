package learning

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Default preference tracking constants.
const (
	// defaultMaxPreferenceTools is the maximum number of distinct tools
	// tracked per session.
	defaultMaxPreferenceTools = 50

	// defaultMinToolUsage is the minimum number of uses before a tool
	// preference is considered significant.
	defaultMinToolUsage = 2

	// preferenceDecayWindow is the time window for preference relevance.
	preferenceDecayWindow = 14 * 24 * time.Hour // 14 days
)

// toolPreference tracks usage frequency and success rate for a single tool.
type toolPreference struct {
	name         string
	useCount     int
	successCount int
	lastUsed     time.Time
}

// responsePreference tracks observed preferences about response characteristics.
type responsePreference struct {
	// totalWordCount is the cumulative word count of user messages.
	totalWordCount int

	// messageCount is the number of messages observed.
	messageCount int

	// avgWordCount is the running average message word count.
	avgWordCount float64
}

// sessionPreferences holds preference data for a single session.
type sessionPreferences struct {
	tools           map[string]*toolPreference
	responsePrefs   responsePreference
	lastInteraction time.Time
}

// PreferenceTracker learns user preferences from behavioral signals.
// It tracks preferred tools, message length patterns, and success rates.
// Thread-safe via its own mutex.
type PreferenceTracker struct {
	mu sync.RWMutex

	// sessions maps session ID to its preference data.
	sessions map[string]*sessionPreferences
}

// NewPreferenceTracker creates a new preference tracker.
func NewPreferenceTracker() *PreferenceTracker {
	return &PreferenceTracker{
		sessions: make(map[string]*sessionPreferences),
	}
}

// RecordInteraction records a user interaction for preference learning.
func (pt *PreferenceTracker) RecordInteraction(session string, tools []string, outcome string, messageWordCount int, ts time.Time) {
	if session == "" {
		return
	}

	pt.mu.Lock()
	defer pt.mu.Unlock()

	sp := pt.getOrCreateSession(session)
	sp.lastInteraction = ts

	isSuccess := outcome == "success" || outcome == ""

	// Track tool preferences
	for _, tool := range tools {
		if tool == "" {
			continue
		}
		tp, ok := sp.tools[tool]
		if !ok {
			// Enforce tool cap
			if len(sp.tools) >= defaultMaxPreferenceTools {
				pt.evictLeastUsedTool(sp)
			}
			tp = &toolPreference{name: tool}
			sp.tools[tool] = tp
		}
		tp.useCount++
		tp.lastUsed = ts
		if isSuccess {
			tp.successCount++
		}
	}

	// Track response preferences
	sp.responsePrefs.totalWordCount += messageWordCount
	sp.responsePrefs.messageCount++
	if sp.responsePrefs.messageCount > 0 {
		sp.responsePrefs.avgWordCount = float64(sp.responsePrefs.totalWordCount) / float64(sp.responsePrefs.messageCount)
	}
}

// PredictFromPreferences generates predictions based on learned user preferences.
func (pt *PreferenceTracker) PredictFromPreferences(session, currentMessage string) []sequencePrediction {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	sp, ok := pt.sessions[session]
	if !ok {
		return nil
	}

	var preds []sequencePrediction

	// Predict likely tools based on usage frequency and success rate
	topTools := pt.getTopTools(sp, 3)
	for _, tp := range topTools {
		if tp.useCount < defaultMinToolUsage {
			continue
		}
		successRate := float64(tp.successCount) / float64(tp.useCount)
		confidence := clampConfidence(successRate * float64(tp.useCount) / float64(max(sp.responsePrefs.messageCount, 1)))

		preds = append(preds, sequencePrediction{
			Action:     tp.name,
			Confidence: confidence,
			Reasoning:  fmt.Sprintf("Preferred tool (used %d times, %.0f%% success)", tp.useCount, successRate*100),
		})
	}

	// If the current message is short, predict lightweight tools
	// If long, predict analysis-heavy tools
	if currentMessage != "" {
		wordCount := len(strings.Fields(currentMessage))
		if sp.responsePrefs.avgWordCount > 0 {
			ratio := float64(wordCount) / sp.responsePrefs.avgWordCount
			if ratio > 2.0 {
				// Longer than usual — might need analysis tools
				preds = append(preds, sequencePrediction{
					Action:     "analysis",
					Confidence: clampConfidence(0.3 * (ratio - 1)),
					Reasoning:  fmt.Sprintf("Message is %.0fx longer than average, suggesting complex task", ratio),
				})
			}
		}
	}

	return preds
}

// GetPreferencePatterns returns recognized preference patterns for a session.
func (pt *PreferenceTracker) GetPreferencePatterns(session string) []sequencePattern {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	sp, ok := pt.sessions[session]
	if !ok {
		return nil
	}

	var patterns []sequencePattern

	// Tool preference patterns
	topTools := pt.getTopTools(sp, 10)
	if len(topTools) > 0 {
		var toolNames []string
		for _, tp := range topTools {
			if tp.useCount >= defaultMinToolUsage {
				toolNames = append(toolNames, tp.name)
			}
		}
		if len(toolNames) > 0 {
			totalUses := 0
			for _, tp := range topTools {
				totalUses += tp.useCount
			}
			patterns = append(patterns, sequencePattern{
				Name:       "preferred-tools",
				Frequency:  totalUses,
				Confidence: clampConfidence(float64(len(toolNames)) / float64(max(len(sp.tools), 1))),
				Sequence:   toolNames,
				LastSeen:   sp.lastInteraction,
			})
		}
	}

	// Message length preference pattern
	if sp.responsePrefs.messageCount >= 5 {
		var lengthLabel string
		avg := sp.responsePrefs.avgWordCount
		switch {
		case avg < 10:
			lengthLabel = "concise"
		case avg < 30:
			lengthLabel = "moderate"
		default:
			lengthLabel = "detailed"
		}

		patterns = append(patterns, sequencePattern{
			Name:       fmt.Sprintf("message-style-%s", lengthLabel),
			Frequency:  sp.responsePrefs.messageCount,
			Confidence: clampConfidence(float64(sp.responsePrefs.messageCount) / 20.0),
			Sequence:   []string{fmt.Sprintf("avg_words=%.0f", avg), lengthLabel},
			LastSeen:   sp.lastInteraction,
		})
	}

	return patterns
}

// RemoveSession removes all preference data for a session.
func (pt *PreferenceTracker) RemoveSession(session string) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	delete(pt.sessions, session)
}

// Reset clears all preference data.
func (pt *PreferenceTracker) Reset() {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	pt.sessions = make(map[string]*sessionPreferences)
}

// --- Internal helpers ---

// getOrCreateSession returns or creates preference data for a session.
// Caller must hold pt.mu write lock.
func (pt *PreferenceTracker) getOrCreateSession(session string) *sessionPreferences {
	sp, ok := pt.sessions[session]
	if !ok {
		sp = &sessionPreferences{
			tools: make(map[string]*toolPreference),
		}
		pt.sessions[session] = sp
	}
	return sp
}

// getTopTools returns the most-used tools for a session, sorted by use count.
func (pt *PreferenceTracker) getTopTools(sp *sessionPreferences, limit int) []*toolPreference {
	var tools []*toolPreference
	for _, tp := range sp.tools {
		tools = append(tools, tp)
	}
	sort.Slice(tools, func(i, j int) bool {
		return tools[i].useCount > tools[j].useCount
	})
	if len(tools) > limit {
		tools = tools[:limit]
	}
	return tools
}

// evictLeastUsedTool removes the tool with the lowest use count from the
// session's tool preferences. Caller must hold pt.mu write lock.
func (pt *PreferenceTracker) evictLeastUsedTool(sp *sessionPreferences) {
	var minName string
	minCount := -1

	for name, tp := range sp.tools {
		if minCount < 0 || tp.useCount < minCount {
			minName = name
			minCount = tp.useCount
		}
	}

	if minName != "" {
		delete(sp.tools, minName)
	}
}
