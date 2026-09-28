package scheduler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
)

// scheduleSpans returns, per top-level array element, the byte span of its
// "schedule" string literal ({-1,-1} if absent).
func scheduleSpans(data []byte) ([][2]int, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if t, err := dec.Token(); err != nil || t != json.Delim('[') {
		return nil, errors.New("jobs file is not a JSON array")
	}
	var spans [][2]int
	for dec.More() {
		if t, err := dec.Token(); err != nil || t != json.Delim('{') {
			return nil, fmt.Errorf("job %d is not an object", len(spans))
		}
		sp := [2]int{-1, -1}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, _ := kt.(string)
			before := int(dec.InputOffset())
			var raw json.RawMessage
			if err := dec.Decode(&raw); err != nil {
				return nil, err
			}
			if key != "schedule" {
				continue
			}
			if sp[0] >= 0 {
				return nil, fmt.Errorf("job %d has duplicate schedule keys", len(spans))
			}
			after := int(dec.InputOffset())
			idx := bytes.Index(data[before:after], raw)
			if idx < 0 || len(raw) == 0 || raw[0] != '"' {
				return nil, fmt.Errorf("job %d: schedule is not a string", len(spans))
			}
			sp = [2]int{before + idx, before + idx + len(raw)}
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		spans = append(spans, sp)
	}
	return spans, nil
}

// RewriteSchedules returns data with the schedule of each job index in
// repl replaced, leaving every other byte untouched, and verifies that the
// result differs from data only in those schedule values.
func RewriteSchedules(data []byte, repl map[int]string) ([]byte, error) {
	spans, err := scheduleSpans(data)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	last := 0
	for i, sp := range spans {
		newVal, ok := repl[i]
		if !ok {
			continue
		}
		if sp[0] < 0 {
			return nil, fmt.Errorf("job %d has no schedule", i)
		}
		lit, err := json.Marshal(newVal)
		if err != nil {
			return nil, err
		}
		out.Write(data[last:sp[0]])
		out.Write(lit)
		last = sp[1]
	}
	out.Write(data[last:])
	res := out.Bytes()

	// Semantic check: every job identical except the replaced schedules.
	var before, after []map[string]interface{}
	if err := json.Unmarshal(data, &before); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(res, &after); err != nil {
		return nil, fmt.Errorf("rewritten file is invalid JSON: %w", err)
	}
	if len(before) != len(after) {
		return nil, errors.New("rewrite changed the number of jobs")
	}
	for i := range before {
		if v, ok := repl[i]; ok {
			if after[i]["schedule"] != v {
				return nil, fmt.Errorf("job %d: schedule not rewritten as expected", i)
			}
			after[i]["schedule"] = before[i]["schedule"]
		}
		if !reflect.DeepEqual(before[i], after[i]) {
			return nil, fmt.Errorf("job %d: rewrite changed fields other than schedule", i)
		}
	}
	return res, nil
}

// TZApplyResult reports what ApplyTZMigration did.
type TZApplyResult struct {
	Jobs       []TZJobResult
	Changed    int
	BackupPath string // empty when nothing was written
}

// ApplyTZMigration migrates the jobs file at path in place: it writes a
// timestamped backup next to it, then atomically replaces the file (temp
// file + fsync + rename), preserving its mode and all non-schedule bytes.
// Already-migrated jobs are skipped, so re-running is a no-op.
func ApplyTZMigration(path string, opts TZMigrateOptions) (*TZApplyResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	jobs, err := PlanTZMigration(data, opts)
	if err != nil {
		return nil, err
	}
	res := &TZApplyResult{Jobs: jobs}
	repl := map[int]string{}
	for _, j := range jobs {
		if j.Action == TZConvert {
			repl[j.Index] = j.NewSchedule
		}
	}
	res.Changed = len(repl)
	if len(repl) == 0 {
		return res, nil
	}
	newData, err := RewriteSchedules(data, repl)
	if err != nil {
		return nil, err
	}

	backup := fmt.Sprintf("%s.bak-%s", path, opts.now().UTC().Format("20060102T150405Z"))
	if err := writeFileSync(backup, data, st.Mode().Perm(), true); err != nil {
		return nil, fmt.Errorf("write backup: %w", err)
	}
	res.BackupPath = backup

	// Refuse if something (e.g. the running scheduler's saveJobs) rewrote
	// the file since we read it; the caller can simply re-run.
	if cur, err := os.ReadFile(path); err != nil || !bytes.Equal(cur, data) {
		return nil, fmt.Errorf("%s changed while migrating; nothing written (backup at %s), re-run", path, backup)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".migrate-*")
	if err != nil {
		return nil, err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(newData); err != nil {
		tmp.Close()
		cleanup()
		return nil, err
	}
	if err := tmp.Chmod(st.Mode().Perm()); err != nil {
		tmp.Close()
		cleanup()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return nil, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return nil, err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return res, nil
}

func writeFileSync(name string, data []byte, perm os.FileMode, excl bool) error {
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if excl {
		flags = os.O_WRONLY | os.O_CREATE | os.O_EXCL
	}
	f, err := os.OpenFile(name, flags, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
