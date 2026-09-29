package gateway

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"conduit/internal/heartbeat"
	"conduit/internal/scheduler"
	"conduit/internal/sessions"
	"conduit/internal/tools/types"
)

// conduit-385r: cron/heartbeat runs delete their session when the run left
// nothing but its prompt in it.

func sessionKeysLike(t *testing.T, store *sessions.Store, pattern string) []string {
	t.Helper()
	rows, err := store.DB().Query(`SELECT key FROM sessions WHERE key LIKE ? ORDER BY key`, pattern)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return keys
}

func rowCount(t *testing.T, store *sessions.Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := store.DB().QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func cronJob(id, command string) *scheduler.Job {
	return &scheduler.Job{ID: id, Command: command, Type: scheduler.JobTypeGo}
}

func TestCronJob_SilentRunLeavesNoSession(t *testing.T) {
	gw, store, p := newTurnTestGateway(t)
	p.content = func(int) string { return "NO_REPLY" }
	// An interactive session with a lone user row (same shape as a
	// prompt-only session) is never touched.
	tg, err := store.GetOrCreateSession("42", "telegram")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMessage(tg.Key, "user", "hello?", nil); err != nil {
		t.Fatal(err)
	}

	job := cronJob("quiet", "check the thing")
	job.Skills = []string{"research"}
	job.Target = "telegram:42"
	if err := gw.executeScheduledJob(context.Background(), job); err != nil {
		t.Fatalf("cron job: %v", err)
	}
	if p.callCount() != 1 {
		t.Fatalf("provider calls = %d, want 1", p.callCount())
	}
	if keys := sessionKeysLike(t, store, "cron_quiet_%"); len(keys) != 0 {
		t.Fatalf("silent cron run left sessions %v", keys)
	}
	if n := rowCount(t, store, `SELECT COUNT(*) FROM messages WHERE session_key LIKE 'cron_%'`); n != 0 {
		t.Fatalf("%d cron messages left", n)
	}
	if n := rowCount(t, store, `SELECT COUNT(*) FROM messages_fts WHERE session_key LIKE 'cron_%'`); n != 0 {
		t.Fatalf("%d cron FTS rows left", n)
	}
	if got := transcript(t, store, tg.Key); len(got) != 1 {
		t.Fatalf("interactive session transcript = %v", got)
	}
}

func TestCronJob_ReplyKeepsSession(t *testing.T) {
	gw, store, p := newTurnTestGateway(t)
	p.content = func(int) string { return "Found 3 new items." }
	if err := gw.executeScheduledJob(context.Background(), cronJob("digest", "summarize")); err != nil {
		t.Fatal(err)
	}
	keys := sessionKeysLike(t, store, "cron_digest_%")
	if len(keys) != 1 {
		t.Fatalf("sessions = %v, want the run's session kept", keys)
	}
	want := "user:summarize|assistant:Found 3 new items."
	if got := strings.Join(transcript(t, store, keys[0]), "|"); got != want {
		t.Fatalf("transcript = %q, want %q", got, want)
	}
	s, err := store.GetSession(keys[0])
	if err != nil {
		t.Fatal(err)
	}
	if s.Context["model"] == "" {
		t.Error("kept session lost its context")
	}
}

func TestCronJob_FailedRunCleansUp(t *testing.T) {
	gw, store, p := newTurnTestGateway(t)
	p.fail = func(int) error { return errors.New("provider down") }
	err := gw.executeScheduledJob(context.Background(), cronJob("flaky", "do it"))
	if err == nil || !strings.Contains(err.Error(), "AI execution failed") {
		t.Fatalf("err = %v, want the AI failure (cleanup must not mask it)", err)
	}
	if keys := sessionKeysLike(t, store, "cron_flaky_%"); len(keys) != 0 {
		t.Fatalf("failed cron run left sessions %v", keys)
	}
}

func TestCronJob_StoppedRunCleansUp(t *testing.T) {
	gw, store, p := newTurnTestGateway(t)
	p.hold(1)
	errc := make(chan error, 1)
	go func() { errc <- gw.executeScheduledJob(context.Background(), cronJob("long", "take a while")) }()
	waitEntered(t, p, 1)
	keys := sessionKeysLike(t, store, "cron_long_%")
	if len(keys) != 1 {
		t.Fatalf("sessions during the run = %v", keys)
	}
	if running, _ := gw.turns().Stop(keys[0]); !running {
		t.Fatal("/stop did not reach the cron turn")
	}
	if err := <-errc; err == nil {
		t.Fatal("stopped cron run reported success")
	}
	if keys := sessionKeysLike(t, store, "cron_long_%"); len(keys) != 0 {
		t.Fatalf("stopped cron run left sessions %v", keys)
	}
}

func TestCronJob_PanickingRunCleansUp(t *testing.T) {
	gw, store, p := newTurnTestGateway(t)
	p.content = func(int) string { panic("provider exploded") }

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("panic did not propagate")
			}
		}()
		_ = gw.executeScheduledJob(context.Background(), cronJob("boom", "explode"))
	}()
	if keys := sessionKeysLike(t, store, "cron_boom_%"); len(keys) != 0 {
		t.Fatalf("panicking cron run left sessions %v", keys)
	}
	if n := len(activeRequestKeys(gw)); n != 0 {
		t.Fatalf("%d turns still registered", n)
	}
}

// Concurrent runs of the same job each clean up their own session only.
func TestCronJob_ConcurrentRunsOfSameJob(t *testing.T) {
	gw, store, p := newTurnTestGateway(t)
	// Odd calls are silent, even calls reply: exactly half the runs keep
	// their session, whichever run gets which call.
	p.content = func(n int) string {
		if n%2 == 1 {
			return "HEARTBEAT_OK"
		}
		return fmt.Sprintf("result %d", n)
	}
	const runs = 8
	var wg sync.WaitGroup
	errs := make(chan error, runs)
	for i := 0; i < runs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- gw.executeScheduledJob(context.Background(), cronJob("same", "go"))
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	keys := sessionKeysLike(t, store, "cron_same_%")
	if len(keys) != runs/2 {
		t.Fatalf("kept %d sessions, want %d: %v", len(keys), runs/2, keys)
	}
	for _, k := range keys {
		tr := transcript(t, store, k)
		if len(tr) != 2 || !strings.HasPrefix(tr[1], "assistant:result ") {
			t.Fatalf("kept session %s transcript = %v", k, tr)
		}
	}
}

// A sub-agent spawned by a cron turn reports back into the cron session
// after the cron run ended: the session is kept for it and the delivery
// (and the wake turn) land in it.
func TestCronJob_SubAgentParentKeptAndDelivered(t *testing.T) {
	gw, store, p := newTurnTestGateway(t)
	gw.setLifecycleCtx(context.Background())
	p.content = func(n int) string {
		switch n {
		case 1:
			return "NO_REPLY" // the cron turn itself stays silent
		case 2:
			return "sub-agent found something"
		default:
			return "Heads up: something was found."
		}
	}
	p.hold(1)
	p.hold(2)

	errc := make(chan error, 1)
	go func() { errc <- gw.executeScheduledJob(context.Background(), cronJob("spawner", "delegate")) }()
	waitEntered(t, p, 1)
	parent := sessionKeysLike(t, store, "cron_spawner_%")
	if len(parent) != 1 {
		t.Fatalf("cron sessions = %v", parent)
	}
	// The cron turn spawns a sub-agent (as the SessionsSpawn tool would,
	// from inside the turn's context).
	spawnCtx := types.WithRequestContext(context.Background(), "", "", parent[0])
	child, err := gw.SpawnSubAgent(spawnCtx, "look into it", "", "", "", 30)
	if err != nil {
		t.Fatal(err)
	}
	waitEntered(t, p, 2)

	p.release(1)
	if err := <-errc; err != nil {
		t.Fatalf("cron job: %v", err)
	}
	if keys := sessionKeysLike(t, store, "cron_spawner_%"); len(keys) != 1 {
		t.Fatalf("cron session with a running sub-agent was deleted: %v", keys)
	}

	p.release(2)
	waitForCond(t, "sub-agent result delivered to the cron session", func() bool {
		for _, m := range transcript(t, store, parent[0]) {
			if m == "user:sub-agent found something" {
				return true
			}
		}
		return false
	})
	waitForCond(t, "sub-agent finished", func() bool { return !gw.turns().Busy(child) })

	// The wake turn processes the delivery in the kept session.
	gw.wakeSession(parent[0])
	want := "user:delegate|user:sub-agent found something|assistant:Heads up: something was found."
	if got := strings.Join(transcript(t, store, parent[0]), "|"); got != want {
		t.Fatalf("cron session transcript = %q, want %q", got, want)
	}
}

// Delivering into a scheduler session that was already cleaned up fails
// cleanly: an error for the caller to log, no resurrected row.
func TestSubAgentDeliveryToDeletedParent(t *testing.T) {
	gw, store, p := newTurnTestGateway(t)
	p.content = func(int) string { return "NO_REPLY" }
	if err := gw.executeScheduledJob(context.Background(), cronJob("gone", "x")); err != nil {
		t.Fatal(err)
	}
	if keys := sessionKeysLike(t, store, "cron_gone_%"); len(keys) != 0 {
		t.Fatalf("sessions = %v", keys)
	}
	// Any key the run had; reconstruct one with the same shape.
	deleted := "cron_gone_1_cron_deadbeef"
	err := gw.sendToSessionWakeWithSource(context.Background(), deleted, "", "late result", types.WakeSourceSubAgentSilent)
	if err == nil {
		t.Fatal("delivery to a deleted session reported success")
	}
	gw.finishSubAgent("subagent_x", subAgentSpawn{parentSessionKey: deleted}, subAgentOutcome{result: "late result"})
	gw.wakeSession(deleted)
	if keys := sessionKeysLike(t, store, "cron_gone_%"); len(keys) != 0 {
		t.Fatalf("late delivery resurrected %v", keys)
	}
	if n := rowCount(t, store, `SELECT COUNT(*) FROM messages WHERE session_key = ?`, deleted); n != 0 {
		t.Fatalf("%d orphan messages", n)
	}
}

func TestReleaseScheduledSession_Guards(t *testing.T) {
	gw, store, p := newTurnTestGateway(t)

	// Interactive and other non-scheduler keys are refused outright.
	for _, pair := range [][2]string{{"42", "telegram"}, {"me", "tui"}, {"x", "test"}, {"sub", "subagent"}} {
		s, err := store.GetOrCreateSession(pair[0], pair[1])
		if err != nil {
			t.Fatal(err)
		}
		gw.releaseScheduledSession(s.Key, "cron")
		if _, err := store.GetSession(s.Key); err != nil {
			t.Fatalf("%s session deleted: %v", pair[1], err)
		}
	}

	// A scheduler session with a turn running is kept until it is idle.
	s, err := store.GetOrCreateSession("cron", "cron_busy_1")
	if err != nil {
		t.Fatal(err)
	}
	p.content = func(int) string { return "NO_REPLY" }
	p.hold(1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		gw.turns().Run(context.Background(), TurnRequest{Session: s, Text: "wake", NonInteractiveSource: "cron", ScheduledJob: "busy"}, discardTurnSink{})
	}()
	waitEntered(t, p, 1)
	gw.releaseScheduledSession(s.Key, "cron")
	if _, err := store.GetSession(s.Key); err != nil {
		t.Fatalf("busy session deleted: %v", err)
	}
	p.release(1)
	<-done
	gw.releaseScheduledSession(s.Key, "cron")
	if _, err := store.GetSession(s.Key); err == nil {
		t.Fatal("idle prompt-only session kept")
	}

	// Released twice / already gone: no error, nothing happens.
	gw.releaseScheduledSession(s.Key, "cron")
}

// ---- heartbeat ------------------------------------------------------------

func newHeartbeatExecutor(t *testing.T, gw *Gateway, retries int) *heartbeat.JobExecutor {
	t.Helper()
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "HEARTBEAT.md"), []byte("# HEARTBEAT.md\n\n## Check status\nCheck the system status.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := heartbeat.DefaultExecutorConfig()
	cfg.MaxRetries = retries
	cfg.RetryDelaySeconds = 0
	cfg.TimeoutSeconds = 30
	return heartbeat.NewJobExecutor(ws, gw.sessions, cfg)
}

func TestHeartbeat_SilentRunLeavesNoSession(t *testing.T) {
	gw, store, p := newTurnTestGateway(t)
	p.content = func(int) string { return "HEARTBEAT_OK" }
	ex := newHeartbeatExecutor(t, gw, 0)
	res, err := ex.ExecuteHeartbeatJob(withScheduledJobID(context.Background(), "agent_heartbeat_main"), newTurnAIExecutor(gw))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsHeartbeatOK() {
		t.Fatalf("result = %+v, want HEARTBEAT_OK", res)
	}
	if keys := sessionKeysLike(t, store, "heartbeat_%"); len(keys) != 0 {
		t.Fatalf("silent heartbeat left sessions %v", keys)
	}
}

func TestHeartbeat_AlertRunKeepsSession(t *testing.T) {
	gw, store, p := newTurnTestGateway(t)
	p.content = func(int) string { return "ALERT: disk almost full" }
	ex := newHeartbeatExecutor(t, gw, 0)
	res, err := ex.ExecuteHeartbeatJob(context.Background(), newTurnAIExecutor(gw))
	if err != nil {
		t.Fatal(err)
	}
	keys := sessionKeysLike(t, store, "heartbeat_%")
	// HeartbeatResult.SessionKey is the channel ID the key was built from
	// ("heartbeat_<nanos>"); the stored key extends it.
	if len(keys) != 1 || !strings.HasPrefix(keys[0], res.SessionKey+"_") {
		t.Fatalf("sessions = %v (result key %q), want the run's session kept", keys, res.SessionKey)
	}
	if got := transcript(t, store, keys[0]); len(got) != 2 {
		t.Fatalf("transcript = %v", got)
	}
}

func TestHeartbeat_FailedRunCleansUpAfterRetries(t *testing.T) {
	gw, store, p := newTurnTestGateway(t)
	p.fail = func(int) error { return errors.New("provider down") }
	ai := newTurnAIExecutor(gw)
	ex := newHeartbeatExecutor(t, gw, 2)
	if _, err := ex.ExecuteHeartbeatJob(context.Background(), ai); err == nil {
		t.Fatal("failing heartbeat reported success")
	}
	if keys := sessionKeysLike(t, store, "heartbeat_%"); len(keys) != 0 {
		t.Fatalf("failed heartbeat left sessions %v", keys)
	}
	ai.mu.Lock()
	n := len(ai.stored)
	ai.mu.Unlock()
	if n != 0 {
		t.Fatalf("retry bookkeeping leaked %d entries", n)
	}
}

// The full scheduler path: a heartbeat job routed through
// executeScheduledJob and the heartbeat integration.
func TestHeartbeatJob_SilentRunViaSchedulerPath(t *testing.T) {
	gw, store, p := newTurnTestGateway(t)
	p.content = func(int) string { return "HEARTBEAT_OK" }
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "HEARTBEAT.md"), []byte("# HEARTBEAT.md\n\n## Check status\nCheck the system status.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := scheduler.New(t.TempDir(), gw.executeScheduledJob)
	hb := heartbeat.NewGatewayIntegration(ws, gw.sessions, gw.ai, s, nil, nil, "llama3", 30)
	hb.SetAIExecutor(newTurnAIExecutor(gw))
	t.Cleanup(func() { _ = hb.Close() })
	gw.monitoring = &MonitoringService{HeartbeatIntegration: hb}

	job := &scheduler.Job{ID: "agent_heartbeat_main", Type: scheduler.JobTypeGo, Command: "heartbeat",
		Metadata: map[string]interface{}{"heartbeat": true}}
	if err := gw.executeScheduledJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if p.callCount() != 1 {
		t.Fatalf("provider calls = %d", p.callCount())
	}
	if keys := sessionKeysLike(t, store, "heartbeat_%"); len(keys) != 0 {
		t.Fatalf("silent heartbeat job left sessions %v", keys)
	}
}
