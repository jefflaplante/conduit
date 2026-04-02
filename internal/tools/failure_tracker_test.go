package tools

import (
	"sync"
	"testing"
)

func TestNewFailureTracker(t *testing.T) {
	t.Run("uses provided threshold", func(t *testing.T) {
		ft := NewFailureTracker(5)
		if ft.threshold != 5 {
			t.Errorf("expected threshold 5, got %d", ft.threshold)
		}
	})

	t.Run("defaults to 3 for zero threshold", func(t *testing.T) {
		ft := NewFailureTracker(0)
		if ft.threshold != 3 {
			t.Errorf("expected threshold 3, got %d", ft.threshold)
		}
	})

	t.Run("defaults to 3 for negative threshold", func(t *testing.T) {
		ft := NewFailureTracker(-1)
		if ft.threshold != 3 {
			t.Errorf("expected threshold 3, got %d", ft.threshold)
		}
	})
}

func TestFailureTracker_RecordFailure(t *testing.T) {
	ft := NewFailureTracker(3)

	t.Run("increments failure count", func(t *testing.T) {
		count := ft.RecordFailure("test_tool")
		if count != 1 {
			t.Errorf("expected count 1, got %d", count)
		}

		count = ft.RecordFailure("test_tool")
		if count != 2 {
			t.Errorf("expected count 2, got %d", count)
		}

		count = ft.RecordFailure("test_tool")
		if count != 3 {
			t.Errorf("expected count 3, got %d", count)
		}
	})

	t.Run("tracks different tools separately", func(t *testing.T) {
		ft2 := NewFailureTracker(3)
		ft2.RecordFailure("tool_a")
		ft2.RecordFailure("tool_a")
		ft2.RecordFailure("tool_b")

		if !ft2.ShouldPivot("tool_a") == false {
			// tool_a has 2 failures, below threshold
		}
		countA := ft2.RecordFailure("tool_a")
		countB := ft2.RecordFailure("tool_b")

		if countA != 3 {
			t.Errorf("expected tool_a count 3, got %d", countA)
		}
		if countB != 2 {
			t.Errorf("expected tool_b count 2, got %d", countB)
		}
	})
}

func TestFailureTracker_RecordSuccess(t *testing.T) {
	ft := NewFailureTracker(3)

	t.Run("resets failure count", func(t *testing.T) {
		ft.RecordFailure("test_tool")
		ft.RecordFailure("test_tool")
		ft.RecordSuccess("test_tool")

		// After success, count should be reset
		count := ft.RecordFailure("test_tool")
		if count != 1 {
			t.Errorf("expected count 1 after reset, got %d", count)
		}
	})

	t.Run("no-op for non-existent tool", func(t *testing.T) {
		// Should not panic
		ft.RecordSuccess("nonexistent_tool")
	})
}

func TestFailureTracker_ShouldPivot(t *testing.T) {
	ft := NewFailureTracker(3)

	t.Run("returns false below threshold", func(t *testing.T) {
		ft.RecordFailure("test_tool")
		ft.RecordFailure("test_tool")

		if ft.ShouldPivot("test_tool") {
			t.Error("expected ShouldPivot false with 2 failures")
		}
	})

	t.Run("returns true at threshold", func(t *testing.T) {
		ft2 := NewFailureTracker(3)
		ft2.RecordFailure("test_tool")
		ft2.RecordFailure("test_tool")
		ft2.RecordFailure("test_tool")

		if !ft2.ShouldPivot("test_tool") {
			t.Error("expected ShouldPivot true with 3 failures")
		}
	})

	t.Run("returns true above threshold", func(t *testing.T) {
		ft3 := NewFailureTracker(3)
		ft3.RecordFailure("test_tool")
		ft3.RecordFailure("test_tool")
		ft3.RecordFailure("test_tool")
		ft3.RecordFailure("test_tool")

		if !ft3.ShouldPivot("test_tool") {
			t.Error("expected ShouldPivot true with 4 failures")
		}
	})

	t.Run("returns false for unknown tool", func(t *testing.T) {
		if ft.ShouldPivot("unknown_tool") {
			t.Error("expected ShouldPivot false for unknown tool")
		}
	})
}

func TestFailureTracker_Reset(t *testing.T) {
	ft := NewFailureTracker(3)

	ft.RecordFailure("tool_a")
	ft.RecordFailure("tool_a")
	ft.RecordFailure("tool_b")
	ft.RecordFailure("tool_b")
	ft.RecordFailure("tool_b")

	ft.Reset()

	if ft.ShouldPivot("tool_a") {
		t.Error("expected ShouldPivot false for tool_a after reset")
	}
	if ft.ShouldPivot("tool_b") {
		t.Error("expected ShouldPivot false for tool_b after reset")
	}

	// Count should start fresh
	count := ft.RecordFailure("tool_a")
	if count != 1 {
		t.Errorf("expected count 1 after reset, got %d", count)
	}
}

func TestFailureTracker_GetFailedTools(t *testing.T) {
	ft := NewFailureTracker(3)

	t.Run("returns empty for no failures", func(t *testing.T) {
		failed := ft.GetFailedTools()
		if len(failed) != 0 {
			t.Errorf("expected empty list, got %v", failed)
		}
	})

	t.Run("returns only tools at threshold", func(t *testing.T) {
		ft2 := NewFailureTracker(3)
		// tool_a: 3 failures (at threshold)
		ft2.RecordFailure("tool_a")
		ft2.RecordFailure("tool_a")
		ft2.RecordFailure("tool_a")
		// tool_b: 2 failures (below threshold)
		ft2.RecordFailure("tool_b")
		ft2.RecordFailure("tool_b")
		// tool_c: 4 failures (above threshold)
		ft2.RecordFailure("tool_c")
		ft2.RecordFailure("tool_c")
		ft2.RecordFailure("tool_c")
		ft2.RecordFailure("tool_c")

		failed := ft2.GetFailedTools()
		if len(failed) != 2 {
			t.Errorf("expected 2 failed tools, got %d: %v", len(failed), failed)
		}

		// Check that tool_a and tool_c are in the list
		hasA, hasC := false, false
		for _, tool := range failed {
			if tool == "tool_a" {
				hasA = true
			}
			if tool == "tool_c" {
				hasC = true
			}
		}
		if !hasA {
			t.Error("expected tool_a in failed tools")
		}
		if !hasC {
			t.Error("expected tool_c in failed tools")
		}
	})
}

func TestFailureTracker_Concurrency(t *testing.T) {
	ft := NewFailureTracker(100) // High threshold to avoid pivot triggers

	var wg sync.WaitGroup
	const numGoroutines = 100
	const numIterations = 100

	// Concurrent failures
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < numIterations; j++ {
				ft.RecordFailure("concurrent_tool")
			}
		}()
	}
	wg.Wait()

	// Verify all failures were recorded
	ft.mu.Lock()
	count := ft.failures["concurrent_tool"]
	ft.mu.Unlock()

	expected := numGoroutines * numIterations
	if count != expected {
		t.Errorf("expected %d failures, got %d", expected, count)
	}
}
