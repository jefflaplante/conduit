package scheduler

import (
	"context"
	"errors"
	"log"
	"sort"
	"time"
)

// conduit-31jg.77: graceful-drain support.
//
// The gateway's ShutdownManager used to count only interactive turns, so a
// SIGHUP deploy cancelled agent_heartbeat_main 24s into its LLM chain. The
// scheduler now exposes its in-flight runs so the drain can wait for them
// (within the shared drain budget), refuses to start new runs once draining,
// and records runs that were still going when the budget expired as
// "interrupted by shutdown" (LastError + metadata marker). Opted-in job types
// re-run once shortly after the next Start.

var (
	// ErrDraining is returned by RunNow (and makes cron ticks no-ops) once
	// BeginDrain has been called.
	ErrDraining = errors.New("scheduler is draining for shutdown")

	// ErrInterruptedByShutdown is the cancellation cause handed to running
	// jobs when the drain budget expires (or Stop is called mid-run).
	ErrInterruptedByShutdown = errors.New("interrupted by shutdown")
)

const (
	// MetaInterruptedAt (RFC3339, UTC) is set on a job whose run was
	// cancelled by shutdown; consumed and cleared by the next Start.
	MetaInterruptedAt = "interrupted_at"

	// MetaRerunOnInterrupt opts a job into one post-restart re-run after an
	// interrupted run. Heartbeat jobs are opted in by default (see
	// DefaultRerunPolicy); everything else must set this explicitly because
	// an arbitrary LLM job may already have produced side effects (messages
	// sent, files written) before it was cancelled.
	MetaRerunOnInterrupt = "rerun_on_interrupt"

	defaultRerunDelay   = 30 * time.Second
	rerunMaxAge         = 15 * time.Minute
	rerunMinGap         = 2 * time.Minute
	defaultStopRunWait  = 3 * time.Second
	interruptedErrorFmt = "interrupted by shutdown after %s: %v"
)

// RerunPolicy decides whether an interrupted job re-runs after restart.
type RerunPolicy func(job *Job) bool

// DefaultRerunPolicy re-runs heartbeat jobs (a periodic, read-mostly status
// check whose cancelled run delivered nothing; an extra run costs one LLM
// call and at worst repeats an alert) and jobs that set
// metadata.rerun_on_interrupt=true. The heartbeat test mirrors
// heartbeat.IsHeartbeatJob (heartbeat imports scheduler, so it cannot be
// called from here).
func DefaultRerunPolicy(job *Job) bool {
	if job == nil {
		return false
	}
	if v, ok := job.Metadata[MetaRerunOnInterrupt].(bool); ok {
		return v
	}
	if v, ok := job.Metadata["heartbeat"].(bool); ok && v {
		return true
	}
	return job.Command == "heartbeat"
}

// WithRerunPolicy overrides DefaultRerunPolicy.
func WithRerunPolicy(p RerunPolicy) Option {
	return func(s *Scheduler) {
		if p != nil {
			s.rerunPolicy = p
		}
	}
}

// withRerunDelay shortens the post-restart re-run delay (tests).
func withRerunDelay(d time.Duration) Option {
	return func(s *Scheduler) { s.rerunDelay = d }
}

// BeginDrain stops the scheduler from starting new runs (cron ticks are
// skipped, RunNow returns ErrDraining). In-flight runs continue. Idempotent.
func (s *Scheduler) BeginDrain() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.draining {
		s.draining = true
		log.Printf("[Scheduler] Draining: no new job runs will start (%d in flight)", len(s.running))
	}
}

// Draining reports whether BeginDrain (or Stop) has been called.
func (s *Scheduler) Draining() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.draining
}

// RunningJobs returns the IDs of jobs currently executing, sorted.
func (s *Scheduler) RunningJobs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.running))
	for id := range s.running {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// WaitIdle blocks until no job is executing or ctx is done.
func (s *Scheduler) WaitIdle(ctx context.Context) error {
	for {
		s.mu.RLock()
		n := len(s.running)
		ch := s.runChanged
		s.mu.RUnlock()
		if n == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		}
	}
}

// InterruptRunning cancels the context of every in-flight run with
// ErrInterruptedByShutdown and returns how many were running. Runs that then
// fail are recorded as interrupted. Implies BeginDrain.
func (s *Scheduler) InterruptRunning() int {
	s.BeginDrain()
	s.mu.RLock()
	n := len(s.running)
	for id := range s.running {
		log.Printf("[Scheduler] Drain budget exhausted: cancelling running job %s (interrupted by shutdown)", id)
	}
	s.mu.RUnlock()
	s.cancel(ErrInterruptedByShutdown)
	return n
}

// notifyRunChangedLocked wakes WaitIdle callers. Caller holds s.mu.
func (s *Scheduler) notifyRunChangedLocked() {
	close(s.runChanged)
	s.runChanged = make(chan struct{})
}

// scheduleInterruptedReruns consumes MetaInterruptedAt markers left by the
// previous process and, for jobs the rerun policy accepts, triggers one run
// after rerunDelay. Skipped when the interruption is stale (> rerunMaxAge,
// e.g. the gateway was down for hours) or the regular schedule fires soon
// anyway. Caller must not hold s.mu.
func (s *Scheduler) scheduleInterruptedReruns() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	cleared := false
	for id, job := range s.jobs {
		raw, ok := job.Metadata[MetaInterruptedAt]
		if !ok {
			continue
		}
		delete(job.Metadata, MetaInterruptedAt)
		cleared = true

		at, err := time.Parse(time.RFC3339, toString(raw))
		switch {
		case err != nil:
			log.Printf("[Scheduler] Job %s: ignoring unparseable %s=%v", id, MetaInterruptedAt, raw)
			continue
		case !job.Enabled || job.Type != JobTypeGo:
			continue
		case !s.rerunPolicy(job):
			log.Printf("[Scheduler] Job %s was interrupted by shutdown at %s; not re-running (not opted in via %s)", id, at.Format(time.RFC3339), MetaRerunOnInterrupt)
			continue
		case now.Sub(at) > rerunMaxAge:
			log.Printf("[Scheduler] Job %s was interrupted by shutdown at %s; too long ago to re-run", id, at.Format(time.RFC3339))
			continue
		case job.NextRun != nil && job.NextRun.Before(now.Add(s.rerunDelay+rerunMinGap)):
			log.Printf("[Scheduler] Job %s was interrupted by shutdown; next regular run %s is soon, not re-running", id, job.NextRun.Format(time.RFC3339))
			continue
		}

		log.Printf("[Scheduler] Job %s was interrupted by shutdown at %s; re-running once in %s", id, at.Format(time.RFC3339), s.rerunDelay)
		jobID := id
		t := time.AfterFunc(s.rerunDelay, func() {
			if s.ctx.Err() != nil {
				return
			}
			s.executeJob(jobID) // honours draining and the running flag
		})
		s.rerunTimers = append(s.rerunTimers, t)
	}
	if cleared {
		if err := s.saveJobs(); err != nil {
			log.Printf("[Scheduler] Warning: failed to persist cleared interruption markers: %v", err)
		}
	}
}

func toString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
