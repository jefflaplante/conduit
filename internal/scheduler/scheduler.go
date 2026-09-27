package scheduler

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
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

// splitTZPrefix separates a leading "CRON_TZ=<zone> " or "TZ=<zone> " from a
// cron expression (robfig/cron understands the prefix natively).
func splitTZPrefix(schedule string) (prefix, rest string) {
	schedule = strings.TrimSpace(schedule)
	if strings.HasPrefix(schedule, "CRON_TZ=") || strings.HasPrefix(schedule, "TZ=") {
		if i := strings.IndexAny(schedule, " \t"); i > 0 {
			return schedule[:i] + " ", strings.TrimSpace(schedule[i+1:])
		}
		return schedule, ""
	}
	return "", schedule
}

// normalizeSchedule converts a cron expression to the correct field count for
// the given job type. Go jobs need 6-field (with seconds) for robfig/cron;
// system jobs need 5-field (standard crontab).
func normalizeSchedule(schedule string, jobType JobType) (string, error) {
	// conduit-31jg.34: allow an explicit per-job zone for Go jobs.
	// conduit-31jg.74: and for system jobs, rendered DST-proof for vixie
	// cron (see crontab_tz.go); validated below.
	tzPrefix, schedule := splitTZPrefix(schedule)
	fields := strings.Fields(schedule)

	switch len(fields) {
	case 5:
		if jobType == JobTypeGo {
			// Prepend "0" seconds field for Go cron
			schedule = "0 " + schedule
		}
		// System jobs: 5-field is already correct
	case 6:
		if jobType == JobTypeSystem {
			// Strip seconds field for system crontab
			if fields[0] != "0" {
				log.Printf("[Scheduler] Warning: stripping non-zero seconds field '%s' from schedule for system job", fields[0])
			}
			schedule = strings.Join(fields[1:], " ")
		}
		// Go jobs: 6-field is already correct
	default:
		return "", fmt.Errorf("invalid cron expression: expected 5 or 6 fields, got %d", len(fields))
	}

	// Validate with the appropriate parser
	schedule = tzPrefix + schedule
	var err error
	if jobType == JobTypeGo {
		_, err = cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow).Parse(schedule)
	} else {
		_, err = cron.ParseStandard(schedule)
	}
	if err != nil {
		return "", fmt.Errorf("invalid cron expression: %v", err)
	}
	if tzPrefix != "" && jobType == JobTypeSystem {
		if _, err := renderCrontabSchedule(schedule, crontabDaemonLocation(), time.Now()); err != nil {
			return "", fmt.Errorf("invalid cron expression: %v", err)
		}
	}

	return schedule, nil
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

// AddJob adds a new job, replacing any existing job with the same ID (upsert).
// If a job with the same ID already exists its cron entry is removed before
// the new one is registered, preventing duplicate concurrent executions.
func (s *Scheduler) AddJob(job *Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Upsert: if a job with this ID already exists, unschedule it first so we
	// don't accumulate ghost cron entries in the robfig/cron runner.
	if existing, ok := s.jobs[job.ID]; ok {
		if existing.Type == JobTypeGo && existing.entryID != 0 {
			s.cron.Remove(existing.entryID)
		} else if existing.Type == JobTypeSystem {
			// Best-effort removal; ignore errors (job may already be absent).
			_ = s.removeSystemCrontab(existing)
		}
	}

	job.CreatedAt = time.Now()
	if job.Metadata == nil {
		job.Metadata = make(map[string]interface{})
	}

	// Normalize cron expression for the job type
	normalized, err := normalizeSchedule(job.Schedule, job.Type)
	if err != nil {
		return err
	}
	job.Schedule = normalized

	if job.Type == JobTypeGo {
		if err := s.scheduleGoJob(job); err != nil {
			return err
		}
	} else if job.Type == JobTypeSystem {
		if err := s.addSystemCrontab(job); err != nil {
			return err
		}
	}

	s.jobs[job.ID] = job
	return s.saveJobs()
}

// RemoveJob removes a job
func (s *Scheduler) RemoveJob(jobID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, exists := s.jobs[jobID]
	if !exists {
		return fmt.Errorf("job %s not found", jobID)
	}

	if job.Type == JobTypeGo && job.entryID != 0 {
		s.cron.Remove(job.entryID)
	} else if job.Type == JobTypeSystem {
		if err := s.removeSystemCrontab(job); err != nil {
			return err
		}
	}

	delete(s.jobs, jobID)
	return s.saveJobs()
}

// GetJob returns a job by ID
func (s *Scheduler) GetJob(jobID string) (*Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	job, exists := s.jobs[jobID]
	if !exists {
		return nil, fmt.Errorf("job %s not found", jobID)
	}
	return job, nil
}

// ListJobs returns all jobs
func (s *Scheduler) ListJobs() []*Job {
	s.mu.RLock()
	defer s.mu.RUnlock()

	jobs := make([]*Job, 0, len(s.jobs))
	for _, job := range s.jobs {
		jobs = append(jobs, job)
	}
	return jobs
}

// EnableJob enables a job
func (s *Scheduler) EnableJob(jobID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, exists := s.jobs[jobID]
	if !exists {
		return fmt.Errorf("job %s not found", jobID)
	}

	if !job.Enabled {
		job.Enabled = true

		// Normalize schedule before scheduling
		if normalized, err := normalizeSchedule(job.Schedule, job.Type); err == nil {
			job.Schedule = normalized
		}

		if job.Type == JobTypeGo {
			if err := s.scheduleGoJob(job); err != nil {
				return err
			}
		} else if job.Type == JobTypeSystem {
			if err := s.addSystemCrontab(job); err != nil {
				return err
			}
		}
	}

	return s.saveJobs()
}

// DisableJob disables a job
func (s *Scheduler) DisableJob(jobID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, exists := s.jobs[jobID]
	if !exists {
		return fmt.Errorf("job %s not found", jobID)
	}

	if job.Enabled {
		job.Enabled = false

		if job.Type == JobTypeGo && job.entryID != 0 {
			s.cron.Remove(job.entryID)
			job.entryID = 0
		} else if job.Type == JobTypeSystem {
			if err := s.removeSystemCrontab(job); err != nil {
				return err
			}
		}
	}

	return s.saveJobs()
}

// RunNow executes a job immediately. It returns ErrJobRunning if the job is
// already executing (conduit-31jg.34).
func (s *Scheduler) RunNow(jobID string) error {
	snap, err := s.beginRun(jobID)
	if err != nil {
		return err
	}
	go s.runSnapshot(snap)
	return nil
}

// scheduleGoJob adds a job to the Go cron scheduler
func (s *Scheduler) scheduleGoJob(job *Job) error {
	// Remove existing entry if any
	if job.entryID != 0 {
		s.cron.Remove(job.entryID)
	}

	// conduit-31jg.34: capture the ID, not the pointer; executeJob looks the
	// job up under s.mu and runs a snapshot.
	jobID := job.ID
	entryID, err := s.cron.AddFunc(job.Schedule, func() {
		s.executeJob(jobID)
	})
	if err != nil {
		return fmt.Errorf("failed to schedule job: %v", err)
	}

	job.entryID = entryID

	// Calculate next run time
	entry := s.cron.Entry(entryID)
	if !entry.Next.IsZero() {
		job.NextRun = &entry.Next
	}

	log.Printf("[Scheduler] Scheduled Go job: %s (%s) - next run: %v", job.ID, job.Name, job.NextRun)
	return nil
}

// snapshotJob returns a copy of job that the executor may read without
// holding s.mu. Caller must hold s.mu.
func snapshotJob(job *Job) *Job {
	c := *job
	if job.Skills != nil {
		c.Skills = append([]string(nil), job.Skills...)
	}
	if job.Metadata != nil {
		c.Metadata = make(map[string]interface{}, len(job.Metadata))
		for k, v := range job.Metadata {
			c.Metadata[k] = v
		}
	}
	if job.LastRun != nil {
		t := *job.LastRun
		c.LastRun = &t
	}
	if job.NextRun != nil {
		t := *job.NextRun
		c.NextRun = &t
	}
	return &c
}

// beginRun marks jobID running and records the run start, returning a
// snapshot for the executor. conduit-31jg.34: the executor used to receive
// the live *Job, which reloadFromData mutates under s.mu (data race), and
// nothing stopped overlapping runs of a slow job.
func (s *Scheduler) beginRun(jobID string) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, exists := s.jobs[jobID]
	if !exists {
		return nil, fmt.Errorf("job %s not found", jobID)
	}
	if s.draining { // conduit-31jg.77: no new runs once shutdown drain began
		return nil, ErrDraining
	}
	if s.running[jobID] {
		return nil, ErrJobRunning
	}
	s.running[jobID] = true
	now := time.Now()
	job.LastRun = &now
	job.RunCount++
	return snapshotJob(job), nil
}

// executeJob is the cron entry point: it runs the job unless a previous run
// is still in progress (SkipIfStillRunning semantics, shared with RunNow).
func (s *Scheduler) executeJob(jobID string) {
	snap, err := s.beginRun(jobID)
	if err != nil {
		if errors.Is(err, ErrJobRunning) {
			log.Printf("[Scheduler] Skipping job %s: previous run still in progress", jobID)
		} else if errors.Is(err, ErrDraining) {
			log.Printf("[Scheduler] Skipping job %s: scheduler draining for shutdown", jobID)
		}
		return
	}
	s.runSnapshot(snap)
}

// runSnapshot executes a snapshot taken by beginRun and records the outcome
// on the live job (if it still exists).
func (s *Scheduler) runSnapshot(snap *Job) {
	log.Printf("[Scheduler] Executing job: %s (%s)", snap.ID, snap.Name)
	started := time.Now()

	var err error
	if snap.Type == JobTypeGo {
		if s.executor != nil {
			err = s.executor(s.ctx, snap)
		}
	} else if snap.Type == JobTypeSystem {
		// System jobs are run by crontab, not us
		log.Printf("[Scheduler] Warning: executeJob called for system job %s", snap.ID)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, snap.ID)
	s.notifyRunChangedLocked() // conduit-31jg.77: wake WaitIdle

	// conduit-31jg.77: a run that failed after the scheduler context was
	// cancelled (drain budget expired / Stop) was interrupted by shutdown,
	// not a genuine job failure; make that visible.
	interrupted := err != nil && s.ctx.Err() != nil
	elapsed := time.Since(started).Round(time.Second)
	switch {
	case interrupted:
		log.Printf("[Scheduler] Job %s interrupted by shutdown after %s: %v", snap.ID, elapsed, err)
	case err != nil:
		log.Printf("[Scheduler] Job %s failed: %v", snap.ID, err)
	default:
		log.Printf("[Scheduler] Job %s completed", snap.ID)
	}

	job, exists := s.jobs[snap.ID]
	if !exists {
		return // removed while running
	}
	switch {
	case interrupted:
		job.LastError = fmt.Sprintf(interruptedErrorFmt, elapsed, err)
		if job.Metadata == nil {
			job.Metadata = make(map[string]interface{})
		}
		job.Metadata[MetaInterruptedAt] = time.Now().UTC().Format(time.RFC3339)
	case err != nil:
		job.LastError = err.Error()
	default:
		job.LastError = ""
	}

	// Handle one-shot jobs. conduit-31jg.77: an interrupted one-shot that is
	// opted into re-run is kept so the next Start can run it.
	if job.OneShot && !(interrupted && s.rerunPolicy(job)) {
		if job.Type == JobTypeGo && job.entryID != 0 {
			s.cron.Remove(job.entryID)
		}
		delete(s.jobs, job.ID)
		s.saveJobs()
		log.Printf("[Scheduler] One-shot job %s removed", job.ID)
		return
	}

	// Update next run time
	if job.Type == JobTypeGo && job.entryID != 0 {
		entry := s.cron.Entry(job.entryID)
		if !entry.Next.IsZero() {
			job.NextRun = &entry.Next
		}
	}
	s.saveJobs()
}

// addSystemCrontab adds a job to the system crontab
func (s *Scheduler) addSystemCrontab(job *Job) error {
	// Get current crontab
	entries, err := s.readSystemCrontab()
	if err != nil {
		return err
	}

	// Remove any existing entry for this job
	entries = s.filterCrontabEntries(entries, job.ID)

	// Add new entry (conduit-31jg.74: CRON_TZ schedules get a zone guard)
	entry, err := s.crontabLine(job)
	if err != nil {
		return err
	}
	entries = append(entries, entry)

	// Write back
	return s.writeSystemCrontab(entries)
}

// removeSystemCrontab removes a job from the system crontab
func (s *Scheduler) removeSystemCrontab(job *Job) error {
	entries, err := s.readSystemCrontab()
	if err != nil {
		return err
	}

	entries = s.filterCrontabEntries(entries, job.ID)
	return s.writeSystemCrontab(entries)
}

// readSystemCrontab reads the current user's crontab
func (s *Scheduler) readSystemCrontab() ([]string, error) {
	cmd := exec.Command("crontab", "-l")
	output, err := cmd.CombinedOutput()
	if err != nil {
		// No crontab for user is okay — the message may appear in stderr
		// (captured via CombinedOutput) or in the error string itself.
		combined := string(output) + " " + err.Error()
		if strings.Contains(strings.ToLower(combined), "no crontab") {
			return []string{}, nil
		}
		// Also treat a generic exit status 1 with no stdout as "no crontab"
		// since some crontab implementations don't emit a descriptive message.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 && len(strings.TrimSpace(string(output))) == 0 {
			return []string{}, nil
		}
		return nil, fmt.Errorf("failed to read crontab: %w (output: %s)", err, strings.TrimSpace(string(output)))
	}

	lines := strings.Split(string(output), "\n")
	var entries []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Skip empty lines and stderr noise from CombinedOutput
		if trimmed == "" || strings.HasPrefix(strings.ToLower(trimmed), "crontab:") {
			continue
		}
		entries = append(entries, line)
	}
	return entries, nil
}

// writeSystemCrontab writes entries to the user's crontab
func (s *Scheduler) writeSystemCrontab(entries []string) error {
	content := strings.Join(entries, "\n")
	if len(entries) > 0 {
		content += "\n"
	}

	cmd := exec.Command("crontab", "-")
	cmd.Stdin = strings.NewReader(content)
	return cmd.Run()
}

// filterCrontabEntries removes entries for a specific job ID
func (s *Scheduler) filterCrontabEntries(entries []string, jobID string) []string {
	marker := fmt.Sprintf(CrontabJobIDFormat, jobID)
	var filtered []string
	for _, entry := range entries {
		if !strings.Contains(entry, marker) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

// loadJobs loads jobs from disk
func (s *Scheduler) loadJobs() error {
	data, err := os.ReadFile(s.jobsFile)
	if err != nil {
		if os.IsNotExist(err) {
			s.jobsLoaded = true // No file yet is a valid initial state
			return nil
		}
		return err
	}

	var jobs []*Job
	if err := json.Unmarshal(data, &jobs); err != nil {
		return err
	}

	now := time.Now()
	fastForwarded := false
	for _, job := range jobs {
		// Fast-forward past-due NextRun values so jobs aren't stuck after a restart.
		if job.NextRun != nil && job.NextRun.Before(now) {
			// Determine the correct parser for this job type.
			var schedule cron.Schedule
			var parseErr error
			if job.Type == JobTypeGo {
				schedule, parseErr = cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow).Parse(job.Schedule)
			} else {
				schedule, parseErr = cron.ParseStandard(job.Schedule)
			}
			if parseErr != nil {
				log.Printf("[Scheduler] Job %s: cannot fast-forward past-due next_run (unparseable schedule %q): %v — skipping", job.ID, job.Schedule, parseErr)
			} else {
				oldNextRun := *job.NextRun
				newNextRun := schedule.Next(now.In(s.location)) // conduit-31jg.34
				job.NextRun = &newNextRun
				log.Printf("[Scheduler] Job %s: fast-forwarded past-due next_run from %v to %v", job.ID, oldNextRun.Format(time.RFC3339), newNextRun.Format(time.RFC3339))
				fastForwarded = true
			}
		}
		s.jobs[job.ID] = job
	}

	s.jobsLoaded = true

	// Persist corrected next_run values so subsequent restarts don't repeat warnings.
	if fastForwarded {
		if err := s.saveJobs(); err != nil {
			log.Printf("[Scheduler] Warning: failed to persist fast-forwarded next_run values: %v", err)
		}
	}

	return nil
}

// saveJobs saves jobs to disk
func (s *Scheduler) saveJobs() error {
	if !s.jobsLoaded {
		return fmt.Errorf("refusing to save: jobs were not loaded from disk (would wipe existing data)")
	}

	if err := os.MkdirAll(filepath.Dir(s.jobsFile), 0755); err != nil {
		return err
	}

	jobs := make([]*Job, 0, len(s.jobs))
	for _, job := range s.jobs {
		jobs = append(jobs, job)
	}

	data, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		return err
	}

	// Atomic write: temp file + rename to prevent corruption on crash
	tmpFile := s.jobsFile + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmpFile, s.jobsFile); err != nil {
		return err
	}

	// Update self-write tracking so the file watcher skips this change
	s.lastWriteTime = time.Now()
	s.lastContentHash = sha256.Sum256(data)
	return nil
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

// watchJobsFile periodically checks cron_jobs.json for external edits.
func (s *Scheduler) watchJobsFile() {
	defer s.wg.Done()
	defer func() {
		// Optional test hook: signal exit so tests can assert synchronous
		// shutdown without polling. Non-blocking send so a missing/closed
		// channel can never deadlock production callers.
		if s.watchExited != nil {
			select {
			case s.watchExited <- struct{}{}:
			default:
			}
		}
	}()

	interval := s.watchInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.checkAndReload()
		}
	}
}

// checkAndReload reads the jobs file and reloads if the content hash changed.
func (s *Scheduler) checkAndReload() {
	data, err := os.ReadFile(s.jobsFile)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[Scheduler] Failed to read jobs file for reload: %v", err)
		}
		return
	}

	hash := sha256.Sum256(data)

	s.mu.Lock()
	if hash == s.lastContentHash {
		s.mu.Unlock()
		return
	}

	// Self-write cooldown: skip reload if we wrote recently, but update hash
	if time.Since(s.lastWriteTime) < 5*time.Second {
		s.lastContentHash = hash
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	if err := s.reloadFromData(data, hash); err != nil {
		log.Printf("[Scheduler] Failed to reload jobs: %v", err)
	}
}

// ReloadJobs forces an immediate reload of jobs from cron_jobs.json, skipping
// the self-write cooldown. Returns an error if the file cannot be read or parsed.
func (s *Scheduler) ReloadJobs() error {
	data, err := os.ReadFile(s.jobsFile)
	if err != nil {
		return fmt.Errorf("failed to read jobs file: %w", err)
	}

	hash := sha256.Sum256(data)
	return s.reloadFromData(data, hash)
}

// reloadFromData diffs file content against in-memory jobs and applies changes.
func (s *Scheduler) reloadFromData(data []byte, hash [32]byte) error {
	var fileJobs []*Job
	if err := json.Unmarshal(data, &fileJobs); err != nil {
		return fmt.Errorf("failed to parse jobs file: %w", err)
	}

	fileJobMap := make(map[string]*Job, len(fileJobs))
	for _, job := range fileJobs {
		fileJobMap[job.ID] = job
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var added, removed, modified int

	// Remove jobs no longer in the file
	for id, job := range s.jobs {
		if _, exists := fileJobMap[id]; !exists {
			if job.Type == JobTypeGo && job.entryID != 0 {
				s.cron.Remove(job.entryID)
			}
			delete(s.jobs, id)
			removed++
		}
	}

	// Add new jobs and update modified ones
	for id, fileJob := range fileJobMap {
		normalized, err := normalizeSchedule(fileJob.Schedule, fileJob.Type)
		if err != nil {
			log.Printf("[Scheduler] Skipping job %s during reload: %v", id, err)
			continue
		}
		fileJob.Schedule = normalized

		existingJob, exists := s.jobs[id]
		if !exists {
			// New job
			if fileJob.Metadata == nil {
				fileJob.Metadata = make(map[string]interface{})
			}
			if fileJob.Enabled && fileJob.Type == JobTypeGo {
				if err := s.scheduleGoJob(fileJob); err != nil {
					log.Printf("[Scheduler] Failed to schedule reloaded job %s: %v", id, err)
					continue
				}
			}
			s.jobs[id] = fileJob
			added++
		} else if existingJob.Schedule != fileJob.Schedule ||
			existingJob.Command != fileJob.Command ||
			existingJob.Enabled != fileJob.Enabled ||
			existingJob.Type != fileJob.Type ||
			existingJob.Name != fileJob.Name ||
			existingJob.Target != fileJob.Target ||
			existingJob.Model != fileJob.Model ||
			existingJob.OneShot != fileJob.OneShot ||
			!reflect.DeepEqual(existingJob.Skills, fileJob.Skills) {
			// Modified — unschedule old, update fields, reschedule
			if existingJob.Type == JobTypeGo && existingJob.entryID != 0 {
				s.cron.Remove(existingJob.entryID)
				existingJob.entryID = 0
			}

			// Preserve runtime fields (entryID, LastRun, RunCount, LastError, NextRun)
			existingJob.Schedule = fileJob.Schedule
			existingJob.Command = fileJob.Command
			existingJob.Enabled = fileJob.Enabled
			existingJob.Type = fileJob.Type
			existingJob.Name = fileJob.Name
			existingJob.Model = fileJob.Model
			existingJob.Target = fileJob.Target
			existingJob.OneShot = fileJob.OneShot
			existingJob.Skills = fileJob.Skills

			if existingJob.Enabled && existingJob.Type == JobTypeGo {
				if err := s.scheduleGoJob(existingJob); err != nil {
					log.Printf("[Scheduler] Failed to reschedule job %s: %v", id, err)
				}
			}
			modified++
		}
	}

	s.lastContentHash = hash

	if added > 0 || removed > 0 || modified > 0 {
		log.Printf("[Scheduler] Reloaded jobs from file: %d added, %d removed, %d modified", added, removed, modified)
	}

	return nil
}
