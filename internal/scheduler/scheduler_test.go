package scheduler

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestScheduler_ConcurrentJobExecution(t *testing.T) {
	dir := t.TempDir()

	var executionCount int64
	var execMu sync.Mutex

	executor := func(ctx context.Context, job *Job) error {
		execMu.Lock()
		executionCount++
		execMu.Unlock()
		time.Sleep(5 * time.Millisecond)
		return nil
	}

	s := New(dir, executor)
	if err := s.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer s.Stop()

	// Add a job with a fast schedule
	job := &Job{
		ID:       "race-test-job",
		Name:     "Race Test",
		Schedule: "* * * * * *", // every second (6-field cron with seconds)
		Type:     JobTypeGo,
		Command:  "test",
		Enabled:  true,
	}

	if err := s.AddJob(job); err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}

	// Concurrently execute the job and read job state
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.executeJob(job)
		}()
	}

	// Concurrently read job state via ListJobs and GetJob
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				_ = s.ListJobs()
				_, _ = s.GetJob("race-test-job")
			}
		}()
	}

	wg.Wait()

	// Verify the job was executed successfully
	execMu.Lock()
	count := executionCount
	execMu.Unlock()

	if count < 10 {
		t.Errorf("expected at least 10 executions, got %d", count)
	}

	// Verify job fields are accessible without panic
	j, err := s.GetJob("race-test-job")
	if err != nil {
		t.Fatalf("GetJob failed: %v", err)
	}
	if j.RunCount < 10 {
		t.Errorf("expected RunCount >= 10, got %d", j.RunCount)
	}
}
