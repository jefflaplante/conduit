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
		pt.RecordCall("tool_a", nil)
		pt.RecordCall("tool_b", nil)

		if len(pt.recentCalls) != 2 {
			t.Errorf("expected 2 calls, got %d", len(pt.recentCalls))
		}
		if pt.recentCalls[0].toolName != "tool_a" || pt.recentCalls[1].toolName != "tool_b" {
			t.Errorf("unexpected calls: %v", pt.recentCalls)
		}
	})

	t.Run("trims to max history", func(t *testing.T) {
		pt := NewPatternTracker(3)
		pt.RecordCall("a", nil)
		pt.RecordCall("b", nil)
		pt.RecordCall("c", nil)
		pt.RecordCall("d", nil)

		if len(pt.recentCalls) != 3 {
			t.Errorf("expected 3 calls after trim, got %d", len(pt.recentCalls))
		}
		// Should have b, c, d (oldest dropped)
		if pt.recentCalls[0].toolName != "b" {
			t.Errorf("expected first call 'b', got %s", pt.recentCalls[0].toolName)
		}
	})

	t.Run("includes args in signature", func(t *testing.T) {
		pt := NewPatternTracker(10)
		pt.RecordCall("Bash", map[string]interface{}{"command": "ls"})
		pt.RecordCall("Bash", map[string]interface{}{"command": "make"})

		// Same tool name but different signatures
		if pt.recentCalls[0].toolName != "Bash" || pt.recentCalls[1].toolName != "Bash" {
			t.Error("tool names should both be Bash")
		}
		if pt.recentCalls[0].signature == pt.recentCalls[1].signature {
			t.Error("signatures should differ for different args")
		}
	})
}

func TestDetectCircular_TwoElementPattern(t *testing.T) {
	t.Run("detects A->B repeated 3 times", func(t *testing.T) {
		pt := NewPatternTracker(10)
		// A B A B A B
		calls := []string{"a", "b", "a", "b", "a", "b"}
		for _, c := range calls {
			pt.RecordCall(c, nil)
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
			pt.RecordCall(c, nil)
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
			pt.RecordCall(c, nil)
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
			pt.RecordCall(c, nil)
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
			pt.RecordCall(c, nil)
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
			pt.RecordCall(c, nil)
		}

		detected, _ := pt.DetectCircular()
		if detected {
			t.Error("should not detect pattern in varied calls")
		}
	})

	t.Run("no pattern with insufficient history", func(t *testing.T) {
		pt := NewPatternTracker(10)
		pt.RecordCall("a", nil)
		pt.RecordCall("b", nil)

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

func TestDetectCircular_SameToolDifferentArgs(t *testing.T) {
	t.Run("no detection when same tool has different args", func(t *testing.T) {
		pt := NewPatternTracker(10)
		// Bash with different commands - normal workflow, not a loop
		commands := []string{"ls", "make build", "make test", "git status", "make lint", "make build"}
		for _, cmd := range commands {
			pt.RecordCall("Bash", map[string]interface{}{"command": cmd})
		}

		detected, _ := pt.DetectCircular()
		if detected {
			t.Error("should not detect pattern when same tool has different args")
		}
	})

	t.Run("detects when same tool has identical args", func(t *testing.T) {
		pt := NewPatternTracker(10)
		// Same Bash command repeated - likely stuck in loop
		for i := 0; i < 6; i++ {
			pt.RecordCall("Bash", map[string]interface{}{"command": "make test"})
		}

		detected, pattern := pt.DetectCircular()
		if !detected {
			t.Error("expected to detect same tool with same args repeated")
		}
		if pattern != "Bash -> Bash" {
			t.Errorf("expected 'Bash -> Bash', got '%s'", pattern)
		}
	})

	t.Run("detects alternating pattern with args", func(t *testing.T) {
		pt := NewPatternTracker(10)
		// read -> edit -> read -> edit with same args each time
		for i := 0; i < 3; i++ {
			pt.RecordCall("Read", map[string]interface{}{"path": "/foo.go"})
			pt.RecordCall("Edit", map[string]interface{}{"path": "/foo.go", "old": "x", "new": "y"})
		}

		detected, pattern := pt.DetectCircular()
		if !detected {
			t.Error("expected to detect alternating pattern")
		}
		if pattern != "Read -> Edit" {
			t.Errorf("expected 'Read -> Edit', got '%s'", pattern)
		}
	})

	t.Run("no detection when alternating args differ", func(t *testing.T) {
		pt := NewPatternTracker(10)
		// read -> edit but editing different things each time
		paths := []string{"/a.go", "/b.go", "/c.go"}
		for _, p := range paths {
			pt.RecordCall("Read", map[string]interface{}{"path": p})
			pt.RecordCall("Edit", map[string]interface{}{"path": p, "old": "x", "new": "y"})
		}

		detected, _ := pt.DetectCircular()
		if detected {
			t.Error("should not detect when args differ each iteration")
		}
	})
}

func TestReset(t *testing.T) {
	pt := NewPatternTracker(10)
	pt.RecordCall("a", nil)
	pt.RecordCall("b", nil)
	pt.RecordCall("c", nil)

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

func TestInjectThinkStep(t *testing.T) {
	msg := InjectThinkStep("Bash -> Bash")
	if msg == "" {
		t.Error("expected non-empty message")
	}
	// Check it mentions the pattern
	if !patternContains(msg, "Bash -> Bash") {
		t.Error("think step should contain the pattern")
	}
	// Check it has directive language
	if !patternContains(msg, "STOP") {
		t.Error("think step should start with STOP")
	}
	if !patternContains(msg, "MUST") {
		t.Error("think step should have directive language")
	}
	// Check it asks for reflection
	if !patternContains(msg, "trying to accomplish") {
		t.Error("think step should ask what LLM is trying to accomplish")
	}
	if !patternContains(msg, "DIFFERENT approach") {
		t.Error("think step should ask for different approach")
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
			pt.RecordCall(c, nil)
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
			pt.RecordCall(c, nil)
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

func TestOnCircularCallback(t *testing.T) {
	t.Run("fires when circular pattern detected", func(t *testing.T) {
		pt := NewPatternTracker(10)

		var gotPattern, gotSigHash string
		callCount := 0
		pt.OnCircular = func(pattern string, signatureHash string) {
			callCount++
			gotPattern = pattern
			gotSigHash = signatureHash
		}

		// A B A B A B — triggers 2-element circular detection
		calls := []string{"a", "b", "a", "b", "a", "b"}
		for _, c := range calls {
			pt.RecordCall(c, nil)
		}

		detected, pattern := pt.DetectCircular()
		if !detected {
			t.Fatal("expected circular pattern to be detected")
		}
		if callCount != 1 {
			t.Errorf("expected OnCircular called once, got %d", callCount)
		}
		if gotPattern != pattern {
			t.Errorf("callback pattern %q should match returned pattern %q", gotPattern, pattern)
		}
		if gotSigHash == "" {
			t.Error("expected non-empty signature hash")
		}
	})

	t.Run("not called when no pattern detected", func(t *testing.T) {
		pt := NewPatternTracker(10)

		callCount := 0
		pt.OnCircular = func(pattern string, signatureHash string) {
			callCount++
		}

		// Varied calls — no repeating pattern
		calls := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
		for _, c := range calls {
			pt.RecordCall(c, nil)
		}

		detected, _ := pt.DetectCircular()
		if detected {
			t.Fatal("should not detect pattern in varied calls")
		}
		if callCount != 0 {
			t.Errorf("expected OnCircular not called, got %d calls", callCount)
		}
	})

	t.Run("nil callback does not panic", func(t *testing.T) {
		pt := NewPatternTracker(10)
		// OnCircular is nil by default (not set)

		calls := []string{"a", "b", "a", "b", "a", "b"}
		for _, c := range calls {
			pt.RecordCall(c, nil)
		}

		// Should not panic
		detected, pattern := pt.DetectCircular()
		if !detected {
			t.Fatal("expected circular pattern to be detected")
		}
		if pattern != "a -> b" {
			t.Errorf("expected 'a -> b', got '%s'", pattern)
		}
	})

	t.Run("signature hash is stable for same pattern", func(t *testing.T) {
		var hashes []string

		for i := 0; i < 3; i++ {
			pt := NewPatternTracker(10)
			pt.OnCircular = func(_ string, sigHash string) {
				hashes = append(hashes, sigHash)
			}

			calls := []string{"Read", "Edit", "Read", "Edit", "Read", "Edit"}
			for _, c := range calls {
				pt.RecordCall(c, nil)
			}
			pt.DetectCircular()
		}

		if len(hashes) != 3 {
			t.Fatalf("expected 3 hashes, got %d", len(hashes))
		}
		if hashes[0] != hashes[1] || hashes[1] != hashes[2] {
			t.Errorf("expected stable hashes, got %v", hashes)
		}
	})

	t.Run("different patterns produce different signature hashes", func(t *testing.T) {
		var hash1, hash2 string

		pt1 := NewPatternTracker(10)
		pt1.OnCircular = func(_ string, sigHash string) {
			hash1 = sigHash
		}
		for i := 0; i < 6; i += 2 {
			pt1.RecordCall("a", nil)
			pt1.RecordCall("b", nil)
		}
		pt1.DetectCircular()

		pt2 := NewPatternTracker(10)
		pt2.OnCircular = func(_ string, sigHash string) {
			hash2 = sigHash
		}
		for i := 0; i < 6; i += 2 {
			pt2.RecordCall("x", nil)
			pt2.RecordCall("y", nil)
		}
		pt2.DetectCircular()

		if hash1 == "" || hash2 == "" {
			t.Fatal("expected non-empty hashes")
		}
		if hash1 == hash2 {
			t.Error("different patterns should produce different signature hashes")
		}
	})

	t.Run("fires for 3-element pattern", func(t *testing.T) {
		pt := NewPatternTracker(15)

		var gotPattern string
		callCount := 0
		pt.OnCircular = func(pattern string, signatureHash string) {
			callCount++
			gotPattern = pattern
		}

		// A B C A B C A B C — 3-element pattern repeated 3 times
		calls := []string{"a", "b", "c", "a", "b", "c", "a", "b", "c"}
		for _, c := range calls {
			pt.RecordCall(c, nil)
		}

		detected, _ := pt.DetectCircular()
		if !detected {
			t.Fatal("expected 3-element circular pattern to be detected")
		}
		if callCount != 1 {
			t.Errorf("expected OnCircular called once, got %d", callCount)
		}
		if gotPattern != "a -> b -> c" {
			t.Errorf("expected 'a -> b -> c', got '%s'", gotPattern)
		}
	})

	t.Run("not called with insufficient history", func(t *testing.T) {
		pt := NewPatternTracker(10)

		callCount := 0
		pt.OnCircular = func(pattern string, signatureHash string) {
			callCount++
		}

		pt.RecordCall("a", nil)
		pt.RecordCall("b", nil)

		pt.DetectCircular()
		if callCount != 0 {
			t.Errorf("expected OnCircular not called with insufficient history, got %d calls", callCount)
		}
	})
}

func TestComputeSignature(t *testing.T) {
	pt := NewPatternTracker(10)

	t.Run("no args returns just tool name", func(t *testing.T) {
		sig := pt.computeSignature("Bash", nil)
		if sig != "Bash" {
			t.Errorf("expected 'Bash', got '%s'", sig)
		}
	})

	t.Run("empty args returns just tool name", func(t *testing.T) {
		sig := pt.computeSignature("Bash", map[string]interface{}{})
		if sig != "Bash" {
			t.Errorf("expected 'Bash', got '%s'", sig)
		}
	})

	t.Run("same args produce same signature", func(t *testing.T) {
		args := map[string]interface{}{"command": "ls -la"}
		sig1 := pt.computeSignature("Bash", args)
		sig2 := pt.computeSignature("Bash", args)
		if sig1 != sig2 {
			t.Errorf("same args should produce same signature: %s vs %s", sig1, sig2)
		}
	})

	t.Run("different args produce different signature", func(t *testing.T) {
		sig1 := pt.computeSignature("Bash", map[string]interface{}{"command": "ls"})
		sig2 := pt.computeSignature("Bash", map[string]interface{}{"command": "pwd"})
		if sig1 == sig2 {
			t.Error("different args should produce different signatures")
		}
	})

	t.Run("different tools same args produce different signature", func(t *testing.T) {
		args := map[string]interface{}{"path": "/foo"}
		sig1 := pt.computeSignature("Read", args)
		sig2 := pt.computeSignature("Write", args)
		if sig1 == sig2 {
			t.Error("different tools should produce different signatures")
		}
	})
}
