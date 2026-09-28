package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"conduit/internal/ai"
	"conduit/internal/approval"
	"conduit/internal/channels"
	"conduit/internal/monitoring"
	"conduit/internal/sessions"
	"conduit/internal/tools"
	"conduit/internal/tools/types"
)

// TurnRunner owns the conversational turn pipeline shared by every entry
// point (conduit-31jg.35): Telegram/channel ingress, WebSocket chat, the
// in-process TUI client, session wakes and the HTTP test endpoint. Before it
// existed each entry point carried its own copy of this sequence and they
// drifted (transcript ordering, /stop, compaction, reflection).
//
// Run, in order:
//
//  1. registers the turn as queued (cancellable by /stop) and, if the session
//     is busy, tells the sink (busy-ack);
//  2. acquires the per-session turn lock (ai.Router.AcquireTurn), giving up
//     if cancelled while queued;
//  3. registers the turn's cancel func as the session's RUNNING request
//     (ActiveRequests) — only the lock holder ever writes it, so /stop
//     always reaches the running turn (conduit-31jg.23);
//  4. persists the user message INSIDE the lock, then builds history and
//     calls the router through its lock-bypassing lease, and persists the
//     reply INSIDE the lock — so a queued turn sees the previous reply and the
//     transcript reads uA,aA,uB,aB (conduit-31jg.22);
//  5. applies context warnings, session cost, SPAR reflection injection and
//     the auto-compaction trigger for every entry point (conduit-31jg.49);
//  6. hands the result to the sink while still holding the lock, so
//     deliveries for consecutive turns cannot interleave.
//
// Token usage is recorded by the router (bd-27hs) from resp.Usage once per
// turn; the runner does not keep its own accumulator.
//
// Entry points (conduit-31jg.35, .66): Telegram/channel ingress, WebSocket
// chat and /goodbye, the in-process TUI client, session wakes, the HTTP test
// endpoint, sub-agents, and scheduler jobs (cron prompts and the heartbeat).
// Scheduler turns set TurnRequest.ScheduledJob; see its doc for how they
// interact with the shutdown drain.
//
// /stop semantics (see Stop): the running turn is cancelled AND every turn
// queued behind it for that session is dropped. Dropped turns never ran:
// their user message is not persisted and their sink only sees Finish with
// Dropped=true.
type TurnRunner struct {
	sessions  *sessions.Store
	ai        *ai.Router
	compactor turnCompactor
	hooks     turnHooks
	active    activeTurnRegistry
	metrics   func() monitoring.MetricsCollectorInterface
	logger    *slog.Logger
	// draining, when set, reports gateway shutdown drain; queued turns are
	// dropped instead of started once it is true.
	draining func() bool
	// drainDeadline, when set, reports the shutdown drain deadline once
	// draining; attached to every turn ctx for the tool layer
	// (types.DrainDeadline, conduit-31jg.88).
	drainDeadline func() (time.Time, bool)

	mu     sync.Mutex
	queued map[string][]*queuedTurn
	// scheduled maps the session key of each RUNNING scheduler-owned turn
	// to its job ID (conduit-31jg.66). Guarded by mu.
	scheduled map[string]string
	// inflight holds every queued or running turn; drainLog records the
	// turns that ended while the gateway was draining, with their outcome.
	// Both guarded by mu (conduit-31jg.88).
	inflight map[*turnState]struct{}
	drainLog []TurnSnapshot

	// subagents tracks parent→child sub-agent sessions for SessionsCancel
	// and the /stop cascade (conduit-38cz, conduit-31jg.84). Own lock.
	subagents *subAgentRegistry
}

// TurnOutcome is how a turn in flight during a shutdown drain ended
// (conduit-31jg.88).
type TurnOutcome string

const (
	// TurnCompleted: finished within the drain budget.
	TurnCompleted TurnOutcome = "completed"
	// TurnForceCancelled: still running at the drain deadline.
	TurnForceCancelled TurnOutcome = "force_cancelled"
	// TurnDropped: queued behind the drain; never ran.
	TurnDropped TurnOutcome = "dropped"
	// TurnStopped: cancelled by /stop during the drain (no notice).
	TurnStopped TurnOutcome = "stopped"
)

// Turn kinds recorded in TurnSnapshot.Kind (conduit-31jg.88).
const (
	TurnKindInteractive = "interactive" // live human: Telegram/channel, WS, TUI
	TurnKindScheduled   = "scheduled"   // cron / heartbeat job
	TurnKindSubAgent    = "subagent"
	TurnKindWake        = "wake"
	TurnKindOther       = "other"
)

// TurnSnapshot describes one queued or running turn (conduit-31jg.88). It
// is also the restart breadcrumb's per-turn record.
type TurnSnapshot struct {
	SessionKey string `json:"session_key"`
	ChannelID  string `json:"channel_id,omitempty"`
	UserID     string `json:"user_id,omitempty"`
	// Kind is one of the TurnKind* values; Source the entry point
	// ("telegram", "websocket", "tui", "cron", "wake:…").
	Kind   string `json:"kind"`
	Source string `json:"source,omitempty"`
	// ScheduledJob is the owning scheduler job, if any.
	ScheduledJob string `json:"scheduled_job,omitempty"`
	// ParentSessionKey is the session that spawned this sub-agent turn
	// (conduit-38cz); "" for other turns.
	ParentSessionKey string `json:"parent_session_key,omitempty"`
	// UserMessageID is the persisted user row ("" while queued).
	UserMessageID string `json:"user_message_id,omitempty"`
	// Preview is the first line of the request, at most turnPreviewMax runes.
	Preview   string    `json:"preview,omitempty"`
	StartedAt time.Time `json:"started_at"`
	Running   bool      `json:"running"`
	// Outcome is set by DrainReport / the drain log.
	Outcome TurnOutcome `json:"outcome,omitempty"`
}

// Interactive reports whether the turn came from a live human.
func (s TurnSnapshot) Interactive() bool { return s.Kind == TurnKindInteractive }

// turnState is the runner's mutable record of one turn; guarded by r.mu.
type turnState struct{ snap TurnSnapshot }

const turnPreviewMax = 120

// turnPreview is the first non-empty line of text, at most turnPreviewMax
// runes (with an ellipsis when cut).
func turnPreview(text string) string {
	line := ""
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			line = l
			break
		}
	}
	if utf8.RuneCountInString(line) > turnPreviewMax {
		line = string([]rune(line)[:turnPreviewMax-1]) + "…"
	}
	return line
}

func newTurnState(req TurnRequest) *turnState {
	snap := TurnSnapshot{
		SessionKey:   req.Session.Key,
		ChannelID:    req.ChannelID,
		UserID:       req.UserID,
		ScheduledJob: req.ScheduledJob,
		StartedAt:    time.Now(),
		Source:       req.NonInteractiveSource,

		ParentSessionKey: req.ParentSessionKey,
	}
	text := req.Text
	if strings.TrimSpace(text) == "" {
		text = req.StoreText
	}
	snap.Preview = turnPreview(text)
	switch {
	case req.ScheduledJob != "":
		snap.Kind = TurnKindScheduled
	case req.Origin != nil:
		snap.Kind = TurnKindInteractive
		snap.Source = req.Origin.Source
	case req.NonInteractiveSource == "subagent":
		snap.Kind = TurnKindSubAgent
	case strings.HasPrefix(req.NonInteractiveSource, "wake"):
		snap.Kind = TurnKindWake
	default:
		snap.Kind = TurnKindOther
	}
	return &turnState{snap: snap}
}

// Snapshot returns every queued and running turn (Running tells which),
// oldest first. Lock order: takes r.mu only; callers must not hold
// ActiveRequestsMu (r.mu is always taken before it). conduit-31jg.88
func (r *TurnRunner) Snapshot() []TurnSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshotLocked(false)
}

// DrainReport returns the turns seen by the shutdown drain with their
// outcome: those that ended while draining (completed / dropped / stopped)
// plus those still in flight, marked force_cancelled (running) or dropped
// (queued). The drain calls it at its deadline BEFORE cancelling anything,
// or once everything drained. Same lock order as Snapshot. conduit-31jg.88
func (r *TurnRunner) DrainReport() []TurnSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := append([]TurnSnapshot(nil), r.drainLog...)
	out = append(out, r.snapshotLocked(true)...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

func (r *TurnRunner) snapshotLocked(withOutcome bool) []TurnSnapshot {
	out := make([]TurnSnapshot, 0, len(r.inflight))
	for t := range r.inflight {
		snap := t.snap
		if withOutcome {
			snap.Outcome = TurnDropped
			if snap.Running {
				snap.Outcome = TurnForceCancelled
			}
		}
		out = append(out, snap)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

// endTurnLocked removes t from the in-flight set and, while the gateway
// drains, records how it ended. Caller holds r.mu.
func (r *TurnRunner) endTurnLocked(t *turnState, outcome TurnOutcome) {
	delete(r.inflight, t)
	if r.draining != nil && r.draining() {
		snap := t.snap
		snap.Outcome = outcome
		r.drainLog = append(r.drainLog, snap)
	}
}

// turnCompactor is the slice of *ai.CompactionEngine the runner uses.
type turnCompactor interface {
	ShouldCompact(promptTokens int, model string) bool
	Compact(ctx context.Context, session *sessions.Session) (*ai.CompactionResult, error)
}

// turnHooks are the optional gateway-level SPAR reflection hooks.
type turnHooks interface {
	// IsFarewell reports whether text should trigger a session reflection.
	IsFarewell(text string) bool
	// ReflectionPrompt returns the prompt to inject, "" when disabled.
	ReflectionPrompt() string
	// ReflectionEnabled reports whether context-budget reflection is armed.
	ReflectionEnabled() bool
	// AfterReflection writes session metrics after the model reflected.
	AfterReflection(ctx context.Context, session *sessions.Session)
}

// activeTurnRegistry points at the sessionKey → cancel map consulted by
// /stop and the shutdown drain. get is re-read on every access so tests (and
// owners) may swap the map.
type activeTurnRegistry struct {
	mu  *sync.RWMutex
	get func() map[string]context.CancelFunc
}

var errTurnDraining = errors.New("gateway is shutting down")

type queuedTurn struct {
	cancel context.CancelFunc
}

// TurnRequest describes one user (or wake) turn.
type TurnRequest struct {
	Session   *sessions.Session
	ChannelID string
	UserID    string

	// Text is the message as the sender wrote it. It is the base of the
	// model-facing text and the input to farewell detection.
	Text string
	// StoreText is the transcript form of the message ("" = Text), e.g. the
	// "[Photo] …" marker for attachments.
	StoreText     string
	StoreMetadata map[string]string
	// PersistedUserMessageID, when set, names a user row already in the
	// transcript (session wake: the inter-session message was delivered to
	// the mailbox earlier). The runner does not store a second copy.
	PersistedUserMessageID string
	Attachments            []ai.Attachment

	// Origin marks a live human turn (conduit-31jg.43). nil means the turn is
	// non-interactive, labelled NonInteractiveSource. SPAR reflection runs
	// only for interactive turns.
	Origin               *approval.Origin
	NonInteractiveSource string

	// SanitizeStored stores the reply after channels.SanitizeOutgoingText
	// (WS/TUI); channel adapters store the raw reply (reply tags intact).
	SanitizeStored bool

	// Decorate adds path-specific values to the turn context (e.g. wake source).
	Decorate func(context.Context) context.Context

	// ScheduledJob names the scheduler job that owns this turn (cron prompt,
	// heartbeat; conduit-31jg.66). The turn is registered in ActiveRequests
	// like any other (visible to /stop and status), but:
	//   - the shutdown drain already counts it through the scheduler's
	//     running flag (conduit-31jg.77), so it does not count it again;
	//   - it may start while the gateway drains: the scheduler stopped
	//     starting new jobs, and a job already running is waited for within
	//     the drain budget and then interrupted by the scheduler, which
	//     records it as interrupted (heartbeat re-runs after restart).
	ScheduledJob string

	// SkipReflection disables SPAR farewell / context-budget prompt
	// injection for this turn (the /goodbye turn already is the reflection).
	SkipReflection bool

	// ParentSessionKey links a sub-agent turn to the session that spawned
	// it (TurnSnapshot, conduit-38cz).
	ParentSessionKey string
}

// TurnResult is what the runner hands to TurnSink.Finish.
type TurnResult struct {
	// Content is the reply to deliver: model text plus any context-window
	// warning. Empty when Silent, Empty or on error.
	Content string
	// Raw is the model's final text as returned by the router.
	Raw string
	// Silent: the model chose not to reply (NO_REPLY / HEARTBEAT_OK).
	Silent bool
	// Empty: the model returned no text at all.
	Empty bool

	Response    ai.ConversationResponse
	Usage       *ai.Usage
	Model       string
	RequestCost float64
	SessionCost float64

	Err error
	// Cancelled: the running turn was cancelled (/stop, shutdown).
	Cancelled bool
	// Dropped: cancelled while queued; the turn never ran and its user
	// message was not persisted.
	Dropped bool

	// UserMessageID is the transcript row of this turn's user message
	// ("" when the turn was dropped or the message could not be stored).
	UserMessageID string
}

// Delivered reports whether the result carries a reply for the user.
func (r *TurnResult) Delivered() bool {
	return r.Err == nil && !r.Cancelled && !r.Dropped && !r.Silent && !r.Empty && r.Content != ""
}

// TurnSink adapts the runner to one entry point's output transport.
type TurnSink interface {
	// Queued is called before waiting when another turn holds the session.
	Queued(ctx context.Context)
	// Begin is called once the turn lock is held and the user message is
	// stored. Returning a non-nil callback selects the streaming router path;
	// nil selects the non-streaming path with Progress updates.
	Begin(ctx context.Context) ai.StreamCallback
	// Progress receives conversational status updates (non-streaming path).
	Progress(status string)
	// ToolEvent receives tool execution events.
	ToolEvent(ctx context.Context, ev tools.ToolEventInfo)
	// Finish receives the outcome. It runs while the turn lock is held
	// (except for Dropped turns, which never held it).
	Finish(ctx context.Context, res *TurnResult)
}

// NewTurnRunner builds a runner. compactor, hooks and metrics may be nil.
// When active.mu is nil the runner keeps its own registry.
func NewTurnRunner(store *sessions.Store, router *ai.Router, compactor turnCompactor, hooks turnHooks,
	active activeTurnRegistry, metrics func() monitoring.MetricsCollectorInterface, logger *slog.Logger) *TurnRunner {
	if active.mu == nil {
		m := make(map[string]context.CancelFunc)
		active = activeTurnRegistry{mu: &sync.RWMutex{}, get: func() map[string]context.CancelFunc { return m }}
	}
	if logger == nil {
		logger = slog.Default()
	}
	if metrics == nil {
		metrics = func() monitoring.MetricsCollectorInterface { return nil }
	}
	return &TurnRunner{
		sessions:  store,
		ai:        router,
		compactor: compactor,
		hooks:     hooks,
		active:    active,
		metrics:   metrics,
		logger:    logger,
		queued:    make(map[string][]*queuedTurn),
		scheduled: make(map[string]string),
		inflight:  make(map[*turnState]struct{}),
		subagents: newSubAgentRegistry(),
	}
}

// scheduledTurnKeys returns a snapshot of the running scheduler-owned turns
// (session key → job ID). The shutdown drain uses it to avoid counting
// those turns twice (conduit-31jg.66). Callers must not hold the
// ActiveRequests lock (lock order: r.mu before active.mu).
func (r *TurnRunner) scheduledTurnKeys() map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]string, len(r.scheduled))
	for k, v := range r.scheduled {
		out[k] = v
	}
	return out
}

// Busy reports whether sessionKey has a running or queued turn.
func (r *TurnRunner) Busy(sessionKey string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.busyLocked(sessionKey)
}

func (r *TurnRunner) busyLocked(sessionKey string) bool {
	if len(r.queued[sessionKey]) > 0 {
		return true
	}
	r.active.mu.RLock()
	_, running := r.active.get()[sessionKey]
	r.active.mu.RUnlock()
	return running
}

// Stop cancels the running turn for sessionKey and drops every turn queued
// behind it (conduit-31jg.23). Every turn in flight at the moment of the call
// is cancelled — a turn that is mid-hand-off from queued to running is in one
// of the two sets, both under r.mu. Turns that arrive after Stop are not
// affected.
func (r *TurnRunner) Stop(sessionKey string) (stoppedRunning bool, droppedQueued int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, q := range r.queued[sessionKey] {
		q.cancel()
	}
	droppedQueued = len(r.queued[sessionKey])
	delete(r.queued, sessionKey)

	r.active.mu.RLock()
	cancel, ok := r.active.get()[sessionKey]
	r.active.mu.RUnlock()
	if ok && cancel != nil {
		cancel()
		stoppedRunning = true
	}
	return stoppedRunning, droppedQueued
}

// stopResponse renders Stop's outcome for the /stop commands.
func stopResponse(stoppedRunning bool, droppedQueued int) (string, bool) {
	switch {
	case stoppedRunning && droppedQueued > 0:
		return fmt.Sprintf("Stopping current operation... (%d queued message(s) dropped)", droppedQueued), true
	case stoppedRunning:
		return "Stopping current operation...", true
	case droppedQueued > 0:
		return fmt.Sprintf("Dropped %d queued message(s).", droppedQueued), true
	}
	return "No active operation to stop.", false
}

func (r *TurnRunner) removeQueued(key string, q *queuedTurn) {
	list := r.queued[key]
	for i, e := range list {
		if e == q {
			list = append(list[:i:i], list[i+1:]...)
			break
		}
	}
	if len(list) == 0 {
		delete(r.queued, key)
	} else {
		r.queued[key] = list
	}
}

func (r *TurnRunner) updateActiveMetric(n int) {
	if m := r.metrics(); m != nil {
		m.UpdateActiveRequests(n)
	}
}

// Run executes one turn. It blocks until the turn finished (or was dropped)
// and returns the same result the sink's Finish received.
func (r *TurnRunner) Run(ctx context.Context, req TurnRequest, sink TurnSink) *TurnResult {
	rec := &resultRecorder{TurnSink: sink}
	key := req.Session.Key
	if r.ai == nil {
		rec.Finish(ctx, &TurnResult{Err: errors.New("AI router not configured")})
		return rec.res
	}

	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// 1. Queue registration + busy-ack.
	q := &queuedTurn{cancel: cancel}
	ts := newTurnState(req) // conduit-31jg.88: visible to Snapshot/DrainReport
	r.mu.Lock()
	busy := r.busyLocked(key)
	r.queued[key] = append(r.queued[key], q)
	r.inflight[ts] = struct{}{}
	r.mu.Unlock()
	if busy {
		rec.Queued(reqCtx)
	}

	// 2. Per-session turn lock; a /stop while queued makes this return.
	lockedCtx, release, lockErr := r.ai.AcquireTurn(reqCtx, key)

	// 3. queued → running, atomically with respect to Stop.
	r.mu.Lock()
	r.removeQueued(key, q)
	if lockErr == nil && reqCtx.Err() != nil {
		// Stopped between acquiring the lock and this hand-off: still a
		// queued turn from /stop's point of view, so drop it.
		lockErr = reqCtx.Err()
		release()
	}
	if lockErr == nil && req.ScheduledJob == "" && r.draining != nil && r.draining() {
		// The shutdown drain cancels running turns via ActiveRequests; a
		// turn that was queued behind one must not start a fresh turn.
		// Scheduler turns are exempt: the scheduler owns their drain
		// (see TurnRequest.ScheduledJob).
		lockErr = errTurnDraining
		release()
	}
	running := 0
	if lockErr == nil {
		r.active.mu.Lock()
		r.active.get()[key] = cancel
		running = len(r.active.get())
		r.active.mu.Unlock()
		if req.ScheduledJob != "" {
			r.scheduled[key] = req.ScheduledJob
		}
		ts.snap.Running = true
	} else {
		outcome := TurnStopped
		if errors.Is(lockErr, errTurnDraining) {
			outcome = TurnDropped
		}
		r.endTurnLocked(ts, outcome)
	}
	r.mu.Unlock()
	if lockErr != nil {
		r.logger.Info("turn dropped before it started", "session_key", key, "reason", lockErr)
		rec.Finish(ctx, &TurnResult{Dropped: true, Err: lockErr})
		return rec.res
	}
	r.updateActiveMetric(running)

	var afterUnlock func()
	func() {
		defer func() {
			outcome := TurnCompleted
			if rec.res != nil && rec.res.Cancelled {
				outcome = TurnStopped
			}
			r.mu.Lock()
			r.endTurnLocked(ts, outcome)
			delete(r.scheduled, key)
			r.active.mu.Lock()
			delete(r.active.get(), key)
			n := len(r.active.get())
			r.active.mu.Unlock()
			r.mu.Unlock()
			r.updateActiveMetric(n)
			release()
		}()
		afterUnlock = r.runLocked(lockedCtx, ctx, req, rec, ts)
	}()
	if afterUnlock != nil {
		afterUnlock()
	}
	if rec.res == nil {
		rec.res = &TurnResult{}
	}
	return rec.res
}

// resultRecorder lets Run return the result delivered to the sink without
// threading it through runLocked's many exits.
type resultRecorder struct {
	TurnSink
	res *TurnResult
}

func (rr *resultRecorder) Finish(ctx context.Context, res *TurnResult) {
	rr.res = res
	rr.TurnSink.Finish(ctx, res)
}

// userMsgStamp records the turn's user message ID on every result.
type userMsgStamp struct {
	TurnSink
	id string
}

func (u *userMsgStamp) Finish(ctx context.Context, res *TurnResult) {
	res.UserMessageID = u.id
	u.TurnSink.Finish(ctx, res)
}

// runLocked is the body of a turn; the caller holds the session turn lock.
// It returns an optional function to run after the lock is released.
func (r *TurnRunner) runLocked(ctx, parentCtx context.Context, req TurnRequest, sink TurnSink, ts *turnState) func() {
	session := req.Session
	key := session.Key

	// Refresh the session: while this turn was queued the previous one may
	// have updated model/cost/warning context.
	if fresh, err := r.sessions.GetSession(key); err == nil && fresh != nil {
		session = fresh
	}

	// 4a. Persist the user message inside the lock (conduit-31jg.22).
	userMsgID := req.PersistedUserMessageID
	if userMsgID == "" {
		storeText := req.StoreText
		if storeText == "" {
			storeText = req.Text
		}
		m, err := r.sessions.AddMessage(key, "user", storeText, req.StoreMetadata)
		if err != nil {
			r.logger.Error("turn: error saving user message", "session_key", key, "error", err)
			sink.Finish(parentCtx, &TurnResult{Err: fmt.Errorf("save user message: %w", err)})
			return nil
		}
		userMsgID = m.ID
	}
	r.mu.Lock()
	ts.snap.UserMessageID = userMsgID // conduit-31jg.88
	r.mu.Unlock()
	ctx = ai.WithCurrentUserMessageID(ctx, userMsgID)
	sink = &userMsgStamp{TurnSink: sink, id: userMsgID}

	// 5a. SPAR reflection injection (interactive turns only).
	messageForAI := req.Text
	isFarewell, isBudgetReflect := false, false
	if req.Origin != nil && r.hooks != nil && !req.SkipReflection {
		if r.hooks.IsFarewell(req.Text) {
			isFarewell = true
			if p := r.hooks.ReflectionPrompt(); p != "" {
				messageForAI = req.Text + "\n\n[System: " + p + "]"
				r.logger.Info("SPAR reflection: farewell detected, injecting reflection prompt",
					"session_key", key, "channel_id", req.ChannelID)
			}
		} else if session.Context["reflection_context_budget_triggered"] == "true" {
			if p := r.hooks.ReflectionPrompt(); p != "" {
				messageForAI = req.Text + "\n\n[System: " + p + "]"
				isBudgetReflect = true
				_ = r.sessions.SetSessionContextBatch(key, map[string]string{
					"reflection_context_budget_triggered": "",
				})
				r.logger.Info("SPAR reflection: context budget triggered, injecting reflection prompt",
					"session_key", key)
			}
		}
	}

	// Turn context.
	ctx = types.WithRequestContext(ctx, req.ChannelID, req.UserID, key)
	// conduit-31jg.88: tools cap their timeout once a drain begins.
	ctx = types.WithDrainDeadline(ctx, r.drainDeadline)
	if req.Origin != nil {
		o := *req.Origin
		o.SessionKey = key
		ctx = approval.WithInteractiveOrigin(ctx, o) // conduit-31jg.43
	} else {
		ctx = approval.WithNonInteractive(ctx, req.NonInteractiveSource) // conduit-31jg.43
	}
	if len(req.Attachments) > 0 {
		ctx = ai.WithAttachments(ctx, req.Attachments)
	}
	ctx = tools.WithToolEventCallback(ctx, func(ev tools.ToolEventInfo) { sink.ToolEvent(ctx, ev) })
	if req.Decorate != nil {
		ctx = req.Decorate(ctx)
	}
	// conduit-31jg.75: tool side calls (Image tool vision analysis) made
	// under this ctx are metered into this ledger and added to the turn cost.
	ctx, sideCalls := ai.WithSideCallLedger(ctx)

	modelOverride := session.Context["model"]
	providerOverride := session.Context["provider"]

	// 4b. Generate.
	var conv ai.ConversationResponse
	var err error
	if onDelta := sink.Begin(ctx); onDelta != nil {
		conv, err = r.ai.GenerateResponseStreaming(ctx, session, messageForAI, providerOverride, modelOverride, onDelta)
	}
	if conv == nil && err == nil {
		conv, err = r.ai.GenerateResponseWithToolsAndProgress(ctx, session, messageForAI, providerOverride, modelOverride, sink.Progress)
	}

	res := &TurnResult{Response: conv, Model: modelOverride}
	if err != nil {
		if ctx.Err() == context.Canceled {
			r.logger.Debug("turn cancelled", "session_key", key)
			res.Cancelled = true
		}
		res.Err = err
		if !res.Cancelled {
			r.logger.Error("turn: error generating AI response", "session_key", key, "error", err)
		}
		sink.Finish(parentCtx, res)
		return nil
	}

	raw := conv.GetContent()
	res.Raw = raw
	res.Usage = conv.GetUsage()
	res.Empty = raw == ""
	res.Silent = !res.Empty && channels.IsSilentResponse(raw)
	content := raw

	// 5b. Path-independent accounting: context warning, session cost,
	// auto-compaction (conduit-31jg.49 — previously WS-only).
	if u := res.Usage; u != nil {
		// conduit-31jg.17: size the context window by the model actually
		// used — the override, else the configured default (never a
		// hardcoded literal).
		modelUsed := modelOverride
		if modelUsed == "" {
			modelUsed = r.ai.DefaultModel()
		}
		batch := map[string]string{}
		if !res.Silent && !res.Empty {
			if w := contextWarningIfNeeded(session, u.Context(), modelUsed); w.Text != "" { // conduit-31jg.15: last-call context, not the turn sum
				content += w.Text
				batch[w.Key] = "true"
				// SPAR: trigger reflection on next message when context budget >= 80%
				if w.Key == "context_warned_80" && r.hooks != nil && r.hooks.ReflectionEnabled() {
					batch["reflection_context_budget_triggered"] = "true"
				}
			}
		}
		// conduit-31jg.57: price through the gateway resolver (overrides,
		// provider-prefixed IDs, cache tokens). An unpriced model is counted
		// in session_unpriced_requests instead of silently adding $0.
		var priced bool
		res.RequestCost, priced = r.ai.TurnCost(providerOverride, modelOverride, *u)
		// conduit-31jg.75: plus side calls made by tools during the turn,
		// each already priced on its own provider + model by meterCall.
		if sc := sideCalls.Usage(); sc.PricedCalls+sc.UnpricedCalls > 0 {
			res.RequestCost += sc.CostUSD
			priced = priced && sc.UnpricedCalls == 0
		}
		prevCost, _ := strconv.ParseFloat(session.Context["session_total_cost"], 64)
		res.SessionCost = prevCost + res.RequestCost
		prevCount, _ := strconv.Atoi(session.Context["session_request_count"])
		batch["session_total_cost"] = fmt.Sprintf("%.6f", res.SessionCost)
		batch["session_request_count"] = strconv.Itoa(prevCount + 1)
		if !priced {
			prevUnpriced, _ := strconv.Atoi(session.Context["session_unpriced_requests"])
			batch["session_unpriced_requests"] = strconv.Itoa(prevUnpriced + 1)
		}
		_ = r.sessions.SetSessionContextBatch(key, batch)

		r.maybeCompact(session, u.Context(), modelUsed)
	}

	// 4c. Persist the reply inside the lock (conduit-31jg.22).
	if !res.Silent && !res.Empty {
		res.Content = content
		stored := content
		if req.SanitizeStored {
			stored = channels.SanitizeOutgoingText(stored)
		}
		if stored != "" {
			if _, aerr := r.sessions.AddMessage(key, "assistant", stored, nil); aerr != nil {
				r.logger.Error("turn: error saving AI message", "session_key", key, "error", aerr)
			}
		}
	} else if res.Silent {
		r.logger.Debug("silent response detected, suppressing", "session_key", key, "response_chars", len(raw))
	} else {
		r.logger.Warn("empty response content, not delivering", "session_key", key)
	}

	// 6. Deliver while still holding the lock.
	sink.Finish(parentCtx, res)

	if (isFarewell || isBudgetReflect) && res.Delivered() && r.hooks != nil {
		return func() {
			if updated, sErr := r.sessions.GetSession(key); sErr == nil {
				r.hooks.AfterReflection(parentCtx, updated)
			}
		}
	}
	return nil
}

// maybeCompact triggers asynchronous auto-compaction when the prompt is over
// the configured threshold. conduit-31jg.21 made Compact safe outside the
// turn lock (snapshotted IDs, one transaction, per-session in-flight guard).
func (r *TurnRunner) maybeCompact(session *sessions.Session, promptTokens int, model string) {
	if r.compactor == nil {
		return
	}
	// model is already resolved to the configured default by the caller;
	// "" means provider default (ai.DefaultContextWindow).
	if !r.compactor.ShouldCompact(promptTokens, model) {
		return
	}
	go func() {
		compactCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if _, err := r.compactor.Compact(compactCtx, session); errors.Is(err, ai.ErrCompactionInProgress) {
			r.logger.Info("auto-compact skipped: already in progress", "session_key", session.Key)
		} else if err != nil {
			r.logger.Warn("auto-compact failed", "session_key", session.Key, "error", err)
		}
	}()
}
