package tools

import (
	"testing"
)

func TestNewPatternTracker(t *testing.T) {
	t.Run("default max history", func(t *testing.T) {
		pt := NewPatternTracker(0)
		if pt.maxHistory != 10 {
			t.Errorf("expected default maxHistory=10, got %d", pt.maxHistory)
		}
	})

	t.Run("custom max history", func(t *testing.T) {
		pt := NewPatternTracker(20)
		if pt.maxHistory != 20 {
			t.Errorf("expected maxHistory=20, got %d", pt.maxHistory)
		}
	})

	t.Run("negative max history defaults to 10", func(t *testing.T) {
		pt := NewPatternTracker(-5)
		if pt.maxHistory != 10 {
			t.Errorf("expected default maxHistory=10, got %d", pt.maxHistory)
		}
	})
}

func TestRecordCall(t *testing.T) {
	t.Run("adds calls to history", func(t *testing.T) {
		pt := NewPatternTracker(10)
		pt.RecordCall("tool_a")
		pt.RecordCall("tool_b")

		if len(pt.recentCalls) != 2 {
			t.Errorf("expected 2 calls, got %d", len(pt.recentCalls))
		}
		if pt.recentCalls[0] != "tool_a" || pt.recentCalls[1] != "tool_b" {
			t.Errorf("unexpected calls: %v", pt.recentCalls)
		}
	})

	t.Run("trims to max history", func(t *testing.T) {
		pt := NewPatternTracker(3)
		pt.RecordCall("a")
		pt.RecordCall("b")
		pt.RecordCall("c")
		pt.RecordCall("d")

		if len(pt.recentCalls) != 3 {
			t.Errorf("expected 3 calls after trim, got %d", len(pt.recentCalls))
		}
		// Should have b, c, d (oldest dropped)
		if pt.recentCalls[0] != "b" {
			t.Errorf("expected first call 'b', got %s", pt.recentCalls[0])
		}
	})
}

func TestDetectCircular_TwoElementPattern(t *testing.T) {
	t.Run("detects A->B repeated 3 times", func(t *testing.T) {
		pt := NewPatternTracker(10)
		// A B A B A B
		calls := []string{"a", "b", "a", "b", "a", "b"}
		for _, c := range calls {
			pt.RecordCall(c)
		}

		detected, pattern := pt.DetectCircular()
		if !detected {
			t.Error("expected circular pattern to be detected")
		}
		if pattern != "a -> b" {
			t.Errorf("expected pattern 'a -> b', got '%s'", pattern)
		}
	})

	t.Run("no detection with only 2 repetitions", func(t *testing.T) {
		pt := NewPatternTracker(10)
		// A B A B (only 2 repetitions)
		calls := []string{"a", "b", "a", "b"}
		for _, c := range calls {
			pt.RecordCall(c)
		}

		detected, _ := pt.DetectCircular()
		if detected {
			t.Error("should not detect with only 2 repetitions")
		}
	})

	t.Run("detects pattern after other calls", func(t *testing.T) {
		pt := NewPatternTracker(10)
		// x y A B A B A B
		calls := []string{"x", "y", "a", "b", "a", "b", "a", "b"}
		for _, c := range calls {
			pt.RecordCall(c)
		}

		detected, pattern := pt.DetectCircular()
		if !detected {
			t.Error("expected circular pattern to be detected")
		}
		if pattern != "a -> b" {
			t.Errorf("expected pattern 'a -> b', got '%s'", pattern)
		}
	})
}

func TestDetectCircular_ThreeElementPattern(t *testing.T) {
	t.Run("detects A->B->C repeated 3 times", func(t *testing.T) {
		pt := NewPatternTracker(15)
		// A B C A B C A B C
		calls := []string{"a", "b", "c", "a", "b", "c", "a", "b", "c"}
		for _, c := range calls {
			pt.RecordCall(c)
		}

		detected, pattern := pt.DetectCircular()
		if !detected {
			t.Error("expected circular pattern to be detected")
		}
		if pattern != "a -> b -> c" {
			t.Errorf("expected pattern 'a -> b -> c', got '%s'", pattern)
		}
	})

	t.Run("no detection with only 2 repetitions", func(t *testing.T) {
		pt := NewPatternTracker(10)
		// A B C A B C (only 2 repetitions)
		calls := []string{"a", "b", "c", "a", "b", "c"}
		for _, c := range calls {
			pt.RecordCall(c)
		}

		detected, _ := pt.DetectCircular()
		if detected {
			t.Error("should not detect 3-element pattern with only 2 repetitions")
		}
	})
}

func TestDetectCircular_NoPattern(t *testing.T) {
	t.Run("no pattern with varied calls", func(t *testing.T) {
		pt := NewPatternTracker(10)
		calls := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
		for _, c := range calls {
			pt.RecordCall(c)
		}

		detected, _ := pt.DetectCircular()
		if detected {
			t.Error("should not detect pattern in varied calls")
		}
	})

	t.Run("no pattern with insufficient history", func(t *testing.T) {
		pt := NewPatternTracker(10)
		pt.RecordCall("a")
		pt.RecordCall("b")

		detected, _ := pt.DetectCircular()
		if detected {
			t.Error("should not detect pattern with < 6 calls")
		}
	})

	t.Run("empty history", func(t *testing.T) {
		pt := NewPatternTracker(10)

		detected, _ := pt.DetectCircular()
		if detected {
			t.Error("should not detect pattern with empty history")
		}
	})
}

func TestReset(t *testing.T) {
	pt := NewPatternTracker(10)
	pt.RecordCall("a")
	pt.RecordCall("b")
	pt.RecordCall("c")

	pt.Reset()

	if len(pt.recentCalls) != 0 {
		t.Errorf("expected empty history after reset, got %d calls", len(pt.recentCalls))
	}
}

func TestInjectWarning(t *testing.T) {
	warning := InjectWarning("a -> b")
	if warning == "" {
		t.Error("expected non-empty warning")
	}
	// Check it mentions the pattern
	if !patternContains(warning, "a -> b") {
		t.Error("warning should contain the pattern")
	}
}

func patternContains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && patternContainsHelper(s, substr))
}

func patternContainsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestDetectCircular_RealWorldScenarios(t *testing.T) {
	t.Run("read file then edit file loop", func(t *testing.T) {
		pt := NewPatternTracker(10)
		// Simulates: read -> edit -> read -> edit -> read -> edit
		calls := []string{"read_file", "edit_file", "read_file", "edit_file", "read_file", "edit_file"}
		for _, c := range calls {
			pt.RecordCall(c)
		}

		detected, pattern := pt.DetectCircular()
		if !detected {
			t.Error("expected to detect read/edit loop")
		}
		if pattern != "read_file -> edit_file" {
			t.Errorf("expected 'read_file -> edit_file', got '%s'", pattern)
		}
	})

	t.Run("search validate search loop", func(t *testing.T) {
		pt := NewPatternTracker(15)
		// Simulates: search -> validate -> fix -> search -> validate -> fix -> search -> validate -> fix
		calls := []string{
			"web_search", "validate", "fix",
			"web_search", "validate", "fix",
			"web_search", "validate", "fix",
		}
		for _, c := range calls {
			pt.RecordCall(c)
		}

		detected, pattern := pt.DetectCircular()
		if !detected {
			t.Error("expected to detect 3-element loop")
		}
		if pattern != "web_search -> validate -> fix" {
			t.Errorf("expected 'web_search -> validate -> fix', got '%s'", pattern)
		}
	})
}
