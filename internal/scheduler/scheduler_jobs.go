package scheduler

import (
	"fmt"
	"time"
)

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
