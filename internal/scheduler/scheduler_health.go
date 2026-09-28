package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// conduit-2six: job-failure observability.
//
// The wildlife cron job failed silently for 42 days: its cron-log.jsonl
// entries were written by the AI session inside the job prompt (so a run
// whose first LLM call failed left no trace) and nothing alerted on repeated
// failure. The scheduler now
//
//   - appends a status:"error" (or "interrupted") record to the failure log
//     (the workspace's memory/cron-log.jsonl) for every failed run itself;
//   - keeps a persisted per-job failure streak (Job.FailureStreak, saved in
//     cron_jobs.json with the rest of the job's runtime state);
//   - hands a FailureNotifier ONE "failing" event per streak, once the streak
//     reaches the threshold (or has lasted about a day, so daily jobs do not
//     take three days to alert), and a "recovered" event on the next success
//     after an alerted streak.
//
// Runs interrupted by shutdown (conduit-31jg.77) neither extend nor reset a
// streak. Context-deadline timeouts DO count as failures (a job that
// always times out is broken for the owner just the same), but are tracked
// separately so the alert can say so.

const (
	// DefaultFailureAlertThreshold is the consecutive-failure count that
	// triggers an alert when the configured threshold is 0.
	DefaultFailureAlertThreshold = 3

	// longStreakAge alerts on a streak of at least two failures that has
	// lasted this long even below the threshold (a daily job would otherwise
	// need three days). About a day, with slack for run-duration jitter.
	longStreakAge = 23 * time.Hour

	// maxLoggedErrorLen bounds the error text in failure-log records.
	maxLoggedErrorLen = 2000

	// notifyTimeout bounds one FailureNotifier call. Independent of the
	// scheduler context so a notice still goes out while shutting down.
	notifyTimeout = 30 * time.Second
)

// FailureStreak is a job's current run of consecutive failed runs. Nil on a
// healthy job.
type FailureStreak struct {
	Count    int       `json:"count"`
	Timeouts int       `json:"timeouts,omitempty"` // how many of Count were deadline timeouts
	Since    time.Time `json:"since"`              // first failure of the streak
	Alerted  bool      `json:"alerted,omitempty"`  // "failing" event already emitted
}

// ConsecutiveFailures returns the job's current failure-streak length.
func (j *Job) ConsecutiveFailures() int {
	if j == nil || j.FailureStreak == nil {
		return 0
	}
	return j.FailureStreak.Count
}

// JobHealthKind distinguishes failing from recovered notices.
type JobHealthKind string

const (
	JobHealthFailing   JobHealthKind = "failing"
	JobHealthRecovered JobHealthKind = "recovered"
)

// JobHealthEvent is handed to the FailureNotifier.
type JobHealthEvent struct {
	Kind                JobHealthKind
	JobID               string
	JobName             string
	ConsecutiveFailures int
	Timeouts            int // failures in the streak that were deadline timeouts
	FailingSince        time.Time
	LastError           string // failing only
	LastTimeout         bool   // failing only: the latest failure was a timeout
	At                  time.Time
}

// Message renders the owner-facing notice, with times shown in loc (nil
// means UTC).
func (e JobHealthEvent) Message(loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	name := e.JobID
	if e.JobName != "" && e.JobName != e.JobID {
		name = fmt.Sprintf("%q (%s)", e.JobName, e.JobID)
	}
	since := e.FailingSince.In(loc).Format("Mon Jan 2 15:04 MST")

	var b strings.Builder
	switch e.Kind {
	case JobHealthRecovered:
		fmt.Fprintf(&b, "✅ Scheduled job %s recovered: the latest run succeeded after %d consecutive failure(s) (failing since %s).",
			name, e.ConsecutiveFailures, since)
	default:
		fmt.Fprintf(&b, "⚠️ Scheduled job %s has failed %d run(s) in a row (since %s).", name, e.ConsecutiveFailures, since)
		if e.LastTimeout {
			b.WriteString("\nThe latest run TIMED OUT (context deadline exceeded): the job's turn ran past its time limit, usually a slow or unresponsive model provider rather than a bug in the job itself.")
		}
		if e.Timeouts > 0 && e.Timeouts < e.ConsecutiveFailures {
			fmt.Fprintf(&b, "\n%d of the %d failures were timeouts.", e.Timeouts, e.ConsecutiveFailures)
		} else if e.Timeouts > 1 && e.Timeouts == e.ConsecutiveFailures {
			b.WriteString("\nAll of these failures were timeouts.")
		}
		if e.LastError != "" {
			fmt.Fprintf(&b, "\nLast error: %s", truncate(e.LastError, 300))
		}
		b.WriteString("\nNo further alerts for this job until it succeeds again.")
	}
	return b.String()
}

// FailureNotifier delivers job-health events (the gateway routes them through
// the heartbeat DeliveryRegistry). Called synchronously from the run's
// goroutine, outside the scheduler lock.
type FailureNotifier interface {
	NotifyJobHealth(ctx context.Context, ev JobHealthEvent) error
}

// WithFailureNotifier installs n and the consecutive-failure threshold that
// triggers a "failing" event: 0 means DefaultFailureAlertThreshold, a
// negative value disables notifications (streaks are still tracked).
func WithFailureNotifier(n FailureNotifier, threshold int) Option {
	return func(s *Scheduler) {
		s.notifier = n
		s.failureThreshold = threshold
	}
}

// WithFailureLog appends a JSONL record for every failed or interrupted run
// to path (conduit-2six). Empty disables.
func WithFailureLog(path string) Option {
	return func(s *Scheduler) { s.failureLog = path }
}

// effectiveThreshold resolves the configured threshold (0 = default).
func (s *Scheduler) effectiveThreshold() int {
	if s.failureThreshold == 0 {
		return DefaultFailureAlertThreshold
	}
	return s.failureThreshold
}

// isTimeout reports whether a run error is a context-deadline timeout.
func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, context.DeadlineExceeded) ||
		strings.Contains(err.Error(), context.DeadlineExceeded.Error())
}

// updateStreakLocked records a finished (non-interrupted) run's outcome on
// job's failure streak and returns the event to emit, if any. Caller holds
// s.mu.
func (s *Scheduler) updateStreakLocked(job *Job, runErr error, now time.Time) *JobHealthEvent {
	if runErr == nil {
		st := job.FailureStreak
		job.FailureStreak = nil
		if st == nil || !st.Alerted {
			return nil
		}
		return &JobHealthEvent{
			Kind:                JobHealthRecovered,
			JobID:               job.ID,
			JobName:             job.Name,
			ConsecutiveFailures: st.Count,
			Timeouts:            st.Timeouts,
			FailingSince:        st.Since,
			At:                  now,
		}
	}

	st := job.FailureStreak
	if st == nil {
		st = &FailureStreak{Since: now}
		job.FailureStreak = st
	}
	st.Count++
	timeout := isTimeout(runErr)
	if timeout {
		st.Timeouts++
	}

	threshold := s.effectiveThreshold()
	if st.Alerted || threshold < 0 {
		return nil
	}
	if st.Count < threshold && (st.Count < 2 || now.Sub(st.Since) < longStreakAge) {
		return nil
	}
	st.Alerted = true
	return &JobHealthEvent{
		Kind:                JobHealthFailing,
		JobID:               job.ID,
		JobName:             job.Name,
		ConsecutiveFailures: st.Count,
		Timeouts:            st.Timeouts,
		FailingSince:        st.Since,
		LastError:           runErr.Error(),
		LastTimeout:         timeout,
		At:                  now,
	}
}

// failureLogRecord is one line of the failure log.
type failureLogRecord struct {
	TS                  string  `json:"ts"`
	Job                 string  `json:"job"`
	JobID               string  `json:"job_id"`
	Status              string  `json:"status"` // "error" or "interrupted"
	Error               string  `json:"error"`
	Timeout             bool    `json:"timeout,omitempty"`
	ConsecutiveFailures int     `json:"consecutive_failures,omitempty"`
	DurationSeconds     float64 `json:"duration_s"`
	Source              string  `json:"source"`
}

func newFailureLogRecord(snap *Job, runErr error, interrupted bool, streak int, elapsed time.Duration, now time.Time) *failureLogRecord {
	name := snap.Name
	if name == "" {
		name = snap.ID
	}
	status := "error"
	if interrupted {
		status = "interrupted"
	}
	return &failureLogRecord{
		TS:                  now.UTC().Format(time.RFC3339),
		Job:                 name,
		JobID:               snap.ID,
		Status:              status,
		Error:               truncate(runErr.Error(), maxLoggedErrorLen),
		Timeout:             !interrupted && isTimeout(runErr),
		ConsecutiveFailures: streak,
		DurationSeconds:     elapsed.Round(time.Millisecond).Seconds(),
		Source:              "scheduler",
	}
}

// appendFailureLog writes rec to the failure log. Best effort: errors are
// logged, never returned. Must not be called with s.mu held.
func (s *Scheduler) appendFailureLog(rec *failureLogRecord) {
	if s.failureLog == "" || rec == nil {
		return
	}
	line, err := json.Marshal(rec)
	if err != nil {
		log.Printf("[Scheduler] Failure log: marshal: %v", err)
		return
	}
	line = append(line, '\n')

	s.logMu.Lock()
	defer s.logMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.failureLog), 0o755); err != nil {
		log.Printf("[Scheduler] Failure log: %v", err)
		return
	}
	f, err := os.OpenFile(s.failureLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		log.Printf("[Scheduler] Failure log: %v", err)
		return
	}
	if _, err := f.Write(line); err != nil {
		log.Printf("[Scheduler] Failure log: write: %v", err)
	}
	if err := f.Close(); err != nil {
		log.Printf("[Scheduler] Failure log: close: %v", err)
	}
}

// emitHealthEvent hands ev to the notifier. Must not be called with s.mu held.
func (s *Scheduler) emitHealthEvent(ev *JobHealthEvent) {
	if ev == nil {
		return
	}
	switch ev.Kind {
	case JobHealthRecovered:
		log.Printf("[Scheduler] Job %s recovered after %d consecutive failure(s)", ev.JobID, ev.ConsecutiveFailures)
	default:
		log.Printf("[Scheduler] Job %s has failed %d consecutive run(s) (%d timeout(s)); alerting", ev.JobID, ev.ConsecutiveFailures, ev.Timeouts)
	}
	if s.notifier == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
	defer cancel()
	if err := s.notifier.NotifyJobHealth(ctx, *ev); err != nil {
		log.Printf("[Scheduler] Job %s: %s notice not delivered: %v", ev.JobID, ev.Kind, err)
	}
}

// JobHealthStatus is a point-in-time copy of a job's failure state.
type JobHealthStatus struct {
	ConsecutiveFailures int
	Timeouts            int
	FailingSince        *time.Time
	LastError           string
}

// JobHealth returns the failure state of every job that has a failure
// streak or a last error, keyed by job ID. Safe for concurrent use (unlike
// reading ListJobs' live pointers while a run finishes).
func (s *Scheduler) JobHealth() map[string]JobHealthStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]JobHealthStatus)
	for id, job := range s.jobs {
		if job.FailureStreak == nil && job.LastError == "" {
			continue
		}
		h := JobHealthStatus{LastError: job.LastError}
		if st := job.FailureStreak; st != nil {
			h.ConsecutiveFailures = st.Count
			h.Timeouts = st.Timeouts
			since := st.Since
			h.FailingSince = &since
		}
		out[id] = h
	}
	return out
}

// failingJobsLocked maps job ID to streak length for failing jobs. Caller
// holds s.mu.
func (s *Scheduler) failingJobsLocked() map[string]int {
	out := make(map[string]int)
	for id, job := range s.jobs {
		if n := job.ConsecutiveFailures(); n > 0 {
			out[id] = n
		}
	}
	return out
}

// truncate shortens s to at most n bytes on a rune boundary.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
