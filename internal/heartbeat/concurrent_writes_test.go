package heartbeat

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"conduit/internal/brain"
	"conduit/internal/sessions"
)

// TestHeartbeat_ConcurrentBrainWrites_NoBusyError is a regression test for
// conduit-3qq8. It simulates many concurrent alert-syncing goroutines (the
// realistic shape of heartbeat + REM-cycle + user message traffic all hitting
// the same brain DB) and asserts that SQLITE_BUSY never surfaces to callers —
// retry + eventual success is acceptable.
func TestHeartbeat_ConcurrentBrainWrites_NoBusyError(t *testing.T) {
	tmp := t.TempDir()
	brainPath := filepath.Join(tmp, "brain.db")

	b, err := brain.New(brainPath, brain.WithMaxLTMEntries(1000))
	if err != nil {
		t.Fatalf("failed to open brain: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })

	// heartbeatBrainWriter-equivalent: write/delete/list under sense.alerts.*
	writer := &testBrainWriter{b: b}

	const (
		writers      = 16 // goroutines contending for DB writes
		opsPerWriter = 50
	)

	var wg sync.WaitGroup
	var busyErrs int64
	var otherErrs int64

	start := make(chan struct{})
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(wid int) {
			defer wg.Done()
			<-start
			ctx := context.Background()
			for i := 0; i < opsPerWriter; i++ {
				key := fmt.Sprintf("sense.alerts.heartbeat_task_execution.w%d_i%d", wid, i)
				value := fmt.Sprintf("severity=warning timestamp=%s message=concurrent write", time.Now().UTC().Format(time.RFC3339))

				if err := writer.StoreAlert(ctx, key, value); err != nil {
					if isBusy(err) {
						atomic.AddInt64(&busyErrs, 1)
					} else {
						atomic.AddInt64(&otherErrs, 1)
						t.Errorf("writer %d: unexpected StoreAlert error: %v", wid, err)
					}
					continue
				}

				// Every few ops, list + delete to exercise the full path that
				// heartbeat's processHeartbeatResult drives.
				if i%5 == 0 {
					if _, err := writer.ListAlertKeys(ctx, "sense.alerts."); err != nil {
						atomic.AddInt64(&otherErrs, 1)
						t.Errorf("writer %d: list failed: %v", wid, err)
					}
					if err := writer.DeleteAlert(ctx, key); err != nil {
						if isBusy(err) {
							atomic.AddInt64(&busyErrs, 1)
						} else {
							atomic.AddInt64(&otherErrs, 1)
							t.Errorf("writer %d: DeleteAlert error: %v", wid, err)
						}
					}
				}
			}
		}(w)
	}

	close(start)
	wg.Wait()

	if got := atomic.LoadInt64(&busyErrs); got != 0 {
		t.Fatalf("expected 0 SQLITE_BUSY errors surfaced to caller, got %d", got)
	}
	if got := atomic.LoadInt64(&otherErrs); got != 0 {
		t.Fatalf("expected 0 non-busy errors, got %d", got)
	}
}

// TestSessions_ConcurrentWrites_NoBusyError exercises the session store write
// paths that the heartbeat executor hits during ExecuteHeartbeatJob (session
// creation, message inserts, context updates, activity marking).
func TestSessions_ConcurrentWrites_NoBusyError(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "sessions.db")

	store, err := sessions.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to open sessions: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Seed a session that all writers will pound on (the heartbeat-like pattern
	// creates a fresh session each run, but many overlapping runs hit the same
	// sessions table).
	base, err := store.GetOrCreateSession("heartbeat", "base-heartbeat-session")
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}

	const (
		writers      = 16
		opsPerWriter = 30
	)

	var wg sync.WaitGroup
	var busyErrs int64
	var otherErrs int64

	start := make(chan struct{})
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(wid int) {
			defer wg.Done()
			<-start
			for i := 0; i < opsPerWriter; i++ {
				// Mix of writes: new sessions, messages into the shared base,
				// context updates, activity marks — same cocktail heartbeat hits.
				sess, err := store.GetOrCreateSession("heartbeat", fmt.Sprintf("sess_w%d_i%d", wid, i))
				if err != nil {
					trackErr(t, err, &busyErrs, &otherErrs, "GetOrCreateSession")
					continue
				}

				if _, err := store.AddMessage(sess.Key, "user", fmt.Sprintf("msg %d/%d", wid, i), nil); err != nil {
					trackErr(t, err, &busyErrs, &otherErrs, "AddMessage")
				}
				if _, err := store.AddMessage(base.Key, "assistant", fmt.Sprintf("from-%d-%d", wid, i), nil); err != nil {
					trackErr(t, err, &busyErrs, &otherErrs, "AddMessage base")
				}

				if err := store.SetSessionContext(base.Key, fmt.Sprintf("k_w%d", wid), fmt.Sprintf("v_%d", i)); err != nil {
					trackErr(t, err, &busyErrs, &otherErrs, "SetSessionContext")
				}

				store.MarkSessionActivity(base.Key)
			}
		}(w)
	}

	close(start)
	wg.Wait()

	if got := atomic.LoadInt64(&busyErrs); got != 0 {
		t.Fatalf("expected 0 SQLITE_BUSY errors surfaced to caller, got %d", got)
	}
	if got := atomic.LoadInt64(&otherErrs); got != 0 {
		t.Fatalf("expected 0 non-busy errors, got %d", got)
	}
}

func trackErr(t *testing.T, err error, busy, other *int64, op string) {
	t.Helper()
	if isBusy(err) {
		atomic.AddInt64(busy, 1)
		return
	}
	atomic.AddInt64(other, 1)
	t.Errorf("%s: unexpected error: %v", op, err)
}

func isBusy(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "SQLITE_BUSY") ||
		strings.Contains(msg, "database table is locked")
}

// testBrainWriter mirrors gateway.heartbeatBrainWriter so the concurrent-write
// test exercises the real brain.Store / brain.Delete / brain.List code paths
// without pulling in the gateway package.
type testBrainWriter struct {
	b *brain.Brain
}

func (w *testBrainWriter) StoreAlert(ctx context.Context, key, value string) error {
	return w.b.Store(ctx, key, value, brain.TierLongTerm, "system:heartbeat")
}

func (w *testBrainWriter) DeleteAlert(ctx context.Context, key string) error {
	return w.b.Delete(ctx, key)
}

func (w *testBrainWriter) ListAlertKeys(ctx context.Context, prefix string) ([]string, error) {
	entries, err := w.b.List(ctx, prefix, "")
	if err != nil {
		return nil, err
	}
	keys := make([]string, len(entries))
	for i, e := range entries {
		keys[i] = e.Key
	}
	return keys, nil
}
