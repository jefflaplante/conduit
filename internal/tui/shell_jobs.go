package tui

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"conduit/internal/procutil"

	tea "github.com/charmbracelet/bubbletea"
)

// JobStatus represents the status of a background job
type JobStatus int

const (
	JobRunning JobStatus = iota
	JobCompleted
	JobFailed
	JobCancelled
)

func (s JobStatus) String() string {
	switch s {
	case JobRunning:
		return "running"
	case JobCompleted:
		return "completed"
	case JobFailed:
		return "failed"
	case JobCancelled:
		return "cancelled"
	default:
		return "unknown"
	}
}

// BackgroundJob represents a job running in the background
type BackgroundJob struct {
	ID         int
	Command    string
	Status     JobStatus
	StartTime  time.Time
	EndTime    time.Time
	Output     *procutil.CappedBuffer // bounded, concurrency-safe (conduit-31jg.69)
	Error      error
	cancel     context.CancelFunc
	mu         sync.Mutex
	outputDone chan struct{} // signals when output collection is complete
}

// JobManager manages background jobs for a session
type JobManager struct {
	jobs     map[int]*BackgroundJob
	nextID   int32 // atomic counter
	mu       sync.RWMutex
	jobsDone chan int // channel to signal when a job completes
}

// NewJobManager creates a new job manager
func NewJobManager() *JobManager {
	return &JobManager{
		jobs:     make(map[int]*BackgroundJob),
		jobsDone: make(chan int, 10),
	}
}

// AddJob adds a new job and returns its ID
func (jm *JobManager) AddJob(cmd string, cancel context.CancelFunc) *BackgroundJob {
	id := int(atomic.AddInt32(&jm.nextID, 1))
	job := &BackgroundJob{
		ID:         id,
		Command:    cmd,
		Status:     JobRunning,
		StartTime:  time.Now(),
		Output:     procutil.NewCappedBuffer(MaxOutputBytes),
		cancel:     cancel,
		outputDone: make(chan struct{}),
	}
	jm.mu.Lock()
	jm.jobs[id] = job
	jm.mu.Unlock()
	return job
}

// GetJob returns a job by ID
func (jm *JobManager) GetJob(id int) *BackgroundJob {
	jm.mu.RLock()
	defer jm.mu.RUnlock()
	return jm.jobs[id]
}

// ListJobs returns all jobs (for display)
func (jm *JobManager) ListJobs() []*BackgroundJob {
	jm.mu.RLock()
	defer jm.mu.RUnlock()
	jobs := make([]*BackgroundJob, 0, len(jm.jobs))
	for _, job := range jm.jobs {
		jobs = append(jobs, job)
	}
	return jobs
}

// CancelJob cancels a running job
func (jm *JobManager) CancelJob(id int) error {
	jm.mu.RLock()
	job := jm.jobs[id]
	jm.mu.RUnlock()

	if job == nil {
		return fmt.Errorf("no such job: %d", id)
	}
	if job.Status != JobRunning {
		return fmt.Errorf("job %d is not running", id)
	}
	if job.cancel != nil {
		job.cancel()
	}
	return nil
}

// MarkComplete marks a job as complete
func (jm *JobManager) MarkComplete(id int, status JobStatus, err error) {
	jm.mu.Lock()
	if job, ok := jm.jobs[id]; ok {
		job.mu.Lock()
		job.Status = status
		job.EndTime = time.Now()
		job.Error = err
		job.mu.Unlock()
	}
	jm.mu.Unlock()
	// Signal completion
	select {
	case jm.jobsDone <- id:
	default:
	}
}

// CleanupOldJobs removes completed jobs older than JobCleanupAge
func (jm *JobManager) CleanupOldJobs() {
	cutoff := time.Now().Add(-JobCleanupAge)
	jm.mu.Lock()
	defer jm.mu.Unlock()
	for id, job := range jm.jobs {
		if job.Status != JobRunning && job.EndTime.Before(cutoff) {
			delete(jm.jobs, id)
		}
	}
}

// IsBackgroundCommand checks if the command should run in the background (ends with &)
func IsBackgroundCommand(cmdLine string) (bool, string) {
	cmdLine = strings.TrimSpace(cmdLine)
	if strings.HasSuffix(cmdLine, "&") {
		// Remove the trailing & and any extra whitespace
		cmd := strings.TrimSpace(strings.TrimSuffix(cmdLine, "&"))
		return true, cmd
	}
	return false, cmdLine
}

// IsJobsCommand checks if the command is the 'jobs' builtin
func IsJobsCommand(cmdLine string) bool {
	return strings.TrimSpace(cmdLine) == "jobs"
}

// IsKillCommand checks if the command is a 'kill %N' command
// Returns (isKill, jobID) where jobID is 0 if parsing failed
func IsKillCommand(cmdLine string) (bool, int) {
	cmdLine = strings.TrimSpace(cmdLine)

	// Must start with "kill " followed by something
	if !strings.HasPrefix(cmdLine, "kill ") {
		return false, 0
	}

	// Get the rest after "kill "
	rest := strings.TrimSpace(cmdLine[5:])

	// Must have % job reference
	if !strings.HasPrefix(rest, "%") {
		return false, 0
	}

	// Extract the number
	idStr := strings.TrimPrefix(rest, "%")
	idStr = strings.TrimSpace(idStr)

	// Must be a positive integer
	id, err := strconv.Atoi(idStr)
	if err != nil || id <= 0 {
		return true, 0 // It's a kill %X command but malformed
	}

	return true, id
}

// FormatJobsList returns a formatted list of jobs for display
func (s *ShellState) FormatJobsList() string {
	if s.Jobs == nil {
		return "No jobs"
	}
	jobs := s.Jobs.ListJobs()
	if len(jobs) == 0 {
		return "No jobs"
	}
	var sb strings.Builder
	for _, job := range jobs {
		job.mu.Lock()
		status := job.Status.String()
		duration := ""
		if job.Status == JobRunning {
			duration = fmt.Sprintf(" (running for %s)", time.Since(job.StartTime).Round(time.Second))
		} else {
			duration = fmt.Sprintf(" (took %s)", job.EndTime.Sub(job.StartTime).Round(time.Second))
		}
		job.mu.Unlock()
		sb.WriteString(fmt.Sprintf("[%d] %s%s  %s\n", job.ID, status, duration, truncateCommand(job.Command, 50)))
	}
	return strings.TrimRight(sb.String(), "\n")
}

// truncateCommand truncates a command string for display
func truncateCommand(cmd string, maxLen int) string {
	if len(cmd) <= maxLen {
		return cmd
	}
	return cmd[:maxLen-3] + "..."
}

// BackgroundJobStartedMsg signals that a background job was started
type BackgroundJobStartedMsg struct {
	SessionKey string
	JobID      int
	Command    string
}

// BackgroundJobCompletedMsg signals that a background job completed
type BackgroundJobCompletedMsg struct {
	SessionKey string
	JobID      int
	Status     JobStatus
	Output     string
	Error      error
}

// executeBackgroundCmd runs a command in the background and tracks it in the job manager
func executeBackgroundCmd(sessionKey, cmdLine, workDir string, jobs *JobManager) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		job := jobs.AddJob(cmdLine, cancel)

		// Start a goroutine to run the command
		go func() {
			cmd := exec.CommandContext(ctx, "sh", "-c", cmdLine)
			cmd.Dir = workDir
			procutil.ConfigureGroupKill(cmd, 0, 0) // conduit-31jg.20: kill the whole group on timeout/cancel

			// conduit-31jg.69: bounded output, and Wait (bounded by
			// WaitDelay) instead of readers that a lingering grandchild
			// could block forever.
			cmd.Stdout = job.Output
			cmd.Stderr = job.Output

			if err := cmd.Start(); err != nil {
				fmt.Fprintf(job.Output, "Error starting command: %v\n", err)
				close(job.outputDone)
				jobs.MarkComplete(job.ID, JobFailed, err)
				return
			}

			err := cmd.Wait()
			close(job.outputDone)
			if procutil.IsWaitDelayOnly(err) {
				err = nil
			}
			if ctx.Err() == context.Canceled {
				jobs.MarkComplete(job.ID, JobCancelled, nil)
			} else if err != nil {
				jobs.MarkComplete(job.ID, JobFailed, err)
			} else {
				jobs.MarkComplete(job.ID, JobCompleted, nil)
			}
		}()

		return BackgroundJobStartedMsg{
			SessionKey: sessionKey,
			JobID:      job.ID,
			Command:    cmdLine,
		}
	}
}

// watchBackgroundJobs returns a tea.Cmd that waits for background job completion notifications
func watchBackgroundJobs(sessionKey string, jobs *JobManager) tea.Cmd {
	return func() tea.Msg {
		jobID := <-jobs.jobsDone
		job := jobs.GetJob(jobID)
		if job == nil {
			return nil
		}
		output := job.Output.String()
		job.mu.Lock()
		status := job.Status
		err := job.Error
		job.mu.Unlock()

		// Truncate output if needed
		truncated, _ := TruncateOutput(output, MaxOutputLines)

		return BackgroundJobCompletedMsg{
			SessionKey: sessionKey,
			JobID:      jobID,
			Status:     status,
			Output:     truncated,
			Error:      err,
		}
	}
}
