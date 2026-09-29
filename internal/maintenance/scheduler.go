package maintenance

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"
)

// Scheduler holds the registered maintenance tasks and runs them on demand
// (`conduit maintenance run` / `run-task`). There is no in-process schedule
// (conduit-3kgo): the cron-driven Start/Stop was never started by the
// gateway, and could not have been (a 5-field spec under cron.WithSeconds).
// To run maintenance periodically, invoke the CLI from a system timer.
// Manual runs execute immediately; there is no maintenance-window check.
type Scheduler struct {
	db     *sql.DB
	config Config
	tasks  map[string]Task
	order  []string // registration order; RunNow runs tasks in this order
	status map[string]TaskStatus
	mu     sync.RWMutex
	logger *log.Logger
}

// NewScheduler creates a new maintenance task runner.
func NewScheduler(db *sql.DB, config Config, logger *log.Logger) *Scheduler {
	if logger == nil {
		logger = log.Default()
	}

	return &Scheduler{
		db:     db,
		config: config,
		tasks:  make(map[string]Task),
		status: make(map[string]TaskStatus),
		logger: logger,
	}
}

// RegisterTask registers a maintenance task with the scheduler
func (s *Scheduler) RegisterTask(task Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	name := task.Name()
	if _, exists := s.tasks[name]; !exists {
		s.order = append(s.order, name)
	}
	s.tasks[name] = task

	// Initialize status
	s.status[name] = TaskStatus{
		Name:        name,
		Description: task.Description(),
		NextRun:     task.NextRun(),
		Enabled:     true,
	}

	s.logger.Printf("[Maintenance] Registered task: %s", name)
	return nil
}

// RunNow executes all maintenance tasks immediately, in registration order
// (session cleanup before database optimisation, so VACUUM sees the freed
// pages).
func (s *Scheduler) RunNow(ctx context.Context) error {
	s.mu.RLock()
	order := append([]string(nil), s.order...)
	tasks := make(map[string]Task, len(s.tasks))
	for name, task := range s.tasks {
		tasks[name] = task
	}
	s.mu.RUnlock()

	s.logger.Printf("[Maintenance] Running %d tasks immediately", len(tasks))

	for _, name := range order {
		s.executeTask(ctx, name, tasks[name])
	}

	return nil
}

// RunTask executes a specific maintenance task by name
func (s *Scheduler) RunTask(ctx context.Context, taskName string) error {
	s.mu.RLock()
	task, exists := s.tasks[taskName]
	s.mu.RUnlock()

	if !exists {
		return fmt.Errorf("task %s not found", taskName)
	}

	s.executeTask(ctx, taskName, task)
	return nil
}

// TaskNames returns the registered task names in registration order.
func (s *Scheduler) TaskNames() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.order...)
}

// GetStatus returns the current status of all maintenance tasks
func (s *Scheduler) GetStatus() map[string]TaskStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	status := make(map[string]TaskStatus, len(s.status))
	for name, stat := range s.status {
		status[name] = stat
	}

	return status
}

// executeTask runs a single maintenance task and updates its status
func (s *Scheduler) executeTask(ctx context.Context, name string, task Task) {
	s.logger.Printf("[Maintenance] Starting task: %s", name)

	start := time.Now()
	result := task.Execute(ctx)
	result.Duration = time.Since(start)

	// Update task status
	s.mu.Lock()
	status := s.status[name]
	status.LastRun = start
	status.NextRun = task.NextRun()
	status.LastResult = result
	s.status[name] = status
	s.mu.Unlock()

	// Log result
	if result.Success {
		s.logger.Printf("[Maintenance] Task %s completed successfully in %v: %s",
			name, result.Duration, result.Message)

		if result.RecordsProcessed > 0 {
			s.logger.Printf("[Maintenance] Task %s processed %d records",
				name, result.RecordsProcessed)
		}

		if result.SpaceReclaimed > 0 {
			s.logger.Printf("[Maintenance] Task %s reclaimed %d bytes",
				name, result.SpaceReclaimed)
		}
	} else {
		s.logger.Printf("[Maintenance] Task %s failed after %v: %s",
			name, result.Duration, result.Message)
		if result.Error != nil {
			s.logger.Printf("[Maintenance] Task %s error: %v", name, result.Error)
		}
	}
}
