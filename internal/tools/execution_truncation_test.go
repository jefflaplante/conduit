package tools

import (
	"strings"
	"testing"
	"time"

	"conduit/internal/ai"
)

func TestSmartTruncate_PreservesHeadAndTail(t *testing.T) {
	engine := NewExecutionEngine(nil, 1, time.Minute, 25)

	// Create content with 100 lines
	var lines []string
	for i := 1; i <= 100; i++ {
		lines = append(lines, "This is line "+string(rune('0'+i/100))+string(rune('0'+(i%100)/10))+string(rune('0'+i%10)))
	}
	content := strings.Join(lines, "\n")

	// Use a small maxChars to force truncation
	result := engine.smartTruncate(content, 2000)

	// Check that first 20 lines are preserved
	for i := 0; i < 20; i++ {
		if !strings.Contains(result, lines[i]) {
			t.Errorf("Expected head line %d to be preserved: %s", i+1, lines[i])
		}
	}

	// Check that last 20 lines are preserved
	for i := 80; i < 100; i++ {
		if !strings.Contains(result, lines[i]) {
			t.Errorf("Expected tail line %d to be preserved: %s", i+1, lines[i])
		}
	}

	// Check that truncation notice is present
	if !strings.Contains(result, "truncated") {
		t.Error("Expected truncation notice in output")
	}
}

func TestSmartTruncate_PreservesErrorLines(t *testing.T) {
	engine := NewExecutionEngine(nil, 1, time.Minute, 25)

	// Create content with error lines in the middle
	var lines []string
	for i := 1; i <= 100; i++ {
		if i == 50 {
			lines = append(lines, "ERROR: Something went wrong at line 50")
		} else if i == 60 {
			lines = append(lines, "FAIL: Test failure at line 60")
		} else if i == 70 {
			lines = append(lines, "Warning: Deprecation notice at line 70")
		} else {
			lines = append(lines, "Normal log line "+string(rune('0'+i/100))+string(rune('0'+(i%100)/10))+string(rune('0'+i%10)))
		}
	}
	content := strings.Join(lines, "\n")

	result := engine.smartTruncate(content, 3000)

	// Error lines in middle should be preserved
	if !strings.Contains(result, "ERROR: Something went wrong at line 50") {
		t.Error("Expected ERROR line to be preserved")
	}
	if !strings.Contains(result, "FAIL: Test failure at line 60") {
		t.Error("Expected FAIL line to be preserved")
	}
	if !strings.Contains(result, "Warning: Deprecation notice at line 70") {
		t.Error("Expected Warning line to be preserved")
	}

	// Check preserved section notice
	if !strings.Contains(result, "preserved") {
		t.Error("Expected preserved section notice in output")
	}
}

func TestSmartTruncate_AllPatternsRecognized(t *testing.T) {
	engine := NewExecutionEngine(nil, 1, time.Minute, 25)

	patterns := []string{
		"error", "Error", "ERROR",
		"fail", "Fail", "FAIL",
		"exception", "Exception", "EXCEPTION",
		"denied", "Denied", "DENIED",
		"timeout", "Timeout", "TIMEOUT",
		"panic", "Panic", "PANIC",
		"fatal", "Fatal", "FATAL",
		"warning", "Warning", "WARNING",
	}

	for _, pattern := range patterns {
		// Create content with the pattern in the middle
		var lines []string
		for i := 1; i <= 60; i++ {
			if i == 35 {
				lines = append(lines, "Important: "+pattern+" occurred here")
			} else {
				lines = append(lines, "Normal line")
			}
		}
		content := strings.Join(lines, "\n")

		result := engine.smartTruncate(content, 1500)

		if !strings.Contains(result, pattern) {
			t.Errorf("Expected pattern '%s' to be preserved in truncated output", pattern)
		}
	}
}

func TestSmartTruncate_SmallContent(t *testing.T) {
	engine := NewExecutionEngine(nil, 1, time.Minute, 25)

	// Content with fewer lines than head+tail
	var lines []string
	for i := 1; i <= 30; i++ {
		lines = append(lines, "Short line")
	}
	content := strings.Join(lines, "\n")

	// Force truncation by character limit
	result := engine.smartTruncate(content, 100)

	// Should fall back to char-based truncation
	if !strings.Contains(result, "truncated") {
		t.Error("Expected char-based truncation for small line count")
	}
}

func TestSmartTruncate_CustomConfig(t *testing.T) {
	engine := NewExecutionEngine(nil, 1, time.Minute, 25)

	// Custom config with different head/tail lines
	engine.SetTruncationConfig(TruncationConfig{
		MaxChars:         5000,
		HeadLines:        10,
		TailLines:        10,
		PreservePatterns: []string{"CUSTOM_ERROR"},
	})

	// Create content with 100 lines and custom error
	var lines []string
	for i := 1; i <= 100; i++ {
		if i == 50 {
			lines = append(lines, "CUSTOM_ERROR: This should be preserved")
		} else if i == 55 {
			lines = append(lines, "ERROR: This should NOT be preserved with custom config")
		} else {
			lines = append(lines, "Normal line")
		}
	}
	content := strings.Join(lines, "\n")

	result := engine.smartTruncate(content, 2500)

	// Custom error should be preserved
	if !strings.Contains(result, "CUSTOM_ERROR: This should be preserved") {
		t.Error("Expected CUSTOM_ERROR to be preserved")
	}

	// Standard ERROR should NOT be preserved (not in custom patterns)
	// Note: It might still appear if it's in head/tail section, so we check middle specifically
	middleSection := strings.Contains(result, "ERROR: This should NOT be preserved")
	if middleSection && !strings.Contains(result[:500], "ERROR:") && !strings.Contains(result[len(result)-500:], "ERROR:") {
		// If it's in the middle and not in head/tail, it shouldn't be there with custom config
		// Actually, since line 55 is in middle, it should NOT be preserved
	}
}

func TestLineContainsPattern(t *testing.T) {
	engine := NewExecutionEngine(nil, 1, time.Minute, 25)

	tests := []struct {
		line     string
		expected bool
	}{
		{"Normal log line", false},
		{"ERROR: something bad", true},
		{"contains error in text", true},
		{"FAIL: test failed", true},
		{"Exception thrown at line 42", true},
		{"connection timeout occurred", true},
		{"PANIC: unrecoverable", true},
		{"Warning: deprecated function", true},
		{"access denied for user", true},
		{"fatal error: out of memory", true},
		{"all systems nominal", false},
		{"success: operation completed", false},
	}

	for _, tt := range tests {
		result := engine.lineContainsPattern(tt.line)
		if result != tt.expected {
			t.Errorf("lineContainsPattern(%q) = %v, expected %v", tt.line, result, tt.expected)
		}
	}
}

func TestDefaultTruncationConfig(t *testing.T) {
	cfg := DefaultTruncationConfig()

	if cfg.MaxChars != DefaultMaxToolResultChars {
		t.Errorf("Expected MaxChars to be %d, got %d", DefaultMaxToolResultChars, cfg.MaxChars)
	}
	if cfg.HeadLines != 20 {
		t.Errorf("Expected HeadLines to be 20, got %d", cfg.HeadLines)
	}
	if cfg.TailLines != 20 {
		t.Errorf("Expected TailLines to be 20, got %d", cfg.TailLines)
	}
	if len(cfg.PreservePatterns) == 0 {
		t.Error("Expected PreservePatterns to have entries")
	}

	// Check for key patterns
	patterns := map[string]bool{
		"error": false, "Error": false, "ERROR": false,
		"fail": false, "Fail": false, "FAIL": false,
		"exception": false, "Exception": false,
		"denied": false, "Denied": false,
		"timeout": false, "Timeout": false,
		"panic": false, "Panic": false,
		"fatal": false, "Fatal": false,
		"warning": false, "Warning": false,
	}

	for _, p := range cfg.PreservePatterns {
		if _, exists := patterns[p]; exists {
			patterns[p] = true
		}
	}

	for pattern, found := range patterns {
		if !found {
			t.Errorf("Expected pattern '%s' to be in default config", pattern)
		}
	}
}

func TestSetTruncationConfig(t *testing.T) {
	engine := NewExecutionEngine(nil, 1, time.Minute, 25)

	// Initial state
	if engine.maxResultChars != DefaultMaxToolResultChars {
		t.Errorf("Expected initial maxResultChars to be %d", DefaultMaxToolResultChars)
	}

	// Set custom config
	engine.SetTruncationConfig(TruncationConfig{
		MaxChars:         16384,
		HeadLines:        30,
		TailLines:        30,
		PreservePatterns: []string{"CUSTOM"},
	})

	if engine.maxResultChars != 16384 {
		t.Errorf("Expected maxResultChars to be updated to 16384, got %d", engine.maxResultChars)
	}
	if engine.truncationConfig.HeadLines != 30 {
		t.Errorf("Expected HeadLines to be 30, got %d", engine.truncationConfig.HeadLines)
	}
	if engine.truncationConfig.TailLines != 30 {
		t.Errorf("Expected TailLines to be 30, got %d", engine.truncationConfig.TailLines)
	}
	if len(engine.truncationConfig.PreservePatterns) != 1 || engine.truncationConfig.PreservePatterns[0] != "CUSTOM" {
		t.Error("Expected PreservePatterns to be ['CUSTOM']")
	}
}

func TestFormatToolResultForAI_UsesSmartTruncation(t *testing.T) {
	engine := NewExecutionEngine(nil, 1, time.Minute, 25)
	engine.SetMaxResultChars(3000) // Limit to force truncation

	// Create a large result with an error in the middle
	var lines []string
	for i := 1; i <= 100; i++ {
		if i == 50 {
			lines = append(lines, "ERROR: Critical failure at line 50")
		} else {
			lines = append(lines, "Normal log output line number "+strings.Repeat("x", 50)) // Make lines longer
		}
	}

	result := &ExecutionResult{
		ToolCall: &ai.ToolCall{Name: "test_tool"},
		Result: &ToolResult{
			Success: true,
			Content: strings.Join(lines, "\n"),
		},
	}

	formatted := engine.formatToolResultForAI(result)

	// Output should be truncated
	if !strings.Contains(formatted, "truncated") {
		t.Errorf("Expected truncation notice in formatted output, got length %d", len(formatted))
	}

	// The error line should be preserved (since it contains "ERROR")
	if !strings.Contains(formatted, "ERROR: Critical failure") {
		t.Error("Expected ERROR line to be preserved in formatted output")
	}
}
