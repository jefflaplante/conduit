package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeEvents(t *testing.T, eventsFile string, events []string) {
	t.Helper()
	if err := os.WriteFile(eventsFile, []byte(strings.Join(events, "\n")+"\n"), 0644); err != nil {
		t.Fatalf("Failed to write test events: %v", err)
	}
}

func runConverter(t *testing.T, eventsFile string, hours int) string {
	t.Helper()
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Failed to create pipe: %v", err)
	}
	os.Stdout = w

	converterErr := convertEvents(eventsFile, hours)

	os.Stdout = oldStdout
	w.Close()
	var buf strings.Builder
	_, _ = bufio.NewReader(r).WriteTo(&buf)
	r.Close()

	if converterErr != nil {
		t.Fatalf("convertEvents failed: %v", converterErr)
	}
	return buf.String()
}

func TestConvertEvents(t *testing.T) {
	tmpDir := t.TempDir()
	eventsFile := filepath.Join(tmpDir, "test-events.jsonl")

	now := time.Now().UTC()
	writeEvents(t, eventsFile, []string{
		`{"ts":"` + now.Add(-2*time.Hour).Format(time.RFC3339) + `","query":"solar","keys":["solar.panel_count","solar.battery"],"tiers":["longterm","longterm"]}`,
		`{"ts":"` + now.Add(-48*time.Hour).Format(time.RFC3339) + `","query":"old query","keys":["old.key"],"tiers":["longterm"]}`,
		`{"ts":"` + now.Add(-1*time.Hour).Format(time.RFC3339) + `","query":"jeff","keys":["jeff.name","jeff.birthday","jeff.email"],"tiers":["longterm","longterm","working"]}`,
		`{"ts":"` + now.Add(-3*time.Hour).Format(time.RFC3339) + `","query":"test","keys":["test.key"],"tiers":["working"]}`,
	})

	output := runConverter(t, eventsFile, 24)

	var selections []feedSelection
	if err := json.Unmarshal([]byte(output), &selections); err != nil {
		t.Fatalf("Failed to parse output: %v", err)
	}

	// Expect 3 selections:
	// - old.key excluded (48h old, outside 24h window)
	// - test.key + jeff.email excluded (working tier)
	// - solar.panel_count, solar.battery, jeff.name, jeff.birthday included
	if len(selections) != 4 {
		t.Fatalf("Expected 4 selections, got %d: %+v", len(selections), selections)
	}

	found := make(map[string]bool)
	for _, s := range selections {
		found[s.Key] = true
		if s.Type != "selected" {
			t.Errorf("Expected type 'selected', got '%s'", s.Type)
		}
	}

	for _, want := range []string{"solar.panel_count", "solar.battery", "jeff.name", "jeff.birthday"} {
		if !found[want] {
			t.Errorf("Missing key: %s", want)
		}
	}
	if found["old.key"] {
		t.Error("old.key (48h) should be excluded")
	}
	if found["test.key"] || found["jeff.email"] {
		t.Error("working-tier keys should be excluded")
	}
}

func TestConvertEventsNeighbors(t *testing.T) {
	tmpDir := t.TempDir()
	eventsFile := filepath.Join(tmpDir, "neighbors-events.jsonl")

	now := time.Now().UTC()
	writeEvents(t, eventsFile, []string{
		`{"ts":"` + now.Add(-1*time.Hour).Format(time.RFC3339) + `","query":"solar","keys":["solar.panel_count","solar.battery","solar.inverter"],"tiers":["longterm","longterm","longterm"]}`,
	})

	output := runConverter(t, eventsFile, 24)

	var selections []feedSelection
	if err := json.Unmarshal([]byte(output), &selections); err != nil {
		t.Fatalf("Failed to parse output: %v", err)
	}
	if len(selections) != 3 {
		t.Fatalf("Expected 3 selections, got %d", len(selections))
	}

	for _, s := range selections {
		if len(s.Neighbors) != 2 {
			t.Errorf("Key %s: expected 2 neighbors, got %d (%v)", s.Key, len(s.Neighbors), s.Neighbors)
		}
	}
}

func TestConvertEventsEmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	eventsFile := filepath.Join(tmpDir, "empty-events.jsonl")

	if err := os.WriteFile(eventsFile, []byte(""), 0644); err != nil {
		t.Fatalf("Failed to write empty file: %v", err)
	}

	output := runConverter(t, eventsFile, 24)

	var selections []feedSelection
	if err := json.Unmarshal([]byte(output), &selections); err != nil {
		t.Fatalf("Failed to parse output: %v", err)
	}
	if len(selections) != 0 {
		t.Errorf("Expected 0 selections, got %d", len(selections))
	}
}

func TestConvertEventsMissingFile(t *testing.T) {
	// Missing file is OK — empty output
	output := runConverter(t, filepath.Join(t.TempDir(), "nonexistent.jsonl"), 24)

	var selections []feedSelection
	if err := json.Unmarshal([]byte(output), &selections); err != nil {
		t.Fatalf("Failed to parse output: %v", err)
	}
	if len(selections) != 0 {
		t.Errorf("Expected 0 selections, got %d", len(selections))
	}
}

func TestConvertEventsMalformedLines(t *testing.T) {
	tmpDir := t.TempDir()
	eventsFile := filepath.Join(tmpDir, "malformed-events.jsonl")

	now := time.Now().UTC()
	writeEvents(t, eventsFile, []string{
		`{"ts":"not-a-timestamp","query":"bad","keys":["bad.key"],"tiers":["longterm"]}`,
		`{invalid json`,
		`{"ts":"` + now.Add(-1*time.Hour).Format(time.RFC3339) + `","query":"good","keys":["good.key"],"tiers":["longterm"]}`,
	})

	output := runConverter(t, eventsFile, 24)

	var selections []feedSelection
	if err := json.Unmarshal([]byte(output), &selections); err != nil {
		t.Fatalf("Failed to parse output: %v", err)
	}
	if len(selections) != 1 {
		t.Fatalf("Expected 1 selection (malformed lines skipped), got %d", len(selections))
	}
	if selections[0].Key != "good.key" {
		t.Errorf("Expected good.key, got %s", selections[0].Key)
	}
}

// conduit-31jg.40: the default events file is derived, not hardcoded.
func TestResolveEventsFile(t *testing.T) {
	clear := func() {
		for _, k := range []string{"CONDUIT_RECALL_EVENTS", "CONDUIT_WORKSPACE", "CONDUIT_CONFIG", "CONDUIT_HOME"} {
			t.Setenv(k, "")
		}
	}

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{"workspace":{"context_dir":"/srv/ws"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		env  map[string]string
		flag string
		want string
	}{
		{"explicit env file", map[string]string{"CONDUIT_RECALL_EVENTS": "/x/ev.jsonl", "CONDUIT_WORKSPACE": "/ignored"}, "", "/x/ev.jsonl"},
		{"workspace env", map[string]string{"CONDUIT_WORKSPACE": "/ws"}, "", "/ws/memory/recall-events.jsonl"},
		{"config flag", nil, cfgPath, "/srv/ws/memory/recall-events.jsonl"},
		{"CONDUIT_CONFIG", map[string]string{"CONDUIT_CONFIG": cfgPath}, "", "/srv/ws/memory/recall-events.jsonl"},
		{"CONDUIT_HOME", map[string]string{"CONDUIT_HOME": dir}, "", "/srv/ws/memory/recall-events.jsonl"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clear()
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			got, err := resolveEventsFile(c.flag)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}

	t.Run("relative workspace anchors at config dir", func(t *testing.T) {
		clear()
		rel := filepath.Join(dir, "rel.json")
		if err := os.WriteFile(rel, []byte(`{"workspace":{"context_dir":"workspace"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := resolveEventsFile(rel)
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(dir, "workspace", "memory", "recall-events.jsonl"); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("missing config errors", func(t *testing.T) {
		clear()
		if _, err := resolveEventsFile(filepath.Join(dir, "nope.json")); err == nil {
			t.Error("expected error for missing config")
		}
	})
}
