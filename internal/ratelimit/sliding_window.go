// Package ratelimit provides sliding window rate limiting for the Conduit gateway.
package ratelimit

import (
	"sync"
	"time"
)

// WindowBucket tracks requests within a time window for a specific client/IP
type WindowBucket struct {
	timestamps []time.Time  // Request timestamps within the window
	lastAccess time.Time    // Last access time for cleanup
	mu         sync.RWMutex // Per-bucket locking for fine-grained concurrency
}

// SlidingWindow implements sliding window rate limiting algorithm
type SlidingWindow struct {
	buckets     sync.Map       // string (client/IP) -> *WindowBucket
	windowDur   time.Duration  // Window duration (e.g., 60 seconds)
	limit       int            // Max requests allowed in window
	cleanupTick *time.Ticker   // Periodic cleanup ticker
	stopCleanup chan struct{}  // Signal to stop cleanup goroutine
	cleanupWG   sync.WaitGroup // Wait group for cleanup goroutine
}

// NewSlidingWindow creates a new sliding window rate limiter
func NewSlidingWindow(windowDuration time.Duration, limit int, cleanupInterval time.Duration) *SlidingWindow {
	sw := &SlidingWindow{
		windowDur:   windowDuration,
		limit:       limit,
		cleanupTick: time.NewTicker(cleanupInterval),
		stopCleanup: make(chan struct{}),
	}

	// Start cleanup goroutine
	sw.cleanupWG.Add(1)
	go sw.cleanupLoop()

	return sw
}

// Allow checks if a request should be allowed for the given identifier
// Returns: (allowed bool, remaining int, resetTime time.Time, retryAfter int)
func (sw *SlidingWindow) Allow(identifier string) (bool, int, time.Time, int) {
	now := time.Now()

	// Get or create bucket for this identifier
	bucketInterface, _ := sw.buckets.LoadOrStore(identifier, &WindowBucket{
		timestamps: make([]time.Time, 0),
		lastAccess: now,
	})
	bucket := bucketInterface.(*WindowBucket)

	bucket.mu.Lock()
	defer bucket.mu.Unlock()

	// Update last access time
	bucket.lastAccess = now

	// Remove timestamps outside the window
	sw.cleanExpiredTimestamps(bucket, now)

	// Check if we're at the limit
	currentCount := len(bucket.timestamps)
	if currentCount >= sw.limit {
		// Calculate when the oldest request will expire
		if len(bucket.timestamps) > 0 {
			oldestTime := bucket.timestamps[0]
			resetTime := oldestTime.Add(sw.windowDur)
			retryAfter := int(time.Until(resetTime).Seconds())
			// Ensure retry-after is always at least 1 second
			if retryAfter <= 0 {
				retryAfter = 1
			}
			return false, 0, resetTime, retryAfter
		}
		// Fallback if timestamps are empty (shouldn't happen)
		resetTime := now.Add(sw.windowDur)
		retryAfterSecs := int(sw.windowDur.Seconds())
		if retryAfterSecs <= 0 {
			retryAfterSecs = 1
		}
		return false, 0, resetTime, retryAfterSecs
	}

	// Add current timestamp
	bucket.timestamps = append(bucket.timestamps, now)
	remaining := sw.limit - len(bucket.timestamps)

	// Calculate reset time (when the oldest request will expire)
	resetTime := now.Add(sw.windowDur)
	if len(bucket.timestamps) > 0 {
		// Reset time is when the oldest timestamp expires
		resetTime = bucket.timestamps[0].Add(sw.windowDur)
	}

	return true, remaining, resetTime, 0
}

// cleanExpiredTimestamps removes timestamps outside the current window
func (sw *SlidingWindow) cleanExpiredTimestamps(bucket *WindowBucket, now time.Time) {
	cutoff := now.Add(-sw.windowDur)

	// Find first timestamp that's still valid
	validStart := 0
	for i, ts := range bucket.timestamps {
		if ts.After(cutoff) {
			validStart = i
			break
		}
		validStart = len(bucket.timestamps) // All expired if we don't find any valid
	}

	// Keep only valid timestamps
	if validStart > 0 {
		// Create new slice to avoid holding references to expired timestamps
		newTimestamps := make([]time.Time, len(bucket.timestamps)-validStart)
		copy(newTimestamps, bucket.timestamps[validStart:])
		bucket.timestamps = newTimestamps
	}
}

// cleanupLoop periodically removes expired buckets to prevent memory leaks
func (sw *SlidingWindow) cleanupLoop() {
	defer sw.cleanupWG.Done()

	for {
		select {
		case <-sw.cleanupTick.C:
			sw.performCleanup()
		case <-sw.stopCleanup:
			return
		}
	}
}

// performCleanup removes buckets that haven't been accessed recently
func (sw *SlidingWindow) performCleanup() {
	now := time.Now()
	cutoff := now.Add(-sw.windowDur * 2) // Remove buckets inactive for 2x window duration

	keysToDelete := make([]string, 0)

	sw.buckets.Range(func(key, value interface{}) bool {
		bucket := value.(*WindowBucket)
		bucket.mu.RLock()
		lastAccess := bucket.lastAccess
		bucket.mu.RUnlock()

		if lastAccess.Before(cutoff) {
			keysToDelete = append(keysToDelete, key.(string))
		}
		return true
	})

	// Delete expired buckets
	for _, key := range keysToDelete {
		sw.buckets.Delete(key)
	}

	if len(keysToDelete) > 0 {
		// Note: In production, you might want to use a proper logger here
		// log.Printf("[RateLimit] Cleaned up %d expired buckets", len(keysToDelete))
	}
}

// Stop stops the sliding window rate limiter and cleans up resources
func (sw *SlidingWindow) Stop() {
	if sw.cleanupTick != nil {
		sw.cleanupTick.Stop()
	}
	close(sw.stopCleanup)
	sw.cleanupWG.Wait()
}

// GetStats returns current statistics about the rate limiter
func (sw *SlidingWindow) GetStats() Stats {
	bucketCount := 0
	totalTimestamps := 0

	sw.buckets.Range(func(key, value interface{}) bool {
		bucketCount++
		bucket := value.(*WindowBucket)
		bucket.mu.RLock()
		totalTimestamps += len(bucket.timestamps)
		bucket.mu.RUnlock()
		return true
	})

	return Stats{
		ActiveBuckets:   bucketCount,
		TotalTimestamps: totalTimestamps,
		WindowDuration:  sw.windowDur,
		Limit:           sw.limit,
	}
}

// Stats contains statistics about the rate limiter
type Stats struct {
	ActiveBuckets   int           // Number of active client/IP buckets
	TotalTimestamps int           // Total request timestamps being tracked
	WindowDuration  time.Duration // Window duration
	Limit           int           // Request limit per window
}
