package gateway

import (
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"conduit/internal/config"
)

// conduit-31jg.27: a port conflict used to be logged from the
// ListenAndServe goroutine while the gateway kept running without HTTP/WS/
// health. listenHTTP binds synchronously and reports the error.
func TestListenHTTP_BindFailureReturnsError(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()

	start := time.Now()
	ln, err := listenHTTP(busy.Addr().String(), 300*time.Millisecond)
	if err == nil {
		ln.Close()
		t.Fatal("expected bind error on busy port")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("retry window not honoured: %v", time.Since(start))
	}

	// Non-EADDRINUSE errors fail immediately.
	if _, err := listenHTTP("256.0.0.1:1", 5*time.Second); err == nil {
		t.Fatal("expected error for invalid address")
	}
}

func TestListenHTTP_RetriesUntilPortFrees(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := busy.Addr().String()
	time.AfterFunc(300*time.Millisecond, func() { busy.Close() })

	ln, err := listenHTTP(addr, 3*time.Second)
	if err != nil {
		t.Fatalf("expected bind to succeed after predecessor released port: %v", err)
	}
	ln.Close()
}

// End-to-end: Gateway.Start returns a bind error instead of running headless.
func TestGatewayStart_FailsOnBusyPort(t *testing.T) {
	busy, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	port := busy.Addr().(*net.TCPAddr).Port

	dir := t.TempDir()
	t.Setenv("CONDUIT_DATA_DIR", dir)
	cfg := config.Default()
	cfg.Port = port
	cfg.DataDir = dir
	cfg.Database.Path = filepath.Join(dir, "gw.db")
	cfg.AI.Providers = []config.ProviderConfig{{Name: "anthropic", Type: "anthropic", APIKey: "test-key", Model: "claude-sonnet-4-20250514"}}
	cfg.Tools.Sandbox.WorkspaceDir = filepath.Join(dir, "workspace")
	cfg.Tools.Sandbox.AllowedPaths = []string{cfg.Tools.Sandbox.WorkspaceDir}
	cfg.Channels = nil
	cfg.Heartbeat.Enabled = false
	cfg.Logging.Level = "error"

	gw, err := New(cfg)
	if err != nil {
		t.Skipf("cannot construct gateway in this environment: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- gw.Start(ctx) }()
	select {
	case err := <-errCh:
		if err == nil || !strings.Contains(err.Error(), "bind") {
			t.Fatalf("Start error = %v, want bind failure", err)
		}
	case <-time.After(12 * time.Second):
		t.Fatal("Start did not fail on busy port (running headless?)")
	}
}

// The fixed 2s post-cancel sleep is replaced by waiting for the gateway's
// stop sequence; onShutdown (re-exec) must run only after it, and Done must
// close only after onShutdown.
func TestShutdownManager_WaitsForGatewayStopBeforeOnShutdown(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	gw := newTestGatewayForShutdown(t, &config.Config{DataDir: t.TempDir()})
	sm := NewShutdownManager(logger, gw)

	markStopped := sm.TrackGateway()
	cancelled := make(chan struct{})
	sm.SetCancel(func() { close(cancelled) })
	var stoppedAt, hookAt time.Time
	sm.SetOnShutdown(func() { hookAt = time.Now() })

	if err := sm.BeginShutdown("test", time.Second); err != nil {
		t.Fatal(err)
	}
	<-cancelled
	time.Sleep(100 * time.Millisecond) // simulate stopAll
	select {
	case <-sm.Done():
		t.Fatal("Done closed before gateway stop finished")
	default:
	}
	stoppedAt = time.Now()
	markStopped()

	select {
	case <-sm.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown sequence did not complete after gateway stopped")
	}
	if hookAt.IsZero() || hookAt.Before(stoppedAt) {
		t.Fatalf("onShutdown ran at %v, gateway stopped at %v", hookAt, stoppedAt)
	}
	if sm.State() != StateStopped {
		t.Fatalf("state = %s, want stopped", sm.State())
	}
}
