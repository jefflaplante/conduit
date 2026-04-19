package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"conduit/internal/config"
)

func TestShutdownManager_StateTransitions(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	gw := &Gateway{
		clients:        make(map[string]*Client),
		activeRequests: make(map[string]context.CancelFunc),
		config:         &config.Config{DataDir: t.TempDir()},
	}
	sm := NewShutdownManager(logger, gw)

	if sm.State() != StateRunning {
		t.Fatalf("expected StateRunning, got %s", sm.State())
	}
	if sm.IsDraining() {
		t.Fatal("should not be draining initially")
	}

	cancelled := make(chan struct{})
	sm.SetCancel(func() { close(cancelled) })

	if err := sm.BeginShutdown("test", 2*time.Second); err != nil {
		t.Fatalf("BeginShutdown: %v", err)
	}

	// Should transition to draining immediately
	time.Sleep(50 * time.Millisecond)
	if !sm.IsDraining() {
		t.Fatal("should be draining after BeginShutdown")
	}

	// Wait for cancel to be called
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel was not called within timeout")
	}

	// Second call should be a no-op error
	if err := sm.BeginShutdown("duplicate", time.Second); err == nil {
		t.Error("expected error on duplicate BeginShutdown")
	}
}

func TestShutdownManager_DrainActiveRequests(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	gw := &Gateway{
		clients:        make(map[string]*Client),
		activeRequests: make(map[string]context.CancelFunc),
		config:         &config.Config{DataDir: t.TempDir()},
	}

	// Simulate an active request that completes after 500ms
	var requestCancelled bool
	gw.activeRequestsMu.Lock()
	gw.activeRequests["session-1"] = func() { requestCancelled = true }
	gw.activeRequestsMu.Unlock()

	go func() {
		time.Sleep(500 * time.Millisecond)
		gw.activeRequestsMu.Lock()
		delete(gw.activeRequests, "session-1")
		gw.activeRequestsMu.Unlock()
	}()

	sm := NewShutdownManager(logger, gw)
	done := make(chan struct{})
	sm.SetCancel(func() { close(done) })

	if err := sm.BeginShutdown("drain-test", 5*time.Second); err != nil {
		t.Fatalf("BeginShutdown: %v", err)
	}

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown did not complete")
	}

	if requestCancelled {
		t.Error("request should have drained naturally, not been force-cancelled")
	}
}

func TestShutdownManager_DrainTimeout_ForceCancels(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	gw := &Gateway{
		clients:        make(map[string]*Client),
		activeRequests: make(map[string]context.CancelFunc),
		config:         &config.Config{DataDir: t.TempDir()},
	}

	var mu sync.Mutex
	forceCancelled := false
	gw.activeRequestsMu.Lock()
	gw.activeRequests["stuck-session"] = func() {
		mu.Lock()
		forceCancelled = true
		mu.Unlock()
	}
	gw.activeRequestsMu.Unlock()

	sm := NewShutdownManager(logger, gw)
	done := make(chan struct{})
	sm.SetCancel(func() { close(done) })

	if err := sm.BeginShutdown("timeout-test", 1*time.Second); err != nil {
		t.Fatalf("BeginShutdown: %v", err)
	}

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown did not complete after drain timeout")
	}

	mu.Lock()
	if !forceCancelled {
		t.Error("stuck request should have been force-cancelled after drain timeout")
	}
	mu.Unlock()
}

func TestShutdownManager_WritesBreadcrumb(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	dataDir := t.TempDir()

	gw := &Gateway{
		clients:        make(map[string]*Client),
		activeRequests: make(map[string]context.CancelFunc),
		config:         &config.Config{DataDir: dataDir},
	}
	gw.clients["ws-1"] = &Client{
		ID:         "ws-1",
		SessionKey: "session-abc",
		UserID:     "jeff",
	}

	sm := NewShutdownManager(logger, gw)
	done := make(chan struct{})
	sm.SetCancel(func() { close(done) })

	if err := sm.BeginShutdown("breadcrumb-test", 2*time.Second); err != nil {
		t.Fatalf("BeginShutdown: %v", err)
	}

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown did not complete")
	}

	path := filepath.Join(dataDir, ".conduit-restart.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("breadcrumb file not written: %v", err)
	}

	var breadcrumb RestartBreadcrumb
	if err := json.Unmarshal(data, &breadcrumb); err != nil {
		t.Fatalf("invalid breadcrumb JSON: %v", err)
	}

	if breadcrumb.Reason != "breadcrumb-test" {
		t.Errorf("reason = %q, want %q", breadcrumb.Reason, "breadcrumb-test")
	}
	if len(breadcrumb.ActiveSessions) != 1 {
		t.Fatalf("expected 1 active session, got %d", len(breadcrumb.ActiveSessions))
	}
	if breadcrumb.ActiveSessions[0].SessionKey != "session-abc" {
		t.Errorf("session key = %q, want %q", breadcrumb.ActiveSessions[0].SessionKey, "session-abc")
	}
}

func TestShutdownState_String(t *testing.T) {
	tests := []struct {
		state ShutdownState
		want  string
	}{
		{StateRunning, "running"},
		{StateDraining, "draining"},
		{StateTerminate, "terminating"},
		{StateStopped, "stopped"},
		{ShutdownState(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.state.String(); got != tt.want {
			t.Errorf("ShutdownState(%d).String() = %q, want %q", tt.state, got, tt.want)
		}
	}
}
