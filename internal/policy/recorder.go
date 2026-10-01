package policy

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// DefaultMaxLogBytes is the size at which the decision log rotates to
// "<path>.1" (one generation is kept).
const DefaultMaxLogBytes = 10 << 20

// FileRecorder appends decision records to a JSONL file, 0600.
type FileRecorder struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
}

// NewFileRecorder records to path, creating its directory (0700).
func NewFileRecorder(path string) (*FileRecorder, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("policy log dir: %w", err)
	}
	return &FileRecorder{path: path, maxBytes: DefaultMaxLogBytes}, nil
}

// Record appends rec. Failures are logged, never returned: the audit log
// must not break a tool call.
func (r *FileRecorder) Record(rec Record) {
	line, err := json.Marshal(rec)
	if err != nil {
		log.Printf("[policy] encode decision: %v", err)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if st, err := os.Stat(r.path); err == nil && st.Size()+int64(len(line)) >= r.maxBytes {
		if err := os.Rename(r.path, r.path+".1"); err != nil {
			log.Printf("[policy] rotate decision log: %v", err)
		}
	}
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		log.Printf("[policy] open decision log: %v", err)
		return
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		log.Printf("[policy] write decision log: %v", err)
	}
}

// ReadRecords returns the records at or after since from path and its
// rotated generation, oldest first. A missing file is not an error.
func ReadRecords(path string, since time.Time) ([]Record, error) {
	var out []Record
	for _, p := range []string{path + ".1", path} {
		f, err := os.Open(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		recs, err := readJSONL(f, since)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		out = append(out, recs...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out, nil
}

func readJSONL(r io.Reader, since time.Time) ([]Record, error) {
	var out []Record
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var rec Record
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			continue // a torn last line from a crash; skip it
		}
		if !rec.Time.Before(since) {
			out = append(out, rec)
		}
	}
	return out, sc.Err()
}

// ClassCount is the number of decisions of one kind for one class.
type ClassCount struct {
	Class    string
	Decision Decision
	Count    int
}

// Summary is a shadow-mode report over a set of records.
type Summary struct {
	Total  int
	Counts []ClassCount // by class, then decision
	// Flagged are the records whose effective decision is not allow:
	// what enforcement would have stopped or asked about.
	Flagged []Record
}

// Summarize groups records for the report.
func Summarize(records []Record) Summary {
	type key struct {
		class string
		d     Decision
	}
	n := map[key]int{}
	s := Summary{Total: len(records)}
	for _, r := range records {
		n[key{r.Class, r.Decision}]++
		if r.Decision != Allow {
			s.Flagged = append(s.Flagged, r)
		}
	}
	for k, c := range n {
		s.Counts = append(s.Counts, ClassCount{Class: k.class, Decision: k.d, Count: c})
	}
	sort.Slice(s.Counts, func(i, j int) bool {
		if s.Counts[i].Class != s.Counts[j].Class {
			return s.Counts[i].Class < s.Counts[j].Class
		}
		return s.Counts[i].Decision < s.Counts[j].Decision
	})
	return s
}

// DecisionLogFile is the decision log's name under <data_dir>/logs.
const DecisionLogFile = "policy-decisions.jsonl"

// DecisionLogPath is where the gateway records decisions and where
// `conduit policy report` reads them.
func DecisionLogPath(dataDirRoot string) string {
	return filepath.Join(dataDirRoot, "logs", DecisionLogFile)
}
