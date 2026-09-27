package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"conduit/internal/agent"
	"conduit/internal/ai"
	"conduit/internal/brain"
	"conduit/internal/heartbeat"
	"conduit/internal/protocol"
	"conduit/internal/reflection"
	"conduit/internal/scheduler"
	"conduit/internal/tools"
	"conduit/internal/tui"
)

// conduit-31jg.66: sub-agents, cron / heartbeat scheduler jobs and the WS
// /goodbye reflection run on the shared TurnRunner; WS and TUI get a queued
// notice.

func activeRequestKeys(gw *Gateway) []string {
	gw.ws.ActiveRequestsMu.RLock()
	defer gw.ws.ActiveRequestsMu.RUnlock()
	keys := make([]string, 0, len(gw.ws.ActiveRequests))
	for k := range gw.ws.ActiveRequests {
		keys = append(keys, k)
	}
	return keys
}

func isActive(gw *Gateway, key string) bool {
	gw.ws.ActiveRequestsMu.RLock()
	defer gw.ws.ActiveRequestsMu.RUnlock()
	_, ok := gw.ws.ActiveRequests[key]
	return ok
}

func lastTranscript(t *testing.T, gw *Gateway, key string) string {
	t.Helper()
	tr := transcript(t, gw.sessions, key)
	if len(tr) == 0 {
		return ""
	}
	return tr[len(tr)-1]
}

// recordingSink is a TurnSink that records Queued/Finish for assertions.
type recordingSink struct {
	mu     sync.Mutex
	queued int
	res    *TurnResult
}

func (s *recordingSink) Queued(context.Context)                         { s.mu.Lock(); s.queued++; s.mu.Unlock() }
func (s *recordingSink) Begin(context.Context) ai.StreamCallback        { return nil }
func (s *recordingSink) Progress(string)                                {}
func (s *recordingSink) ToolEvent(context.Context, tools.ToolEventInfo) {}
func (s *recordingSink) Finish(_ context.Context, r *TurnResult) {
	s.mu.Lock()
	s.res = r
	s.mu.Unlock()
}

// ---- sub-agents ---------------------------------------------------------

func TestSubAgent_OnTurnRunner_VisibleStoppableTranscriptInLock(t *testing.T) {
	gw, _, p := newTurnTestGateway(t)
	gw.ctx = context.Background()
	p.hold(1)

	key, err := gw.SpawnSubAgent(context.Background(), "research the thing", "", "", "", 30)
	if err != nil {
		t.Fatal(err)
	}
	waitEntered(t, p, 1)

	if !isActive(gw, key) {
		t.Fatalf("sub-agent turn not in ActiveRequests: %v", activeRequestKeys(gw))
	}
	// The task is persisted inside the turn lock, before the model call.
	if got := transcript(t, gw.sessions, key); len(got) != 1 || got[0] != "user:research the thing" {
		t.Fatalf("transcript during the sub-agent turn = %v, want the task row", got)
	}

	if running, _ := gw.turns().Stop(key); !running {
		t.Fatal("/stop did not reach the running sub-agent turn")
	}
	waitForCond(t, "sub-agent failure recorded", func() bool {
		return strings.HasPrefix(lastTranscript(t, gw, key), "assistant:Error: sub-agent stopped")
	})
	waitForCond(t, "sub-agent turn deregistered", func() bool { return !gw.turns().Busy(key) })
}

func TestSubAgent_OnTurnRunner_CompletesWithCostAndModel(t *testing.T) {
	gw, _, p := newTurnTestGateway(t)
	gw.ctx = context.Background()
	p.usage = ai.Usage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120}

	key, err := gw.SpawnSubAgentWithSkills(context.Background(), "summarize", "", "", "", 30, []string{"research"})
	if err != nil {
		t.Fatal(err)
	}
	waitForCond(t, "sub-agent reply", func() bool { return lastTranscript(t, gw, key) == "assistant:reply 1" })
	waitForCond(t, "sub-agent idle", func() bool { return !gw.turns().Busy(key) })

	want := []string{"user:summarize", "assistant:reply 1"}
	if got := transcript(t, gw.sessions, key); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("transcript = %v, want %v", got, want)
	}
	s, err := gw.sessions.GetSession(key)
	if err != nil {
		t.Fatal(err)
	}
	if s.Context["session_request_count"] != "1" {
		t.Errorf("session_request_count = %q, want 1 (runner accounting)", s.Context["session_request_count"])
	}
	if s.Context["model"] != gw.getSubagentModel("") || s.Context["skill_filter"] != "research" {
		t.Errorf("sub-agent context model=%q skill_filter=%q not persisted", s.Context["model"], s.Context["skill_filter"])
	}
}

// ---- cron ---------------------------------------------------------------

// fakeDrainSched is a drainableScheduler reporting a fixed set of running
// jobs.
type fakeDrainSched struct{ jobs []string }

func (f *fakeDrainSched) BeginDrain()           {}
func (f *fakeDrainSched) RunningJobs() []string { return f.jobs }
func (f *fakeDrainSched) InterruptRunning() int { return len(f.jobs) }

func TestCronJob_OnTurnRunner_RegistersWithoutDoubleCount(t *testing.T) {
	gw, _, p := newTurnTestGateway(t)
	p.hold(1)

	errc := make(chan error, 1)
	job := &scheduler.Job{ID: "briefing", Command: "morning briefing", Type: scheduler.JobTypeGo}
	go func() { errc <- gw.executeScheduledJob(context.Background(), job) }()
	waitEntered(t, p, 1)

	var key string
	for _, k := range activeRequestKeys(gw) {
		if strings.HasPrefix(k, agent.CronSessionKeyPrefix+"briefing_") {
			key = k
		}
	}
	if key == "" {
		t.Fatalf("cron turn not in ActiveRequests: %v", activeRequestKeys(gw))
	}
	if got := gw.turns().scheduledTurnKeys()[key]; got != "briefing" {
		t.Fatalf("cron turn owner = %q, want briefing", got)
	}
	if got := transcript(t, gw.sessions, key); len(got) != 1 || got[0] != "user:morning briefing" {
		t.Fatalf("transcript during cron turn = %v", got)
	}

	// The drain counts the cron turn once — as the scheduler job — not
	// again as an interactive request.
	sm := NewShutdownManager(newTestLogger(), gw)
	requests, jobs := sm.inFlight(&fakeDrainSched{jobs: []string{"briefing"}})
	if requests != 0 || len(jobs) != 1 {
		t.Fatalf("inFlight = %d requests, %v jobs; want 0 requests, 1 job", requests, jobs)
	}
	// Without a drainable scheduler nobody else waits for it: count it.
	if requests, _ := sm.inFlight(nil); requests != 1 {
		t.Fatalf("inFlight without scheduler = %d requests, want 1", requests)
	}

	p.release(1)
	if err := <-errc; err != nil {
		t.Fatalf("cron job: %v", err)
	}
	want := []string{"user:morning briefing", "assistant:reply 1"}
	if got := transcript(t, gw.sessions, key); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("transcript = %v, want %v", got, want)
	}
	if len(gw.turns().scheduledTurnKeys()) != 0 || isActive(gw, key) {
		t.Fatal("cron turn still registered after it finished")
	}
}

func newCronDrainGateway(t *testing.T) (*Gateway, *turnProvider, *scheduler.Scheduler, *ShutdownManager) {
	t.Helper()
	gw, _, p := newTurnTestGateway(t)
	gw.config.DataDir = t.TempDir()
	s := scheduler.New(t.TempDir(), gw.executeScheduledJob)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	if err := s.AddJob(&scheduler.Job{ID: "briefing", Schedule: neverFires, Type: scheduler.JobTypeGo, Command: "morning briefing", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	gw.scheduler = s
	sm := NewShutdownManager(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})), gw)
	sm.drainPoll = 10 * time.Millisecond
	gw.shutdownMgr = sm
	return gw, p, s, sm
}

func jobLastError(s *scheduler.Scheduler, id string) string {
	for _, j := range s.ListJobs() {
		if j.ID == id {
			return j.LastError
		}
	}
	return "<missing>"
}

// A cron turn holding its turn lock while the drain waits completes within
// the budget: the drain waits for it and does not cancel it.
func TestCronJob_DrainWaitsForTurn(t *testing.T) {
	gw, p, s, sm := newCronDrainGateway(t)
	p.hold(1)
	if err := s.RunNow("briefing"); err != nil {
		t.Fatal(err)
	}
	waitEntered(t, p, 1)

	cancelled := make(chan struct{})
	sm.SetCancel(func() { close(cancelled) })
	if err := sm.BeginShutdown("SIGHUP", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
		t.Fatal("drain ended while the cron turn was running")
	case <-time.After(200 * time.Millisecond):
	}
	p.release(1)
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("drain did not end after the cron turn finished")
	}
	waitForCond(t, "job idle", func() bool { return len(s.RunningJobs()) == 0 })
	if e := jobLastError(s, "briefing"); e != "" {
		t.Fatalf("job LastError = %q, want clean completion", e)
	}
	if len(activeRequestKeys(gw)) != 0 {
		t.Fatalf("turn still registered: %v", activeRequestKeys(gw))
	}
}

// A cron turn still running when the drain budget expires is interrupted by
// the scheduler (recorded as interrupted, not failed), without deadlock.
func TestCronJob_DrainBudgetInterruptsTurn(t *testing.T) {
	gw, p, s, sm := newCronDrainGateway(t)
	p.hold(1) // never released: only ctx cancellation ends the call
	if err := s.RunNow("briefing"); err != nil {
		t.Fatal(err)
	}
	waitEntered(t, p, 1)

	cancelled := make(chan struct{})
	sm.SetCancel(func() { close(cancelled) })
	begin := time.Now()
	if err := sm.BeginShutdown("SIGTERM", 300*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("drain did not end at its budget")
	}
	if d := time.Since(begin); d > 3*time.Second {
		t.Fatalf("drain took %v", d)
	}
	waitForCond(t, "job interrupted", func() bool { return len(s.RunningJobs()) == 0 })
	if e := jobLastError(s, "briefing"); !strings.Contains(e, "interrupted by shutdown") {
		t.Fatalf("job LastError = %q, want interrupted by shutdown", e)
	}
	waitForCond(t, "turn deregistered", func() bool { return len(activeRequestKeys(gw)) == 0 })
}

// ---- heartbeat executor -------------------------------------------------

func TestHeartbeatExecutor_OnTurnRunner(t *testing.T) {
	gw, store, p := newTurnTestGateway(t)
	sess, _ := store.GetOrCreateSession("heartbeat", "heartbeat_1")
	ex := newTurnAIExecutor(gw)
	ctx := withScheduledJobID(context.Background(), "agent_heartbeat_main")

	// Attempt 1 is stopped mid-turn; the executor reports context.Canceled
	// (the heartbeat does not retry a stopped turn).
	p.hold(1)
	errc := make(chan error, 1)
	go func() { _, err := ex.ExecutePrompt(ctx, sess, "check things", ""); errc <- err }()
	waitEntered(t, p, 1)
	if got := gw.turns().scheduledTurnKeys()[sess.Key]; got != "agent_heartbeat_main" {
		t.Fatalf("heartbeat turn owner = %q", got)
	}
	gw.turns().Stop(sess.Key)
	if err := <-errc; err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("stopped attempt err = %v, want context.Canceled", err)
	}

	// A retry reuses the stored prompt row instead of appending it again.
	resp, err := ex.ExecutePrompt(ctx, sess, "check things", "")
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetContent() != "reply 2" {
		t.Fatalf("content = %q", resp.GetContent())
	}
	want := []string{"user:check things", "assistant:reply 2"}
	if got := transcript(t, store, sess.Key); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("transcript = %v, want %v", got, want)
	}
	if n := countContaining(p.request(2), "user", "check things"); n != 1 {
		t.Fatalf("retry request carries the prompt %d times, want 1", n)
	}
}

// ---- WS /goodbye --------------------------------------------------------

func TestGoodbye_ReflectionTurnInLock(t *testing.T) {
	gw, store, p := newTurnTestGateway(t)
	b, err := brain.New(filepath.Join(t.TempDir(), "brain.db"), brain.WithAutoFlushInterval(0))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	rs := reflection.NewStore(b.DB())
	gw.sessionReflector = reflection.NewSessionReflector(rs)
	gw.reflectionStore = rs

	sess, _ := store.GetOrCreateSession("u1", "ws_u1")
	for _, m := range [][2]string{{"user", "hi"}, {"assistant", "hello"}, {"user", "thanks"}} {
		if _, err := store.AddMessage(sess.Key, m[0], m[1], nil); err != nil {
			t.Fatal(err)
		}
	}

	p.hold(1)
	c := newTestWSClient("c1")
	c.Send = make(chan []byte, 256)
	responses := make(chan string, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		gw.handleReflectiveSessionEnd(context.Background(), c, sess.Key, func(s string) { responses <- s })
	}()
	waitEntered(t, p, 1)
	if !isActive(gw, sess.Key) {
		t.Fatal("/goodbye reflection turn not registered (not /stop-able)")
	}

	// A message arriving during /goodbye queues behind it and starts on the
	// cleared session.
	sink := &recordingSink{}
	doneB := make(chan struct{})
	go func() {
		defer close(doneB)
		gw.turns().Run(context.Background(), TurnRequest{Session: sess, Text: "message B"}, sink)
	}()
	waitForCond(t, "B queued", func() bool { return gw.turns().queuedCount(sess.Key) == 1 })

	p.release(1)
	<-done
	waitEntered(t, p, 2)
	<-doneB

	if r := <-responses; !strings.Contains(r, "Goodbye") {
		t.Fatalf("goodbye response = %q", r)
	}
	if n := countContaining(p.request(2), "assistant", "reply 1"); n != 0 {
		t.Fatalf("turn after /goodbye saw the reflection reply: %+v", p.request(2))
	}
	want := []string{"user:message B", "assistant:reply 2"}
	if got := transcript(t, store, sess.Key); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("transcript = %v, want %v (reflection persisted and cleared inside the lock)", got, want)
	}
}

// ---- queued notice ------------------------------------------------------

func TestQueuedNotice_WebSocket(t *testing.T) {
	gw, store, p := newTurnTestGateway(t)
	sess, _ := store.GetOrCreateSession("u1", "tui_u1")
	p.hold(1)
	doneA := make(chan struct{})
	go func() {
		defer close(doneA)
		gw.turns().Run(context.Background(), TurnRequest{Session: sess, Text: "A"}, &recordingSink{})
	}()
	waitEntered(t, p, 1)

	c := newTestWSClient("c1")
	c.SetSessionKey(sess.Key)
	doneB := make(chan struct{})
	go func() {
		defer close(doneB)
		gw.handleWebSocketChat(context.Background(), c, &protocol.ChatMessage{SessionKey: sess.Key, UserID: "u1", RequestID: "r2", Text: "B"})
	}()

	select {
	case raw := <-c.Send:
		var out map[string]interface{}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		if out["type"] != "command_response" || out["command"] != tui.QueuedNoticeCommand || out["session_key"] != sess.Key {
			t.Fatalf("first message to the queued WS client = %v, want a queued notice", out)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no queued notice sent to the WS client")
	}
	p.release(1)
	<-doneA
	waitEntered(t, p, 2)
	<-doneB
}

func TestQueuedNotice_TUI(t *testing.T) {
	gw, store, p := newTurnTestGateway(t)
	c := NewDirectClient(DirectClientConfig{
		ParentCtx: context.Background(), UserID: "jeff", Sessions: store, AI: gw.ai, Turns: gw.turns(),
	})
	defer c.Close()
	sess, _ := store.GetOrCreateSession("jeff", "tui_jeff")
	p.hold(1)
	if err := c.SendChatWithID(sess.Key, "A", "r1"); err != nil {
		t.Fatal(err)
	}
	waitEntered(t, p, 1)
	if err := c.SendChatWithID(sess.Key, "B", "r2"); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(10 * time.Second)
	for found := false; !found; {
		select {
		case m := <-c.inbox:
			if cr, ok := m.(tui.CommandResponseMsg); ok && cr.Command == tui.QueuedNoticeCommand {
				if cr.SessionKey != sess.Key || cr.RequestID != "r2" {
					t.Fatalf("queued notice = %+v", cr)
				}
				found = true
			}
		case <-deadline:
			t.Fatal("no queued notice delivered to the TUI")
		}
	}
	p.release(1)
	waitEntered(t, p, 2)
	waitForCond(t, "turns done", func() bool { return !gw.turns().Busy(sess.Key) })
}

// The heartbeat job's turn runs on the TurnRunner; when the drain budget
// expires it is interrupted by the scheduler and recorded as interrupted
// (so it re-runs after restart), and the stopped turn is not retried.
func TestHeartbeatJob_DrainInterruptKeepsRerunMarker(t *testing.T) {
	gw, p, s, sm := newCronDrainGateway(t)
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "HEARTBEAT.md"), []byte("# HEARTBEAT.md\n\n## Check status\nCheck the system status.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hb := heartbeat.NewGatewayIntegration(ws, gw.sessions, gw.ai, s, nil, nil, "llama3", 30)
	hb.SetAIExecutor(newTurnAIExecutor(gw))
	t.Cleanup(func() { _ = hb.Close() })
	gw.monitoring = &MonitoringService{HeartbeatIntegration: hb}
	if err := s.AddJob(&scheduler.Job{
		ID: "agent_heartbeat_main", Schedule: neverFires, Type: scheduler.JobTypeGo,
		Command: "heartbeat", Enabled: true, Metadata: map[string]interface{}{"heartbeat": true},
	}); err != nil {
		t.Fatal(err)
	}

	p.hold(1)
	if err := s.RunNow("agent_heartbeat_main"); err != nil {
		t.Fatal(err)
	}
	waitEntered(t, p, 1)
	var hbKey string
	for k, job := range gw.turns().scheduledTurnKeys() {
		if job == "agent_heartbeat_main" {
			hbKey = k
		}
	}
	if hbKey == "" || !isActive(gw, hbKey) {
		t.Fatalf("heartbeat turn not registered: scheduled=%v active=%v", gw.turns().scheduledTurnKeys(), activeRequestKeys(gw))
	}

	cancelled := make(chan struct{})
	sm.SetCancel(func() { close(cancelled) })
	if err := sm.BeginShutdown("SIGTERM", 300*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("drain did not end at its budget")
	}
	waitForCond(t, "heartbeat job ended", func() bool { return len(s.RunningJobs()) == 0 })

	var job *scheduler.Job
	for _, j := range s.ListJobs() {
		if j.ID == "agent_heartbeat_main" {
			job = j
		}
	}
	if job == nil || !strings.Contains(job.LastError, "interrupted by shutdown") {
		t.Fatalf("heartbeat job = %+v, want LastError interrupted by shutdown", job)
	}
	if _, ok := job.Metadata[scheduler.MetaInterruptedAt]; !ok {
		t.Fatalf("heartbeat job lacks %s marker (no post-restart re-run): %v", scheduler.MetaInterruptedAt, job.Metadata)
	}
	if n := p.callCount(); n != 1 {
		t.Fatalf("interrupted heartbeat turn retried: %d provider calls", n)
	}
}

// /stop during the /goodbye reflection aborts the session end: the session
// is kept and the client's stream is closed.
func TestGoodbye_StopKeepsSession(t *testing.T) {
	gw, store, p := newTurnTestGateway(t)
	b, err := brain.New(filepath.Join(t.TempDir(), "brain.db"), brain.WithAutoFlushInterval(0))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	rs := reflection.NewStore(b.DB())
	gw.sessionReflector = reflection.NewSessionReflector(rs)
	gw.reflectionStore = rs

	sess, _ := store.GetOrCreateSession("u1", "ws_u1")
	for _, m := range [][2]string{{"user", "hi"}, {"assistant", "hello"}, {"user", "thanks"}} {
		if _, err := store.AddMessage(sess.Key, m[0], m[1], nil); err != nil {
			t.Fatal(err)
		}
	}
	p.hold(1)
	c := newTestWSClient("c1")
	c.Send = make(chan []byte, 256)
	responses := make(chan string, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		gw.handleReflectiveSessionEnd(context.Background(), c, sess.Key, func(s string) { responses <- s })
	}()
	waitEntered(t, p, 1)
	if running, _ := gw.turns().Stop(sess.Key); !running {
		t.Fatal("/stop did not reach the /goodbye reflection turn")
	}
	<-done
	if r := <-responses; r != "Session end cancelled." {
		t.Fatalf("response = %q", r)
	}
	want := []string{"user:hi", "assistant:hello", "user:thanks", "user:/goodbye"}
	if got := transcript(t, store, sess.Key); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("transcript = %v, want %v (session kept, prompt stored as /goodbye)", got, want)
	}
	var sawEnd bool
	for len(c.Send) > 0 {
		var out map[string]interface{}
		_ = json.Unmarshal(<-c.Send, &out)
		sawEnd = sawEnd || out["type"] == "stream_end"
	}
	if !sawEnd {
		t.Fatal("no stream_end after the stopped reflection")
	}
}
