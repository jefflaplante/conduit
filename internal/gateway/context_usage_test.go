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
	tracker.RecordUsage("anthropic", "claude-sonnet-4-20250514", 1000, 500, 0, 0, 1200)

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

func TestContextWarningIfNeeded_NoWarningUnderThreshold(t *testing.T) {
	session := &sessions.Session{
		Key:     "test-warn-1",
		UserID:  "jeff",
		Context: map[string]string{},
	}

	// 50% of 200k = 100k tokens — under 60% threshold
	warning := contextWarningIfNeeded(session, 100000, "claude-sonnet-4-20250514")
	if warning.Text != "" {
		t.Errorf("Expected no warning at 50%%, got: %s", warning.Text)
	}
}

func TestContextWarningIfNeeded_WarningAt60Pct(t *testing.T) {
	session := &sessions.Session{
		Key:     "test-warn-2",
		UserID:  "jeff",
		Context: map[string]string{},
	}

	// 65% of 200k = 130k tokens
	warning := contextWarningIfNeeded(session, 130000, "claude-sonnet-4-20250514")
	if warning.Text == "" {
		t.Error("Expected warning at 65%")
	}
	if !strings.Contains(warning.Text, "🟡") {
		t.Error("Expected yellow warning icon at 60% threshold")
	}
	if !strings.Contains(warning.Text, "60%") {
		t.Error("Expected '60%' mention in warning")
	}
	if warning.Key != "context_warned_60" {
		t.Errorf("Expected key 'context_warned_60', got %q", warning.Key)
	}
}

func TestContextWarningIfNeeded_WarningAt80Pct(t *testing.T) {
	session := &sessions.Session{
		Key:     "test-warn-3",
		UserID:  "jeff",
		Context: map[string]string{},
	}

	// 85% of 200k = 170k tokens
	warning := contextWarningIfNeeded(session, 170000, "claude-sonnet-4-20250514")
	if warning.Text == "" {
		t.Error("Expected warning at 85%")
	}
	if !strings.Contains(warning.Text, "🔴") {
		t.Error("Expected red warning icon at 80% threshold")
	}
	if !strings.Contains(warning.Text, "80%") {
		t.Error("Expected '80%' mention in warning")
	}
	if warning.Key != "context_warned_80" {
		t.Errorf("Expected key 'context_warned_80', got %q", warning.Key)
	}
}

func TestContextWarningIfNeeded_NoRepeatWarning(t *testing.T) {
	session := &sessions.Session{
		Key:    "test-warn-4",
		UserID: "jeff",
		Context: map[string]string{
			"context_warned_60": "true",
		},
	}

	// 65% again, but already warned
	warning := contextWarningIfNeeded(session, 130000, "claude-sonnet-4-20250514")
	if warning.Text != "" {
		t.Errorf("Should not repeat 60%% warning, got: %s", warning.Text)
	}
}

func TestContextWarningIfNeeded_EscalatesFrom60To80(t *testing.T) {
	session := &sessions.Session{
		Key:    "test-warn-5",
		UserID: "jeff",
		Context: map[string]string{
			"context_warned_60": "true",
		},
	}

	// 85% — already warned at 60, should still fire at 80
	warning := contextWarningIfNeeded(session, 170000, "claude-sonnet-4-20250514")
	if warning.Text == "" {
		t.Error("Expected 80% warning even though 60% was already sent")
	}
	if !strings.Contains(warning.Text, "🔴") {
		t.Error("Expected red warning icon for 80% escalation")
	}
}

func TestContextWarningIfNeeded_NilSession(t *testing.T) {
	warning := contextWarningIfNeeded(nil, 130000, "claude-sonnet-4-20250514")
	if warning.Text != "" {
		t.Error("Expected no warning for nil session")
	}
}

func TestContextWarningIfNeeded_ZeroTokens(t *testing.T) {
	session := &sessions.Session{
		Key:     "test-warn-6",
		UserID:  "jeff",
		Context: map[string]string{},
	}

	warning := contextWarningIfNeeded(session, 0, "claude-sonnet-4-20250514")
	if warning.Text != "" {
		t.Error("Expected no warning for zero tokens")
	}
}
