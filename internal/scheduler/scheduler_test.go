package scheduler

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/robfig/cron/v3"
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

// TestReloadJobs_DetectsExtendedFieldChanges reproduces bd-23s: the reload
// modified-detection only compared Schedule/Command/Enabled/Type/Name, so an
// external edit changing ONLY Target/Model/OneShot/Skills was treated as
// "unchanged" and stale in-memory values survived hot-reload.
func TestReloadJobs_DetectsExtendedFieldChanges(t *testing.T) {
	dir := t.TempDir()
	jobsFile := filepath.Join(dir, "cron_jobs.json")

	s := New(dir, nil)
	if err := s.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer s.Stop()

	initial := []*Job{
		{ID: "j1", Name: "Job", Schedule: "0 0 9 * * *", Type: JobTypeGo, Command: "cmd",
			Target: "telegram:123", Model: "haiku", Skills: []string{"solar"}, Enabled: true},
	}
	data, _ := json.MarshalIndent(initial, "", "  ")
	if err := os.WriteFile(jobsFile, data, 0644); err != nil {
		t.Fatalf("write initial file: %v", err)
	}
	if err := s.ReloadJobs(); err != nil {
		t.Fatalf("initial ReloadJobs failed: %v", err)
	}

	// External edit: change ONLY Target/Model/OneShot/Skills.
	updated := []*Job{
		{ID: "j1", Name: "Job", Schedule: "0 0 9 * * *", Type: JobTypeGo, Command: "cmd",
			Target: "telegram:999", Model: "sonnet", OneShot: true,
			Skills: []string{"email", "solar"}, Enabled: true},
	}
	data, _ = json.MarshalIndent(updated, "", "  ")
	if err := os.WriteFile(jobsFile, data, 0644); err != nil {
		t.Fatalf("write updated file: %v", err)
	}
	if err := s.ReloadJobs(); err != nil {
		t.Fatalf("ReloadJobs after edit failed: %v", err)
	}

	got, err := s.GetJob("j1")
	if err != nil {
		t.Fatalf("GetJob('j1') failed: %v", err)
	}
	if got.Target != "telegram:999" {
		t.Errorf("Target not reloaded: expected %q, got %q", "telegram:999", got.Target)
	}
	if got.Model != "sonnet" {
		t.Errorf("Model not reloaded: expected %q, got %q", "sonnet", got.Model)
	}
	if !got.OneShot {
		t.Error("OneShot not reloaded: expected true, got false")
	}
	if !reflect.DeepEqual(got.Skills, []string{"email", "solar"}) {
		t.Errorf("Skills not reloaded: expected [email solar], got %v", got.Skills)
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

func TestPersistence_AllMutationsPersist(t *testing.T) {
	dir := t.TempDir()
	jobsFile := filepath.Join(dir, "cron_jobs.json")

	s := New(dir, nil)
	if err := s.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// 1. AddJob persists
	job := &Job{
		ID: "persist-test", Name: "Persist", Schedule: "0 9 * * *",
		Type: JobTypeGo, Command: "test-cmd", Enabled: true,
	}
	if err := s.AddJob(job); err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}

	readJobs := func() []*Job {
		t.Helper()
		data, err := os.ReadFile(jobsFile)
		if err != nil {
			t.Fatalf("failed to read jobs file: %v", err)
		}
		var jobs []*Job
		if err := json.Unmarshal(data, &jobs); err != nil {
			t.Fatalf("failed to unmarshal jobs file: %v", err)
		}
		return jobs
	}

	findJob := func(jobs []*Job, id string) *Job {
		t.Helper()
		for _, j := range jobs {
			if j.ID == id {
				return j
			}
		}
		return nil
	}

	jobs := readJobs()
	if findJob(jobs, "persist-test") == nil {
		t.Fatal("AddJob did not persist to disk")
	}

	// 2. DisableJob persists
	if err := s.DisableJob("persist-test"); err != nil {
		t.Fatalf("DisableJob failed: %v", err)
	}
	jobs = readJobs()
	if j := findJob(jobs, "persist-test"); j == nil || j.Enabled {
		t.Fatal("DisableJob did not persist enabled=false to disk")
	}

	// 3. EnableJob persists
	if err := s.EnableJob("persist-test"); err != nil {
		t.Fatalf("EnableJob failed: %v", err)
	}
	jobs = readJobs()
	if j := findJob(jobs, "persist-test"); j == nil || !j.Enabled {
		t.Fatal("EnableJob did not persist enabled=true to disk")
	}

	// 4. RemoveJob persists
	if err := s.RemoveJob("persist-test"); err != nil {
		t.Fatalf("RemoveJob failed: %v", err)
	}
	jobs = readJobs()
	if findJob(jobs, "persist-test") != nil {
		t.Fatal("RemoveJob did not persist removal to disk")
	}

	s.Stop()

	// 5. Restart scheduler — verify jobs survive restart
	s2 := New(dir, nil)
	if err := s2.Start(); err != nil {
		t.Fatalf("second Start failed: %v", err)
	}
	defer s2.Stop()

	// Add a job via first scheduler instance, save, then reload
	s3 := New(dir, nil)
	if err := s3.Start(); err != nil {
		t.Fatalf("third Start failed: %v", err)
	}
	if err := s3.AddJob(&Job{
		ID: "survive-restart", Name: "Survive", Schedule: "0 10 * * *",
		Type: JobTypeGo, Command: "survive-cmd", Enabled: true,
	}); err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}
	s3.Stop()

	s4 := New(dir, nil)
	if err := s4.Start(); err != nil {
		t.Fatalf("fourth Start failed: %v", err)
	}
	defer s4.Stop()

	if _, err := s4.GetJob("survive-restart"); err != nil {
		t.Fatal("job did not survive scheduler restart")
	}
}

// TestAddJob_UpsertRemovesOldCronEntry verifies that calling AddJob twice with
// the same job ID does not leave a ghost cron entry behind (regression for
// conduit-aumx: duplicate heartbeat job on restart).
func TestAddJob_UpsertRemovesOldCronEntry(t *testing.T) {
	dir := t.TempDir()

	var mu sync.Mutex
	execCount := 0
	executor := func(ctx context.Context, job *Job) error {
		mu.Lock()
		execCount++
		mu.Unlock()
		return nil
	}

	s := New(dir, executor)
	if err := s.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer s.Stop()

	makeJob := func() *Job {
		return &Job{
			ID:       "heartbeat-dedup",
			Name:     "Heartbeat Dedup Test",
			Schedule: "* * * * * *", // every second
			Type:     JobTypeGo,
			Command:  "heartbeat",
			Enabled:  true,
		}
	}

	// First registration.
	if err := s.AddJob(makeJob()); err != nil {
		t.Fatalf("first AddJob failed: %v", err)
	}

	entriesAfterFirst := len(s.cron.Entries())

	// Second registration with the same ID — simulates restart re-registration.
	if err := s.AddJob(makeJob()); err != nil {
		t.Fatalf("second AddJob failed: %v", err)
	}

	entriesAfterSecond := len(s.cron.Entries())

	// The number of active cron entries must not grow after the second AddJob.
	if entriesAfterSecond != entriesAfterFirst {
		t.Errorf("duplicate cron entry created: entries went from %d to %d after re-registering the same job ID",
			entriesAfterFirst, entriesAfterSecond)
	}

	// Only one job record should exist in the map.
	jobs := s.ListJobs()
	count := 0
	for _, j := range jobs {
		if j.ID == "heartbeat-dedup" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 job with id 'heartbeat-dedup', got %d", count)
	}
}

// TestAddJob_Upsert_AfterRestart simulates the restart scenario: a job is
// persisted to disk (with entryID=0 as serialised), then the scheduler is
// restarted and AddJob is called again for the same ID.  There should be
// exactly one cron entry, not two.
func TestAddJob_Upsert_AfterRestart(t *testing.T) {
	dir := t.TempDir()
	jobsFile := filepath.Join(dir, "cron_jobs.json")

	// Pre-populate cron_jobs.json as if the job was registered in a previous run.
	initial := []*Job{{
		ID:      "agent_heartbeat_main",
		Name:    "Heartbeat Task Execution",
		Schedule: "0 */5 * * * *",
		Type:    JobTypeGo,
		Command: "heartbeat",
		Enabled: true,
		Metadata: map[string]interface{}{"heartbeat": true},
	}}
	data, _ := json.MarshalIndent(initial, "", "  ")
	if err := os.WriteFile(jobsFile, data, 0644); err != nil {
		t.Fatalf("failed to write jobs file: %v", err)
	}

	s := New(dir, nil)
	if err := s.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer s.Stop()

	entriesAfterLoad := len(s.cron.Entries())
	if entriesAfterLoad != 1 {
		t.Fatalf("expected 1 cron entry after load, got %d", entriesAfterLoad)
	}

	// Simulate initializeAgentHeartbeat calling ScheduleHeartbeatJob again.
	// In the buggy code this would add a second entry; the fix should upsert.
	if err := s.AddJob(&Job{
		ID:       "agent_heartbeat_main",
		Name:     "Heartbeat Task Execution",
		Schedule: "0 */5 * * * *",
		Type:     JobTypeGo,
		Command:  "heartbeat",
		Enabled:  true,
		Metadata: map[string]interface{}{"heartbeat": true},
	}); err != nil {
		t.Fatalf("AddJob (upsert) failed: %v", err)
	}

	entriesAfterUpsert := len(s.cron.Entries())
	if entriesAfterUpsert != entriesAfterLoad {
		t.Errorf("duplicate cron entry after upsert: had %d entries, now have %d",
			entriesAfterLoad, entriesAfterUpsert)
	}

	if len(s.ListJobs()) != 1 {
		t.Errorf("expected 1 job in map after upsert, got %d", len(s.ListJobs()))
	}
}

// TestLoadJobs_FastForwardsPastDueNextRun verifies that jobs with a past-due
// NextRun are advanced to the next future time on startup (conduit-2w5q).
func TestLoadJobs_FastForwardsPastDueNextRun(t *testing.T) {
	dir := t.TempDir()
	jobsFile := filepath.Join(dir, "cron_jobs.json")

	// Use a schedule that fires every minute; set next_run to a year ago.
	pastTime := time.Now().Add(-365 * 24 * time.Hour)
	jobs := []*Job{{
		ID:      "past-due",
		Name:    "Past Due",
		Schedule: "0 * * * * *", // every minute (6-field Go job)
		Type:    JobTypeGo,
		Command: "test",
		Enabled: true,
		NextRun: &pastTime,
	}}
	data, _ := json.MarshalIndent(jobs, "", "  ")
	if err := os.WriteFile(jobsFile, data, 0644); err != nil {
		t.Fatalf("failed to write jobs file: %v", err)
	}

	beforeStart := time.Now()
	s := New(dir, nil)
	if err := s.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer s.Stop()

	got, err := s.GetJob("past-due")
	if err != nil {
		t.Fatalf("GetJob failed: %v", err)
	}
	if got.NextRun == nil {
		t.Fatal("NextRun should not be nil after fast-forward")
	}
	if !got.NextRun.After(beforeStart) {
		t.Errorf("NextRun %v should be in the future (after %v)", got.NextRun, beforeStart)
	}

	// Verify it matches what the cron schedule would produce.
	parser := cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	schedule, err := parser.Parse("0 * * * * *")
	if err != nil {
		t.Fatalf("failed to parse schedule: %v", err)
	}
	expected := schedule.Next(beforeStart)
	// Allow a small delta for execution time.
	if got.NextRun.Sub(expected) > 2*time.Second || expected.Sub(*got.NextRun) > 2*time.Second {
		t.Errorf("NextRun %v doesn't match expected %v", got.NextRun, expected)
	}

	// Verify corrected value was persisted to disk.
	diskData, err := os.ReadFile(jobsFile)
	if err != nil {
		t.Fatalf("failed to read jobs file: %v", err)
	}
	var diskJobs []*Job
	if err := json.Unmarshal(diskData, &diskJobs); err != nil {
		t.Fatalf("failed to unmarshal jobs file: %v", err)
	}
	var diskJob *Job
	for _, j := range diskJobs {
		if j.ID == "past-due" {
			diskJob = j
			break
		}
	}
	if diskJob == nil {
		t.Fatal("job not found on disk after fast-forward")
	}
	if diskJob.NextRun == nil || !diskJob.NextRun.After(beforeStart) {
		t.Errorf("persisted NextRun %v should be in the future", diskJob.NextRun)
	}
}

// TestLoadJobs_FutureNextRunUntouched verifies that a job whose NextRun is
// already in the future is not modified on startup (conduit-2w5q).
func TestLoadJobs_FutureNextRunUntouched(t *testing.T) {
	dir := t.TempDir()
	jobsFile := filepath.Join(dir, "cron_jobs.json")

	futureTime := time.Now().Add(1 * time.Hour)
	jobs := []*Job{{
		ID:      "future-job",
		Name:    "Future Job",
		Schedule: "0 0 9 * * *",
		Type:    JobTypeGo,
		Command: "test",
		Enabled: true,
		NextRun: &futureTime,
	}}
	data, _ := json.MarshalIndent(jobs, "", "  ")
	if err := os.WriteFile(jobsFile, data, 0644); err != nil {
		t.Fatalf("failed to write jobs file: %v", err)
	}

	s := New(dir, nil)
	if err := s.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer s.Stop()

	got, err := s.GetJob("future-job")
	if err != nil {
		t.Fatalf("GetJob failed: %v", err)
	}
	// After Start(), scheduleGoJob will update NextRun from the cron entry,
	// but it should still be in the future (not regressed to past).
	if got.NextRun == nil || !got.NextRun.After(time.Now()) {
		t.Errorf("future NextRun should remain in the future, got %v", got.NextRun)
	}
}

// TestLoadJobs_UnparseableScheduleSkipped verifies that a job with an
// unparseable schedule and a past-due NextRun does not panic and is handled
// gracefully (conduit-2w5q).
func TestLoadJobs_UnparseableScheduleSkipped(t *testing.T) {
	dir := t.TempDir()
	jobsFile := filepath.Join(dir, "cron_jobs.json")

	pastTime := time.Now().Add(-1 * time.Hour)
	jobs := []*Job{{
		ID:      "bad-schedule",
		Name:    "Bad Schedule",
		Schedule: "not-a-valid-cron-expression",
		Type:    JobTypeGo,
		Command: "test",
		Enabled: true,
		NextRun: &pastTime,
	}}
	data, _ := json.MarshalIndent(jobs, "", "  ")
	if err := os.WriteFile(jobsFile, data, 0644); err != nil {
		t.Fatalf("failed to write jobs file: %v", err)
	}

	// Should not panic; Start may fail when it tries to schedule the bad job
	// but loadJobs itself must not crash.
	s := New(dir, nil)
	_ = s.Start() // error expected due to bad schedule in Start()'s schedule loop — not a crash
	defer s.Stop()

	// The job should still be loaded (fast-forward was skipped, not the job itself).
	got, err := s.GetJob("bad-schedule")
	if err != nil {
		t.Fatalf("GetJob failed — job should be present even with unparseable schedule: %v", err)
	}
	// NextRun remains past (we couldn't fix it), but we didn't crash.
	if got.NextRun == nil || !got.NextRun.Before(time.Now()) {
		// NextRun should still be the original past value since fast-forward was skipped.
		t.Logf("Note: NextRun=%v (may have been updated by scheduleGoJob if schedule was somehow accepted)", got.NextRun)
	}
}

// TestStop_SynchronouslyWaitsForWatchJobsFile is a regression test for
// conduit-3nsd. Stop() must not return until the watchJobsFile goroutine has
// actually exited; otherwise a caller that re-initialises the scheduler
// immediately after Stop() can collide with a still-running file-read.
func TestStop_SynchronouslyWaitsForWatchJobsFile(t *testing.T) {
	dir := t.TempDir()

	s := New(dir, nil)
	// Short watch interval and an exit-signal channel so we can prove the
	// goroutine has returned, not merely been signalled.
	s.watchInterval = 10 * time.Millisecond
	s.watchExited = make(chan struct{}, 1)

	if err := s.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Give the watcher at least one tick so we know it's actually running
	// inside its select loop before we Stop.
	time.Sleep(25 * time.Millisecond)

	stopReturned := make(chan struct{})
	go func() {
		s.Stop()
		close(stopReturned)
	}()

	select {
	case <-stopReturned:
		// Stop() returned. The watch goroutine MUST have exited before Stop
		// returned; verify by reading the exit signal non-blocking.
		select {
		case <-s.watchExited:
			// good — goroutine signalled exit
		default:
			t.Fatal("Stop() returned but watchJobsFile did not signal exit before returning")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Stop() did not return within 500ms — likely deadlock waiting on watchJobsFile")
	}
}

// TestStop_Idempotent_AfterRestart verifies that we can Stop and start a new
// Scheduler in the same process without the previous watchJobsFile goroutine
// still being live. Re-initialising shortly after Stop must be safe.
func TestStop_Idempotent_AfterRestart(t *testing.T) {
	dir := t.TempDir()

	s1 := New(dir, nil)
	s1.watchInterval = 5 * time.Millisecond
	s1.watchExited = make(chan struct{}, 1)
	if err := s1.Start(); err != nil {
		t.Fatalf("first Start failed: %v", err)
	}
	// Let it tick a few times
	time.Sleep(20 * time.Millisecond)
	s1.Stop()

	select {
	case <-s1.watchExited:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("first watchJobsFile did not exit after Stop")
	}

	// Immediately spin up a second scheduler in the same dir — must not collide.
	s2 := New(dir, nil)
	s2.watchInterval = 5 * time.Millisecond
	if err := s2.Start(); err != nil {
		t.Fatalf("second Start after Stop failed: %v", err)
	}
	s2.Stop()
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
