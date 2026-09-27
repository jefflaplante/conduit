package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// processRestartBreadcrumb reads the restart breadcrumb written by the previous
// gateway instance (if any) and injects a resume message into each session
// recorded in the breadcrumb. The breadcrumb file is removed after processing.
func (g *Gateway) processRestartBreadcrumb() {
	dataDir := g.config.DataDir
	if dataDir == "" {
		dataDir = "."
	}

	path := filepath.Join(dataDir, ".conduit-restart.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}

	var breadcrumb RestartBreadcrumb
	if err := json.Unmarshal(data, &breadcrumb); err != nil {
		g.logger.Warn("failed to parse restart breadcrumb", "error", err, "path", path)
		os.Remove(path)
		return
	}

	resumed := 0
	for _, s := range breadcrumb.ActiveSessions {
		session, err := g.sessions.GetSession(s.SessionKey)
		if err != nil || session == nil {
			g.logger.Debug("skipping stale session from breadcrumb", "session", s.SessionKey)
			continue
		}

		msg := fmt.Sprintf("Gateway restarted successfully at %s. Reason: %s. Previous sessions have been restored — you may continue where you left off.",
			breadcrumb.Timestamp.Format(time.RFC3339), breadcrumb.Reason)

		if _, err := g.sessions.AddMessage(s.SessionKey, "assistant", msg, nil); err != nil {
			g.logger.Warn("failed to inject restart resume message", "session", s.SessionKey, "error", err)
			continue
		}
		resumed++
	}

	os.Remove(path)
	g.logger.Info("processed restart breadcrumb", "sessions_resumed", resumed, "reason", breadcrumb.Reason)
}

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

// RestartBreadcrumb captures session state so the LLM can resume post-restart.
type RestartBreadcrumb struct {
	ActiveSessions []BreadcrumbSession `json:"active_sessions"`
	TriggerAction  string              `json:"trigger_action,omitempty"`
	Reason         string              `json:"reason"`
	Timestamp      time.Time           `json:"timestamp"`
}

type BreadcrumbSession struct {
	SessionKey string `json:"session_key"`
	UserID     string `json:"user_id"`
	LastMsgID  string `json:"last_message_id,omitempty"`
	ChannelID  string `json:"channel_id,omitempty"`
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

	// Phase 2: Write breadcrumb for LLM session resumption
	sm.writeBreadcrumb()

	// Phase 3: Drain — wait for in-flight requests to finish
	sm.drainActiveRequests()

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
func (sm *ShutdownManager) drainActiveRequests() {
	gw := sm.gateway
	sched := sm.drainScheduler()
	if sched != nil {
		sched.BeginDrain()
	}
	if (gw == nil || gw.ws == nil) && sched == nil {
		return
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

	activeRequests := func() int {
		if gw == nil || gw.ws == nil {
			return 0
		}
		gw.ws.ActiveRequestsMu.RLock()
		defer gw.ws.ActiveRequestsMu.RUnlock()
		return len(gw.ws.ActiveRequests)
	}
	runningJobs := func() []string {
		if sched == nil {
			return nil
		}
		return sched.RunningJobs()
	}

	for {
		active := activeRequests()
		jobs := runningJobs()
		if active == 0 && len(jobs) == 0 {
			sm.logger.Info("all active requests and scheduler jobs drained")
			return
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
			if gw != nil && gw.ws != nil {
				gw.ws.ActiveRequestsMu.RLock()
				for sessionKey, cancelFn := range gw.ws.ActiveRequests {
					sm.logger.Warn("force-cancelling request", "session", sessionKey)
					cancelFn()
				}
				gw.ws.ActiveRequestsMu.RUnlock()
			}
			if sched != nil && len(jobs) > 0 {
				for _, id := range jobs {
					sm.logger.Warn("cancelling scheduler job: interrupted by shutdown", "job_id", id)
				}
				sched.InterruptRunning()
			}
			return
		}

		<-ticker.C
		sm.logger.Debug("waiting for in-flight work to drain", "requests", active, "jobs", jobs)
	}
}

func (sm *ShutdownManager) writeBreadcrumb() {
	gw := sm.gateway
	if gw.config == nil {
		return
	}

	dataDir := gw.config.DataDir
	if dataDir == "" {
		dataDir = "."
	}

	var activeSessions []BreadcrumbSession
	if gw.ws != nil {
		gw.ws.ClientMu.RLock()
		seen := make(map[string]bool)
		for _, client := range gw.ws.Clients {
			sk := client.SessionKey() // conduit-31jg.25
			if sk != "" && !seen[sk] {
				seen[sk] = true
				activeSessions = append(activeSessions, BreadcrumbSession{
					SessionKey: sk,
					UserID:     client.UserID,
					ChannelID:  client.ID,
				})
			}
		}
		gw.ws.ClientMu.RUnlock()
	}

	sm.mu.Lock()
	trigger := sm.triggerAction
	sm.mu.Unlock()

	breadcrumb := RestartBreadcrumb{
		ActiveSessions: activeSessions,
		TriggerAction:  trigger,
		Reason:         sm.reason,
		Timestamp:      time.Now(),
	}

	data, err := json.MarshalIndent(breadcrumb, "", "  ")
	if err != nil {
		sm.logger.Error("failed to marshal restart breadcrumb", "error", err)
		return
	}

	path := filepath.Join(dataDir, ".conduit-restart.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		sm.logger.Error("failed to write restart breadcrumb", "error", err, "path", path)
		return
	}

	sm.logger.Info("restart breadcrumb written", "path", path, "sessions", len(activeSessions))
}
