package gateway

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"conduit/internal/config"
	"conduit/internal/scheduler"
)

// conduit-31jg.77: the shutdown drain must wait for in-flight scheduler jobs
// (agent_heartbeat_main was cancelled 24s into its chain on a SIGHUP deploy
// because only interactive turns were counted).

const neverFires = "0 0 0 1 1 *"

func newShutdownWithScheduler(t *testing.T, exec scheduler.JobExecutor, jobIDs ...string) (*ShutdownManager, *scheduler.Scheduler) {
	t.Helper()
	dir := t.TempDir()
	s := scheduler.New(dir, exec)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	for _, id := range jobIDs {
		if err := s.AddJob(&scheduler.Job{ID: id, Schedule: neverFires, Type: scheduler.JobTypeGo, Command: "x", Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	gw := newTestGatewayForShutdown(t, &config.Config{DataDir: t.TempDir()})
	gw.scheduler = s
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	sm := NewShutdownManager(logger, gw)
	sm.drainPoll = 10 * time.Millisecond
	return sm, s
}

func TestShutdownDrain_WaitsForRunningSchedulerJob(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var jobErr error
	finished := make(chan struct{})
	exec := func(ctx context.Context, job *scheduler.Job) error {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			jobErr = ctx.Err()
		}
		close(finished)
		return jobErr
	}
	sm, s := newShutdownWithScheduler(t, exec, "agent_heartbeat_main")
	if err := s.RunNow("agent_heartbeat_main"); err != nil {
		t.Fatal(err)
	}
	<-started

	cancelled := make(chan time.Time, 1)
	sm.SetCancel(func() { cancelled <- time.Now() })
	if err := sm.BeginShutdown("SIGHUP", 10*time.Second); err != nil {
		t.Fatal(err)
	}

	select {
	case <-cancelled:
		t.Fatal("gateway cancelled while a scheduler job was still running")
	case <-time.After(300 * time.Millisecond):
	}
	releasedAt := time.Now()
	close(release)
	select {
	case at := <-cancelled:
		if at.Before(releasedAt) {
			t.Fatal("cancel happened before the job finished")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("drain did not finish after the job ended")
	}
	<-finished
	if jobErr != nil {
		t.Fatalf("job was cancelled (%v); it should have completed", jobErr)
	}
}

func TestShutdownDrain_BudgetCapsSchedulerWait(t *testing.T) {
	started := make(chan struct{})
	exec := func(ctx context.Context, job *scheduler.Job) error {
		close(started)
		<-ctx.Done()
		return context.Cause(ctx)
	}
	sm, s := newShutdownWithScheduler(t, exec, "briefing")
	if err := s.RunNow("briefing"); err != nil {
		t.Fatal(err)
	}
	<-started

	cancelled := make(chan struct{})
	sm.SetCancel(func() { close(cancelled) })
	begin := time.Now()
	if err := sm.BeginShutdown("SIGTERM", 400*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("drain budget did not cap the wait")
	}
	if d := time.Since(begin); d < 400*time.Millisecond || d > 3*time.Second {
		t.Fatalf("drain took %v, want ~400ms budget", d)
	}

	// The job was cancelled at budget expiry and recorded as interrupted.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	job, err := s.GetJob("briefing")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(job.LastError, "interrupted by shutdown") {
		t.Fatalf("LastError = %q, want interrupted-by-shutdown marker", job.LastError)
	}
}

func TestShutdownDrain_NoNewSchedulerJobsDuringDrain(t *testing.T) {
	started := make(chan string, 4)
	release := make(chan struct{})
	exec := func(ctx context.Context, job *scheduler.Job) error {
		started <- job.ID
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}
	sm, s := newShutdownWithScheduler(t, exec, "long", "late")
	if err := s.RunNow("long"); err != nil {
		t.Fatal(err)
	}
	<-started
	sm.SetCancel(func() {})
	if err := sm.BeginShutdown("SIGHUP", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	// BeginDrain happens at the start of the drain phase; wait for it.
	deadline := time.Now().Add(2 * time.Second)
	for !s.Draining() {
		if time.Now().After(deadline) {
			t.Fatal("scheduler never entered drain")
		}
		time.Sleep(time.Millisecond)
	}
	if err := s.RunNow("late"); !errors.Is(err, scheduler.ErrDraining) {
		t.Fatalf("RunNow during drain = %v, want ErrDraining", err)
	}
	close(release)
	select {
	case <-sm.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not complete")
	}
	select {
	case id := <-started:
		t.Fatalf("job %s started during drain", id)
	default:
	}
}

func TestShutdownDrain_ShortenDrainCapsHUPBudget(t *testing.T) {
	started := make(chan struct{})
	exec := func(ctx context.Context, job *scheduler.Job) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	sm, s := newShutdownWithScheduler(t, exec, "rem")
	if err := s.RunNow("rem"); err != nil {
		t.Fatal(err)
	}
	<-started
	cancelled := make(chan struct{})
	sm.SetCancel(func() { close(cancelled) })
	if err := sm.BeginShutdown("SIGHUP", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	begin := time.Now()
	sm.ShortenDrain(300 * time.Millisecond) // SIGTERM joins the HUP drain
	sm.ShortenDrain(time.Minute)            // never extends
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("ShortenDrain did not cap the 30s drain")
	}
	if d := time.Since(begin); d > 3*time.Second {
		t.Fatalf("drain ended %v after ShortenDrain(300ms)", d)
	}
}
