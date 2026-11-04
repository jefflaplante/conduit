package gateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"conduit/internal/mqttevents"
)

// readPendingArray reads pending.json and fails unless it is a bare JSON
// array (the only shape alert-flush.sh accepts).
func readPendingArray(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(data)), "[") {
		t.Fatalf("pending.json is not a bare array:\n%s", data)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Error("pending.json must end with a newline")
	}
	var entries []map[string]any
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("pending.json: %v\n%s", err, data)
	}
	return entries
}

func testNotice(prio string) mqttevents.Notice {
	return mqttevents.Notice{
		RuleID:    "r",
		Topic:     "sensors/a",
		Priority:  prio,
		Message:   "AUTOMATED EVENT (rule r, topic sensors/a). body",
		Timestamp: time.Date(2026, 1, 1, 12, 0, 0, 0, time.FixedZone("test", -8*3600)),
	}
}

func TestMQTTAlertQueue_MissingFileCreatesBareArray(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory", "alerts", "pending.json")
	if err := newMQTTEventQueuer(path).Enqueue(testNotice("high")); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	entries := readPendingArray(t, path)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	e := entries[0]
	want := map[string]any{
		"source":    "mqtt-events",
		"severity":  "critical",
		"title":     "MQTT event: rule r on sensors/a",
		"message":   "AUTOMATED EVENT (rule r, topic sensors/a). body",
		"timestamp": "2026-01-01T12:00:00-08:00",
		"rule_id":   "r",
		"topic":     "sensors/a",
	}
	for k, v := range want {
		if e[k] != v {
			t.Errorf("%s = %v, want %v", k, e[k], v)
		}
	}
	if _, ok := e["details"]; ok {
		t.Error("details must be omitted for a plain queue rule")
	}
	if _, err := os.Stat(path + ".lock"); err != nil {
		t.Errorf("lock file not created: %v", err)
	}
}

func TestMQTTAlertQueue_EmptyFileStartsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pending.json")
	if err := os.WriteFile(path, []byte("  \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := newMQTTEventQueuer(path).Enqueue(testNotice("low")); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if n := len(readPendingArray(t, path)); n != 1 {
		t.Fatalf("want 1 entry, got %d", n)
	}
}

// Entries written by the Python producers (alertlock.py) are loose dicts
// with keys Go knows nothing about; they must survive the round trip.
func TestMQTTAlertQueue_PreservesExistingLooseEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pending.json")
	existing := `[
  {"source": "cron-check", "severity": "warning", "title": "Disk", "timestamp": "2026-01-01T00:00:00-08:00", "custom_key": {"nested": [1, 2, 3]}, "count": 7},
  {"source": "other", "message": "no title here", "flag": true}
]`
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := newMQTTEventQueuer(path).Enqueue(testNotice("medium")); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	entries := readPendingArray(t, path)
	if len(entries) != 3 {
		t.Fatalf("want 3 entries, got %d", len(entries))
	}
	var orig []map[string]any
	if err := json.Unmarshal([]byte(existing), &orig); err != nil {
		t.Fatal(err)
	}
	for i := range orig {
		a, _ := json.Marshal(orig[i])
		b, _ := json.Marshal(entries[i])
		if string(a) != string(b) {
			t.Errorf("entry %d changed:\n got %s\nwant %s", i, b, a)
		}
	}
	if entries[2]["source"] != "mqtt-events" || entries[2]["severity"] != "warning" {
		t.Errorf("appended entry = %v", entries[2])
	}
}

// A prior object-envelope write ({alerts:[...], version, last_sync}) is
// unwrapped back into a bare array so the queue heals itself.
func TestMQTTAlertQueue_ObjectEnvelopeSelfHeals(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pending.json")
	env := `{"alerts": [{"id": "a1", "source": "x", "severity": "info", "status": "pending"}], "version": 1, "last_sync": "2026-01-01T00:00:00Z"}`
	if err := os.WriteFile(path, []byte(env), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := newMQTTEventQueuer(path).Enqueue(testNotice("high")); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	entries := readPendingArray(t, path)
	if len(entries) != 2 || entries[0]["id"] != "a1" || entries[1]["source"] != "mqtt-events" {
		t.Fatalf("want [a1, mqtt-events], got %v", entries)
	}
}

// A file we cannot understand is never clobbered (alert-flush.sh's rule:
// corruption is not empty).
func TestMQTTAlertQueue_UnparseableInputErrorsAndLeavesFile(t *testing.T) {
	cases := map[string]string{
		"garbage":          "not json at all",
		"truncated array":  `[{"source": "x"`,
		"object no alerts": `{"version": 1}`,
		"alerts not array": `{"alerts": "nope"}`,
		"scalar":           `42`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pending.json")
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := newMQTTEventQueuer(path).Enqueue(testNotice("high")); err == nil {
				t.Fatal("want error for an unparseable queue")
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != content {
				t.Errorf("file modified:\n got %q\nwant %q", got, content)
			}
			matches, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "pending.json.tmp-*"))
			if len(matches) != 0 {
				t.Errorf("temp files left behind: %v", matches)
			}
		})
	}
}

func TestMQTTAlertQueue_SeverityMapping(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pending.json")
	q := newMQTTEventQueuer(path)
	prios := []string{"high", "medium", "low", ""}
	for _, p := range prios {
		if err := q.Enqueue(testNotice(p)); err != nil {
			t.Fatalf("Enqueue(%q): %v", p, err)
		}
	}
	entries := readPendingArray(t, path)
	want := []string{"critical", "warning", "info", "info"}
	if len(entries) != len(want) {
		t.Fatalf("want %d entries, got %d", len(want), len(entries))
	}
	for i, e := range entries {
		if e["severity"] != want[i] {
			t.Errorf("priority %q: severity = %v, want %s", prios[i], e["severity"], want[i])
		}
	}
}

func TestMQTTAlertQueue_DowngradeReasonInDetails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pending.json")
	n := testNotice("high")
	n.Downgraded = "max_concurrent_spawns (2) reached"
	if err := newMQTTEventQueuer(path).Enqueue(n); err != nil {
		t.Fatal(err)
	}
	entries := readPendingArray(t, path)
	if d, _ := entries[0]["details"].(string); !strings.Contains(d, "max_concurrent_spawns") {
		t.Errorf("details = %q, want the downgrade reason", d)
	}
}

func TestMQTTAlertQueue_ZeroTimestampUsesNow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pending.json")
	n := testNotice("low")
	n.Timestamp = time.Time{}
	before := time.Now().Add(-time.Second)
	if err := newMQTTEventQueuer(path).Enqueue(n); err != nil {
		t.Fatal(err)
	}
	ts, err := time.Parse(time.RFC3339, readPendingArray(t, path)[0]["timestamp"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if ts.Before(before) {
		t.Errorf("timestamp %v not defaulted to now", ts)
	}
}

// bd-j2of regression: production pending.json is a bare array written by
// alertlock.py. SharedAlertQueue failed with "cannot unmarshal array" on
// every rule match and the engine fell back to log-only.
func TestMQTTAlertQueue_BareArrayRegression_bdj2of(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory", "alerts", "pending.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`[{"source": "cron", "severity": "info", "title": "t", "message": "m", "timestamp": "2026-01-01T00:00:00Z"}]`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	q := newMQTTEventQueuer(path)
	for i := 0; i < 5; i++ {
		if err := q.Enqueue(testNotice("medium")); err != nil {
			t.Fatalf("Enqueue %d against a bare-array queue: %v", i, err)
		}
	}
	if n := len(readPendingArray(t, path)); n != 6 {
		t.Fatalf("want 6 entries, got %d", n)
	}
}
