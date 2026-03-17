package scheduler

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

func TestNormalizeSchedule(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		jobType  JobType
		expected string
		wantErr  bool
	}{
		// Go jobs: 5-field → prepend "0"
		{"go_5field_standard", "0 9 * * *", JobTypeGo, "0 0 9 * * *", false},
		{"go_5field_every15", "*/15 * * * *", JobTypeGo, "0 */15 * * * *", false},
		{"go_5field_monday9am", "0 9 * * 1", JobTypeGo, "0 0 9 * * 1", false},

		// Go jobs: 6-field → keep as-is
		{"go_6field_everysec", "* * * * * *", JobTypeGo, "* * * * * *", false},
		{"go_6field_zerosec", "0 0 9 * * *", JobTypeGo, "0 0 9 * * *", false},
		{"go_6field_30sec", "30 0 9 * * *", JobTypeGo, "30 0 9 * * *", false},

		// System jobs: 5-field → keep as-is
		{"system_5field_standard", "0 9 * * *", JobTypeSystem, "0 9 * * *", false},
		{"system_5field_every15", "*/15 * * * *", JobTypeSystem, "*/15 * * * *", false},

		// System jobs: 6-field → strip first field
		{"system_6field_zerosec", "0 0 9 * * *", JobTypeSystem, "0 9 * * *", false},
		{"system_6field_30sec", "30 0 9 * * *", JobTypeSystem, "0 9 * * *", false},
		{"system_6field_starsec", "* */5 * * * *", JobTypeSystem, "*/5 * * * *", false},

		// Invalid field counts
		{"too_few_fields", "* * *", JobTypeGo, "", true},
		{"too_many_fields", "0 0 0 9 * * *", JobTypeGo, "", true},
		{"single_field", "*", JobTypeSystem, "", true},

		// Invalid expressions
		{"invalid_go_expr", "99 99 99 99 99", JobTypeGo, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeSchedule(tt.input, tt.jobType)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil (result: %q)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.expected {
				t.Errorf("normalizeSchedule(%q, %q) = %q, want %q", tt.input, tt.jobType, got, tt.expected)
			}
		})
	}
}

func TestAddJob_NormalizesSchedule(t *testing.T) {
	dir := t.TempDir()
	s := New(dir, nil)
	if err := s.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer s.Stop()

	// Add a Go job with a 5-field expression — should be normalized to 6-field
	job := &Job{
		ID:       "normalize-test",
		Name:     "Normalize Test",
		Schedule: "0 9 * * *",
		Type:     JobTypeGo,
		Command:  "test",
		Enabled:  true,
	}
	if err := s.AddJob(job); err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}

	got, _ := s.GetJob("normalize-test")
	if got.Schedule != "0 0 9 * * *" {
		t.Errorf("expected schedule '0 0 9 * * *', got %q", got.Schedule)
	}
}

func TestStartNormalizesLoadedJobs(t *testing.T) {
	dir := t.TempDir()

	// Write a jobs file with a 5-field Go job
	jobs := []*Job{{
		ID:       "loaded-5field",
		Name:     "Loaded",
		Schedule: "*/10 * * * *",
		Type:     JobTypeGo,
		Command:  "test",
		Enabled:  true,
	}}
	data, _ := json.MarshalIndent(jobs, "", "  ")
	os.WriteFile(filepath.Join(dir, "cron_jobs.json"), data, 0644)

	s := New(dir, nil)
	if err := s.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer s.Stop()

	got, err := s.GetJob("loaded-5field")
	if err != nil {
		t.Fatalf("GetJob failed: %v", err)
	}
	if got.Schedule != "0 */10 * * * *" {
		t.Errorf("expected normalized schedule '0 */10 * * * *', got %q", got.Schedule)
	}
}

func TestReloadJobs_AddRemoveModify(t *testing.T) {
	dir := t.TempDir()
	jobsFile := filepath.Join(dir, "cron_jobs.json")

	var execCount int
	var mu sync.Mutex
	executor := func(ctx context.Context, job *Job) error {
		mu.Lock()
		execCount++
		mu.Unlock()
		return nil
	}

	// Start with two jobs
	initial := []*Job{
		{ID: "keep", Name: "Keep", Schedule: "0 0 9 * * *", Type: JobTypeGo, Command: "keep-cmd", Enabled: true},
		{ID: "remove-me", Name: "Remove", Schedule: "0 0 10 * * *", Type: JobTypeGo, Command: "remove-cmd", Enabled: true},
	}
	data, _ := json.MarshalIndent(initial, "", "  ")
	os.WriteFile(jobsFile, data, 0644)

	s := New(dir, executor)
	if err := s.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer s.Stop()

	// Verify initial state
	if len(s.ListJobs()) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(s.ListJobs()))
	}

	// Write new file: remove "remove-me", modify "keep", add "new-one"
	updated := []*Job{
		{ID: "keep", Name: "Keep Modified", Schedule: "0 0 11 * * *", Type: JobTypeGo, Command: "keep-cmd-v2", Enabled: true},
		{ID: "new-one", Name: "New", Schedule: "*/5 * * * *", Type: JobTypeGo, Command: "new-cmd", Enabled: true},
	}
	data, _ = json.MarshalIndent(updated, "", "  ")
	os.WriteFile(jobsFile, data, 0644)

	// Force reload
	if err := s.ReloadJobs(); err != nil {
		t.Fatalf("ReloadJobs failed: %v", err)
	}

	jobs := s.ListJobs()
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs after reload, got %d", len(jobs))
	}

	// "remove-me" should be gone
	if _, err := s.GetJob("remove-me"); err == nil {
		t.Error("expected 'remove-me' to be removed")
	}

	// "keep" should be modified
	keep, err := s.GetJob("keep")
	if err != nil {
		t.Fatalf("GetJob('keep') failed: %v", err)
	}
	if keep.Name != "Keep Modified" {
		t.Errorf("expected name 'Keep Modified', got %q", keep.Name)
	}
	if keep.Schedule != "0 0 11 * * *" {
		t.Errorf("expected schedule '0 0 11 * * *', got %q", keep.Schedule)
	}
	if keep.Command != "keep-cmd-v2" {
		t.Errorf("expected command 'keep-cmd-v2', got %q", keep.Command)
	}

	// "new-one" should exist with normalized schedule (5-field → 6-field)
	newJob, err := s.GetJob("new-one")
	if err != nil {
		t.Fatalf("GetJob('new-one') failed: %v", err)
	}
	if newJob.Schedule != "0 */5 * * * *" {
		t.Errorf("expected schedule '0 */5 * * * *', got %q", newJob.Schedule)
	}
}

func TestReloadJobs_PreservesRuntimeFields(t *testing.T) {
	dir := t.TempDir()
	jobsFile := filepath.Join(dir, "cron_jobs.json")

	s := New(dir, nil)
	if err := s.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer s.Stop()

	// Add a job and simulate execution
	job := &Job{
		ID: "runtime-test", Name: "RT", Schedule: "0 0 9 * * *",
		Type: JobTypeGo, Command: "cmd", Enabled: true,
	}
	if err := s.AddJob(job); err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}

	// Simulate runtime state
	s.mu.Lock()
	now := time.Now()
	s.jobs["runtime-test"].LastRun = &now
	s.jobs["runtime-test"].RunCount = 42
	s.jobs["runtime-test"].LastError = "prev error"
	s.mu.Unlock()

	// Write file with modified name but same ID
	fileJobs := []*Job{
		{ID: "runtime-test", Name: "RT Updated", Schedule: "0 0 10 * * *",
			Type: JobTypeGo, Command: "cmd-v2", Enabled: true},
	}
	data, _ := json.MarshalIndent(fileJobs, "", "  ")
	os.WriteFile(jobsFile, data, 0644)

	if err := s.ReloadJobs(); err != nil {
		t.Fatalf("ReloadJobs failed: %v", err)
	}

	got, _ := s.GetJob("runtime-test")
	if got.RunCount != 42 {
		t.Errorf("expected RunCount=42 preserved, got %d", got.RunCount)
	}
	if got.LastRun == nil || !got.LastRun.Equal(now) {
		t.Error("expected LastRun to be preserved")
	}
	if got.LastError != "prev error" {
		t.Errorf("expected LastError preserved, got %q", got.LastError)
	}
	// But name/schedule/command should be updated
	if got.Name != "RT Updated" {
		t.Errorf("expected name 'RT Updated', got %q", got.Name)
	}
}

func TestReloadJobs_NoChanges(t *testing.T) {
	dir := t.TempDir()
	jobsFile := filepath.Join(dir, "cron_jobs.json")

	jobs := []*Job{
		{ID: "stable", Name: "Stable", Schedule: "0 0 9 * * *", Type: JobTypeGo, Command: "cmd", Enabled: true},
	}
	data, _ := json.MarshalIndent(jobs, "", "  ")
	os.WriteFile(jobsFile, data, 0644)

	s := New(dir, nil)
	if err := s.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer s.Stop()

	// Reload with same content should succeed without changes
	if err := s.ReloadJobs(); err != nil {
		t.Fatalf("ReloadJobs failed: %v", err)
	}

	if len(s.ListJobs()) != 1 {
		t.Errorf("expected 1 job, got %d", len(s.ListJobs()))
	}
}

func TestCheckAndReload_SelfWriteCooldown(t *testing.T) {
	dir := t.TempDir()
	jobsFile := filepath.Join(dir, "cron_jobs.json")

	s := New(dir, nil)
	if err := s.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer s.Stop()

	// Add a job (triggers saveJobs, sets lastWriteTime)
	job := &Job{
		ID: "cooldown-test", Name: "CD", Schedule: "0 0 9 * * *",
		Type: JobTypeGo, Command: "cmd", Enabled: true,
	}
	if err := s.AddJob(job); err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}

	// Write a different file while within cooldown
	fileJobs := []*Job{
		{ID: "sneaky-add", Name: "Sneaky", Schedule: "0 0 10 * * *",
			Type: JobTypeGo, Command: "sneaky", Enabled: true},
	}
	data, _ := json.MarshalIndent(fileJobs, "", "  ")
	os.WriteFile(jobsFile, data, 0644)

	// checkAndReload should skip due to cooldown
	s.checkAndReload()

	// The original job should still be there, sneaky-add should not
	if _, err := s.GetJob("cooldown-test"); err != nil {
		t.Error("expected 'cooldown-test' to survive cooldown skip")
	}
	if _, err := s.GetJob("sneaky-add"); err == nil {
		t.Error("expected 'sneaky-add' to NOT be loaded during cooldown")
	}
}
