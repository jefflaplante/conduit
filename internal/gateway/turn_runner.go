package gateway

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"conduit/internal/ai"
	"conduit/internal/approval"
	"conduit/internal/monitoring"
	"conduit/internal/sessions"
	"conduit/internal/tools"
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

var (
	errTurnDraining = errors.New("gateway is shutting down")
	errTurnPanicked = errors.New("turn panicked")
)

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
	r.setTurnState(key, sessions.SessionStateProcessing, nil) // conduit-3kgo

	var afterUnlock func()
	func() {
		completed := false
		defer func() {
			// Runs on panic too: never leave the session in Processing.
			r.endTurnState(key, rec.res, completed)
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
		completed = true
	}()
	if afterUnlock != nil {
		afterUnlock()
	}
	if rec.res == nil {
		rec.res = &TurnResult{}
	}
	return rec.res
}
