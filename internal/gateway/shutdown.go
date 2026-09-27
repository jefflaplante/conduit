package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

type ShutdownState int32

const (
	StateRunning   ShutdownState = 0
	StateDraining  ShutdownState = 1
	StateTerminate ShutdownState = 2
	StateStopped   ShutdownState = 3
)

func (s ShutdownState) String() string {
	switch s {
	case StateRunning:
		return "running"
	case StateDraining:
		return "draining"
	case StateTerminate:
		return "terminating"
	case StateStopped:
		return "stopped"
	default:
		return "unknown"
	}
}

// ShutdownManager orchestrates graceful shutdown in phases:
// DRAINING -> TERMINATE -> STOPPED
type ShutdownManager struct {
	logger        *slog.Logger
	state         atomic.Int32
	reason        string
	triggerAction string
	drainTimeout  time.Duration
	mu            sync.Mutex
	cancel        context.CancelFunc // cancels the gateway lifecycle context
	gateway       *Gateway
	onShutdown    func() // called after shutdown completes (e.g. re-exec)

	// conduit-31jg.27: replace the fixed 2s post-cancel sleep with a real
	// handshake. gatewayStopped is created by Gateway.Start (TrackGateway)
	// and closed when Start has finished stopAll; nil means no gateway run
	// loop to wait for (unit tests). done is closed when the whole sequence
	// (including onShutdown) has finished.
	gatewayStopped chan struct{}
	stopWait       time.Duration
	done           chan struct{}

	// conduit-31jg.77: absolute end of the drain phase (guarded by mu; set
	// when the drain starts, lowered by ShortenDrain) and its poll interval.
	drainDeadline time.Time
	drainPoll     time.Duration
}

func NewShutdownManager(logger *slog.Logger, gw *Gateway) *ShutdownManager {
	return &ShutdownManager{
		logger:       logger,
		gateway:      gw,
		drainTimeout: 30 * time.Second,
		stopWait:     gatewayStopTimeout + 5*time.Second,
		done:         make(chan struct{}),
	}
}

// TrackGateway is called by Gateway.Start; the returned func must be called
// when Start's shutdown sequence (stopAll) has completed. The shutdown
// sequence waits for it (bounded) before running onShutdown / reporting
// StateStopped, so a re-exec never races the old listener. conduit-31jg.27.
func (sm *ShutdownManager) TrackGateway() (markStopped func()) {
	ch := make(chan struct{})
	sm.mu.Lock()
	sm.gatewayStopped = ch
	sm.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { close(ch) }) }
}

// Done is closed once a shutdown sequence begun by BeginShutdown has fully
// completed (drain, gateway stop, onShutdown hook).
func (sm *ShutdownManager) Done() <-chan struct{} {
	return sm.done
}

func (sm *ShutdownManager) State() ShutdownState {
	return ShutdownState(sm.state.Load())
}

func (sm *ShutdownManager) IsDraining() bool {
	return sm.State() >= StateDraining
}

func (sm *ShutdownManager) SetCancel(cancel context.CancelFunc) {
	sm.mu.Lock()
	sm.cancel = cancel
	sm.mu.Unlock()
}

func (sm *ShutdownManager) SetOnShutdown(fn func()) {
	sm.mu.Lock()
	sm.onShutdown = fn
	sm.mu.Unlock()
}

func (sm *ShutdownManager) SetTriggerAction(action string) {
	sm.mu.Lock()
	sm.triggerAction = action
	sm.mu.Unlock()
}

// BeginShutdown initiates graceful shutdown. Safe to call multiple times —
// second call is a no-op if already draining.
func (sm *ShutdownManager) BeginShutdown(reason string, timeout time.Duration) error {
	if !sm.state.CompareAndSwap(int32(StateRunning), int32(StateDraining)) {
		sm.logger.Warn("shutdown already in progress", "state", sm.State().String())
		return fmt.Errorf("shutdown already in progress (state: %s)", sm.State())
	}

	sm.mu.Lock()
	sm.reason = reason
	if timeout > 0 {
		sm.drainTimeout = timeout
	}
	// conduit-31jg.88: fix the deadline now so DrainDeadline (tool-call
	// caps) sees it from the moment the state flips to draining.
	if sm.drainDeadline.IsZero() {
		sm.drainDeadline = time.Now().Add(sm.drainTimeout)
	}
	sm.mu.Unlock()

	sm.logger.Info("graceful shutdown initiated",
		"reason", reason,
		"drain_timeout", sm.drainTimeout,
	)

	go sm.runShutdownSequence()
	return nil
}

func (sm *ShutdownManager) runShutdownSequence() {
	// Phase 1: Notify connected clients
	sm.notifyClients()

	// Phase 2: Drain — wait for in-flight requests to finish
	turns := sm.drainActiveRequests()

	// Phase 3: Write the restart breadcrumb once the drain has resolved, so
	// each turn's outcome is known (conduit-31jg.88; it used to be written
	// before the drain and listed only WebSocket clients).
	sm.writeBreadcrumb(turns)

	// Phase 4: Transition to terminate
	sm.state.Store(int32(StateTerminate))
	sm.logger.Info("drain complete, terminating")

	// Phase 5: Cancel the gateway context — triggers the existing shutdown sequence
	// in gateway.go Start() (HTTP server shutdown, channels, SSH, etc.)
	sm.mu.Lock()
	cancelFn := sm.cancel
	sm.mu.Unlock()

	if cancelFn != nil {
		cancelFn()
	}

	// Wait for the gateway's own stop sequence (HTTP, WS drain, channels,
	// ...) to finish instead of guessing with a fixed sleep (conduit-31jg.27).
	sm.mu.Lock()
	stopped := sm.gatewayStopped
	stopWait := sm.stopWait
	sm.mu.Unlock()
	if stopped != nil {
		select {
		case <-stopped:
		case <-time.After(stopWait):
			sm.logger.Warn("gateway stop sequence did not finish in time", "wait", stopWait)
		}
	}

	sm.state.Store(int32(StateStopped))
	sm.logger.Info("shutdown complete", "reason", sm.reason)

	// Execute post-shutdown hook (e.g. re-exec for restart)
	sm.mu.Lock()
	onShutdown := sm.onShutdown
	sm.mu.Unlock()

	// onShutdown (re-exec) runs before done is closed: main waits on Done()
	// after Start returns, so the process cannot exit ahead of the re-exec.
	if onShutdown != nil {
		onShutdown()
	}
	close(sm.done)
}

func (sm *ShutdownManager) notifyClients() {
	gw := sm.gateway
	if gw.ws == nil {
		return
	}
	gw.ws.ClientMu.RLock()
	clients := make([]*Client, 0, len(gw.ws.Clients))
	for _, c := range gw.ws.Clients {
		clients = append(clients, c)
	}
	gw.ws.ClientMu.RUnlock()

	if len(clients) == 0 {
		return
	}

	sm.logger.Info("notifying clients of pending restart", "count", len(clients))

	msg := fmt.Sprintf(`{"type":"system","content":"Gateway is restarting (%s). Please wait..."}`, sm.reason)
	for _, client := range clients {
		select {
		case client.Send <- []byte(msg):
		default:
			sm.logger.Warn("failed to notify client (send buffer full)", "client_id", client.ID)
		}
	}
}

// drainableScheduler is the part of *scheduler.Scheduler the drain needs
// (conduit-31jg.77). Asserted at runtime so scheduler.SchedulerInterface
// (and its test mocks) stay unchanged.
type drainableScheduler interface {
	BeginDrain()
	RunningJobs() []string
	InterruptRunning() int
}

func (sm *ShutdownManager) drainScheduler() drainableScheduler {
	if sm.gateway == nil || sm.gateway.scheduler == nil {
		return nil
	}
	ds, _ := sm.gateway.scheduler.(drainableScheduler)
	return ds
}

// DrainDeadline reports the end of the drain phase once a shutdown has
// begun (ok=false while running). It is read by tool calls through the
// TurnRunner (types.DrainDeadline) to cap their timeout. conduit-31jg.88
func (sm *ShutdownManager) DrainDeadline() (time.Time, bool) {
	if !sm.IsDraining() {
		return time.Time{}, false
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.drainDeadline.IsZero() {
		return time.Time{}, false
	}
	return sm.drainDeadline, true
}

// ShortenDrain caps an in-progress drain so it ends no later than timeout
// from now (never extends it). A SIGTERM arriving during a 30s SIGHUP drain
// uses it to stay inside systemd's TimeoutStopSec (conduit-31jg.77).
func (sm *ShutdownManager) ShortenDrain(timeout time.Duration) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	limit := time.Now().Add(timeout)
	if sm.drainDeadline.IsZero() || limit.Before(sm.drainDeadline) {
		sm.drainDeadline = limit
		sm.logger.Info("drain deadline shortened", "remaining", timeout)
	}
}

// drainActiveRequests waits, within one shared drain budget, for in-flight
// interactive turns (ws.ActiveRequests) AND in-flight scheduler jobs
// (conduit-31jg.77: agent_heartbeat_main was cancelled 24s into its chain
// because only turns were counted). New scheduler runs are blocked for the
// whole drain. When the budget expires, remaining turns are force-cancelled
// and remaining jobs are cancelled and recorded as interrupted by shutdown.
//
// It returns the TurnRunner's drain report (conduit-31jg.88): taken once
// everything drained, or at the deadline BEFORE anything is cancelled, so a
// turn still running is recorded force_cancelled and a queued one dropped.
func (sm *ShutdownManager) drainActiveRequests() []TurnSnapshot {
	gw := sm.gateway
	sched := sm.drainScheduler()
	if sched != nil {
		sched.BeginDrain()
	}
	if (gw == nil || gw.ws == nil) && sched == nil {
		return nil
	}

	sm.mu.Lock()
	if sm.drainDeadline.IsZero() {
		sm.drainDeadline = time.Now().Add(sm.drainTimeout)
	}
	poll := sm.drainPoll
	sm.mu.Unlock()
	if poll <= 0 {
		poll = 500 * time.Millisecond
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	for {
		active, jobs := sm.inFlight(sched)
		if active == 0 && len(jobs) == 0 {
			sm.logger.Info("all active requests and scheduler jobs drained")
			return sm.turnReport()
		}

		sm.mu.Lock()
		deadline := sm.drainDeadline
		sm.mu.Unlock()
		if !time.Now().Before(deadline) {
			sm.logger.Warn("drain timeout exceeded, force-cancelling in-flight work",
				"remaining_requests", active,
				"remaining_jobs", jobs,
				"timeout", sm.drainTimeout,
			)
			report := sm.turnReport() // before cancelling: conduit-31jg.88
			// conduit-31jg.66: interrupt scheduler jobs FIRST. Their turns
			// are also in ActiveRequests; cancelling those before the
			// scheduler context would let a job finish with a plain
			// "context canceled" and be recorded as failed rather than
			// interrupted (losing the heartbeat's post-restart re-run).
			if sched != nil && len(jobs) > 0 {
				for _, id := range jobs {
					sm.logger.Warn("cancelling scheduler job: interrupted by shutdown", "job_id", id)
				}
				sched.InterruptRunning()
			}
			if gw != nil && gw.ws != nil {
				gw.ws.ActiveRequestsMu.RLock()
				for sessionKey, cancelFn := range gw.ws.ActiveRequests {
					sm.logger.Warn("force-cancelling request", "session", sessionKey)
					cancelFn()
				}
				gw.ws.ActiveRequestsMu.RUnlock()
			}
			return report
		}

		<-ticker.C
		sm.logger.Debug("waiting for in-flight work to drain", "requests", active, "jobs", jobs)
	}
}

// inFlight reports the drain's two counts: running turns in ActiveRequests
// that the scheduler does not already account for, and running scheduler
// jobs. A cron or heartbeat turn is registered in ActiveRequests
// (conduit-31jg.66) AND covered by its job's running flag
// (conduit-31jg.77); it is counted once, as a job.
func (sm *ShutdownManager) inFlight(sched drainableScheduler) (requests int, jobs []string) {
	if sched != nil {
		jobs = sched.RunningJobs()
	}
	gw := sm.gateway
	if gw == nil || gw.ws == nil {
		return 0, jobs
	}
	// Snapshot before taking ActiveRequestsMu (runner lock order). Without
	// a drainable scheduler nobody waits for those jobs: count them here.
	var scheduled map[string]string
	if sched != nil {
		scheduled = gw.turns().scheduledTurnKeys()
	}
	gw.ws.ActiveRequestsMu.RLock()
	defer gw.ws.ActiveRequestsMu.RUnlock()
	for key := range gw.ws.ActiveRequests {
		if _, ok := scheduled[key]; !ok {
			requests++
		}
	}
	return requests, jobs
}

// turnReport is the TurnRunner's drain report, nil without a runner.
// Called without ActiveRequestsMu held (runner lock order).
func (sm *ShutdownManager) turnReport() []TurnSnapshot {
	gw := sm.gateway
	if gw == nil || gw.ws == nil {
		return nil
	}
	return gw.turns().DrainReport()
}
