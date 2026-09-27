package brain

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

// Recall-event log (conduit-31jg.40).
//
// Every non-empty Recall appends one JSON line to a recall-events file that
// the brain_spread reinforcement job consumes (see
// cmd/recall-events-converter). The path used to be hardcoded to one
// deployment's workspace; it is now injected via WithRecallEventsPath (the
// gateway derives <workspace>/<memory dir>/recall-events.jsonl from config).
// A Brain constructed without that option — CLI helpers, tests — logs
// nothing. The file is rotated by size so it can no longer grow unbounded.

const (
	// DefaultRecallEventsMaxBytes is the size at which the recall-event log
	// rotates.
	DefaultRecallEventsMaxBytes int64 = 10 << 20 // 10 MiB
	// DefaultRecallEventsBackups is how many rotated segments are kept
	// (<path>.1 is the newest, <path>.N the oldest).
	DefaultRecallEventsBackups = 3
)

// recallEventLog is a size-rotated, append-only JSONL writer.
type recallEventLog struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	backups  int
}

func newRecallEventLog(path string, maxBytes int64, backups int) *recallEventLog {
	if maxBytes <= 0 {
		maxBytes = DefaultRecallEventsMaxBytes
	}
	if backups < 0 {
		backups = 0
	}
	return &recallEventLog{path: path, maxBytes: maxBytes, backups: backups}
}

// WithRecallEventsPath enables recall-event logging to path, rotated at
// DefaultRecallEventsMaxBytes with DefaultRecallEventsBackups segments kept.
// An empty path disables logging (the default).
func WithRecallEventsPath(path string) Option {
	return WithRecallEventsLog(path, DefaultRecallEventsMaxBytes, DefaultRecallEventsBackups)
}

// WithRecallEventsLog is WithRecallEventsPath with explicit rotation limits.
// maxBytes <= 0 selects the default; backups == 0 truncates on rotation.
func WithRecallEventsLog(path string, maxBytes int64, backups int) Option {
	return func(b *Brain) {
		if path == "" {
			b.recallEvents = nil
			return
		}
		b.recallEvents = newRecallEventLog(path, maxBytes, backups)
	}
}

// append writes one line (a trailing newline is added), rotating first when
// the line would push the file past maxBytes.
func (l *recallEventLog) append(line []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if fi, err := os.Stat(l.path); err == nil && fi.Size() > 0 && fi.Size()+int64(len(line))+1 > l.maxBytes {
		if err := l.rotate(); err != nil {
			return fmt.Errorf("rotate: %w", err)
		}
	}

	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(line, '\n'))
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

// rotate shifts <path>.N-1 → <path>.N … <path> → <path>.1, dropping the
// oldest segment. With zero backups the live file is simply removed.
func (l *recallEventLog) rotate() error {
	if l.backups == 0 {
		return os.Remove(l.path)
	}
	_ = os.Remove(fmt.Sprintf("%s.%d", l.path, l.backups))
	for i := l.backups - 1; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", l.path, i)
		if _, err := os.Stat(src); err == nil {
			if err := os.Rename(src, fmt.Sprintf("%s.%d", l.path, i+1)); err != nil {
				return err
			}
		}
	}
	return os.Rename(l.path, l.path+".1")
}

// logRecallEvent records a recall for brain_spread reinforcement.
// Best-effort: failures are logged and never fail the recall.
func (b *Brain) logRecallEvent(query string, results []*Entry) {
	if b.recallEvents == nil || len(results) == 0 {
		return
	}

	type recallEvent struct {
		Timestamp string   `json:"ts"`
		Query     string   `json:"query"`
		Keys      []string `json:"keys"`
		Tiers     []string `json:"tiers"`
	}
	event := recallEvent{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Query:     query,
		Keys:      make([]string, 0, len(results)),
		Tiers:     make([]string, 0, len(results)),
	}
	for _, entry := range results {
		event.Keys = append(event.Keys, entry.Key)
		event.Tiers = append(event.Tiers, string(entry.Tier))
	}

	data, err := json.Marshal(event)
	if err != nil {
		log.Printf("Brain: failed to marshal recall event: %v", err)
		return
	}
	if err := b.recallEvents.append(data); err != nil {
		log.Printf("Brain: failed to write recall event to %s: %v", b.recallEvents.path, err)
	}
}
