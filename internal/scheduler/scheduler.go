package scheduler

import (
	"context"
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
	crontagMarker string // Marker to identify our entries in system crontab
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
		crontagMarker: "# CONDUIT-MANAGED",
	}
}

// Start loads jobs and starts the scheduler
func (s *Scheduler) Start() error {
	// Load saved jobs
	if err := s.loadJobs(); err != nil {
		log.Printf("[Scheduler] Warning: failed to load jobs: %v", err)
	}

	// Schedule all enabled Go jobs
	for _, job := range s.jobs {
		if job.Enabled && job.Type == JobTypeGo {
			if err := s.scheduleGoJob(job); err != nil {
				log.Printf("[Scheduler] Failed to schedule job %s: %v", job.ID, err)
			}
		}
	}

	// Start the cron scheduler
	s.cron.Start()
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

	// Validate cron expression
	if _, err := cron.ParseStandard(job.Schedule); err != nil {
		// Try with seconds
		if _, err := cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow).Parse(job.Schedule); err != nil {
			return fmt.Errorf("invalid cron expression: %v", err)
		}
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
	job.LastRun = &now
	job.RunCount++

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

	if err != nil {
		job.LastError = err.Error()
		log.Printf("[Scheduler] Job %s failed: %v", job.ID, err)
	} else {
		job.LastError = ""
		log.Printf("[Scheduler] Job %s completed", job.ID)
	}

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
	if job.Type == JobTypeGo && job.entryID != 0 {
		entry := s.cron.Entry(job.entryID)
		if !entry.Next.IsZero() {
			job.NextRun = &entry.Next
		}
	}

	s.mu.Lock()
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
	entry := fmt.Sprintf("%s %s %s # CONDUIT-JOB-ID:%s",
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
	marker := fmt.Sprintf("CONDUIT-JOB-ID:%s", jobID)
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

	return nil
}

// saveJobs saves jobs to disk
func (s *Scheduler) saveJobs() error {
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

	return os.WriteFile(s.jobsFile, data, 0644)
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
