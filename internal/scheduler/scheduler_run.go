package scheduler

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

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
