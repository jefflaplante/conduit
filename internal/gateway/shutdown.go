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
	SessionKey   string `json:"session_key"`
	UserID       string `json:"user_id"`
	LastMsgID    string `json:"last_message_id,omitempty"`
	ChannelID    string `json:"channel_id,omitempty"`
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
}

func NewShutdownManager(logger *slog.Logger, gw *Gateway) *ShutdownManager {
	return &ShutdownManager{
		logger:       logger,
		gateway:      gw,
		drainTimeout: 30 * time.Second,
	}
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

	// Give the gateway shutdown sequence time to finish
	// (it has its own 30s http server shutdown timeout)
	time.Sleep(2 * time.Second)

	sm.state.Store(int32(StateStopped))
	sm.logger.Info("shutdown complete", "reason", sm.reason)

	// Execute post-shutdown hook (e.g. re-exec for restart)
	sm.mu.Lock()
	onShutdown := sm.onShutdown
	sm.mu.Unlock()

	if onShutdown != nil {
		onShutdown()
	}
}

func (sm *ShutdownManager) notifyClients() {
	gw := sm.gateway
	gw.clientMu.RLock()
	clients := make([]*Client, 0, len(gw.clients))
	for _, c := range gw.clients {
		clients = append(clients, c)
	}
	gw.clientMu.RUnlock()

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

func (sm *ShutdownManager) drainActiveRequests() {
	gw := sm.gateway
	deadline := time.After(sm.drainTimeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		gw.activeRequestsMu.RLock()
		active := len(gw.activeRequests)
		gw.activeRequestsMu.RUnlock()

		if active == 0 {
			sm.logger.Info("all active requests drained")
			return
		}

		select {
		case <-deadline:
			sm.logger.Warn("drain timeout exceeded, force-cancelling active requests",
				"remaining", active,
				"timeout", sm.drainTimeout,
			)
			gw.activeRequestsMu.RLock()
			for sessionKey, cancelFn := range gw.activeRequests {
				sm.logger.Warn("force-cancelling request", "session", sessionKey)
				cancelFn()
			}
			gw.activeRequestsMu.RUnlock()
			return
		case <-ticker.C:
			sm.logger.Debug("waiting for active requests to drain", "remaining", active)
		}
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

	gw.clientMu.RLock()
	var activeSessions []BreadcrumbSession
	seen := make(map[string]bool)
	for _, client := range gw.clients {
		if client.SessionKey != "" && !seen[client.SessionKey] {
			seen[client.SessionKey] = true
			activeSessions = append(activeSessions, BreadcrumbSession{
				SessionKey: client.SessionKey,
				UserID:     client.UserID,
				ChannelID:  client.ID,
			})
		}
	}
	gw.clientMu.RUnlock()

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
