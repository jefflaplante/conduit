package scheduler

// SchedulerInterface defines the interface for job scheduling.
// This allows for mock implementations in tests.
type SchedulerInterface interface {
	// Start loads jobs and starts the scheduler
	Start() error

	// Stop stops the scheduler
	Stop()

	// AddJob adds a new job to the scheduler
	AddJob(job *Job) error

	// RemoveJob removes a job from the scheduler
	RemoveJob(jobID string) error

	// GetJob returns a job by ID
	GetJob(jobID string) (*Job, error)

	// ListJobs returns all jobs
	ListJobs() []*Job

	// EnableJob enables a job
	EnableJob(jobID string) error

	// DisableJob disables a job
	DisableJob(jobID string) error

	// RunNow executes a job immediately
	RunNow(jobID string) error

	// Status returns scheduler status
	Status() map[string]interface{}
}

// Verify that Scheduler implements SchedulerInterface
var _ SchedulerInterface = (*Scheduler)(nil)
