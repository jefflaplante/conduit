package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newSnapshotTestMiddleware(t *testing.T) *RateLimitMiddleware {
	t.Helper()
	cfg := DefaultRateLimitConfig()
	cfg.Anonymous.WindowSeconds = 60
	cfg.Anonymous.MaxRequests = 5
	cfg.Authenticated.WindowSeconds = 60
	cfg.Authenticated.MaxRequests = 10
	cfg.CleanupIntervalSeconds = 60
	return NewRateLimitMiddleware(RateLimitMiddlewareConfig{Config: cfg})
}

func TestRateLimitMiddleware_Snapshot_DisabledShowsConfig(t *testing.T) {
	cfg := DefaultRateLimitConfig()
	cfg.Enabled = false
	m := NewRateLimitMiddleware(RateLimitMiddlewareConfig{Config: cfg})
	snap := m.Snapshot(5)

	if snap.Enabled {
		t.Error("Enabled: want false")
	}
	if snap.Anonymous.Enabled || snap.Authenticated.Enabled {
		t.Error("tiers should be disabled when middleware is disabled")
	}
	// Limits / window should still reflect the config so agents can reason
	// about the *would-be* ceilings.
	if snap.Anonymous.Limit != cfg.Anonymous.MaxRequests {
		t.Errorf("Anonymous.Limit: want %d, got %d", cfg.Anonymous.MaxRequests, snap.Anonymous.Limit)
	}
}

func TestRateLimitMiddleware_Snapshot_CapturesAnonymousTraffic(t *testing.T) {
	m := newSnapshotTestMiddleware(t)
	defer m.Stop()

	// Drive some anonymous traffic through the middleware.
	handler := m.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for _, ip := range []string{"1.1.1.1", "2.2.2.2", "2.2.2.2"} {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.RemoteAddr = ip + ":5000"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}

	snap := m.Snapshot(0)
	if !snap.Enabled || !snap.Anonymous.Enabled {
		t.Fatal("anonymous tier should be enabled")
	}
	if snap.Anonymous.ActiveBuckets != 2 {
		t.Errorf("ActiveBuckets: want 2, got %d", snap.Anonymous.ActiveBuckets)
	}
	if len(snap.Anonymous.TopIdentifiers) != 2 {
		t.Fatalf("TopIdentifiers: want 2, got %d", len(snap.Anonymous.TopIdentifiers))
	}
	// First entry should be the one with least remaining (2.2.2.2 with 3 remaining < 1.1.1.1 with 4).
	first := snap.Anonymous.TopIdentifiers[0]
	if first.Identifier != "2.2.2.2" {
		t.Errorf("first identifier: want 2.2.2.2 got %s", first.Identifier)
	}
	if first.Used != 2 || first.Remaining != 3 {
		t.Errorf("first: want used=2 remaining=3, got used=%d remaining=%d", first.Used, first.Remaining)
	}
	if first.ResetAt.IsZero() {
		t.Error("ResetAt should be populated")
	}
}

func TestRateLimitMiddleware_Snapshot_TopNCap(t *testing.T) {
	m := newSnapshotTestMiddleware(t)
	defer m.Stop()
	handler := m.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	// Create 4 buckets.
	for _, ip := range []string{"1.1.1.1", "2.2.2.2", "3.3.3.3", "4.4.4.4"} {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.RemoteAddr = ip + ":5000"
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}

	snap := m.Snapshot(2)
	if len(snap.Anonymous.TopIdentifiers) != 2 {
		t.Errorf("TopIdentifiers with topN=2: got %d", len(snap.Anonymous.TopIdentifiers))
	}
	if snap.Anonymous.ActiveBuckets != 4 {
		t.Errorf("ActiveBuckets: want 4, got %d", snap.Anonymous.ActiveBuckets)
	}
}

func TestRateLimitMiddleware_Snapshot_AuthenticatedAndAnonymousIsolated(t *testing.T) {
	m := newSnapshotTestMiddleware(t)
	defer m.Stop()

	// Hit the anonymous limiter directly via the exposed sliding window.
	m.anonymousLimiter.Allow("anon-x")
	m.authenticatedLimiter.Allow("client-a")
	m.authenticatedLimiter.Allow("client-a")

	snap := m.Snapshot(5)
	if snap.Anonymous.ActiveBuckets != 1 {
		t.Errorf("anon buckets: want 1, got %d", snap.Anonymous.ActiveBuckets)
	}
	if snap.Authenticated.ActiveBuckets != 1 {
		t.Errorf("auth buckets: want 1, got %d", snap.Authenticated.ActiveBuckets)
	}
	if snap.Authenticated.Limit != 10 {
		t.Errorf("auth limit: want 10, got %d", snap.Authenticated.Limit)
	}
}

func TestRateLimitMiddleware_Snapshot_TakenAtRecent(t *testing.T) {
	m := newSnapshotTestMiddleware(t)
	defer m.Stop()
	before := time.Now()
	snap := m.Snapshot(0)
	after := time.Now()
	if snap.TakenAt.Before(before) || snap.TakenAt.After(after) {
		t.Errorf("TakenAt %v outside [%v,%v]", snap.TakenAt, before, after)
	}
}
