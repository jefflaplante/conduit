package scheduler

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
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
	cron          *cron.Cron
	jobs          map[string]*Job
	jobsFile      string
	executor      JobExecutor
	mu            sync.RWMutex
	ctx           context.Context
	cancel        context.CancelFunc
	crontagMarker string   // Marker to identify our entries in system crontab
	jobsLoaded    bool     // True after successful loadJobs; prevents saveJobs from wiping unloaded data
	lastContentHash [32]byte   // SHA-256 of last known jobs file content
	lastWriteTime   time.Time  // Timestamp of our own saveJobs() calls
}

// New creates a new scheduler
func New(workspaceDir string, executor JobExecutor) *Scheduler {
	ctx, cancel := context.WithCancel(context.Background())

	return &Scheduler{
		cron:          cron.New(cron.WithSeconds()), // Support 6-field cron (with seconds)
		jobs:          make(map[string]*Job),
		jobsFile:      filepath.Join(workspaceDir, "cron_jobs.json"),
		executor:      executor,
		ctx:           ctx,
		cancel:        cancel,
		crontagMarker: CrontabMarker,
	}
}

// normalizeSchedule converts a cron expression to the correct field count for
// the given job type. Go jobs need 6-field (with seconds) for robfig/cron;
// system jobs need 5-field (standard crontab).
func normalizeSchedule(schedule string, jobType JobType) (string, error) {
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
	var err error
	if jobType == JobTypeGo {
		_, err = cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow).Parse(schedule)
	} else {
		_, err = cron.ParseStandard(schedule)
	}
	if err != nil {
		return "", fmt.Errorf("invalid cron expression: %v", err)
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

	// Start file watcher for hot-reload
	go s.watchJobsFile()

	log.Printf("[Scheduler] Started with %d jobs (%d Go, %d system)",
		len(s.jobs), s.countByType(JobTypeGo), s.countByType(JobTypeSystem))

	return nil
}

// Stop stops the scheduler
func (s *Scheduler) Stop() {
	s.cancel()
	ctx := s.cron.Stop()
	<-ctx.Done()
	log.Printf("[Scheduler] Stopped")
}

// AddJob adds a new job
func (s *Scheduler) AddJob(job *Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()

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

// RunNow executes a job immediately
func (s *Scheduler) RunNow(jobID string) error {
	s.mu.RLock()
	job, exists := s.jobs[jobID]
	s.mu.RUnlock()

	if !exists {
		return fmt.Errorf("job %s not found", jobID)
	}

	go s.executeJob(job)
	return nil
}

// scheduleGoJob adds a job to the Go cron scheduler
func (s *Scheduler) scheduleGoJob(job *Job) error {
	// Remove existing entry if any
	if job.entryID != 0 {
		s.cron.Remove(job.entryID)
	}

	entryID, err := s.cron.AddFunc(job.Schedule, func() {
		s.executeJob(job)
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

// executeJob runs a job
func (s *Scheduler) executeJob(job *Job) {
	log.Printf("[Scheduler] Executing job: %s (%s)", job.ID, job.Name)

	now := time.Now()

	s.mu.Lock()
	job.LastRun = &now
	job.RunCount++
	s.mu.Unlock()

	var err error
	if job.Type == JobTypeGo {
		// Use the executor callback for Go jobs
		if s.executor != nil {
			err = s.executor(s.ctx, job)
		}
	} else if job.Type == JobTypeSystem {
		// System jobs are run by crontab, not us
		// This shouldn't be called for system jobs
		log.Printf("[Scheduler] Warning: executeJob called for system job %s", job.ID)
	}

	s.mu.Lock()
	if err != nil {
		job.LastError = err.Error()
		log.Printf("[Scheduler] Job %s failed: %v", job.ID, err)
	} else {
		job.LastError = ""
		log.Printf("[Scheduler] Job %s completed", job.ID)
	}
	s.mu.Unlock()

	// Handle one-shot jobs
	if job.OneShot {
		s.mu.Lock()
		if job.Type == JobTypeGo && job.entryID != 0 {
			s.cron.Remove(job.entryID)
		}
		delete(s.jobs, job.ID)
		s.saveJobs()
		s.mu.Unlock()
		log.Printf("[Scheduler] One-shot job %s removed", job.ID)
		return
	}

	// Update next run time
	s.mu.Lock()
	if job.Type == JobTypeGo && job.entryID != 0 {
		entry := s.cron.Entry(job.entryID)
		if !entry.Next.IsZero() {
			job.NextRun = &entry.Next
		}
	}
	s.saveJobs()
	s.mu.Unlock()
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

	// Add new entry
	entry := fmt.Sprintf("%s %s %s "+CrontabJobIDFormat,
		job.Schedule, job.Command, s.crontagMarker, job.ID)
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
	output, err := cmd.Output()
	if err != nil {
		// No crontab for user is okay
		if strings.Contains(err.Error(), "no crontab") {
			return []string{}, nil
		}
		return nil, err
	}

	lines := strings.Split(string(output), "\n")
	var entries []string
	for _, line := range lines {
		if line != "" {
			entries = append(entries, line)
		}
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

	for _, job := range jobs {
		s.jobs[job.ID] = job
	}

	s.jobsLoaded = true
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
	ticker := time.NewTicker(30 * time.Second)
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
			existingJob.Name != fileJob.Name {
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
