package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"conduit/internal/mqttevents"
)

// mqttEventQueuer appends notices to the shared alert queue (pending.json)
// in its native format (bd-j2of). The file is owned by the external Python
// tooling (alertlock.py writers, alert-flush.sh consumer): a bare JSON array
// of loose alert dicts, guarded by flock on "<queue>.lock" and replaced
// atomically. heartbeat.SharedAlertQueue's {alerts, version, last_sync}
// envelope is incompatible in both directions (it cannot read the array,
// and alert-flush.sh treats an envelope as corrupt), so it is not used here.
type mqttEventQueuer struct {
	path string
}

func newMQTTEventQueuer(path string) *mqttEventQueuer {
	return &mqttEventQueuer{path: path}
}

// mqttQueueEntry is the loose alert dict the MQTT engine appends, keyed the
// way the Python producers key theirs.
type mqttQueueEntry struct {
	Source    string `json:"source"`
	Severity  string `json:"severity"`
	Title     string `json:"title"`
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
	RuleID    string `json:"rule_id"`
	Topic     string `json:"topic"`
	Details   string `json:"details,omitempty"`
}

func (m *mqttEventQueuer) Enqueue(n mqttevents.Notice) error {
	ts := n.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	entry := mqttQueueEntry{
		Source:    mqttEventAlertSource,
		Severity:  string(mqttEventSeverity(n.Priority)),
		Title:     fmt.Sprintf("MQTT event: rule %s on %s", n.RuleID, n.Topic),
		Message:   n.Message,
		Timestamp: ts.Format(time.RFC3339),
		RuleID:    n.RuleID,
		Topic:     n.Topic,
	}
	if n.Downgraded != "" {
		entry.Details = "downgraded from spawn: " + n.Downgraded
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return appendPendingAlert(m.path, raw)
}

// appendPendingAlert appends one entry to the bare-array queue at path under
// the alertlock.py protocol: exclusive flock on path+".lock", read, append,
// then writeFileAtomic (same-dir 0600 temp file, fsync, rename), which leaves
// the same mode as Python's mkstemp + os.replace.
func appendPendingAlert(path string, entry json.RawMessage) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("alert queue dir: %w", err)
	}
	unlock, err := lockAlertQueue(path + ".lock")
	if err != nil {
		return fmt.Errorf("alert queue lock: %w", err)
	}
	defer unlock()

	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read alert queue: %w", err)
	}
	entries, err := parsePendingAlerts(data)
	if err != nil {
		return fmt.Errorf("alert queue %s: %w", path, err)
	}
	out, err := json.MarshalIndent(append(entries, entry), "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(out, '\n'))
}

// parsePendingAlerts decodes pending.json. Missing/empty is an empty queue;
// a bare array is used as-is; an object with an "alerts" array (a prior
// SharedAlertQueue envelope) is unwrapped so the next write heals the file.
// Anything else is an error: like alert-flush.sh, never treat a file we do
// not understand as empty and clobber it. Entries stay json.RawMessage so
// other producers' keys survive the round trip.
func parsePendingAlerts(data []byte) ([]json.RawMessage, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, nil
	}
	var entries []json.RawMessage
	switch data[0] {
	case '[':
		if err := json.Unmarshal(data, &entries); err != nil {
			return nil, fmt.Errorf("unparseable array: %w", err)
		}
		return entries, nil
	case '{':
		var env map[string]json.RawMessage
		if err := json.Unmarshal(data, &env); err != nil {
			return nil, fmt.Errorf("unparseable object: %w", err)
		}
		raw, ok := env["alerts"]
		if !ok {
			return nil, errors.New("object without an alerts array")
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil, nil
		}
		if err := json.Unmarshal(raw, &entries); err != nil {
			return nil, fmt.Errorf("alerts is not an array: %w", err)
		}
		return entries, nil
	}
	return nil, errors.New("not a JSON array")
}
