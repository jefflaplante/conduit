package scheduler

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// conduit-2six: failure log + consecutive-failure notices.

type recordingNotifier struct {
	mu     sync.Mutex
	events []JobHealthEvent
}

func (r *recordingNotifier) NotifyJobHealth(_ context.Context, ev JobHealthEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
	return nil
}

func (r *recordingNotifier) snapshot() []JobHealthEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]JobHealthEvent(nil), r.events...)
}

// switchExec returns an executor whose outcome is set via the returned
// setter (nil = success).
func switchExec() (JobExecutor, func(error)) {
	var cur atomic.Value
	cur.Store(errBox{})
	exec := func(ctx context.Context, job *Job) error { return cur.Load().(errBox).err }
	return exec, func(err error) { cur.Store(errBox{err}) }
}

type errBox struct{ err error }

func newHealthScheduler(t *testing.T, jobs []*Job, threshold int) (*Scheduler, *recordingNotifier, func(error), string) {
	t.Helper()
	exec, set := switchExec()
	n := &recordingNotifier{}
	logPath := filepath.Join(t.TempDir(), "memory", "cron-log.jsonl")
	s := startDrainTestScheduler(t, jobs, exec, WithFailureNotifier(n, threshold), WithFailureLog(logPath))
	return s, n, set, logPath
}

func streakOf(t *testing.T, s *Scheduler, id string) int {
	t.Helper()
	return s.JobHealth()[id].ConsecutiveFailures
}

func TestFailureStreak_AlertsOnceAtThreshold(t *testing.T) {
	s, n, set, _ := newHealthScheduler(t, []*Job{{ID: "wildlife", Name: "Wildlife digest", Schedule: farFuture, Type: JobTypeGo, Enabled: true}}, 0)
	set(errors.New("provider 500"))

	for i := 1; i <= 2; i++ {
		s.executeJob("wildlife")
		if got := len(n.snapshot()); got != 0 {
			t.Fatalf("alert after %d failure(s), want none before threshold", i)
		}
	}
	s.executeJob("wildlife") // 3rd: default threshold
	evs := n.snapshot()
	if len(evs) != 1 {
		t.Fatalf("want exactly 1 alert at threshold, got %d", len(evs))
	}
	ev := evs[0]
	if ev.Kind != JobHealthFailing || ev.JobID != "wildlife" || ev.ConsecutiveFailures != 3 || ev.LastError != "provider 500" || ev.LastTimeout {
		t.Fatalf("unexpected event: %+v", ev)
	}
	if msg := ev.Message(time.UTC); !strings.Contains(msg, `"Wildlife digest" (wildlife)`) || !strings.Contains(msg, "failed 3 run(s) in a row") || !strings.Contains(msg, "provider 500") {
		t.Errorf("message = %q", msg)
	}

	s.executeJob("wildlife") // 4th and 5th: same streak, no second alert
	s.executeJob("wildlife")
	if got := len(n.snapshot()); got != 1 {
		t.Fatalf("want still 1 alert after N+2 failures, got %d", got)
	}
	if got := streakOf(t, s, "wildlife"); got != 5 {
		t.Fatalf("streak = %d, want 5", got)
	}

	// The streak (including "already alerted") is persisted with the job.
	s2 := New(filepath.Dir(s.jobsFile), nil)
	if err := s2.loadJobs(); err != nil {
		t.Fatal(err)
	}
	st := s2.jobs["wildlife"].FailureStreak
	if st == nil || st.Count != 5 || !st.Alerted {
		t.Fatalf("persisted streak = %+v", st)
	}
}

func TestFailureStreak_RecoveryNotifiesAndResets(t *testing.T) {
	s, n, set, _ := newHealthScheduler(t, []*Job{{ID: "j", Schedule: farFuture, Type: JobTypeGo, Enabled: true}}, 3)
	set(errors.New("boom"))
	for i := 0; i < 4; i++ {
		s.executeJob("j")
	}
	set(nil)
	s.executeJob("j")

	evs := n.snapshot()
	if len(evs) != 2 || evs[0].Kind != JobHealthFailing || evs[1].Kind != JobHealthRecovered {
		t.Fatalf("events = %+v, want [failing recovered]", evs)
	}
	if evs[1].ConsecutiveFailures != 4 {
		t.Errorf("recovered after %d, want 4", evs[1].ConsecutiveFailures)
	}
	if !strings.Contains(evs[1].Message(nil), "recovered") {
		t.Errorf("recovered message = %q", evs[1].Message(nil))
	}
	if got := streakOf(t, s, "j"); got != 0 {
		t.Fatalf("streak after success = %d", got)
	}
	job, _ := s.GetJob("j")
	s.mu.RLock()
	streak, lastErr := job.FailureStreak, job.LastError
	s.mu.RUnlock()
	if streak != nil || lastErr != "" {
		t.Fatalf("after success: streak %+v, last error %q", streak, lastErr)
	}

	// A new streak starts from scratch and must reach the threshold again.
	set(errors.New("boom"))
	s.executeJob("j")
	s.executeJob("j")
	if got := len(n.snapshot()); got != 2 {
		t.Fatalf("new streak below threshold alerted: %d events", got)
	}
}

func TestFailureStreak_SuccessBelowThresholdIsSilent(t *testing.T) {
	s, n, set, _ := newHealthScheduler(t, []*Job{{ID: "j", Schedule: farFuture, Type: JobTypeGo, Enabled: true}}, 3)
	set(errors.New("flaky"))
	s.executeJob("j")
	set(nil)
	s.executeJob("j")
	if evs := n.snapshot(); len(evs) != 0 {
		t.Fatalf("events = %+v, want none", evs)
	}
	if got := streakOf(t, s, "j"); got != 0 {
		t.Fatalf("streak = %d", got)
	}
}

func TestFailureStreak_InterruptedRunsDoNotCount(t *testing.T) {
	started := make(chan struct{}, 1)
	var interruptNext atomic.Bool
	exec := func(ctx context.Context, job *Job) error {
		if interruptNext.Load() {
			started <- struct{}{}
			<-ctx.Done()
			return fmt.Errorf("turn: %w", context.Cause(ctx))
		}
		return errors.New("real failure")
	}
	n := &recordingNotifier{}
	logPath := filepath.Join(t.TempDir(), "cron-log.jsonl")
	s := startDrainTestScheduler(t, []*Job{{ID: "j", Schedule: farFuture, Type: JobTypeGo, Enabled: true}}, exec,
		WithFailureNotifier(n, 3), WithFailureLog(logPath))

	s.executeJob("j")
	s.executeJob("j")
	if got := streakOf(t, s, "j"); got != 2 {
		t.Fatalf("streak = %d, want 2", got)
	}

	interruptNext.Store(true)
	done := make(chan struct{})
	go func() { s.executeJob("j"); close(done) }()
	<-started
	s.InterruptRunning()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("interrupted run did not finish")
	}

	// The interrupted run neither reaches the threshold nor resets the streak.
	if got := streakOf(t, s, "j"); got != 2 {
		t.Fatalf("streak after interrupted run = %d, want 2", got)
	}
	if evs := n.snapshot(); len(evs) != 0 {
		t.Fatalf("interrupted run produced events: %+v", evs)
	}
	recs := readFailureLog(t, logPath)
	if len(recs) != 3 || recs[2].Status != "interrupted" || recs[2].ConsecutiveFailures != 0 {
		t.Fatalf("failure log = %+v", recs)
	}
}

func TestFailureStreak_TimeoutsCountAndAreLabelled(t *testing.T) {
	s, n, set, logPath := newHealthScheduler(t, []*Job{{ID: "hb", Schedule: farFuture, Type: JobTypeGo, Enabled: true}}, 3)
	set(errors.New("AI execution failed: other"))
	s.executeJob("hb")
	set(fmt.Errorf("AI execution failed: %w", context.DeadlineExceeded))
	s.executeJob("hb")
	s.executeJob("hb")

	evs := n.snapshot()
	if len(evs) != 1 {
		t.Fatalf("want 1 alert, got %d", len(evs))
	}
	ev := evs[0]
	if !ev.LastTimeout || ev.Timeouts != 2 || ev.ConsecutiveFailures != 3 {
		t.Fatalf("event = %+v", ev)
	}
	msg := ev.Message(nil)
	if !strings.Contains(msg, "TIMED OUT") || !strings.Contains(msg, "2 of the 3 failures were timeouts") {
		t.Errorf("message = %q", msg)
	}
	recs := readFailureLog(t, logPath)
	if len(recs) != 3 || recs[0].Timeout || !recs[1].Timeout || !recs[2].Timeout ||
		recs[0].Status != "error" || recs[1].Status != "timeout" || recs[2].Status != "timeout" {
		t.Fatalf("failure log timeout flags = %+v", recs)
	}
}

func TestFailureStreak_LongStreakAlertsBelowThreshold(t *testing.T) {
	since := time.Now().Add(-25 * time.Hour)
	job := &Job{ID: "daily", Schedule: farFuture, Type: JobTypeGo, Enabled: true,
		FailureStreak: &FailureStreak{Count: 1, Since: since}}
	s, n, set, _ := newHealthScheduler(t, []*Job{job}, 5)
	set(errors.New("still broken"))
	s.executeJob("daily")
	evs := n.snapshot()
	if len(evs) != 1 || evs[0].ConsecutiveFailures != 2 || !evs[0].FailingSince.Equal(since) {
		t.Fatalf("events = %+v, want one alert for a day-old streak", evs)
	}
}

func TestFailureStreak_NegativeThresholdDisablesAlerts(t *testing.T) {
	s, n, set, _ := newHealthScheduler(t, []*Job{{ID: "j", Schedule: farFuture, Type: JobTypeGo, Enabled: true}}, -1)
	set(errors.New("boom"))
	for i := 0; i < 5; i++ {
		s.executeJob("j")
	}
	if evs := n.snapshot(); len(evs) != 0 {
		t.Fatalf("alerts disabled but got %+v", evs)
	}
	if got := streakOf(t, s, "j"); got != 5 {
		t.Fatalf("streak still tracked: got %d", got)
	}
	if f := s.Status()["failing_jobs"].(map[string]int); f["j"] != 5 {
		t.Fatalf("status failing_jobs = %v", f)
	}
}

func TestFailureLog_RecordsEveryFailedRun(t *testing.T) {
	s, _, set, logPath := newHealthScheduler(t, []*Job{{ID: "w", Name: "Wildlife", Schedule: farFuture, Type: JobTypeGo, Enabled: true}}, 3)
	s.executeJob("w") // success: no record
	set(errors.New("first LLM call failed"))
	s.executeJob("w")
	s.executeJob("w")

	recs := readFailureLog(t, logPath)
	if len(recs) != 2 {
		t.Fatalf("want 2 records, got %d: %+v", len(recs), recs)
	}
	for i, r := range recs {
		if r.Status != "error" || r.Job != "Wildlife" || r.JobID != "w" || r.Error != "first LLM call failed" || r.Source != "scheduler" || r.ConsecutiveFailures != i+1 {
			t.Errorf("record %d = %+v", i, r)
		}
		if _, err := time.Parse(time.RFC3339, r.TS); err != nil {
			t.Errorf("record %d ts %q: %v", i, r.TS, err)
		}
	}
}

func TestJobHealthEvent_MessageAllTimeouts(t *testing.T) {
	ev := JobHealthEvent{Kind: JobHealthFailing, JobID: "hb", ConsecutiveFailures: 3, Timeouts: 3, LastTimeout: true,
		FailingSince: time.Date(2026, 9, 27, 7, 0, 0, 0, time.UTC), LastError: strings.Repeat("x", 500)}
	msg := ev.Message(time.UTC)
	if !strings.Contains(msg, "All of these failures were timeouts") || !strings.Contains(msg, "Sun Sep 27 07:00 UTC") {
		t.Errorf("message = %q", msg)
	}
	if strings.Contains(msg, strings.Repeat("x", 301)) {
		t.Error("last error not truncated")
	}
}

func readFailureLog(t *testing.T, path string) []failureLogRecord {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open failure log: %v", err)
	}
	defer f.Close()
	var out []failureLogRecord
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r failureLogRecord
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("bad line %q: %v", sc.Text(), err)
		}
		out = append(out, r)
	}
	return out
}
