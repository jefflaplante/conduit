package gateway

import (
	"strings"
	"testing"

	"conduit/internal/ai"
	"conduit/internal/sessions"
)

func TestFormatStatusResponse_NoCostData(t *testing.T) {
	session := &sessions.Session{
		Key:    "test-session-123",
		UserID: "jeff",
		Context: map[string]string{
			"model": "claude-sonnet-4-20250514",
		},
	}

	result := formatStatusResponse(session, 10, nil)

	if !strings.Contains(result, "Session Status") {
		t.Error("Expected 'Session Status' header")
	}
	if !strings.Contains(result, "test-session-123") {
		t.Error("Expected session key in output")
	}
	if !strings.Contains(result, "Messages: 10") {
		t.Error("Expected message count")
	}
	if !strings.Contains(result, "jeff") {
		t.Error("Expected user ID")
	}
	// Should NOT contain cost section when no cost data
	if strings.Contains(result, "Session Cost") {
		t.Error("Should not show cost section when no cost data")
	}
}

func TestFormatStatusResponse_WithCostData(t *testing.T) {
	session := &sessions.Session{
		Key:    "test-session-456",
		UserID: "jeff",
		Context: map[string]string{
			"model":                  "claude-sonnet-4-20250514",
			"session_total_cost":     "0.052300",
			"session_request_count":  "12",
			"last_prompt_tokens":     "8421",
			"last_completion_tokens": "500",
			"last_total_tokens":      "8921",
		},
	}

	result := formatStatusResponse(session, 42, nil)

	if !strings.Contains(result, "Session Cost") {
		t.Error("Expected 'Session Cost' section")
	}
	if !strings.Contains(result, "Requests: 12") {
		t.Error("Expected request count")
	}
	if !strings.Contains(result, "$0.0523") {
		t.Error("Expected formatted cost")
	}
	if !strings.Contains(result, "Context Window Usage") {
		t.Error("Expected context window usage section")
	}
}

func TestFormatStatusResponse_WithUsageTracker(t *testing.T) {
	session := &sessions.Session{
		Key:    "test-session-789",
		UserID: "jeff",
		Context: map[string]string{
			"model":              "claude-sonnet-4-20250514",
			"session_total_cost": "0.050000",
		},
	}

	tracker := ai.NewUsageTracker()
	tracker.RecordUsage("anthropic", "claude-sonnet-4-20250514", 1000, 500, 1200)

	result := formatStatusResponse(session, 5, tracker)

	if !strings.Contains(result, "Global Usage") {
		t.Error("Expected 'Global Usage' section")
	}
	if !strings.Contains(result, "anthropic") {
		t.Error("Expected 'anthropic' provider in global usage")
	}
}

func TestFormatStatusResponse_DefaultModel(t *testing.T) {
	session := &sessions.Session{
		Key:     "test-session-default",
		UserID:  "jeff",
		Context: map[string]string{},
	}

	result := formatStatusResponse(session, 0, nil)

	if !strings.Contains(result, "sonnet (default)") {
		t.Error("Expected default model display")
	}
}

func TestFormatNumber(t *testing.T) {
	tests := []struct {
		input    int
		expected string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1,000"},
		{84521, "84,521"},
		{1234567, "1,234,567"},
		{-42, "-42"},
	}

	for _, tt := range tests {
		result := formatNumber(tt.input)
		if result != tt.expected {
			t.Errorf("formatNumber(%d) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}
