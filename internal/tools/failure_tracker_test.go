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
		count := ft.RecordFailure("test_tool", "err1")
		if count != 1 {
			t.Errorf("expected count 1, got %d", count)
		}

		count = ft.RecordFailure("test_tool", "err2")
		if count != 2 {
			t.Errorf("expected count 2, got %d", count)
		}

		count = ft.RecordFailure("test_tool", "err3")
		if count != 3 {
			t.Errorf("expected count 3, got %d", count)
		}
	})

	t.Run("tracks different tools separately", func(t *testing.T) {
		ft2 := NewFailureTracker(3)
		ft2.RecordFailure("tool_a", "err")
		ft2.RecordFailure("tool_a", "err")
		ft2.RecordFailure("tool_b", "err")

		if !ft2.ShouldPivot("tool_a") == false {
			// tool_a has 2 failures, below threshold
		}
		countA := ft2.RecordFailure("tool_a", "err")
		countB := ft2.RecordFailure("tool_b", "err")

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
		ft.RecordFailure("test_tool", "err")
		ft.RecordFailure("test_tool", "err")
		ft.RecordSuccess("test_tool")

		// After success, count should be reset
		count := ft.RecordFailure("test_tool", "err")
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
		ft.RecordFailure("test_tool", "err")
		ft.RecordFailure("test_tool", "err")

		if ft.ShouldPivot("test_tool") {
			t.Error("expected ShouldPivot false with 2 failures")
		}
	})

	t.Run("returns true at threshold", func(t *testing.T) {
		ft2 := NewFailureTracker(3)
		ft2.RecordFailure("test_tool", "err")
		ft2.RecordFailure("test_tool", "err")
		ft2.RecordFailure("test_tool", "err")

		if !ft2.ShouldPivot("test_tool") {
			t.Error("expected ShouldPivot true with 3 failures")
		}
	})

	t.Run("returns true above threshold", func(t *testing.T) {
		ft3 := NewFailureTracker(3)
		ft3.RecordFailure("test_tool", "err")
		ft3.RecordFailure("test_tool", "err")
		ft3.RecordFailure("test_tool", "err")
		ft3.RecordFailure("test_tool", "err")

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

	ft.RecordFailure("tool_a", "err")
	ft.RecordFailure("tool_a", "err")
	ft.RecordFailure("tool_b", "err")
	ft.RecordFailure("tool_b", "err")
	ft.RecordFailure("tool_b", "err")

	ft.Reset()

	if ft.ShouldPivot("tool_a") {
		t.Error("expected ShouldPivot false for tool_a after reset")
	}
	if ft.ShouldPivot("tool_b") {
		t.Error("expected ShouldPivot false for tool_b after reset")
	}

	// Count should start fresh
	count := ft.RecordFailure("tool_a", "err")
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
		ft2.RecordFailure("tool_a", "err")
		ft2.RecordFailure("tool_a", "err")
		ft2.RecordFailure("tool_a", "err")
		// tool_b: 2 failures (below threshold)
		ft2.RecordFailure("tool_b", "err")
		ft2.RecordFailure("tool_b", "err")
		// tool_c: 4 failures (above threshold)
		ft2.RecordFailure("tool_c", "err")
		ft2.RecordFailure("tool_c", "err")
		ft2.RecordFailure("tool_c", "err")
		ft2.RecordFailure("tool_c", "err")

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
				ft.RecordFailure("concurrent_tool", "concurrent error")
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

// --- OnPivot callback tests ---

func TestFailureTracker_OnPivot_FiresAtThreshold(t *testing.T) {
	ft := NewFailureTracker(3)

	var called bool
	var gotTool string
	var gotCount int
	var gotError string

	ft.OnPivot = func(toolName string, failCount int, lastError string) {
		called = true
		gotTool = toolName
		gotCount = failCount
		gotError = lastError
	}

	ft.RecordFailure("WebFetch", "connection refused")
	ft.RecordFailure("WebFetch", "timeout")
	ft.RecordFailure("WebFetch", "502 bad gateway") // threshold=3, should fire

	if !called {
		t.Fatal("expected OnPivot to be called at threshold")
	}
	if gotTool != "WebFetch" {
		t.Errorf("expected tool 'WebFetch', got %q", gotTool)
	}
	if gotCount != 3 {
		t.Errorf("expected failCount 3, got %d", gotCount)
	}
	if gotError != "502 bad gateway" {
		t.Errorf("expected lastError '502 bad gateway', got %q", gotError)
	}
}

func TestFailureTracker_OnPivot_NotCalledBelowThreshold(t *testing.T) {
	ft := NewFailureTracker(3)

	called := false
	ft.OnPivot = func(toolName string, failCount int, lastError string) {
		called = true
	}

	ft.RecordFailure("ReadFile", "not found")
	ft.RecordFailure("ReadFile", "permission denied")

	if called {
		t.Error("OnPivot should not fire below threshold")
	}
}

func TestFailureTracker_OnPivot_NotCalledAboveThreshold(t *testing.T) {
	ft := NewFailureTracker(3)

	callCount := 0
	ft.OnPivot = func(toolName string, failCount int, lastError string) {
		callCount++
	}

	ft.RecordFailure("Bash", "exit code 1")
	ft.RecordFailure("Bash", "exit code 1")
	ft.RecordFailure("Bash", "exit code 1") // fires here
	ft.RecordFailure("Bash", "exit code 1") // should NOT fire again
	ft.RecordFailure("Bash", "exit code 1") // should NOT fire again

	if callCount != 1 {
		t.Errorf("expected OnPivot called exactly once, got %d calls", callCount)
	}
}

func TestFailureTracker_OnPivot_NilDoesNotPanic(t *testing.T) {
	ft := NewFailureTracker(3)
	// OnPivot is nil by default

	// Should not panic even when threshold is reached
	ft.RecordFailure("ReadFile", "err1")
	ft.RecordFailure("ReadFile", "err2")
	ft.RecordFailure("ReadFile", "err3")

	if !ft.ShouldPivot("ReadFile") {
		t.Error("expected ShouldPivot true after 3 failures")
	}
}

func TestFailureTracker_OnPivot_LastErrorCaptured(t *testing.T) {
	ft := NewFailureTracker(3)

	var capturedError string
	ft.OnPivot = func(toolName string, failCount int, lastError string) {
		capturedError = lastError
	}

	ft.RecordFailure("WebSearch", "rate limited")
	ft.RecordFailure("WebSearch", "connection reset")
	ft.RecordFailure("WebSearch", "DNS resolution failed") // this error should be passed

	if capturedError != "DNS resolution failed" {
		t.Errorf("expected lastError 'DNS resolution failed', got %q", capturedError)
	}
}

func TestFailureTracker_OnPivot_SuccessResetsAndDoesNotFireNextFailure(t *testing.T) {
	ft := NewFailureTracker(3)

	callCount := 0
	ft.OnPivot = func(toolName string, failCount int, lastError string) {
		callCount++
	}

	// Reach threshold
	ft.RecordFailure("EditFile", "syntax error")
	ft.RecordFailure("EditFile", "syntax error")
	ft.RecordFailure("EditFile", "syntax error") // fires
	if callCount != 1 {
		t.Fatalf("expected 1 call after first threshold, got %d", callCount)
	}

	// Success resets
	ft.RecordSuccess("EditFile")

	// Next failure starts fresh — should not fire after just one
	ft.RecordFailure("EditFile", "new error")
	if callCount != 1 {
		t.Errorf("expected no additional OnPivot call after success reset, got %d total", callCount)
	}

	// Reach threshold again
	ft.RecordFailure("EditFile", "new error 2")
	ft.RecordFailure("EditFile", "new error 3") // fires again
	if callCount != 2 {
		t.Errorf("expected 2 total OnPivot calls after second threshold, got %d", callCount)
	}
}

func TestFailureTracker_OnPivot_MultipleTools(t *testing.T) {
	ft := NewFailureTracker(2) // threshold of 2 for brevity

	pivotedTools := make(map[string]string) // tool -> lastError
	ft.OnPivot = func(toolName string, failCount int, lastError string) {
		pivotedTools[toolName] = lastError
	}

	ft.RecordFailure("Bash", "exit 1")
	ft.RecordFailure("ReadFile", "not found")
	ft.RecordFailure("Bash", "exit 2")     // fires for Bash
	ft.RecordFailure("ReadFile", "EACCES") // fires for ReadFile

	if len(pivotedTools) != 2 {
		t.Errorf("expected 2 pivoted tools, got %d", len(pivotedTools))
	}
	if pivotedTools["Bash"] != "exit 2" {
		t.Errorf("expected Bash error 'exit 2', got %q", pivotedTools["Bash"])
	}
	if pivotedTools["ReadFile"] != "EACCES" {
		t.Errorf("expected ReadFile error 'EACCES', got %q", pivotedTools["ReadFile"])
	}
}
