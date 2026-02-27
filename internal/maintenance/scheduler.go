package maintenance

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// Scheduler manages and executes maintenance tasks on a schedule
type Scheduler struct {
	db      *sql.DB
	config  Config
	cron    *cron.Cron
	tasks   map[string]Task
	status  map[string]TaskStatus
	mu      sync.RWMutex
	running bool
	logger  *log.Logger
}

// NewScheduler creates a new maintenance scheduler
func NewScheduler(db *sql.DB, config Config, logger *log.Logger) *Scheduler {
	if logger == nil {
		logger = log.Default()
	}

	return &Scheduler{
		db:     db,
		config: config,
		cron:   cron.New(cron.WithSeconds()),
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
	s.tasks[name] = task

	// Initialize status
	s.status[name] = TaskStatus{
		Name:        name,
		Description: task.Description(),
		NextRun:     task.NextRun(),
		Enabled:     true,
		Schedule:    s.config.Schedule,
	}

	s.logger.Printf("[Maintenance] Registered task: %s", name)
	return nil
}

// Start begins the maintenance scheduler
func (s *Scheduler) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return fmt.Errorf("scheduler is already running")
	}

	if !s.config.Enabled {
		s.logger.Println("[Maintenance] Scheduler disabled in configuration")
		return nil
	}

	// Schedule all registered tasks
	for name, task := range s.tasks {
		// Schedule with cron expression
		_, err := s.cron.AddFunc(s.config.Schedule, func(taskName string, maintenanceTask Task) func() {
			return func() {
				s.executeTask(context.Background(), taskName, maintenanceTask)
			}
		}(name, task))

		if err != nil {
			return fmt.Errorf("failed to schedule task %s: %w", name, err)
		}

		s.logger.Printf("[Maintenance] Scheduled task %s with schedule: %s", name, s.config.Schedule)
	}

	s.cron.Start()
	s.running = true

	s.logger.Printf("[Maintenance] Scheduler started with %d tasks", len(s.tasks))
	return nil
}

// Stop stops the maintenance scheduler
func (s *Scheduler) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return nil
	}

	ctx := s.cron.Stop()
	s.running = false

	// Wait for any running tasks to complete
	select {
	case <-ctx.Done():
		s.logger.Println("[Maintenance] Scheduler stopped gracefully")
	case <-time.After(30 * time.Second):
		s.logger.Println("[Maintenance] Scheduler stop timed out")
	}

	return nil
}

// RunNow executes all maintenance tasks immediately
func (s *Scheduler) RunNow(ctx context.Context) error {
	s.mu.RLock()
	tasks := make(map[string]Task, len(s.tasks))
	for name, task := range s.tasks {
		tasks[name] = task
	}
	s.mu.RUnlock()

	s.logger.Printf("[Maintenance] Running %d tasks immediately", len(tasks))

	for name, task := range tasks {
		s.executeTask(ctx, name, task)
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

// IsRunning returns true if the scheduler is currently running
func (s *Scheduler) IsRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running
}

// executeTask runs a single maintenance task and updates its status
func (s *Scheduler) executeTask(ctx context.Context, name string, task Task) {
	s.logger.Printf("[Maintenance] Starting task: %s", name)

	// Check if we're in a maintenance window
	if !s.isMaintenanceWindow() {
		s.logger.Printf("[Maintenance] Skipping task %s - outside maintenance window", name)
		return
	}

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

// isMaintenanceWindow checks if current time is within the configured maintenance window
func (s *Scheduler) isMaintenanceWindow() bool {
	if s.config.Window.StartHour == s.config.Window.EndHour {
		return true // No window restrictions
	}

	loc, err := time.LoadLocation(s.config.Window.TimeZone)
	if err != nil {
		loc = time.UTC
	}

	now := time.Now().In(loc)
	hour := now.Hour()

	startHour := s.config.Window.StartHour
	endHour := s.config.Window.EndHour

	// Handle window that crosses midnight
	if startHour > endHour {
		return hour >= startHour || hour < endHour
	}

	return hour >= startHour && hour < endHour
}
