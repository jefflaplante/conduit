package scheduler

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// JobType indicates whether the job runs in-process or via system crontab
type JobType string

const (
	JobTypeGo     JobType = "go"     // In-process, can spawn sub-agents
	JobTypeSystem JobType = "system" // System crontab, runs scripts

	CrontabMarker      = "# CONDUIT-MANAGED"
	CrontabJobIDFormat = "CONDUIT-JOB-ID:%s"
)

// JobExecutor is called when a Go job fires
type JobExecutor func(ctx context.Context, job *Job) error

// Job represents a scheduled job
type Job struct {
	ID        string                 `json:"id"`
	Name      string                 `json:"name,omitempty"`
	Schedule  string                 `json:"schedule"`         // Cron expression (5 or 6 fields)
	Type      JobType                `json:"type"`             // "go" or "system"
	Command   string                 `json:"command"`          // For system: shell command. For go: prompt/task
	Model     string                 `json:"model,omitempty"`  // For go jobs: AI model to use
	Target    string                 `json:"target,omitempty"` // Channel/session to send output
	Enabled   bool                   `json:"enabled"`
	OneShot   bool                   `json:"oneshot,omitempty"`
	Skills    []string               `json:"skills,omitempty"` // Skill names to load (empty = all)
	CreatedAt time.Time              `json:"created_at"`
	LastRun   *time.Time             `json:"last_run,omitempty"`
	NextRun   *time.Time             `json:"next_run,omitempty"`
	RunCount  int                    `json:"run_count"`
	LastError string                 `json:"last_error,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`

	// Internal: cron entry ID for Go jobs
	entryID cron.EntryID
}

// Scheduler manages both Go cron and system crontab jobs
type Scheduler struct {
	cron            *cron.Cron
	jobs            map[string]*Job
	jobsFile        string
	executor        JobExecutor
	mu              sync.RWMutex
	ctx             context.Context
	cancel          context.CancelCauseFunc
	crontagMarker   string    // Marker to identify our entries in system crontab
	jobsLoaded      bool      // True after successful loadJobs; prevents saveJobs from wiping unloaded data
	lastContentHash [32]byte  // SHA-256 of last known jobs file content
	lastWriteTime   time.Time // Timestamp of our own saveJobs() calls

	// wg tracks long-running goroutines (currently watchJobsFile) so Stop()
	// can wait for them to exit before returning. Without this, callers that
	// re-initialise the scheduler immediately after Stop() can collide with
	// a still-running file-read.
	wg sync.WaitGroup

	// watchInterval is the period between watchJobsFile polls. Defaults to
	// 30s in New(); tests may override it for fast shutdown verification.
	watchInterval time.Duration

	// watchExited is an optional channel that watchJobsFile sends on right
	// before returning. Used by tests to assert synchronous shutdown.
	watchExited chan struct{}

	// conduit-31jg.34: location cron expressions are evaluated in (default
	// time.Local), and the set of job IDs currently executing (guarded by mu)
	// so neither cron ticks nor RunNow start a second concurrent run.
	location *time.Location
	running  map[string]bool

	// conduit-31jg.77: drain support (see drain.go); cancel is now a
	// CancelCauseFunc so shutdown can cancel with ErrInterruptedByShutdown.
	// draining (guarded by mu)
	// blocks new runs; runChanged is closed and replaced whenever a run ends
	// so WaitIdle can block without polling.
	draining    bool
	runChanged  chan struct{}
	rerunPolicy RerunPolicy
	rerunDelay  time.Duration
	rerunTimers []*time.Timer
	stopRunWait time.Duration
}

// ErrJobRunning is returned by RunNow when the job is already executing.
var ErrJobRunning = errors.New("job is already running")

// Option configures a Scheduler.
type Option func(*Scheduler)

// WithLocation evaluates Go-job cron expressions in loc instead of
// time.Local. System (crontab) jobs are unaffected: the cron daemon uses its
// own zone. A per-job "CRON_TZ=<zone> " prefix overrides either.
func WithLocation(loc *time.Location) Option {
	return func(s *Scheduler) {
		if loc != nil {
			s.location = loc
		}
	}
}

// New creates a new scheduler
func New(workspaceDir string, executor JobExecutor, opts ...Option) *Scheduler {
	ctx, cancel := context.WithCancelCause(context.Background())

	s := &Scheduler{
		jobs:          make(map[string]*Job),
		jobsFile:      filepath.Join(workspaceDir, "cron_jobs.json"),
		executor:      executor,
		ctx:           ctx,
		cancel:        cancel,
		crontagMarker: CrontabMarker,
		watchInterval: 30 * time.Second,
		location:      time.Local,
		running:       make(map[string]bool),
		runChanged:    make(chan struct{}),
		rerunPolicy:   DefaultRerunPolicy,
		rerunDelay:    defaultRerunDelay,
		stopRunWait:   defaultStopRunWait,
	}
	for _, opt := range opts {
		opt(s)
	}
	// Support 6-field cron (with seconds), evaluated in s.location.
	s.cron = cron.New(cron.WithSeconds(), cron.WithLocation(s.location))
	return s
}

// Location returns the zone Go-job cron expressions are evaluated in.
func (s *Scheduler) Location() *time.Location {
	return s.location
}

// Start loads jobs and starts the scheduler
func (s *Scheduler) Start() error {
	// Load saved jobs — failure is fatal to prevent saveJobs from wiping unloaded data
	if err := s.loadJobs(); err != nil {
		return fmt.Errorf("failed to load jobs: %w", err)
	}

	// Initialize content hash from current file
	if data, err := os.ReadFile(s.jobsFile); err == nil {
		s.lastContentHash = sha256.Sum256(data)
	}

	// Normalize and schedule all enabled Go jobs
	for _, job := range s.jobs {
		if normalized, err := normalizeSchedule(job.Schedule, job.Type); err != nil {
			log.Printf("[Scheduler] Job %s has invalid schedule %q: %v", job.ID, job.Schedule, err)
		} else {
			job.Schedule = normalized
		}
		if job.Enabled && job.Type == JobTypeGo {
			if err := s.scheduleGoJob(job); err != nil {
				log.Printf("[Scheduler] Failed to schedule job %s: %v", job.ID, err)
			}
		}
	}

	// Start the cron scheduler
	s.cron.Start()

	// conduit-31jg.77: re-run (once) opted-in jobs the previous process
	// interrupted at shutdown.
	s.scheduleInterruptedReruns()

	// Start file watcher for hot-reload. wg.Add must happen before the go
	// statement so Stop() cannot race past Wait() before the goroutine
	// registers itself.
	s.wg.Add(1)
	go s.watchJobsFile()

	log.Printf("[Scheduler] Started with %d jobs (%d Go, %d system)",
		len(s.jobs), s.countByType(JobTypeGo), s.countByType(JobTypeSystem))

	return nil
}

// Stop stops the scheduler. It cancels the scheduler context, drains the cron
// runner, and waits synchronously for the watchJobsFile goroutine to exit so
// callers may safely re-initialise the scheduler immediately after Stop()
// returns without colliding with an in-flight file-read.
//
// conduit-31jg.77: Stop drains first (no new runs), cancels in-flight runs
// with ErrInterruptedByShutdown so they are recorded as interrupted, and
// waits at most stopRunWait for them (cron-started and RunNow alike) instead
// of blocking forever on a job that ignores cancellation. The graceful wait
// for running jobs happens earlier, in the gateway's drain phase.
func (s *Scheduler) Stop() {
	s.BeginDrain()
	s.mu.Lock()
	for _, t := range s.rerunTimers {
		t.Stop()
	}
	s.rerunTimers = nil
	s.mu.Unlock()

	s.cancel(ErrInterruptedByShutdown)
	cronDone := s.cron.Stop()

	waitCtx, cancel := context.WithTimeout(context.Background(), s.stopRunWait)
	defer cancel()
	select {
	case <-cronDone.Done():
	case <-waitCtx.Done():
	}
	if err := s.WaitIdle(waitCtx); err != nil {
		log.Printf("[Scheduler] Stop: jobs still running after %s, abandoning: %v", s.stopRunWait, s.RunningJobs())
	}
	s.wg.Wait()
	log.Printf("[Scheduler] Stopped")
}

// countByType counts jobs by type
func (s *Scheduler) countByType(jobType JobType) int {
	count := 0
	for _, job := range s.jobs {
		if job.Type == jobType {
			count++
		}
	}
	return count
}

// Status returns scheduler status
func (s *Scheduler) Status() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return map[string]interface{}{
		"total_jobs":   len(s.jobs),
		"go_jobs":      s.countByType(JobTypeGo),
		"system_jobs":  s.countByType(JobTypeSystem),
		"cron_entries": len(s.cron.Entries()),
	}
}
