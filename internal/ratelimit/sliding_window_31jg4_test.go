package ratelimit

import (
	"sync"
	"testing"
	"time"
)

// conduit-31jg.4: Stop must be idempotent (double Stop used to panic on
// close of closed channel).
func TestSlidingWindow_StopIdempotent(t *testing.T) {
	sw := NewSlidingWindow(time.Minute, 10, time.Hour)
	sw.Stop()
	sw.Stop()
}

// conduit-31jg.4: cleanup racing with Allow must never leave a request
// recorded only on a bucket that has been evicted from the map. With a huge
// window nothing is ever stale, so we force eviction by back-dating
// lastAccess from a concurrent goroutine and then running cleanup; after all
// goroutines finish, the number of recorded timestamps in the live bucket
// plus the number of evicted-while-holding timestamps must equal the calls.
func TestSlidingWindow_CleanupDoesNotOrphanAllow(t *testing.T) {
	sw := NewSlidingWindow(time.Hour, 1_000_000, time.Hour)
	defer sw.Stop()

	const calls = 5000
	var wg sync.WaitGroup
	stop := make(chan struct{})
	var evictedCount int
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			// Back-date the bucket so performCleanup considers it stale.
			if v, ok := sw.buckets.Load("k"); ok {
				b := v.(*WindowBucket)
				b.mu.Lock()
				b.lastAccess = time.Now().Add(-3 * time.Hour)
				b.mu.Unlock()
			}
			before, _ := sw.buckets.Load("k")
			sw.performCleanup()
			if before != nil {
				if cur, still := sw.buckets.Load("k"); !still || cur != before {
					b := before.(*WindowBucket)
					b.mu.RLock()
					evictedCount += len(b.timestamps)
					b.mu.RUnlock()
				}
			}
		}
	}()

	for i := 0; i < calls; i++ {
		sw.Allow("k")
	}
	close(stop)
	wg.Wait()

	live := 0
	if v, ok := sw.buckets.Load("k"); ok {
		live = len(v.(*WindowBucket).timestamps)
	}
	// Every Allow must land either on the live bucket or on a bucket whose
	// eviction we observed (and counted) *after* the Allow completed.
	// Before the fix, Allows landing on an already-evicted bucket were lost:
	// evictedCount+live < calls.
	if live+evictedCount < calls {
		t.Fatalf("lost %d requests to orphaned buckets", calls-live-evictedCount)
	}
}
