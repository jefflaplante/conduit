package ratelimit

import (
	"sync"
	"testing"
	"time"
)

func TestSlidingWindow_BasicFunctionality(t *testing.T) {
	// Create a rate limiter: 3 requests per 100ms window
	sw := NewSlidingWindow(100*time.Millisecond, 3, 50*time.Millisecond)
	defer sw.Stop()

	client := "test_client"

	// First 3 requests should be allowed
	for i := 0; i < 3; i++ {
		allowed, remaining, _, _ := sw.Allow(client)
		if !allowed {
			t.Errorf("Request %d should be allowed", i+1)
		}
		expectedRemaining := 3 - (i + 1)
		if remaining != expectedRemaining {
			t.Errorf("Request %d: expected remaining %d, got %d", i+1, expectedRemaining, remaining)
		}
	}

	// 4th request should be rejected
	allowed, remaining, _, retryAfter := sw.Allow(client)
	if allowed {
		t.Error("4th request should be rejected")
	}
	if remaining != 0 {
		t.Errorf("Expected remaining 0, got %d", remaining)
	}
	if retryAfter <= 0 {
		t.Errorf("Expected positive retry after, got %d", retryAfter)
	}
}

func TestSlidingWindow_WindowSliding(t *testing.T) {
	// Create a rate limiter: 2 requests per 50ms window
	sw := NewSlidingWindow(50*time.Millisecond, 2, 25*time.Millisecond)
	defer sw.Stop()

	client := "test_client"

	// Use up the limit
	allowed, _, _, _ := sw.Allow(client)
	if !allowed {
		t.Error("First request should be allowed")
	}
	allowed, _, _, _ = sw.Allow(client)
	if !allowed {
		t.Error("Second request should be allowed")
	}

	// Third request should be rejected
	allowed, _, _, _ = sw.Allow(client)
	if allowed {
		t.Error("Third request should be rejected")
	}

	// Wait for window to slide (60ms > 50ms window)
	time.Sleep(60 * time.Millisecond)

	// Now we should be able to make requests again
	allowed, _, _, _ = sw.Allow(client)
	if !allowed {
		t.Error("Request after window slide should be allowed")
	}
	allowed, _, _, _ = sw.Allow(client)
	if !allowed {
		t.Error("Second request after window slide should be allowed")
	}

	// But third should still be rejected
	allowed, _, _, _ = sw.Allow(client)
	if allowed {
		t.Error("Third request after window slide should be rejected")
	}
}

func TestSlidingWindow_MultipleClients(t *testing.T) {
	// Create a rate limiter: 2 requests per 100ms window
	sw := NewSlidingWindow(100*time.Millisecond, 2, 50*time.Millisecond)
	defer sw.Stop()

	// Each client should have independent limits
	clients := []string{"client1", "client2", "client3"}

	for _, client := range clients {
		// Each client can make 2 requests
		allowed, _, _, _ := sw.Allow(client)
		if !allowed {
			t.Errorf("First request for %s should be allowed", client)
		}
		allowed, _, _, _ = sw.Allow(client)
		if !allowed {
			t.Errorf("Second request for %s should be allowed", client)
		}

		// Third request should be rejected for each
		allowed, _, _, _ = sw.Allow(client)
		if allowed {
			t.Errorf("Third request for %s should be rejected", client)
		}
	}
}

func TestSlidingWindow_ConcurrentAccess(t *testing.T) {
	// Create a rate limiter: 100 requests per 200ms window
	sw := NewSlidingWindow(200*time.Millisecond, 100, 100*time.Millisecond)
	defer sw.Stop()

	// Test concurrent access from multiple goroutines
	numGoroutines := 10
	requestsPerGoroutine := 20
	var wg sync.WaitGroup
	var allowedCount int32
	var rejectedCount int32
	var mu sync.Mutex

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(clientID int) {
			defer wg.Done()
			client := "concurrent_client"

			for j := 0; j < requestsPerGoroutine; j++ {
				allowed, _, _, _ := sw.Allow(client)
				mu.Lock()
				if allowed {
					allowedCount++
				} else {
					rejectedCount++
				}
				mu.Unlock()

				// Small delay to spread requests
				time.Sleep(1 * time.Millisecond)
			}
		}(i)
	}

	wg.Wait()

	mu.Lock()
	totalRequests := allowedCount + rejectedCount
	mu.Unlock()

	expectedTotal := int32(numGoroutines * requestsPerGoroutine)
	if totalRequests != expectedTotal {
		t.Errorf("Expected %d total requests, got %d", expectedTotal, totalRequests)
	}

	// We should have exactly 100 allowed requests (the limit)
	if allowedCount != 100 {
		t.Errorf("Expected 100 allowed requests, got %d", allowedCount)
	}

	// The rest should be rejected
	expectedRejected := expectedTotal - 100
	if rejectedCount != expectedRejected {
		t.Errorf("Expected %d rejected requests, got %d", expectedRejected, rejectedCount)
	}
}

func TestSlidingWindow_CleanupFunctionality(t *testing.T) {
	// Create a rate limiter with very frequent cleanup for testing
	sw := NewSlidingWindow(50*time.Millisecond, 5, 10*time.Millisecond)
	defer sw.Stop()

	// Create activity for multiple clients
	clients := []string{"client1", "client2", "client3", "client4", "client5"}
	for _, client := range clients {
		sw.Allow(client)
	}

	// Initial stats should show all clients
	stats := sw.GetStats()
	if stats.ActiveBuckets != 5 {
		t.Errorf("Expected 5 active buckets initially, got %d", stats.ActiveBuckets)
	}

	// Wait longer than cleanup window (2x window duration = 100ms)
	time.Sleep(120 * time.Millisecond)

	// After cleanup, buckets should be removed
	stats = sw.GetStats()
	if stats.ActiveBuckets != 0 {
		t.Errorf("Expected 0 active buckets after cleanup, got %d", stats.ActiveBuckets)
	}
}

func TestSlidingWindow_ResetTimeAccuracy(t *testing.T) {
	sw := NewSlidingWindow(100*time.Millisecond, 1, 50*time.Millisecond)
	defer sw.Stop()

	client := "test_client"
	start := time.Now()

	// Use up the limit
	allowed, _, resetTime, _ := sw.Allow(client)
	if !allowed {
		t.Error("First request should be allowed")
	}

	// Reset time should be approximately start + window duration
	expectedReset := start.Add(100 * time.Millisecond)
	tolerance := 10 * time.Millisecond

	if resetTime.Before(expectedReset.Add(-tolerance)) || resetTime.After(expectedReset.Add(tolerance)) {
		t.Errorf("Reset time %v not within tolerance of expected %v", resetTime, expectedReset)
	}
}

func TestSlidingWindow_RetryAfterCalculation(t *testing.T) {
	sw := NewSlidingWindow(100*time.Millisecond, 1, 50*time.Millisecond)
	defer sw.Stop()

	client := "test_client"

	// Use up the limit
	sw.Allow(client)

	// Next request should be rejected with retry-after
	allowed, _, _, retryAfter := sw.Allow(client)
	if allowed {
		t.Error("Second request should be rejected")
	}

	// Retry-after should be reasonable (between 0 and window duration)
	if retryAfter <= 0 || retryAfter > 100 {
		t.Errorf("Retry-after %d seconds should be between 1 and 100", retryAfter)
	}
}

func TestSlidingWindow_EdgeCasesAtLimit(t *testing.T) {
	sw := NewSlidingWindow(100*time.Millisecond, 3, 50*time.Millisecond)
	defer sw.Stop()

	client := "test_client"

	// Make exactly the limit number of requests
	for i := 0; i < 3; i++ {
		allowed, remaining, _, _ := sw.Allow(client)
		if !allowed {
			t.Errorf("Request %d should be allowed", i+1)
		}
		if remaining != 3-(i+1) {
			t.Errorf("Request %d: expected remaining %d, got %d", i+1, 3-(i+1), remaining)
		}
	}

	// Next request should be rejected
	allowed, remaining, _, _ := sw.Allow(client)
	if allowed {
		t.Error("Request over limit should be rejected")
	}
	if remaining != 0 {
		t.Errorf("Expected remaining 0 when over limit, got %d", remaining)
	}
}

func TestSlidingWindow_StatsAccuracy(t *testing.T) {
	sw := NewSlidingWindow(100*time.Millisecond, 5, 50*time.Millisecond)
	defer sw.Stop()

	// Initially no activity
	stats := sw.GetStats()
	if stats.ActiveBuckets != 0 || stats.TotalTimestamps != 0 {
		t.Errorf("Expected empty stats initially, got buckets=%d, timestamps=%d",
			stats.ActiveBuckets, stats.TotalTimestamps)
	}
	if stats.WindowDuration != 100*time.Millisecond || stats.Limit != 5 {
		t.Errorf("Expected window=100ms, limit=5, got window=%v, limit=%d",
			stats.WindowDuration, stats.Limit)
	}

	// Add some activity
	clients := []string{"client1", "client2"}
	for _, client := range clients {
		for i := 0; i < 2; i++ {
			sw.Allow(client)
		}
	}

	stats = sw.GetStats()
	if stats.ActiveBuckets != 2 {
		t.Errorf("Expected 2 active buckets, got %d", stats.ActiveBuckets)
	}
	if stats.TotalTimestamps != 4 {
		t.Errorf("Expected 4 total timestamps, got %d", stats.TotalTimestamps)
	}
}

// Benchmark tests
func BenchmarkSlidingWindow_Allow(b *testing.B) {
	sw := NewSlidingWindow(time.Minute, 1000, 30*time.Second)
	defer sw.Stop()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		clientID := "bench_client"
		for pb.Next() {
			sw.Allow(clientID)
		}
	})
}

func BenchmarkSlidingWindow_AllowMultipleClients(b *testing.B) {
	sw := NewSlidingWindow(time.Minute, 1000, 30*time.Second)
	defer sw.Stop()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			clientID := "bench_client_" + string(rune(i%10))
			sw.Allow(clientID)
			i++
		}
	})
}
