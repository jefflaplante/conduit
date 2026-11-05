package statehook

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// TempFilePrefix is the same-dir temp prefix for atomic writes
// (state.py's ".jules-state." parity).
const TempFilePrefix = ".jules-state."

// Writer is the atomic single-writer for one state file: same-dir temp +
// rename + re-read + JSON verify (state.py:97-120 parity). One Writer per
// target path; the mutex serializes writers within one process.
type Writer struct {
	path string
	mu   sync.Mutex
	now  func() time.Time
}

// NewWriter builds a Writer for path (absolute, or relative to the CWD the
// caller resolved it from — callers pass workspace-resolved paths).
func NewWriter(path string) *Writer {
	return &Writer{path: path}
}

// Write validates, caps, stamps and atomically writes st.
// It refuses to overwrite the state when fewer than 2 of the 3 required
// content fields are present (state.py:86-90 empty-clobber guard) and never
// touches the target file in that case. Returns the bytes on disk.
func (w *Writer) Write(st *JulesState) (int, error) {
	if w == nil {
		return 0, fmt.Errorf("state writer: nil writer")
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	if st == nil {
		return 0, fmt.Errorf("state writer: refusing to write nil state")
	}
	if n := st.RequiredFieldCount(); n < 2 {
		return 0, fmt.Errorf("state writer: refusing to overwrite state: fewer than 2 of %v required fields present (got %d); guard against empty-clobber",
			RequiredFields, n)
	}
	if strings := st.WrittenAt; strings == "" || !validRFC3339(strings) {
		st.WrittenAt = w.now2().Format(time.RFC3339)
	}

	body, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("state writer: marshal: %w", err)
	}
	body = append(body, '\n')

	dir := filepath.Dir(w.path)
	tmp, err := os.CreateTemp(dir, TempFilePrefix+"*.tmp")
	if err != nil {
		return 0, fmt.Errorf("state writer: temp create: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return 0, fmt.Errorf("state writer: temp write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return 0, fmt.Errorf("state writer: temp sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return 0, fmt.Errorf("state writer: temp close: %w", err)
	}
	if err := os.Rename(tmpName, w.path); err != nil {
		return 0, fmt.Errorf("state writer: rename: %w", err)
	}

	// Verify: re-read from disk and JSON-parse (state.py:109-120 parity).
	back, err := os.ReadFile(w.path)
	if err != nil {
		return 0, fmt.Errorf("state writer: verify read: %w", err)
	}
	if string(back) != string(body) {
		return 0, fmt.Errorf("state writer: verify FAILED: on-disk bytes != written bytes (%d vs %d)", len(back), len(body))
	}
	if _, err := ParseJulesState(back); err != nil {
		return 0, fmt.Errorf("state writer: verify FAILED: on-disk file does not parse: %w", err)
	}
	return len(back), nil
}

// Path returns the target path.
func (w *Writer) Path() string {
	if w == nil {
		return ""
	}
	return w.path
}

func (w *Writer) now2() time.Time {
	if w.now != nil {
		return w.now()
	}
	return time.Now()
}

func validRFC3339(s string) bool {
	_, err := time.Parse(time.RFC3339, s)
	return err == nil
}
