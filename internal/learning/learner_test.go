package learning

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewLearner(t *testing.T) {
	t.Run("default options", func(t *testing.T) {
		l := NewLearner()
		assert.NotNil(t, l)
		assert.Equal(t, 0, l.SessionCount())
		assert.Equal(t, defaultMaxInteractions, l.maxInteractions)
		assert.Equal(t, defaultMaxSessions, l.maxSessions)
		assert.Equal(t, defaultMinPatternOccurrences, l.minPatternOccurrences)
		assert.Equal(t, defaultMinConfidence, l.minConfidence)
	})

	t.Run("custom options", func(t *testing.T) {
		l := NewLearner(
			WithMaxInteractions(100),
			WithMaxSessions(50),
			WithMinPatternOccurrences(5),
			WithMinConfidence(0.5),
		)
		assert.Equal(t, 100, l.maxInteractions)
		assert.Equal(t, 50, l.maxSessions)
		assert.Equal(t, 5, l.minPatternOccurrences)
		assert.Equal(t, 0.5, l.minConfidence)
	})

	t.Run("invalid options ignored", func(t *testing.T) {
		l := NewLearner(
			WithMaxInteractions(-1),
			WithMaxSessions(0),
			WithMinPatternOccurrences(-10),
			WithMinConfidence(0),
			WithMinConfidence(1.5),
		)
		assert.Equal(t, defaultMaxInteractions, l.maxInteractions)
		assert.Equal(t, defaultMaxSessions, l.maxSessions)
		assert.Equal(t, defaultMinPatternOccurrences, l.minPatternOccurrences)
		assert.Equal(t, defaultMinConfidence, l.minConfidence)
	})
}

func TestObserveInteraction(t *testing.T) {
	t.Run("records interactions", func(t *testing.T) {
		l := NewLearner()
		l.ObserveInteraction("sess-1", "hello world", []string{"chat"}, "success")
		assert.Equal(t, 1, l.SessionCount())
		assert.Equal(t, 1, l.InteractionCount("sess-1"))
	})

	t.Run("empty session ignored", func(t *testing.T) {
		l := NewLearner()
		l.ObserveInteraction("", "hello", []string{"chat"}, "success")
		assert.Equal(t, 0, l.SessionCount())
	})

	t.Run("multiple sessions tracked", func(t *testing.T) {
		l := NewLearner()
		l.ObserveInteraction("sess-1", "hello", []string{"chat"}, "success")
		l.ObserveInteraction("sess-2", "world", []string{"search"}, "success")
		assert.Equal(t, 2, l.SessionCount())
		assert.Equal(t, 1, l.InteractionCount("sess-1"))
		assert.Equal(t, 1, l.InteractionCount("sess-2"))
	})

	t.Run("rolling window enforced", func(t *testing.T) {
		l := NewLearner(WithMaxInteractions(5))
		for i := 0; i < 10; i++ {
			l.ObserveInteraction("sess-1", fmt.Sprintf("msg %d", i), []string{"chat"}, "success")
		}
		assert.Equal(t, 5, l.InteractionCount("sess-1"))
	})

	t.Run("LRU eviction", func(t *testing.T) {
		l := NewLearner(WithMaxSessions(3))
		l.ObserveInteraction("sess-1", "a", nil, "success")
		l.ObserveInteraction("sess-2", "b", nil, "success")
		l.ObserveInteraction("sess-3", "c", nil, "success")
		assert.Equal(t, 3, l.SessionCount())

		// Adding a 4th session should evict sess-1 (oldest)
		l.ObserveInteraction("sess-4", "d", nil, "success")
		assert.Equal(t, 3, l.SessionCount())
		assert.Equal(t, 0, l.InteractionCount("sess-1")) // evicted
		assert.Equal(t, 1, l.InteractionCount("sess-4")) // added
	})

	t.Run("LRU evicts least recently used not oldest created", func(t *testing.T) {
		l := NewLearner(WithMaxSessions(3))
		l.ObserveInteraction("sess-1", "a", nil, "success")
		l.ObserveInteraction("sess-2", "b", nil, "success")
		l.ObserveInteraction("sess-3", "c", nil, "success")

		// Touch sess-1 to make it more recent than sess-2
		l.ObserveInteraction("sess-1", "a again", nil, "success")

		// Adding a 4th should evict sess-2 (least recently used)
		l.ObserveInteraction("sess-4", "d", nil, "success")
		assert.Equal(t, 3, l.SessionCount())
		assert.Equal(t, 0, l.InteractionCount("sess-2")) // evicted
		assert.Equal(t, 2, l.InteractionCount("sess-1")) // still here
	})

	t.Run("privacy - no raw content stored", func(t *testing.T) {
		l := NewLearner()
		secret := "my secret password is hunter2"
		l.ObserveInteraction("sess-1", secret, []string{"chat"}, "success")

		// Verify interactions store a hash, not the raw content
		l.mu.RLock()
		sd := l.sessions["sess-1"]
		for _, ix := range sd.interactions {
			assert.NotEqual(t, secret, ix.messageHash)
			assert.NotContains(t, ix.messageHash, "hunter2")
		}
		l.mu.RUnlock()
	})
}

func TestPredictNextAction(t *testing.T) {
	t.Run("no data returns nil", func(t *testing.T) {
		l := NewLearner()
		preds := l.PredictNextAction("sess-1", "hello")
		assert.Nil(t, preds)
	})

	t.Run("unknown session returns nil", func(t *testing.T) {
		l := NewLearner()
		l.ObserveInteraction("sess-1", "hello", []string{"chat"}, "success")
		preds := l.PredictNextAction("sess-999", "hello")
		assert.Nil(t, preds)
	})

	t.Run("repeated sequence produces predictions", func(t *testing.T) {
		l := NewLearner(WithMinConfidence(0.01)) // low threshold for testing
		// Establish a clear pattern: edit -> test -> commit
		for i := 0; i < 10; i++ {
			l.ObserveInteraction("sess-1", "editing file", []string{"file_edit"}, "success")
			l.ObserveInteraction("sess-1", "running tests", []string{"run_tests"}, "success")
			l.ObserveInteraction("sess-1", "committing", []string{"git_commit"}, "success")
		}

		// After "run_tests", should predict "git_commit"
		l.ObserveInteraction("sess-1", "running tests", []string{"run_tests"}, "success")
		preds := l.PredictNextAction("sess-1", "what next?")

		require.NotEmpty(t, preds)
		// The top prediction should involve git_commit
		found := false
		for _, p := range preds {
			if p.Action == "git_commit" {
				found = true
				assert.Greater(t, p.Confidence, 0.0)
				break
			}
		}
		assert.True(t, found, "expected git_commit in predictions, got: %+v", preds)
	})

	t.Run("predictions are sorted by confidence", func(t *testing.T) {
		l := NewLearner(WithMinConfidence(0.01))
		// Build varied patterns
		for i := 0; i < 20; i++ {
			l.ObserveInteraction("sess-1", "search query", []string{"web_search"}, "success")
			l.ObserveInteraction("sess-1", "reading result", []string{"web_fetch"}, "success")
		}
		for i := 0; i < 5; i++ {
			l.ObserveInteraction("sess-1", "search query", []string{"web_search"}, "success")
			l.ObserveInteraction("sess-1", "chat response", []string{"chat"}, "success")
		}

		l.ObserveInteraction("sess-1", "searching", []string{"web_search"}, "success")
		preds := l.PredictNextAction("sess-1", "")

		if len(preds) > 1 {
			for i := 1; i < len(preds); i++ {
				assert.GreaterOrEqual(t, preds[i-1].Confidence, preds[i].Confidence,
					"predictions should be sorted by confidence descending")
			}
		}
	})

	t.Run("predictions capped at max", func(t *testing.T) {
		l := NewLearner(WithMinConfidence(0.01))
		// Create many different follow-up patterns
		tools := []string{"tool_a", "tool_b", "tool_c", "tool_d", "tool_e", "tool_f", "tool_g", "tool_h"}
		for _, tool := range tools {
			for i := 0; i < 5; i++ {
				l.ObserveInteraction("sess-1", "trigger", []string{"trigger"}, "success")
				l.ObserveInteraction("sess-1", "follow", []string{tool}, "success")
			}
		}
		l.ObserveInteraction("sess-1", "trigger", []string{"trigger"}, "success")
		preds := l.PredictNextAction("sess-1", "")
		assert.LessOrEqual(t, len(preds), defaultMaxPredictions)
	})
}

func TestGetUserPatterns(t *testing.T) {
	t.Run("no data returns nil", func(t *testing.T) {
		l := NewLearner()
		patterns := l.GetUserPatterns("sess-1")
		assert.Nil(t, patterns)
	})

	t.Run("detects sequence patterns", func(t *testing.T) {
		l := NewLearner()
		// Repeat a clear pattern many times
		for i := 0; i < 10; i++ {
			l.ObserveInteraction("sess-1", "edit", []string{"file_edit"}, "success")
			l.ObserveInteraction("sess-1", "test", []string{"run_tests"}, "success")
		}

		patterns := l.GetUserPatterns("sess-1")
		require.NotEmpty(t, patterns)

		// Should find the edit->test pattern
		found := false
		for _, p := range patterns {
			if len(p.Sequence) >= 2 {
				for i, s := range p.Sequence {
					if s == "file_edit" && i+1 < len(p.Sequence) && p.Sequence[i+1] == "run_tests" {
						found = true
						assert.GreaterOrEqual(t, p.Frequency, 3)
						break
					}
				}
			}
			if found {
				break
			}
		}
		assert.True(t, found, "expected file_edit->run_tests pattern, got: %+v", patterns)
	})

	t.Run("patterns sorted by frequency", func(t *testing.T) {
		l := NewLearner()
		for i := 0; i < 20; i++ {
			l.ObserveInteraction("sess-1", "a", []string{"tool_a"}, "success")
			l.ObserveInteraction("sess-1", "b", []string{"tool_b"}, "success")
		}
		for i := 0; i < 5; i++ {
			l.ObserveInteraction("sess-1", "c", []string{"tool_c"}, "success")
			l.ObserveInteraction("sess-1", "d", []string{"tool_d"}, "success")
		}

		patterns := l.GetUserPatterns("sess-1")
		if len(patterns) > 1 {
			for i := 1; i < len(patterns); i++ {
				assert.GreaterOrEqual(t, patterns[i-1].Frequency, patterns[i].Frequency,
					"patterns should be sorted by frequency descending")
			}
		}
	})
}

func TestSuggestPrefetch(t *testing.T) {
	t.Run("no data returns nil", func(t *testing.T) {
		l := NewLearner()
		suggestions := l.SuggestPrefetch("sess-1")
		assert.Nil(t, suggestions)
	})

	t.Run("unknown session returns nil", func(t *testing.T) {
		l := NewLearner()
		l.ObserveInteraction("sess-1", "hello", nil, "success")
		suggestions := l.SuggestPrefetch("sess-999")
		assert.Nil(t, suggestions)
	})

	t.Run("produces suggestions from patterns", func(t *testing.T) {
		l := NewLearner(WithMinConfidence(0.01))
		// Build a clear pattern
		for i := 0; i < 15; i++ {
			l.ObserveInteraction("sess-1", "search", []string{"web_search"}, "success")
			l.ObserveInteraction("sess-1", "fetch", []string{"web_fetch"}, "success")
		}
		l.ObserveInteraction("sess-1", "search", []string{"web_search"}, "success")

		suggestions := l.SuggestPrefetch("sess-1")
		// Should suggest web_fetch based on the search->fetch pattern
		if len(suggestions) > 0 {
			found := false
			for _, s := range suggestions {
				if s.Resource == "web_fetch" {
					found = true
					assert.NotEmpty(t, s.Reason)
					assert.NotEmpty(t, s.ExpectedBenefit)
					assert.Greater(t, s.Confidence, 0.0)
				}
			}
			assert.True(t, found, "expected web_fetch suggestion, got: %+v", suggestions)
		}
	})

	t.Run("suggestions capped", func(t *testing.T) {
		l := NewLearner(WithMinConfidence(0.01))
		tools := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
		for _, tool := range tools {
			for i := 0; i < 10; i++ {
				l.ObserveInteraction("sess-1", "trigger", []string{"trigger"}, "success")
				l.ObserveInteraction("sess-1", "follow", []string{tool}, "success")
			}
		}
		l.ObserveInteraction("sess-1", "trigger", []string{"trigger"}, "success")

		suggestions := l.SuggestPrefetch("sess-1")
		assert.LessOrEqual(t, len(suggestions), defaultMaxPrefetchSuggestions)
	})
}

func TestReset(t *testing.T) {
	l := NewLearner()
	for i := 0; i < 10; i++ {
		l.ObserveInteraction("sess-1", fmt.Sprintf("msg %d", i), []string{"chat"}, "success")
	}
	assert.Equal(t, 1, l.SessionCount())
	assert.Equal(t, 10, l.InteractionCount("sess-1"))

	l.Reset()
	assert.Equal(t, 0, l.SessionCount())
	assert.Equal(t, 0, l.InteractionCount("sess-1"))
}

func TestConcurrentAccess(t *testing.T) {
	l := NewLearner()
	var wg sync.WaitGroup

	// Concurrent writes
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			session := fmt.Sprintf("sess-%d", idx%5)
			for j := 0; j < 50; j++ {
				l.ObserveInteraction(session, fmt.Sprintf("msg %d", j), []string{"tool_a", "tool_b"}, "success")
			}
		}(i)
	}

	// Concurrent reads
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			session := fmt.Sprintf("sess-%d", idx%5)
			for j := 0; j < 20; j++ {
				_ = l.PredictNextAction(session, "test")
				_ = l.GetUserPatterns(session)
				_ = l.SuggestPrefetch(session)
				_ = l.SessionCount()
				_ = l.InteractionCount(session)
			}
		}(i)
	}

	wg.Wait()
	// If we get here without deadlock or panic, the test passes
	assert.LessOrEqual(t, l.SessionCount(), defaultMaxSessions)
}

func TestToolsToAction(t *testing.T) {
	tests := []struct {
		tools    []string
		expected string
	}{
		{nil, "chat"},
		{[]string{}, "chat"},
		{[]string{"search"}, "search"},
		{[]string{"search", "fetch"}, "fetch+search"}, // sorted
		{[]string{"c", "a", "b"}, "a+b+c"},
	}

	for _, tt := range tests {
		result := toolsToAction(tt.tools)
		assert.Equal(t, tt.expected, result)
	}
}

func TestHashContent(t *testing.T) {
	t.Run("deterministic", func(t *testing.T) {
		h1 := hashContent("hello world")
		h2 := hashContent("hello world")
		assert.Equal(t, h1, h2)
	})

	t.Run("different for different content", func(t *testing.T) {
		h1 := hashContent("hello")
		h2 := hashContent("world")
		assert.NotEqual(t, h1, h2)
	})

	t.Run("short output", func(t *testing.T) {
		h := hashContent("test content")
		assert.LessOrEqual(t, len(h), 16) // 8 bytes hex = 16 chars
	})
}

func TestTimeOfDayLabel(t *testing.T) {
	tests := []struct {
		hour     int
		expected string
	}{
		{0, "night"},
		{3, "night"},
		{5, "morning"},
		{11, "morning"},
		{12, "afternoon"},
		{16, "afternoon"},
		{17, "evening"},
		{20, "evening"},
		{21, "night"},
		{23, "night"},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.expected, timeOfDayLabel(tt.hour), "hour %d", tt.hour)
	}
}

func TestDeduplicatePredictions(t *testing.T) {
	preds := []Prediction{
		{Action: "search", Confidence: 0.5, Reasoning: "low"},
		{Action: "fetch", Confidence: 0.8, Reasoning: "high"},
		{Action: "search", Confidence: 0.9, Reasoning: "higher"},
	}

	result := deduplicatePredictions(preds)
	assert.Len(t, result, 2)

	// The search prediction should have the higher confidence
	for _, p := range result {
		if p.Action == "search" {
			assert.Equal(t, 0.9, p.Confidence)
		}
	}
}

func TestClampConfidence(t *testing.T) {
	assert.Equal(t, 0.0, clampConfidence(-0.5))
	assert.Equal(t, 0.0, clampConfidence(0.0))
	assert.Equal(t, 0.5, clampConfidence(0.5))
	assert.Equal(t, 1.0, clampConfidence(1.0))
	assert.Equal(t, 1.0, clampConfidence(1.5))
}

func TestDecayFactor(t *testing.T) {
	now := time.Now()
	maxAge := 24 * time.Hour

	t.Run("recent event near 1", func(t *testing.T) {
		factor := decayFactor(now.Add(-1*time.Minute), now, maxAge)
		assert.Greater(t, factor, 0.95)
	})

	t.Run("old event near 0", func(t *testing.T) {
		factor := decayFactor(now.Add(-23*time.Hour), now, maxAge)
		assert.Less(t, factor, 0.1)
	})

	t.Run("beyond maxAge is 0", func(t *testing.T) {
		factor := decayFactor(now.Add(-25*time.Hour), now, maxAge)
		assert.Equal(t, 0.0, factor)
	})

	t.Run("future event is 0", func(t *testing.T) {
		factor := decayFactor(now.Add(1*time.Hour), now, maxAge)
		assert.Equal(t, 0.0, factor)
	})
}
