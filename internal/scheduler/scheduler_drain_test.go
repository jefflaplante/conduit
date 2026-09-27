package scheduler

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// conduit-31jg.77: graceful drain must account for in-flight scheduler jobs.

func startDrainTestScheduler(t *testing.T, jobs []*Job, exec JobExecutor, opts ...Option) *Scheduler {
	t.Helper()
	dir := t.TempDir()
	writeJobsFile(t, dir, jobs)
	s := New(dir, exec, opts...)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	return s
}

// farFuture is a schedule that never fires during a test.
const farFuture = "0 0 0 1 1 *"

func TestWaitIdle_WaitsForRunningJobAndReturnsWhenItEnds(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	exec := func(ctx context.Context, job *Job) error {
		close(started)
		<-release
		return nil
	}
	s := startDrainTestScheduler(t, []*Job{{ID: "hb", Schedule: farFuture, Type: JobTypeGo, Enabled: true}}, exec)

	if err := s.RunNow("hb"); err != nil {
		t.Fatal(err)
	}
	<-started
	s.BeginDrain()
	if got := s.RunningJobs(); len(got) != 1 || got[0] != "hb" {
		t.Fatalf("RunningJobs = %v, want [hb]", got)
	}

	done := make(chan error, 1)
	go func() { done <- s.WaitIdle(context.Background()) }()
	select {
	case err := <-done:
		t.Fatalf("WaitIdle returned %v while job still running", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WaitIdle: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitIdle did not return after job finished")
	}
	if got := s.RunningJobs(); len(got) != 0 {
		t.Fatalf("RunningJobs after finish = %v", got)
	}
}

func TestWaitIdle_ContextCapsWait(t *testing.T) {
	started := make(chan struct{})
	exec := func(ctx context.Context, job *Job) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	s := startDrainTestScheduler(t, []*Job{{ID: "slow", Schedule: farFuture, Type: JobTypeGo, Enabled: true}}, exec)
	if err := s.RunNow("slow"); err != nil {
		t.Fatal(err)
	}
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	begin := time.Now()
	if err := s.WaitIdle(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitIdle err = %v, want DeadlineExceeded", err)
	}
	if d := time.Since(begin); d > 2*time.Second {
		t.Fatalf("WaitIdle took %v, budget was 150ms", d)
	}
}

func TestBeginDrain_NoNewRuns(t *testing.T) {
	var runs int32
	exec := func(ctx context.Context, job *Job) error {
		atomic.AddInt32(&runs, 1)
		return nil
	}
	// "* * * * * *" fires every second.
	s := startDrainTestScheduler(t, []*Job{
		{ID: "tick", Schedule: "* * * * * *", Type: JobTypeGo, Enabled: true},
		{ID: "manual", Schedule: farFuture, Type: JobTypeGo, Enabled: true},
	}, exec)
	s.BeginDrain()
	if err := s.RunNow("manual"); !errors.Is(err, ErrDraining) {
		t.Fatalf("RunNow during drain err = %v, want ErrDraining", err)
	}
	time.Sleep(2200 * time.Millisecond) // at least two cron ticks
	if n := atomic.LoadInt32(&runs); n != 0 {
		t.Fatalf("%d runs started during drain, want 0", n)
	}
}

func TestInterruptRunning_MarksJobInterrupted(t *testing.T) {
	started := make(chan struct{})
	exec := func(ctx context.Context, job *Job) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	s := startDrainTestScheduler(t, []*Job{{ID: "brief", Schedule: farFuture, Type: JobTypeGo, Enabled: true}}, exec)
	if err := s.RunNow("brief"); err != nil {
		t.Fatal(err)
	}
	<-started
	s.BeginDrain()
	if n := s.InterruptRunning(); n != 1 {
		t.Fatalf("InterruptRunning = %d, want 1", n)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	job := s.jobs["brief"]
	if _, ok := job.Metadata[MetaInterruptedAt].(string); !ok {
		t.Fatalf("metadata %s not set: %v", MetaInterruptedAt, job.Metadata)
	}
	if !strings.HasPrefix(job.LastError, "interrupted by shutdown") {
		t.Fatalf("LastError = %q, want prefix %q", job.LastError, "interrupted by shutdown")
	}
}

// An interrupted one-shot without the rerun opt-in keeps today's behavior
// (removed); with it, it survives for the post-restart rerun.
func TestInterruptRunning_OneShotKeptOnlyWhenRerunnable(t *testing.T) {
	started := make(chan string, 2)
	exec := func(ctx context.Context, job *Job) error {
		started <- job.ID
		<-ctx.Done()
		return ctx.Err()
	}
	s := startDrainTestScheduler(t, []*Job{
		{ID: "once", Schedule: farFuture, Type: JobTypeGo, Enabled: true, OneShot: true},
		{ID: "once-rerun", Schedule: farFuture, Type: JobTypeGo, Enabled: true, OneShot: true,
			Metadata: map[string]interface{}{MetaRerunOnInterrupt: true}},
	}, exec)
	for _, id := range []string{"once", "once-rerun"} {
		if err := s.RunNow(id); err != nil {
			t.Fatal(err)
		}
		<-started
	}
	s.InterruptRunning()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.jobs["once"]; ok {
		t.Error("non-rerunnable interrupted one-shot should be removed")
	}
	if _, ok := s.jobs["once-rerun"]; !ok {
		t.Error("rerunnable interrupted one-shot should be kept for rerun")
	}
}

func collectRuns(ran <-chan string, d time.Duration) map[string]int {
	got := map[string]int{}
	timeout := time.After(d)
	for {
		select {
		case id := <-ran:
			got[id]++
		case <-timeout:
			return got
		}
	}
}

func TestStart_RerunsInterruptedHeartbeatOnce(t *testing.T) {
	ran := make(chan string, 8)
	exec := func(ctx context.Context, job *Job) error {
		ran <- job.ID
		return nil
	}
	at := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	jobs := []*Job{
		{ID: "hb", Schedule: farFuture, Type: JobTypeGo, Command: "heartbeat", Enabled: true,
			Metadata: map[string]interface{}{"heartbeat": true, MetaInterruptedAt: at}},
		{ID: "plain", Schedule: farFuture, Type: JobTypeGo, Command: "do things", Enabled: true,
			Metadata: map[string]interface{}{MetaInterruptedAt: at}},
		{ID: "optin", Schedule: farFuture, Type: JobTypeGo, Command: "idempotent", Enabled: true,
			Metadata: map[string]interface{}{MetaRerunOnInterrupt: true, MetaInterruptedAt: at}},
		{ID: "disabled-hb", Schedule: farFuture, Type: JobTypeGo, Command: "heartbeat", Enabled: false,
			Metadata: map[string]interface{}{MetaInterruptedAt: at}},
	}
	s := startDrainTestScheduler(t, jobs, exec, withRerunDelay(10*time.Millisecond))

	got := collectRuns(ran, time.Second)
	if got["hb"] != 1 || got["optin"] != 1 || got["plain"] != 0 || got["disabled-hb"] != 0 {
		t.Fatalf("reruns = %v, want hb:1 optin:1 plain:0 disabled-hb:0", got)
	}
	// Markers are cleared so a second restart doesn't rerun again.
	s.mu.RLock()
	defer s.mu.RUnlock()
	for id, j := range s.jobs {
		if _, ok := j.Metadata[MetaInterruptedAt]; ok {
			t.Errorf("job %s still carries %s", id, MetaInterruptedAt)
		}
	}
}

func TestStart_StaleInterruptionNotRerun(t *testing.T) {
	ran := make(chan string, 1)
	exec := func(ctx context.Context, job *Job) error { ran <- job.ID; return nil }
	at := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	startDrainTestScheduler(t, []*Job{{ID: "hb", Schedule: farFuture, Type: JobTypeGo, Command: "heartbeat", Enabled: true,
		Metadata: map[string]interface{}{MetaInterruptedAt: at}}}, exec, withRerunDelay(10*time.Millisecond))
	if got := collectRuns(ran, 300*time.Millisecond); len(got) != 0 {
		t.Fatalf("stale interrupted job rerun: %v", got)
	}
}

// Stop must not hang on a job that ignores cancellation (bounded wait).
func TestStop_BoundedWithStuckJob(t *testing.T) {
	dir := t.TempDir()
	writeJobsFile(t, dir, []*Job{{ID: "stuck", Schedule: farFuture, Type: JobTypeGo, Enabled: true}})
	started := make(chan struct{})
	release := make(chan struct{})
	s := New(dir, func(ctx context.Context, job *Job) error {
		close(started)
		<-release
		return nil
	})
	s.stopRunWait = 100 * time.Millisecond
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	if err := s.RunNow("stuck"); err != nil {
		t.Fatal(err)
	}
	<-started
	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop hung on a stuck job")
	}
	// Let the straggler finish before TempDir cleanup.
	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
}
