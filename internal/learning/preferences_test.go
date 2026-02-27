package learning

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewPreferenceTracker(t *testing.T) {
	pt := NewPreferenceTracker()
	assert.NotNil(t, pt)
	assert.Empty(t, pt.sessions)
}

func TestPreferenceTracker_RecordInteraction(t *testing.T) {
	t.Run("records tool usage", func(t *testing.T) {
		pt := NewPreferenceTracker()
		now := time.Now()

		pt.RecordInteraction("sess-1", []string{"search", "fetch"}, "success", 10, now)

		pt.mu.RLock()
		sp := pt.sessions["sess-1"]
		assert.Len(t, sp.tools, 2)
		assert.Equal(t, 1, sp.tools["search"].useCount)
		assert.Equal(t, 1, sp.tools["search"].successCount)
		assert.Equal(t, 1, sp.tools["fetch"].useCount)
		pt.mu.RUnlock()
	})

	t.Run("empty session ignored", func(t *testing.T) {
		pt := NewPreferenceTracker()
		pt.RecordInteraction("", []string{"search"}, "success", 10, time.Now())
		assert.Empty(t, pt.sessions)
	})

	t.Run("accumulates counts", func(t *testing.T) {
		pt := NewPreferenceTracker()
		now := time.Now()

		for i := 0; i < 5; i++ {
			pt.RecordInteraction("sess-1", []string{"search"}, "success", 10, now.Add(time.Duration(i)*time.Second))
		}
		pt.RecordInteraction("sess-1", []string{"search"}, "error", 10, now.Add(5*time.Second))

		pt.mu.RLock()
		sp := pt.sessions["sess-1"]
		assert.Equal(t, 6, sp.tools["search"].useCount)
		assert.Equal(t, 5, sp.tools["search"].successCount)
		pt.mu.RUnlock()
	})

	t.Run("tracks message word count", func(t *testing.T) {
		pt := NewPreferenceTracker()
		now := time.Now()

		pt.RecordInteraction("sess-1", nil, "success", 10, now)
		pt.RecordInteraction("sess-1", nil, "success", 20, now.Add(time.Second))
		pt.RecordInteraction("sess-1", nil, "success", 30, now.Add(2*time.Second))

		pt.mu.RLock()
		sp := pt.sessions["sess-1"]
		assert.Equal(t, 3, sp.responsePrefs.messageCount)
		assert.Equal(t, 60, sp.responsePrefs.totalWordCount)
		assert.InDelta(t, 20.0, sp.responsePrefs.avgWordCount, 0.1)
		pt.mu.RUnlock()
	})

	t.Run("tool cap enforced", func(t *testing.T) {
		pt := NewPreferenceTracker()
		now := time.Now()

		// Add max tools
		for i := 0; i < defaultMaxPreferenceTools+10; i++ {
			pt.RecordInteraction("sess-1", []string{fmt.Sprintf("tool_%d", i)}, "success", 5, now.Add(time.Duration(i)*time.Second))
		}

		pt.mu.RLock()
		sp := pt.sessions["sess-1"]
		assert.LessOrEqual(t, len(sp.tools), defaultMaxPreferenceTools)
		pt.mu.RUnlock()
	})

	t.Run("empty tool names skipped", func(t *testing.T) {
		pt := NewPreferenceTracker()
		pt.RecordInteraction("sess-1", []string{""}, "success", 5, time.Now())

		pt.mu.RLock()
		sp := pt.sessions["sess-1"]
		assert.Empty(t, sp.tools)
		pt.mu.RUnlock()
	})
}

func TestPreferenceTracker_PredictFromPreferences(t *testing.T) {
	t.Run("no data returns nil", func(t *testing.T) {
		pt := NewPreferenceTracker()
		preds := pt.PredictFromPreferences("sess-1", "hello")
		assert.Nil(t, preds)
	})

	t.Run("predicts preferred tools", func(t *testing.T) {
		pt := NewPreferenceTracker()
		now := time.Now()

		// Build strong preference for search tool
		for i := 0; i < 20; i++ {
			pt.RecordInteraction("sess-1", []string{"web_search"}, "success", 10, now.Add(time.Duration(i)*time.Second))
		}
		for i := 0; i < 3; i++ {
			pt.RecordInteraction("sess-1", []string{"file_edit"}, "success", 10, now.Add(time.Duration(20+i)*time.Second))
		}

		preds := pt.PredictFromPreferences("sess-1", "test message")
		require.NotEmpty(t, preds)

		// web_search should be a top prediction
		found := false
		for _, p := range preds {
			if p.Action == "web_search" {
				found = true
				assert.Greater(t, p.Confidence, 0.0)
			}
		}
		assert.True(t, found, "expected web_search prediction, got: %+v", preds)
	})

	t.Run("long message suggests analysis", func(t *testing.T) {
		pt := NewPreferenceTracker()
		now := time.Now()

		// Establish normal message length of ~10 words
		for i := 0; i < 20; i++ {
			pt.RecordInteraction("sess-1", []string{"chat"}, "success", 10, now.Add(time.Duration(i)*time.Second))
		}

		// Now send a very long message (5x average)
		longMessage := "word " // Will have >50 words when we check
		for i := 0; i < 50; i++ {
			longMessage += "word "
		}

		preds := pt.PredictFromPreferences("sess-1", longMessage)
		// Should include an "analysis" prediction due to the unusually long message
		found := false
		for _, p := range preds {
			if p.Action == "analysis" {
				found = true
			}
		}
		assert.True(t, found, "expected analysis prediction for long message, got: %+v", preds)
	})
}

func TestPreferenceTracker_GetPreferencePatterns(t *testing.T) {
	t.Run("no data returns nil", func(t *testing.T) {
		pt := NewPreferenceTracker()
		patterns := pt.GetPreferencePatterns("sess-1")
		assert.Nil(t, patterns)
	})

	t.Run("returns tool preference pattern", func(t *testing.T) {
		pt := NewPreferenceTracker()
		now := time.Now()

		for i := 0; i < 10; i++ {
			pt.RecordInteraction("sess-1", []string{"web_search"}, "success", 10, now.Add(time.Duration(i)*time.Second))
		}
		for i := 0; i < 5; i++ {
			pt.RecordInteraction("sess-1", []string{"file_edit"}, "success", 10, now.Add(time.Duration(10+i)*time.Second))
		}

		patterns := pt.GetPreferencePatterns("sess-1")
		require.NotEmpty(t, patterns)

		found := false
		for _, p := range patterns {
			if p.Name == "preferred-tools" {
				found = true
				assert.NotEmpty(t, p.Sequence)
				assert.Greater(t, p.Frequency, 0)
			}
		}
		assert.True(t, found, "expected preferred-tools pattern")
	})

	t.Run("returns message style pattern with enough data", func(t *testing.T) {
		pt := NewPreferenceTracker()
		now := time.Now()

		// Record 5+ interactions for message style detection
		for i := 0; i < 10; i++ {
			pt.RecordInteraction("sess-1", []string{"chat"}, "success", 5, now.Add(time.Duration(i)*time.Second))
		}

		patterns := pt.GetPreferencePatterns("sess-1")
		found := false
		for _, p := range patterns {
			if p.Name == "message-style-concise" {
				found = true
				assert.Equal(t, 10, p.Frequency)
			}
		}
		assert.True(t, found, "expected message-style-concise pattern, got: %+v", patterns)
	})

	t.Run("detects detailed message style", func(t *testing.T) {
		pt := NewPreferenceTracker()
		now := time.Now()

		for i := 0; i < 10; i++ {
			pt.RecordInteraction("sess-1", []string{"chat"}, "success", 50, now.Add(time.Duration(i)*time.Second))
		}

		patterns := pt.GetPreferencePatterns("sess-1")
		found := false
		for _, p := range patterns {
			if p.Name == "message-style-detailed" {
				found = true
			}
		}
		assert.True(t, found, "expected message-style-detailed pattern, got: %+v", patterns)
	})
}

func TestPreferenceTracker_RemoveSession(t *testing.T) {
	pt := NewPreferenceTracker()
	pt.RecordInteraction("sess-1", []string{"search"}, "success", 10, time.Now())

	pt.RemoveSession("sess-1")

	pt.mu.RLock()
	_, ok := pt.sessions["sess-1"]
	pt.mu.RUnlock()
	assert.False(t, ok)
}

func TestPreferenceTracker_Reset(t *testing.T) {
	pt := NewPreferenceTracker()
	pt.RecordInteraction("sess-1", []string{"search"}, "success", 10, time.Now())
	pt.RecordInteraction("sess-2", []string{"fetch"}, "success", 10, time.Now())

	pt.Reset()

	pt.mu.RLock()
	assert.Empty(t, pt.sessions)
	pt.mu.RUnlock()
}

func TestEvictLeastUsedTool(t *testing.T) {
	pt := NewPreferenceTracker()
	now := time.Now()

	// Add tools with different usage counts
	pt.RecordInteraction("sess-1", []string{"often"}, "success", 5, now)
	pt.RecordInteraction("sess-1", []string{"often"}, "success", 5, now.Add(time.Second))
	pt.RecordInteraction("sess-1", []string{"often"}, "success", 5, now.Add(2*time.Second))
	pt.RecordInteraction("sess-1", []string{"rarely"}, "success", 5, now.Add(3*time.Second))

	pt.mu.Lock()
	sp := pt.sessions["sess-1"]
	pt.evictLeastUsedTool(sp)
	pt.mu.Unlock()

	pt.mu.RLock()
	_, hasOften := sp.tools["often"]
	_, hasRarely := sp.tools["rarely"]
	pt.mu.RUnlock()

	assert.True(t, hasOften, "frequently used tool should remain")
	assert.False(t, hasRarely, "rarely used tool should be evicted")
}
