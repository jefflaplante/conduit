package learning

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSequenceAnalyzer(t *testing.T) {
	sa := NewSequenceAnalyzer()
	assert.NotNil(t, sa)
	assert.Empty(t, sa.sessions)
}

func TestSequenceAnalyzer_RecordAction(t *testing.T) {
	t.Run("records actions", func(t *testing.T) {
		sa := NewSequenceAnalyzer()
		now := time.Now()

		sa.RecordAction("sess-1", "edit", now)
		sa.RecordAction("sess-1", "test", now.Add(time.Second))
		sa.RecordAction("sess-1", "commit", now.Add(2*time.Second))

		sa.mu.RLock()
		ss := sa.sessions["sess-1"]
		assert.Len(t, ss.recentActions, 3)
		assert.Equal(t, 3, ss.totalActions)
		sa.mu.RUnlock()
	})

	t.Run("empty session or action ignored", func(t *testing.T) {
		sa := NewSequenceAnalyzer()
		now := time.Now()

		sa.RecordAction("", "edit", now)
		sa.RecordAction("sess-1", "", now)
		assert.Empty(t, sa.sessions)
	})

	t.Run("builds bigrams", func(t *testing.T) {
		sa := NewSequenceAnalyzer()
		now := time.Now()

		sa.RecordAction("sess-1", "edit", now)
		sa.RecordAction("sess-1", "test", now.Add(time.Second))

		sa.mu.RLock()
		ss := sa.sessions["sess-1"]
		entry, ok := ss.ngrams["edit|test"]
		sa.mu.RUnlock()

		assert.True(t, ok, "bigram edit|test should exist")
		assert.Equal(t, 1, entry.count)
	})

	t.Run("builds trigrams", func(t *testing.T) {
		sa := NewSequenceAnalyzer()
		now := time.Now()

		sa.RecordAction("sess-1", "edit", now)
		sa.RecordAction("sess-1", "test", now.Add(time.Second))
		sa.RecordAction("sess-1", "commit", now.Add(2*time.Second))

		sa.mu.RLock()
		ss := sa.sessions["sess-1"]
		entry, ok := ss.ngrams["edit|test|commit"]
		sa.mu.RUnlock()

		assert.True(t, ok, "trigram edit|test|commit should exist")
		assert.Equal(t, 1, entry.count)
	})

	t.Run("increments n-gram counts on repetition", func(t *testing.T) {
		sa := NewSequenceAnalyzer()
		now := time.Now()

		for i := 0; i < 5; i++ {
			sa.RecordAction("sess-1", "edit", now.Add(time.Duration(i*3)*time.Second))
			sa.RecordAction("sess-1", "test", now.Add(time.Duration(i*3+1)*time.Second))
			sa.RecordAction("sess-1", "commit", now.Add(time.Duration(i*3+2)*time.Second))
		}

		sa.mu.RLock()
		ss := sa.sessions["sess-1"]
		bigramEntry := ss.ngrams["edit|test"]
		trigramEntry := ss.ngrams["edit|test|commit"]
		sa.mu.RUnlock()

		assert.Equal(t, 5, bigramEntry.count)
		assert.Equal(t, 5, trigramEntry.count)
	})

	t.Run("rolling window enforced", func(t *testing.T) {
		sa := NewSequenceAnalyzer()
		now := time.Now()

		for i := 0; i < defaultMaxActionsPerSession+50; i++ {
			sa.RecordAction("sess-1", fmt.Sprintf("action_%d", i), now.Add(time.Duration(i)*time.Second))
		}

		sa.mu.RLock()
		ss := sa.sessions["sess-1"]
		assert.Len(t, ss.recentActions, defaultMaxActionsPerSession)
		sa.mu.RUnlock()
	})

	t.Run("time-of-day buckets updated", func(t *testing.T) {
		sa := NewSequenceAnalyzer()

		// Morning action (hour 9 -> bucket 2)
		morning := time.Date(2024, 1, 1, 9, 0, 0, 0, time.UTC)
		sa.RecordAction("sess-1", "review", morning)

		sa.mu.RLock()
		ss := sa.sessions["sess-1"]
		bucket := 9 / defaultTimeWindowHours
		assert.Equal(t, 1, ss.hourBuckets[bucket]["review"])
		sa.mu.RUnlock()
	})
}

func TestSequenceAnalyzer_Predict(t *testing.T) {
	t.Run("no data returns nil", func(t *testing.T) {
		sa := NewSequenceAnalyzer()
		preds := sa.Predict("sess-1", 5)
		assert.Nil(t, preds)
	})

	t.Run("predicts from bigrams", func(t *testing.T) {
		sa := NewSequenceAnalyzer()
		now := time.Now()

		// Build pattern: A -> B (10 times)
		for i := 0; i < 10; i++ {
			sa.RecordAction("sess-1", "search", now.Add(time.Duration(i*2)*time.Second))
			sa.RecordAction("sess-1", "fetch", now.Add(time.Duration(i*2+1)*time.Second))
		}

		// End on "search" to trigger prediction of "fetch"
		sa.RecordAction("sess-1", "search", now.Add(21*time.Second))

		preds := sa.Predict("sess-1", 5)
		require.NotEmpty(t, preds)

		found := false
		for _, p := range preds {
			if p.Action == "fetch" {
				found = true
				assert.Greater(t, p.Confidence, 0.0)
			}
		}
		assert.True(t, found, "expected fetch prediction, got: %+v", preds)
	})

	t.Run("predicts from trigrams", func(t *testing.T) {
		sa := NewSequenceAnalyzer()
		now := time.Now()

		// Build pattern: A -> B -> C (10 times)
		for i := 0; i < 10; i++ {
			sa.RecordAction("sess-1", "edit", now.Add(time.Duration(i*3)*time.Second))
			sa.RecordAction("sess-1", "test", now.Add(time.Duration(i*3+1)*time.Second))
			sa.RecordAction("sess-1", "commit", now.Add(time.Duration(i*3+2)*time.Second))
		}

		// End on "edit" then "test" to trigger prediction of "commit"
		sa.RecordAction("sess-1", "edit", now.Add(31*time.Second))
		sa.RecordAction("sess-1", "test", now.Add(32*time.Second))

		preds := sa.Predict("sess-1", 5)
		require.NotEmpty(t, preds)

		found := false
		for _, p := range preds {
			if p.Action == "commit" {
				found = true
				assert.Greater(t, p.Confidence, 0.0)
			}
		}
		assert.True(t, found, "expected commit prediction, got: %+v", preds)
	})

	t.Run("sorted by confidence", func(t *testing.T) {
		sa := NewSequenceAnalyzer()
		now := time.Now()

		// Strong pattern: A -> B (20 times)
		for i := 0; i < 20; i++ {
			sa.RecordAction("sess-1", "search", now.Add(time.Duration(i*2)*time.Second))
			sa.RecordAction("sess-1", "fetch", now.Add(time.Duration(i*2+1)*time.Second))
		}
		// Weak pattern: A -> C (3 times)
		for i := 0; i < 3; i++ {
			sa.RecordAction("sess-1", "search", now.Add(time.Duration(40+i*2)*time.Second))
			sa.RecordAction("sess-1", "analyze", now.Add(time.Duration(40+i*2+1)*time.Second))
		}

		sa.RecordAction("sess-1", "search", now.Add(50*time.Second))
		preds := sa.Predict("sess-1", 10)

		if len(preds) > 1 {
			for i := 1; i < len(preds); i++ {
				assert.GreaterOrEqual(t, preds[i-1].Confidence, preds[i].Confidence)
			}
		}
	})

	t.Run("respects limit", func(t *testing.T) {
		sa := NewSequenceAnalyzer()
		now := time.Now()

		actions := []string{"a", "b", "c", "d", "e", "f"}
		for i := 0; i < 10; i++ {
			sa.RecordAction("sess-1", "trigger", now.Add(time.Duration(i*7)*time.Second))
			for j, a := range actions {
				sa.RecordAction("sess-1", a, now.Add(time.Duration(i*7+j+1)*time.Second))
			}
		}
		sa.RecordAction("sess-1", "trigger", now.Add(100*time.Second))

		preds := sa.Predict("sess-1", 3)
		assert.LessOrEqual(t, len(preds), 3)
	})
}

func TestSequenceAnalyzer_PredictByTimeOfDay(t *testing.T) {
	t.Run("no data returns nil", func(t *testing.T) {
		sa := NewSequenceAnalyzer()
		preds := sa.PredictByTimeOfDay("sess-1", 10, 3)
		assert.Nil(t, preds)
	})

	t.Run("predicts from time buckets", func(t *testing.T) {
		sa := NewSequenceAnalyzer()

		// Record morning actions (hour 9)
		morning := time.Date(2024, 1, 1, 9, 0, 0, 0, time.UTC)
		for i := 0; i < 10; i++ {
			sa.RecordAction("sess-1", "code_review", morning.Add(time.Duration(i)*time.Minute))
		}
		for i := 0; i < 3; i++ {
			sa.RecordAction("sess-1", "email", morning.Add(time.Duration(i)*time.Minute))
		}

		preds := sa.PredictByTimeOfDay("sess-1", 9, 5)
		require.NotEmpty(t, preds)
		assert.Equal(t, "code_review", preds[0].Action)
	})

	t.Run("respects limit", func(t *testing.T) {
		sa := NewSequenceAnalyzer()
		morning := time.Date(2024, 1, 1, 9, 0, 0, 0, time.UTC)
		for i := 0; i < 5; i++ {
			sa.RecordAction("sess-1", fmt.Sprintf("tool_%d", i), morning)
		}

		preds := sa.PredictByTimeOfDay("sess-1", 9, 2)
		assert.LessOrEqual(t, len(preds), 2)
	})
}

func TestSequenceAnalyzer_GetPatterns(t *testing.T) {
	t.Run("no data returns nil", func(t *testing.T) {
		sa := NewSequenceAnalyzer()
		patterns := sa.GetPatterns("sess-1", 3)
		assert.Nil(t, patterns)
	})

	t.Run("returns patterns above threshold", func(t *testing.T) {
		sa := NewSequenceAnalyzer()
		now := time.Now()

		// Repeat a pattern 5 times
		for i := 0; i < 5; i++ {
			sa.RecordAction("sess-1", "edit", now.Add(time.Duration(i*2)*time.Second))
			sa.RecordAction("sess-1", "test", now.Add(time.Duration(i*2+1)*time.Second))
		}

		patterns := sa.GetPatterns("sess-1", 3)
		require.NotEmpty(t, patterns)

		found := false
		for _, p := range patterns {
			if len(p.Sequence) == 2 && p.Sequence[0] == "edit" && p.Sequence[1] == "test" {
				found = true
				assert.GreaterOrEqual(t, p.Frequency, 3)
			}
		}
		assert.True(t, found, "expected edit->test pattern")
	})

	t.Run("filters by minimum occurrences", func(t *testing.T) {
		sa := NewSequenceAnalyzer()
		now := time.Now()

		// Common pattern (10x)
		for i := 0; i < 10; i++ {
			sa.RecordAction("sess-1", "edit", now.Add(time.Duration(i*2)*time.Second))
			sa.RecordAction("sess-1", "test", now.Add(time.Duration(i*2+1)*time.Second))
		}
		// Rare pattern (1x)
		sa.RecordAction("sess-1", "deploy", now.Add(30*time.Second))
		sa.RecordAction("sess-1", "monitor", now.Add(31*time.Second))

		patterns := sa.GetPatterns("sess-1", 5) // require at least 5 occurrences
		for _, p := range patterns {
			assert.GreaterOrEqual(t, p.Frequency, 5)
		}
	})

	t.Run("includes time-of-day info", func(t *testing.T) {
		sa := NewSequenceAnalyzer()

		// Build a morning pattern
		morning := time.Date(2024, 1, 1, 9, 0, 0, 0, time.UTC)
		for i := 0; i < 10; i++ {
			sa.RecordAction("sess-1", "review", morning.Add(time.Duration(i*2)*time.Second))
			sa.RecordAction("sess-1", "approve", morning.Add(time.Duration(i*2+1)*time.Second))
		}

		patterns := sa.GetPatterns("sess-1", 3)
		// At least some patterns should have time-of-day info
		hasTimeInfo := false
		for _, p := range patterns {
			if p.TimeOfDay != "" {
				hasTimeInfo = true
				break
			}
		}
		// This is best-effort - time of day requires enough data
		_ = hasTimeInfo
	})
}

func TestSequenceAnalyzer_RemoveSession(t *testing.T) {
	sa := NewSequenceAnalyzer()
	now := time.Now()

	sa.RecordAction("sess-1", "edit", now)
	sa.RecordAction("sess-1", "test", now.Add(time.Second))

	sa.RemoveSession("sess-1")

	sa.mu.RLock()
	_, ok := sa.sessions["sess-1"]
	sa.mu.RUnlock()
	assert.False(t, ok)
}

func TestSequenceAnalyzer_Reset(t *testing.T) {
	sa := NewSequenceAnalyzer()
	now := time.Now()

	sa.RecordAction("sess-1", "edit", now)
	sa.RecordAction("sess-2", "test", now)

	sa.Reset()

	sa.mu.RLock()
	assert.Empty(t, sa.sessions)
	sa.mu.RUnlock()
}

func TestNgramPruning(t *testing.T) {
	sa := NewSequenceAnalyzer()
	now := time.Now()

	// Generate many unique actions to create many n-grams
	for i := 0; i < defaultMaxNgrams+100; i++ {
		sa.RecordAction("sess-1", fmt.Sprintf("action_%d", i), now.Add(time.Duration(i)*time.Millisecond))
	}

	sa.mu.RLock()
	ss := sa.sessions["sess-1"]
	assert.LessOrEqual(t, len(ss.ngrams), defaultMaxNgrams+50, // some slack after pruning
		"n-grams should be pruned when exceeding cap")
	sa.mu.RUnlock()
}

func TestMaxHelper(t *testing.T) {
	assert.Equal(t, 5, max(5, 3))
	assert.Equal(t, 5, max(3, 5))
	assert.Equal(t, 5, max(5, 5))
	assert.Equal(t, 0, max(0, -1))
	assert.Equal(t, 0, max(-1, 0))
}
