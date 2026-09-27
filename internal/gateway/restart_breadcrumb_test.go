package gateway

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"conduit/internal/ai"
	"conduit/internal/channels"
	"conduit/internal/config"
	"conduit/internal/protocol"
	"conduit/internal/sessions"
	"conduit/internal/tools/types"
)

// conduit-31jg.88: the restart breadcrumb records every TurnRunner turn the
// drain saw (not just WebSocket clients), written after the drain with each
// turn's outcome; after the restart, interrupted interactive turns get one
// notice on their channel plus a transcript note (or an auto-resume wake).

// captureAdapter is a channel adapter that records outgoing messages.
type captureAdapter struct {
	id  string
	mu  sync.Mutex
	out []*protocol.OutgoingMessage
	in  chan *protocol.IncomingMessage
}

func (a *captureAdapter) ID() string                  { return a.id }
func (a *captureAdapter) Name() string                { return a.id }
func (a *captureAdapter) Type() string                { return "telegram" }
func (a *captureAdapter) Start(context.Context) error { return nil }
func (a *captureAdapter) Stop() error                 { return nil }
func (a *captureAdapter) Status() channels.ChannelStatus {
	return channels.ChannelStatus{Status: channels.StatusOnline}
}
func (a *captureAdapter) IsHealthy() bool { return true }
func (a *captureAdapter) ReceiveMessages() <-chan *protocol.IncomingMessage {
	return a.in
}
func (a *captureAdapter) SendMessage(m *protocol.OutgoingMessage) error {
	a.mu.Lock()
	a.out = append(a.out, m)
	a.mu.Unlock()
	return nil
}
func (a *captureAdapter) sent() []*protocol.OutgoingMessage {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]*protocol.OutgoingMessage(nil), a.out...)
}

type captureFactory struct{ a *captureAdapter }

func (f captureFactory) SupportsType(t string) bool { return t == "telegram" }
func (f captureFactory) CreateAdapter(channels.ChannelConfig) (channels.ChannelAdapter, error) {
	return f.a, nil
}
func (f captureFactory) GetSupportedTypes() []string { return []string{"telegram"} }

// withTelegramCapture registers a capturing "telegram" adapter on gw.
func withTelegramCapture(t *testing.T, gw *Gateway) *captureAdapter {
	t.Helper()
	a := &captureAdapter{id: "telegram", in: make(chan *protocol.IncomingMessage)}
	gw.channelManager.RegisterFactory(captureFactory{a})
	if err := gw.channelManager.CreateAdapter(channels.ChannelConfig{ID: "telegram", Type: "telegram", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	return a
}

// newDrainTurnGateway is a turn-test gateway with a DataDir and a fast
// ShutdownManager installed as gw.shutdownMgr.
func newDrainTurnGateway(t *testing.T) (*Gateway, *sessions.Store, *turnProvider, *ShutdownManager) {
	t.Helper()
	gw, store, p := newTurnTestGateway(t)
	gw.config.DataDir = t.TempDir()
	sm := NewShutdownManager(newTestLogger(), gw)
	sm.drainPoll = 10 * time.Millisecond
	gw.shutdownMgr = sm
	return gw, store, p, sm
}

// runDrain starts a shutdown and waits until the drain handed off to the
// gateway cancel (the breadcrumb is written before that).
func runDrain(t *testing.T, sm *ShutdownManager, reason string, budget time.Duration) {
	t.Helper()
	cancelled := make(chan struct{})
	sm.SetCancel(func() { close(cancelled) })
	if err := sm.BeginShutdown(reason, budget); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(10 * time.Second):
		t.Fatal("drain did not finish")
	}
}

func readBreadcrumb(t *testing.T, dataDir string) RestartBreadcrumb {
	t.Helper()
	path := filepath.Join(dataDir, restartBreadcrumbFile)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("breadcrumb not written: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("breadcrumb mode = %v, want 0600", perm)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var bc RestartBreadcrumb
	if err := json.Unmarshal(data, &bc); err != nil {
		t.Fatalf("invalid breadcrumb: %v", err)
	}
	if bc.Version != restartBreadcrumbVersion {
		t.Errorf("version = %d, want %d", bc.Version, restartBreadcrumbVersion)
	}
	return bc
}

func turnFor(bc RestartBreadcrumb, preview string) (TurnSnapshot, bool) {
	for _, tr := range bc.Turns {
		if tr.Preview == preview {
			return tr, true
		}
	}
	return TurnSnapshot{}, false
}

// restartedGateway simulates the next process: same DB and DataDir, a
// fresh gateway with a capturing Telegram adapter.
func restartedGateway(t *testing.T, store *sessions.Store, dataDir string, mode string) (*Gateway, *captureAdapter) {
	t.Helper()
	gw := &Gateway{
		sessions:       store,
		logger:         newTestLogger(),
		config:         &config.Config{DataDir: dataDir, RestartResume: mode},
		channelManager: newTestChannelManager(t),
		sessionWake:    make(chan string, 8),
	}
	return gw, withTelegramCapture(t, gw)
}

// settle gives the channel manager's router time to deliver.
func settle() { time.Sleep(100 * time.Millisecond) }

func restartNotes(t *testing.T, store *sessions.Store, key string) []string {
	t.Helper()
	var out []string
	for _, row := range transcript(t, store, key) {
		if strings.HasPrefix(row, "assistant:⚠️") {
			out = append(out, row)
		}
	}
	return out
}

// The prod incident: a Telegram turn running at the drain deadline is
// recorded force_cancelled, a message queued behind it dropped; after the
// restart the owner gets exactly one notice and the transcript one note,
// and a second startup repeats nothing.
func TestRestartBreadcrumb_TelegramTurnForceCancelled_NoticeOnce(t *testing.T) {
	gw, store, p, sm := newDrainTurnGateway(t)
	sess, _ := store.GetOrCreateSession("42", "telegram")

	p.hold(1)
	doneA := make(chan struct{})
	go func() {
		defer close(doneA)
		gw.handleIncomingMessage(context.Background(), tgMsg("run the long deploy check\nwith details"))
	}()
	waitEntered(t, p, 1)
	doneB := make(chan struct{})
	go func() {
		defer close(doneB)
		gw.handleIncomingMessage(context.Background(), tgMsg("and then summarize"))
	}()
	waitForCond(t, "B queued", func() bool { return gw.turns().queuedCount(sess.Key) == 1 })

	snap := gw.turns().Snapshot()
	if len(snap) != 2 || !snap[0].Running || snap[1].Running {
		t.Fatalf("Snapshot = %+v, want running A then queued B", snap)
	}
	if snap[0].UserMessageID == "" || snap[1].UserMessageID != "" {
		t.Fatalf("user message IDs = %q/%q, want A persisted, B not", snap[0].UserMessageID, snap[1].UserMessageID)
	}

	runDrain(t, sm, "SIGHUP", 300*time.Millisecond)
	<-doneA
	<-doneB

	bc := readBreadcrumb(t, gw.config.DataDir)
	a, ok := turnFor(bc, "run the long deploy check")
	if !ok {
		t.Fatalf("turn A missing from breadcrumb: %+v", bc.Turns)
	}
	if a.Outcome != TurnForceCancelled || a.Kind != TurnKindInteractive || a.ChannelID != "telegram" ||
		a.UserID != "42" || a.SessionKey != sess.Key || a.UserMessageID == "" || a.Source != "telegram" {
		t.Fatalf("turn A = %+v", a)
	}
	b, ok := turnFor(bc, "and then summarize")
	if !ok || b.Outcome != TurnDropped || b.Kind != TurnKindInteractive {
		t.Fatalf("turn B = %+v (found=%v), want dropped interactive", b, ok)
	}
	if len(bc.ActiveSessions) != 1 || !bc.ActiveSessions[0].Interrupted {
		t.Fatalf("active_sessions = %+v, want the interrupted session mirrored", bc.ActiveSessions)
	}

	// Restart #1: one notice, one transcript note.
	gw2, tg := restartedGateway(t, store, gw.config.DataDir, "")
	before := len(transcript(t, store, sess.Key))
	gw2.processRestartBreadcrumb()
	waitForCond(t, "notice delivered", func() bool { return len(tg.sent()) == 1 })
	msg := tg.sent()[0]
	if msg.UserID != "42" || !strings.Contains(msg.Text, `I was restarted while working on: "run the long deploy check"`) ||
		!strings.Contains(msg.Text, `Reply "continue" to resume`) ||
		!strings.Contains(msg.Text, `I restarted before I got to: "and then summarize". Please resend it.`) {
		t.Fatalf("notice = %+v", msg)
	}
	notes := restartNotes(t, store, sess.Key)
	if len(notes) != 1 || notes[0] != "assistant:"+msg.Text {
		t.Fatalf("transcript notes = %v, want exactly the notice", notes)
	}
	if got := len(transcript(t, store, sess.Key)); got != before+1 {
		t.Fatalf("transcript grew by %d rows, want 1", got-before)
	}
	if _, err := os.Stat(filepath.Join(gw.config.DataDir, restartBreadcrumbFile)); !os.IsNotExist(err) {
		t.Fatalf("breadcrumb not consumed: %v", err)
	}
	if len(gw2.sessionWake) != 0 {
		t.Fatal("notice mode must not wake the session")
	}

	// Restart #2 (crash loop): nothing repeats.
	gw3, tg3 := restartedGateway(t, store, gw.config.DataDir, "")
	gw3.processRestartBreadcrumb()
	settle()
	if n := len(tg3.sent()); n != 0 {
		t.Fatalf("second startup sent %d notices", n)
	}
	if notes := restartNotes(t, store, sess.Key); len(notes) != 1 {
		t.Fatalf("second startup added notes: %v", notes)
	}
}

// A queued turn dropped by the drain alone still gets a notice.
func TestRestartBreadcrumb_DroppedQueuedTurnNotified(t *testing.T) {
	gw, store, p, sm := newDrainTurnGateway(t)
	sess, _ := store.GetOrCreateSession("42", "telegram")

	// A completes within the budget; B, queued behind it, is dropped.
	p.hold(1)
	doneA := make(chan struct{})
	go func() { defer close(doneA); gw.handleIncomingMessage(context.Background(), tgMsg("first")) }()
	waitEntered(t, p, 1)
	doneB := make(chan struct{})
	go func() { defer close(doneB); gw.handleIncomingMessage(context.Background(), tgMsg("second, queued")) }()
	waitForCond(t, "B queued", func() bool { return gw.turns().queuedCount(sess.Key) == 1 })

	cancelled := make(chan struct{})
	sm.SetCancel(func() { close(cancelled) })
	if err := sm.BeginShutdown("SIGHUP", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	waitForCond(t, "drain started", sm.IsDraining)
	p.release(1) // A completes inside the budget; B is dropped by the drain
	<-doneA
	<-doneB
	select {
	case <-cancelled:
	case <-time.After(10 * time.Second):
		t.Fatal("drain did not finish")
	}

	bc := readBreadcrumb(t, gw.config.DataDir)
	if a, ok := turnFor(bc, "first"); !ok || a.Outcome != TurnCompleted {
		t.Fatalf("turn A = %+v, want completed", a)
	}
	if b, ok := turnFor(bc, "second, queued"); !ok || b.Outcome != TurnDropped {
		t.Fatalf("turn B = %+v, want dropped", b)
	}

	gw2, tg := restartedGateway(t, store, gw.config.DataDir, config.RestartResumeAuto)
	gw2.processRestartBreadcrumb()
	waitForCond(t, "notice delivered", func() bool { return len(tg.sent()) == 1 })
	text := tg.sent()[0].Text
	if !strings.Contains(text, `I restarted before I got to: "second, queued"`) || strings.Contains(text, "first") {
		t.Fatalf("notice = %q", text)
	}
	// A dropped turn was never persisted: nothing to auto-resume.
	if len(gw2.sessionWake) != 0 {
		t.Fatal("dropped-only session must not be auto-resumed")
	}
}

// A turn that completed within the budget gets no notice.
func TestRestartBreadcrumb_CompletedTurnNoNotice(t *testing.T) {
	gw, store, p, sm := newDrainTurnGateway(t)
	sess, _ := store.GetOrCreateSession("42", "telegram")
	p.hold(1)
	done := make(chan struct{})
	go func() { defer close(done); gw.handleIncomingMessage(context.Background(), tgMsg("quick question")) }()
	waitEntered(t, p, 1)

	cancelled := make(chan struct{})
	sm.SetCancel(func() { close(cancelled) })
	if err := sm.BeginShutdown("SIGHUP", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	waitForCond(t, "drain started", sm.IsDraining)
	p.release(1)
	<-done
	<-cancelled

	bc := readBreadcrumb(t, gw.config.DataDir)
	if a, ok := turnFor(bc, "quick question"); !ok || a.Outcome != TurnCompleted {
		t.Fatalf("turn = %+v, want completed", bc.Turns)
	}
	gw2, tg := restartedGateway(t, store, gw.config.DataDir, "")
	gw2.processRestartBreadcrumb()
	settle()
	if n := len(tg.sent()); n != 0 {
		t.Fatalf("completed turn produced %d notices", n)
	}
	if notes := restartNotes(t, store, sess.Key); len(notes) != 0 {
		t.Fatalf("completed turn got transcript notes: %v", notes)
	}
}

// Cron / heartbeat turns cut off by the drain get no notice (the scheduler
// re-runs the heartbeat, conduit-31jg.77).
func TestRestartBreadcrumb_CronTurnNoNotice(t *testing.T) {
	gw, p, s, sm := newCronDrainGateway(t)
	p.hold(1)
	if err := s.RunNow("briefing"); err != nil {
		t.Fatal(err)
	}
	waitEntered(t, p, 1)
	runDrain(t, sm, "SIGTERM", 300*time.Millisecond)

	bc := readBreadcrumb(t, gw.config.DataDir)
	tr, ok := turnFor(bc, "morning briefing")
	if !ok || tr.Kind != TurnKindScheduled || tr.ScheduledJob != "briefing" || tr.Outcome != TurnForceCancelled {
		t.Fatalf("cron turn = %+v (found=%v)", tr, ok)
	}
	waitForCond(t, "turn deregistered", func() bool { return len(activeRequestKeys(gw)) == 0 })

	gw2, tg := restartedGateway(t, gw.sessions, gw.config.DataDir, config.RestartResumeAuto)
	gw2.processRestartBreadcrumb()
	settle()
	if n := len(tg.sent()); n != 0 || len(gw2.sessionWake) != 0 {
		t.Fatalf("cron turn: %d notices, %d wakes; want none", n, len(gw2.sessionWake))
	}
	if notes := restartNotes(t, gw.sessions, tr.SessionKey); len(notes) != 0 {
		t.Fatalf("cron session got notes: %v", notes)
	}
}

// A sub-agent cut off by the drain gets no notice; its parent learns it
// was interrupted through the sub-agent failure row.
func TestRestartBreadcrumb_SubAgentNoNotice(t *testing.T) {
	gw, store, p, sm := newDrainTurnGateway(t)
	gw.setLifecycleCtx(context.Background())
	p.hold(1)
	key, err := gw.SpawnSubAgent(context.Background(), "research the thing", "", "", "", 30)
	if err != nil {
		t.Fatal(err)
	}
	waitEntered(t, p, 1)
	runDrain(t, sm, "SIGHUP", 300*time.Millisecond)

	bc := readBreadcrumb(t, gw.config.DataDir)
	tr, ok := turnFor(bc, "research the thing")
	if !ok || tr.Kind != TurnKindSubAgent || tr.Outcome != TurnForceCancelled {
		t.Fatalf("sub-agent turn = %+v (found=%v)", tr, ok)
	}
	waitForCond(t, "sub-agent failure recorded", func() bool {
		return strings.HasPrefix(lastTranscript(t, gw, key), "assistant:Error: sub-agent interrupted by gateway restart")
	})

	gw2, tg := restartedGateway(t, store, gw.config.DataDir, "")
	gw2.processRestartBreadcrumb()
	settle()
	if n := len(tg.sent()); n != 0 {
		t.Fatalf("sub-agent produced %d notices", n)
	}
	if notes := restartNotes(t, store, key); len(notes) != 0 {
		t.Fatalf("sub-agent session got notes: %v", notes)
	}
}

// Old-format breadcrumbs (no version, no turns) are still read.
func TestRestartBreadcrumb_OldFormatStillRead(t *testing.T) {
	dir := t.TempDir()
	store, err := sessions.NewStore(filepath.Join(dir, "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sess, _ := store.GetOrCreateSession("jeff", "ws_client")
	old := `{
  "active_sessions": [{"session_key": "` + sess.Key + `", "user_id": "jeff", "channel_id": "ws-1"}],
  "trigger_action": "gateway_tool",
  "reason": "gateway tool restart",
  "timestamp": "2026-09-27T16:38:47Z"
}`
	if err := os.WriteFile(filepath.Join(dir, restartBreadcrumbFile), []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	gw, tg := restartedGateway(t, store, dir, "")
	gw.processRestartBreadcrumb()
	settle()

	tr := transcript(t, store, sess.Key)
	if len(tr) != 1 || !strings.HasPrefix(tr[0], "assistant:Gateway restarted successfully") ||
		!strings.Contains(tr[0], "gateway tool restart") {
		t.Fatalf("transcript = %v, want the generic restart note", tr)
	}
	if len(tg.sent()) != 0 {
		t.Fatal("old format must not send channel notices")
	}
	if _, err := os.Stat(filepath.Join(dir, restartBreadcrumbFile)); !os.IsNotExist(err) {
		t.Fatal("breadcrumb not removed")
	}
}

// restart_resume "auto" enqueues exactly one wake per interrupted session,
// and the wake continues the turn with its interactive origin.
func TestRestartBreadcrumb_AutoResumeOneWake(t *testing.T) {
	gw, store, p := newTurnTestGateway(t)
	gw.config.DataDir = t.TempDir()
	gw.config.RestartResume = config.RestartResumeAuto
	gw.sessionWake = make(chan string, 8)
	tg := withTelegramCapture(t, gw)
	sess, _ := store.GetOrCreateSession("42", "telegram")
	um, _ := store.AddMessage(sess.Key, "user", "run the long deploy check", nil)

	now := time.Now()
	bc := RestartBreadcrumb{
		Version: restartBreadcrumbVersion, Reason: "SIGHUP", Timestamp: now,
		ActiveSessions: []BreadcrumbSession{{SessionKey: sess.Key, UserID: "42", ChannelID: "telegram", Interrupted: true}},
		Turns: []TurnSnapshot{
			{SessionKey: sess.Key, ChannelID: "telegram", UserID: "42", Kind: TurnKindInteractive, Source: "telegram",
				UserMessageID: um.ID, Preview: "run the long deploy check", StartedAt: now, Running: true, Outcome: TurnForceCancelled},
			{SessionKey: sess.Key, ChannelID: "telegram", UserID: "42", Kind: TurnKindInteractive, Source: "telegram",
				Preview: "and then summarize", StartedAt: now, Outcome: TurnDropped},
			{SessionKey: "cron_x", Kind: TurnKindScheduled, ScheduledJob: "x", Preview: "cron", Outcome: TurnForceCancelled},
		},
	}
	data, _ := json.Marshal(bc)
	if err := os.WriteFile(filepath.Join(gw.config.DataDir, restartBreadcrumbFile), data, 0600); err != nil {
		t.Fatal(err)
	}

	gw.processRestartBreadcrumb()
	if n := len(gw.sessionWake); n != 1 {
		t.Fatalf("wakes enqueued = %d, want 1", n)
	}
	woken := <-gw.sessionWake
	if woken != sess.Key {
		t.Fatalf("woke %q, want %q", woken, sess.Key)
	}
	waitForCond(t, "notice delivered", func() bool { return len(tg.sent()) == 1 })
	if text := tg.sent()[0].Text; !strings.Contains(text, "Picking it back up now") {
		t.Fatalf("auto notice = %q", text)
	}
	msgs, _ := store.GetMessages(sess.Key, 10)
	last := msgs[len(msgs)-1]
	if last.Role != "user" || last.Metadata["wake_source"] != types.WakeSourceRestartResume ||
		last.Metadata[metaResumeInteractive] != "true" || !strings.Contains(last.Content, "interrupted by a gateway restart") {
		t.Fatalf("resume note = %+v", last)
	}

	// Run the wake: the model gets the resume note once and replies.
	gw.clearPendingWake(woken)
	gw.wakeSession(woken)
	if p.callCount() != 1 || countContaining(p.request(1), "user", "continue where you left off") != 1 {
		t.Fatalf("resume turn request = %+v", p.request(1))
	}
	if got := lastTranscript(t, gw, sess.Key); got != "assistant:reply 1" {
		t.Fatalf("last transcript row = %q, want the resumed reply", got)
	}
}

// A drain that starts mid-turn is visible to that turn's tools through
// types.DrainDeadline (the runner attaches a live hook, conduit-31jg.88).
func TestTurnRunner_DrainDeadlineVisibleMidTurn(t *testing.T) {
	gw, _, _, sm := newDrainTurnGateway(t)
	ctxSeen := make(chan context.Context, 1)
	p := &ctxProvider{turnProvider: newTurnProvider(), seen: ctxSeen}
	gw.ai.RegisterProvider("testprov", p)
	p.hold(1)
	done := make(chan struct{})
	go func() { defer close(done); gw.handleIncomingMessage(context.Background(), tgMsg("long thing")) }()
	ctx := <-ctxSeen
	if _, ok := types.DrainDeadline(ctx); ok {
		t.Fatal("drain deadline reported before any drain")
	}

	begin := time.Now()
	runDrain(t, sm, "SIGTERM", 2*time.Second)
	<-done
	dl, ok := types.DrainDeadline(ctx)
	if !ok {
		t.Fatal("turn ctx does not report the drain deadline")
	}
	if dl.Before(begin) || dl.After(begin.Add(2*time.Second+100*time.Millisecond)) {
		t.Fatalf("drain deadline %v not within the 2s budget from %v", dl, begin)
	}
}

type ctxProvider struct {
	*turnProvider
	seen chan context.Context
}

func (p *ctxProvider) GenerateResponse(ctx context.Context, req *ai.GenerateRequest) (*ai.GenerateResponse, error) {
	select {
	case p.seen <- ctx:
	default:
	}
	return p.turnProvider.GenerateResponse(ctx, req)
}

// Drain budget arithmetic (conduit-31jg.27/.88): the breadcrumb is written
// after the drain, inside the same budget; nothing here extends the drain.
func TestDrainDeadline_SetAtBeginShutdownAndNeverExtended(t *testing.T) {
	gw := newTestGatewayForShutdown(t, &config.Config{DataDir: t.TempDir()})
	sm := NewShutdownManager(newTestLogger(), gw)
	if _, ok := sm.DrainDeadline(); ok {
		t.Fatal("deadline before shutdown")
	}
	begin := time.Now()
	done := make(chan struct{})
	sm.SetCancel(func() { close(done) })
	if err := sm.BeginShutdown("SIGHUP", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	dl, ok := sm.DrainDeadline()
	if !ok || dl.After(begin.Add(30*time.Second+time.Second)) {
		t.Fatalf("deadline = %v ok=%v", dl, ok)
	}
	<-done
}
