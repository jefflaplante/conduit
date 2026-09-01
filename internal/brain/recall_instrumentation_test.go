package brain

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withTempEventsFile redirects recallEventsPath to a temp dir for the test's
// duration and restores it after.
func withTempEventsFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "recall-events.jsonl")
	orig := recallEventsPath
	recallEventsPath = path
	t.Cleanup(func() { recallEventsPath = orig })
	return path
}

func readEvents(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Failed to read events file: %v", err)
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return nil
	}
	return strings.Split(content, "\n")
}

func TestLogRecallEvent(t *testing.T) {
	path := withTempEventsFile(t)

	b, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create brain: %v", err)
	}
	defer b.Close()

	b.logRecallEvent("test query", []*Entry{
		{Key: "test.key1", Tier: TierLongTerm},
		{Key: "test.key2", Tier: TierLongTerm},
	})

	lines := readEvents(t, path)
	if len(lines) != 1 {
		t.Fatalf("Expected 1 line, got %d", len(lines))
	}

	var event struct {
		Timestamp string   `json:"ts"`
		Query     string   `json:"query"`
		Keys      []string `json:"keys"`
		Tiers     []string `json:"tiers"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &event); err != nil {
		t.Fatalf("Failed to unmarshal event: %v", err)
	}

	if event.Query != "test query" {
		t.Errorf("Expected query 'test query', got '%s'", event.Query)
	}
	if len(event.Keys) != 2 || len(event.Tiers) != 2 {
		t.Errorf("Expected 2 keys/tiers, got %d/%d", len(event.Keys), len(event.Tiers))
	}
	if event.Keys[0] != "test.key1" || event.Keys[1] != "test.key2" {
		t.Errorf("Expected keys [test.key1, test.key2], got %v", event.Keys)
	}
}

func TestLogRecallEventBestEffort(t *testing.T) {
	// nil/empty results write nothing; unwritable path must not panic
	path := withTempEventsFile(t)

	b, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create brain: %v", err)
	}
	defer b.Close()

	b.logRecallEvent("empty query", nil)
	b.logRecallEvent("empty query", []*Entry{})

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("Expected no events file after empty results")
	}

	// Unwritable path — must not panic, must not write
	recallEventsPath = filepath.Join(t.TempDir(), "nonexistent-dir", "events.jsonl")
	b.logRecallEvent("test query", []*Entry{
		{Key: "test.key1", Tier: TierLongTerm},
	})
}

func TestRecallWithInstrumentation(t *testing.T) {
	path := withTempEventsFile(t)

	b, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create brain: %v", err)
	}
	defer b.Close()

	ctx := context.Background()
	if err := b.Store(ctx, "solar.panel_count", "30", TierLongTerm, "test"); err != nil {
		t.Fatalf("Failed to store entry: %v", err)
	}
	if err := b.Store(ctx, "solar.battery", "14.6kWh", TierLongTerm, "test"); err != nil {
		t.Fatalf("Failed to store entry: %v", err)
	}

	results, err := b.Recall(ctx, "solar", 10)
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("Expected at least one result from recall")
	}

	lines := readEvents(t, path)
	if len(lines) == 0 {
		t.Fatal("Expected at least one event line")
	}

	var event struct {
		Timestamp string   `json:"ts"`
		Query     string   `json:"query"`
		Keys      []string `json:"keys"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &event); err != nil {
		t.Fatalf("Failed to unmarshal event: %v", err)
	}
	if event.Query != "solar" {
		t.Errorf("Expected query 'solar', got '%s'", event.Query)
	}
	if _, err := time.Parse(time.RFC3339, event.Timestamp); err != nil {
		t.Errorf("Invalid timestamp format: %v", err)
	}
}

func TestRecallEventTimestampFormat(t *testing.T) {
	path := withTempEventsFile(t)

	b, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create brain: %v", err)
	}
	defer b.Close()

	ctx := context.Background()
	if err := b.Store(ctx, "test.key", "value", TierLongTerm, "test"); err != nil {
		t.Fatalf("Failed to store entry: %v", err)
	}

	if _, err := b.Recall(ctx, "test", 10); err != nil {
		t.Fatalf("Recall failed: %v", err)
	}

	lines := readEvents(t, path)
	if len(lines) == 0 {
		t.Fatal("Expected at least one event line")
	}

	var event struct {
		Timestamp string `json:"ts"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &event); err != nil {
		t.Fatalf("Failed to unmarshal event: %v", err)
	}

	if _, err := time.Parse(time.RFC3339, event.Timestamp); err != nil {
		t.Errorf("Timestamp not in ISO8601/RFC3339 format: %s (error: %v)", event.Timestamp, err)
	}
}
