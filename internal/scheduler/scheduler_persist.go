package scheduler

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/robfig/cron/v3"
)

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
