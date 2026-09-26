package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// conduit-31jg.34 regression tests.

func writeJobsFile(t *testing.T, dir string, jobs []*Job) {
	t.Helper()
	data, err := json.Marshal(jobs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cron_jobs.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
}

// The executor reads job fields while reloadFromData rewrites the same job.
// Before the fix, executeJob handed the live *Job to the executor, so -race
// reported a data race between these reads and reloadFromData's writes.
func TestExecuteJob_ReloadDuringExecution_NoRace(t *testing.T) {
	dir := t.TempDir()
	started := make(chan struct{})
	release := make(chan struct{})
	var seen atomic.Value

	executor := func(ctx context.Context, job *Job) error {
		close(started)
		for {
			select {
			case <-release:
				seen.Store(job.Command)
				return nil
			default:
				_ = job.Command + job.Model + job.Target + job.Schedule + job.Name
				_ = len(job.Skills)
				_ = job.Enabled
				_ = job.Metadata["k"]
			}
		}
	}

	writeJobsFile(t, dir, []*Job{{ID: "j", Schedule: "0 0 0 1 1 *", Type: JobTypeGo, Command: "v1", Enabled: true, Metadata: map[string]interface{}{"k": 1}}})
	s := New(dir, executor)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	if err := s.RunNow("j"); err != nil {
		t.Fatal(err)
	}
	<-started

	for i := 0; i < 20; i++ {
		cmd := "v2"
		if i%2 == 0 {
			cmd = "v3"
		}
		writeJobsFile(t, dir, []*Job{{ID: "j", Schedule: "0 0 0 1 1 *", Type: JobTypeGo, Command: cmd, Model: cmd, Skills: []string{cmd}, Enabled: true}})
		if err := s.ReloadJobs(); err != nil {
			t.Fatal(err)
		}
	}
	close(release)

	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.RLock()
		running := s.running["j"]
		s.mu.RUnlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job never finished")
		}
		time.Sleep(time.Millisecond)
	}
	if got := seen.Load(); got != "v1" {
		t.Fatalf("executor saw %v; want the v1 snapshot taken at start", got)
	}
}

// A job must never run concurrently with itself, whether triggered by cron
// or RunNow.
func TestExecuteJob_NoOverlappingRuns(t *testing.T) {
	dir := t.TempDir()
	var inFlight, maxInFlight, runs int32
	release := make(chan struct{})
	started := make(chan struct{}, 10)

	executor := func(ctx context.Context, job *Job) error {
		n := atomic.AddInt32(&inFlight, 1)
		for {
			m := atomic.LoadInt32(&maxInFlight)
			if n <= m || atomic.CompareAndSwapInt32(&maxInFlight, m, n) {
				break
			}
		}
		atomic.AddInt32(&runs, 1)
		started <- struct{}{}
		<-release
		atomic.AddInt32(&inFlight, -1)
		return nil
	}

	s := New(dir, executor)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	if err := s.AddJob(&Job{ID: "slow", Schedule: "0 0 0 1 1 *", Type: JobTypeGo, Command: "x", Enabled: true}); err != nil {
		t.Fatal(err)
	}

	if err := s.RunNow("slow"); err != nil {
		t.Fatal(err)
	}
	<-started

	if err := s.RunNow("slow"); !errors.Is(err, ErrJobRunning) {
		t.Fatalf("second RunNow: got %v, want ErrJobRunning", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ { // simulated cron ticks
		wg.Add(1)
		go func() { defer wg.Done(); s.executeJob("slow") }()
	}
	wg.Wait() // skipped ticks return immediately

	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt32(&inFlight) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if maxInFlight != 1 || runs != 1 {
		t.Fatalf("maxInFlight=%d runs=%d; want 1/1", maxInFlight, runs)
	}
	j, _ := s.GetJob("slow")
	s.mu.RLock()
	rc := j.RunCount
	s.mu.RUnlock()
	if rc != 1 {
		t.Fatalf("RunCount=%d; skipped runs must not count", rc)
	}
}

func TestScheduler_UsesConfiguredLocation(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	s := New(t.TempDir(), nil, WithLocation(la))
	if s.Location() != la || s.cron.Location() != la {
		t.Fatalf("location not applied: %v / %v", s.Location(), s.cron.Location())
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	if err := s.AddJob(&Job{ID: "am8", Schedule: "0 8 * * *", Type: JobTypeGo, Command: "x", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	j, _ := s.GetJob("am8")
	if j.NextRun == nil || j.NextRun.In(la).Hour() != 8 || j.NextRun.In(la).Minute() != 0 {
		t.Fatalf("NextRun=%v; want 08:00 America/Los_Angeles", j.NextRun)
	}
}

func TestScheduler_DefaultLocationIsLocal(t *testing.T) {
	s := New(t.TempDir(), nil)
	if s.Location() != time.Local || s.cron.Location() != time.Local {
		t.Fatalf("default location changed: %v", s.Location())
	}
}

func TestNormalizeSchedule_TimezonePrefix(t *testing.T) {
	got, err := normalizeSchedule("CRON_TZ=America/Los_Angeles 0 8 * * *", JobTypeGo)
	if err != nil || got != "CRON_TZ=America/Los_Angeles 0 0 8 * * *" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := normalizeSchedule("CRON_TZ=Not/AZone 0 8 * * *", JobTypeGo); err == nil {
		t.Fatal("expected error for unknown zone")
	}
	if _, err := normalizeSchedule("CRON_TZ=UTC 0 8 * * *", JobTypeSystem); err == nil {
		t.Fatal("system crontab lines cannot carry a per-line TZ prefix")
	}

	s := New(t.TempDir(), nil) // server-local default
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	if err := s.AddJob(&Job{ID: "tz", Schedule: "CRON_TZ=Asia/Tokyo 0 9 * * *", Type: JobTypeGo, Command: "x", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	tokyo, _ := time.LoadLocation("Asia/Tokyo")
	j, _ := s.GetJob("tz")
	if j.NextRun == nil || j.NextRun.In(tokyo).Hour() != 9 {
		t.Fatalf("NextRun=%v; want 09:00 Asia/Tokyo", j.NextRun)
	}
}
