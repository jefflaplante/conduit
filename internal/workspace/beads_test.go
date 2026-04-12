package workspace

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestParseBeadsJSON_EmptyArray(t *testing.T) {
	result, err := ParseBeadsJSON([]byte("[]"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Count != 0 {
		t.Errorf("expected 0 count, got %d", result.Count)
	}
	if result.Summary != "" {
		t.Errorf("expected empty summary, got %q", result.Summary)
	}
}

func TestParseBeadsJSON_EmptyInput(t *testing.T) {
	result, err := ParseBeadsJSON([]byte(""))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Count != 0 {
		t.Errorf("expected 0 count, got %d", result.Count)
	}
}

func TestParseBeadsJSON_InvalidJSON(t *testing.T) {
	_, err := ParseBeadsJSON([]byte(`[{"bad json}]`))
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestParseBeadsJSON_SingleEntry(t *testing.T) {
	input := `[{
		"id": "conduit-abc1",
		"title": "Fix auth bug",
		"status": "in_progress",
		"priority": 1
	}]`

	result, err := ParseBeadsJSON([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Count != 1 {
		t.Errorf("expected 1 count, got %d", result.Count)
	}
	if !strings.Contains(result.Summary, "1 active tasks") {
		t.Errorf("expected '1 active tasks' in summary, got %q", result.Summary)
	}
	if !strings.Contains(result.Summary, "Fix auth bug") {
		t.Errorf("expected title in summary, got %q", result.Summary)
	}
	if !strings.Contains(result.Summary, "P1") {
		t.Errorf("expected P1 in summary, got %q", result.Summary)
	}
	if !strings.Contains(result.Summary, "in_progress") {
		t.Errorf("expected in_progress in summary, got %q", result.Summary)
	}
}

func TestParseBeadsJSON_MultipleEntries(t *testing.T) {
	input := `[
		{"id": "a", "title": "Low priority task", "status": "open", "priority": 3},
		{"id": "b", "title": "Critical fix", "status": "in_progress", "priority": 0},
		{"id": "c", "title": "Medium task", "status": "open", "priority": 2}
	]`

	result, err := ParseBeadsJSON([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Count != 3 {
		t.Errorf("expected 3 count, got %d", result.Count)
	}

	// Verify sorting: P0 should come first
	if result.Entries[0].Priority != 0 {
		t.Errorf("expected first entry to be P0, got P%d", result.Entries[0].Priority)
	}
	if result.Entries[0].Title != "Critical fix" {
		t.Errorf("expected first entry to be 'Critical fix', got %q", result.Entries[0].Title)
	}
}

func TestParseBeadsJSON_SortOrder(t *testing.T) {
	input := `[
		{"id": "a", "title": "Open P1", "status": "open", "priority": 1},
		{"id": "b", "title": "In-progress P1", "status": "in_progress", "priority": 1},
		{"id": "c", "title": "Open P0", "status": "open", "priority": 0}
	]`

	result, err := ParseBeadsJSON([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// P0 first, then P1 in_progress, then P1 open
	expected := []string{"Open P0", "In-progress P1", "Open P1"}
	for i, e := range result.Entries {
		if e.Title != expected[i] {
			t.Errorf("entry[%d]: expected %q, got %q", i, expected[i], e.Title)
		}
	}
}

func TestParseBeadsJSON_TruncatesLongTitles(t *testing.T) {
	longTitle := strings.Repeat("x", 80)
	input := fmt.Sprintf(`[{"id": "a", "title": "%s", "status": "open", "priority": 2}]`, longTitle)

	result, err := ParseBeadsJSON([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Title in summary should be truncated to 60 chars (57 + "...")
	if strings.Contains(result.Summary, longTitle) {
		t.Error("expected title to be truncated in summary")
	}
	if !strings.Contains(result.Summary, "...") {
		t.Error("expected '...' in truncated title")
	}
}

func TestParseBeadsJSON_TruncatesMoreThan10(t *testing.T) {
	var entries []string
	for i := 0; i < 15; i++ {
		entries = append(entries, fmt.Sprintf(`{"id": "id-%d", "title": "Task %d", "status": "open", "priority": 2}`, i, i))
	}
	input := "[" + strings.Join(entries, ",") + "]"

	result, err := ParseBeadsJSON([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Count != 15 {
		t.Errorf("expected 15 count, got %d", result.Count)
	}
	if !strings.Contains(result.Summary, "15 active tasks") {
		t.Errorf("expected '15 active tasks' in summary, got %q", result.Summary)
	}
	if !strings.Contains(result.Summary, "(+5 more)") {
		t.Errorf("expected '(+5 more)' in summary, got %q", result.Summary)
	}
}

func TestParseBeadsJSON_WithLeadingGarbage(t *testing.T) {
	// br might output warnings before the JSON array
	input := `some warning text
[{"id": "a", "title": "Test task", "status": "open", "priority": 1}]`

	result, err := ParseBeadsJSON([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Count != 1 {
		t.Errorf("expected 1 count, got %d", result.Count)
	}
}

func TestFormatBeadsSummary_EmptySlice(t *testing.T) {
	result := FormatBeadsSummary(nil)
	if result.Count != 0 {
		t.Errorf("expected 0 count, got %d", result.Count)
	}
	if result.Summary != "" {
		t.Errorf("expected empty summary, got %q", result.Summary)
	}
}

func TestPriorityLabel(t *testing.T) {
	tests := []struct {
		input    int
		expected string
	}{
		{0, "P0"},
		{1, "P1"},
		{2, "P2"},
		{3, "P3"},
		{4, "P4"},
		{5, "P5"},
	}
	for _, tt := range tests {
		if got := priorityLabel(tt.input); got != tt.expected {
			t.Errorf("priorityLabel(%d) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestRefreshBeadsBrainEntry_BrNotFound(t *testing.T) {
	// RefreshBeadsBrainEntry should not panic when br is not available.
	// We can't easily test this without mocking exec.LookPath, but we can
	// verify the callback is never called when there's an error.
	called := false
	brainStore := func(ctx context.Context, key, value, source string) error {
		called = true
		return nil
	}

	// This test relies on the fact that if br IS available, it will call brainStore.
	// If br is NOT available, it logs and returns without calling brainStore.
	// Either way, it should not panic.
	ctx := context.Background()
	RefreshBeadsBrainEntry(ctx, brainStore)

	// We can't assert on `called` since it depends on environment.
	// The test just verifies no panic.
	_ = called
}

func TestParseBeadsJSON_ExtraFields(t *testing.T) {
	// br outputs many more fields than we need - verify we ignore them
	input := `[{
		"id": "conduit-abc1",
		"title": "Fix auth bug",
		"description": "Lots of details here",
		"status": "in_progress",
		"priority": 1,
		"issue_type": "task",
		"created_at": "2026-04-02T21:39:05.959800Z",
		"created_by": "jeff",
		"updated_at": "2026-04-04T15:42:01.298101Z",
		"source_repo": ".",
		"compaction_level": 0,
		"original_size": 0,
		"dependency_count": 0,
		"dependent_count": 0
	}]`

	result, err := ParseBeadsJSON([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Count != 1 {
		t.Errorf("expected 1 count, got %d", result.Count)
	}
	if result.Entries[0].Title != "Fix auth bug" {
		t.Errorf("expected 'Fix auth bug', got %q", result.Entries[0].Title)
	}
}
