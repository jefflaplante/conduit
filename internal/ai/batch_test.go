package ai

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"conduit/internal/config"
	"conduit/internal/sessions"
)

// --- Helper factories ---

func newTestBatchQueue(maxSize int, ttl time.Duration) *BatchQueue {
	return NewBatchQueue(BatchQueueConfig{
		MaxSize:    maxSize,
		DefaultTTL: ttl,
	})
}

func newTestBatchRequest(priority BatchPriority) *BatchRequest {
	return &BatchRequest{
		Session: &sessions.Session{
			Key:       "test-session",
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
		UserMessage:  "test message",
		ProviderName: "mock",
		Priority:     priority,
	}
}

// --- BatchStatus tests ---

func TestBatchStatus_String(t *testing.T) {
	tests := []struct {
		status   BatchStatus
		expected string
	}{
		{BatchStatusPending, "pending"},
		{BatchStatusProcessing, "processing"},
		{BatchStatusCompleted, "completed"},
		{BatchStatusFailed, "failed"},
		{BatchStatusCancelled, "cancelled"},
		{BatchStatusExpired, "expired"},
		{BatchStatus(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if got := tt.status.String(); got != tt.expected {
				t.Errorf("BatchStatus(%d).String() = %q, want %q", tt.status, got, tt.expected)
			}
		})
	}
}

// --- BatchQueue enqueue/dequeue tests ---

func TestBatchQueue_EnqueueDequeue_FIFO(t *testing.T) {
	q := newTestBatchQueue(0, 0)

	// Enqueue three requests at same priority
	req1 := newTestBatchRequest(BatchPriorityNormal)
	req1.UserMessage = "first"
	req2 := newTestBatchRequest(BatchPriorityNormal)
	req2.UserMessage = "second"
	req3 := newTestBatchRequest(BatchPriorityNormal)
	req3.UserMessage = "third"

	id1, err := q.Enqueue(req1)
	if err != nil {
		t.Fatalf("Enqueue req1 failed: %v", err)
	}
	if id1 == "" {
		t.Fatal("Expected non-empty ticket ID")
	}

	id2, err := q.Enqueue(req2)
	if err != nil {
		t.Fatalf("Enqueue req2 failed: %v", err)
	}

	id3, err := q.Enqueue(req3)
	if err != nil {
		t.Fatalf("Enqueue req3 failed: %v", err)
	}

	// All IDs should be unique
	if id1 == id2 || id2 == id3 || id1 == id3 {
		t.Errorf("Expected unique ticket IDs, got %s, %s, %s", id1, id2, id3)
	}

	// Dequeue should return in FIFO order for same priority
	d1 := q.Dequeue()
	if d1 == nil || d1.UserMessage != "first" {
		t.Errorf("Expected first request, got %v", d1)
	}
	if d1.status != BatchStatusProcessing {
		t.Errorf("Expected processing status, got %s", d1.status)
	}

	d2 := q.Dequeue()
	if d2 == nil || d2.UserMessage != "second" {
		t.Errorf("Expected second request, got %v", d2)
	}

	d3 := q.Dequeue()
	if d3 == nil || d3.UserMessage != "third" {
		t.Errorf("Expected third request, got %v", d3)
	}

	// Queue should now be empty
	d4 := q.Dequeue()
	if d4 != nil {
		t.Errorf("Expected nil from empty queue, got %v", d4)
	}
}

func TestBatchQueue_PriorityOrdering(t *testing.T) {
	q := newTestBatchQueue(0, 0)

	// Enqueue in order: low, normal, urgent, high
	low := newTestBatchRequest(BatchPriorityLow)
	low.UserMessage = "low"

	normal := newTestBatchRequest(BatchPriorityNormal)
	normal.UserMessage = "normal"

	urgent := newTestBatchRequest(BatchPriorityUrgent)
	urgent.UserMessage = "urgent"

	high := newTestBatchRequest(BatchPriorityHigh)
	high.UserMessage = "high"

	q.Enqueue(low)
	q.Enqueue(normal)
	q.Enqueue(urgent)
	q.Enqueue(high)

	// Dequeue should return in priority order: urgent, high, normal, low
	d1 := q.Dequeue()
	if d1 == nil || d1.UserMessage != "urgent" {
		t.Errorf("Expected urgent first, got %v", d1)
	}

	d2 := q.Dequeue()
	if d2 == nil || d2.UserMessage != "high" {
		t.Errorf("Expected high second, got %v", d2)
	}

	d3 := q.Dequeue()
	if d3 == nil || d3.UserMessage != "normal" {
		t.Errorf("Expected normal third, got %v", d3)
	}

	d4 := q.Dequeue()
	if d4 == nil || d4.UserMessage != "low" {
		t.Errorf("Expected low fourth, got %v", d4)
	}
}

func TestBatchQueue_PriorityFIFOWithinSameLevel(t *testing.T) {
	q := newTestBatchQueue(0, 0)

	// Enqueue multiple requests at the same priority
	first := newTestBatchRequest(BatchPriorityHigh)
	first.UserMessage = "first-high"

	second := newTestBatchRequest(BatchPriorityHigh)
	second.UserMessage = "second-high"

	third := newTestBatchRequest(BatchPriorityHigh)
	third.UserMessage = "third-high"

	q.Enqueue(first)
	q.Enqueue(second)
	q.Enqueue(third)

	// Same priority should maintain FIFO order
	d1 := q.Dequeue()
	if d1 == nil || d1.UserMessage != "first-high" {
		t.Errorf("Expected first-high, got %v", d1)
	}

	d2 := q.Dequeue()
	if d2 == nil || d2.UserMessage != "second-high" {
		t.Errorf("Expected second-high, got %v", d2)
	}

	d3 := q.Dequeue()
	if d3 == nil || d3.UserMessage != "third-high" {
		t.Errorf("Expected third-high, got %v", d3)
	}
}

// --- Queue capacity tests ---

func TestBatchQueue_CapacityLimit(t *testing.T) {
	q := newTestBatchQueue(2, 0) // Max 2 requests

	req1 := newTestBatchRequest(BatchPriorityNormal)
	req2 := newTestBatchRequest(BatchPriorityNormal)
	req3 := newTestBatchRequest(BatchPriorityNormal)

	_, err := q.Enqueue(req1)
	if err != nil {
		t.Fatalf("First enqueue should succeed: %v", err)
	}

	_, err = q.Enqueue(req2)
	if err != nil {
		t.Fatalf("Second enqueue should succeed: %v", err)
	}

	_, err = q.Enqueue(req3)
	if err != ErrQueueFull {
		t.Errorf("Third enqueue should fail with ErrQueueFull, got: %v", err)
	}
}

func TestBatchQueue_UnlimitedCapacity(t *testing.T) {
	q := newTestBatchQueue(0, 0) // 0 = unlimited

	// Should be able to enqueue many requests
	for i := 0; i < 100; i++ {
		req := newTestBatchRequest(BatchPriorityNormal)
		_, err := q.Enqueue(req)
		if err != nil {
			t.Fatalf("Enqueue %d failed: %v", i, err)
		}
	}

	if q.Len() != 100 {
		t.Errorf("Expected 100 items, got %d", q.Len())
	}
}

// --- Request expiry/timeout tests ---

func TestBatchQueue_RequestExpiry(t *testing.T) {
	q := newTestBatchQueue(0, 50*time.Millisecond) // 50ms TTL

	req := newTestBatchRequest(BatchPriorityNormal)
	id, _ := q.Enqueue(req)

	// Should be pending immediately
	status, ok := q.Status(id)
	if !ok || status != BatchStatusPending {
		t.Errorf("Expected pending status, got %s (found=%v)", status, ok)
	}

	// Wait for expiry
	time.Sleep(100 * time.Millisecond)

	// Should now be expired
	status, ok = q.Status(id)
	if !ok || status != BatchStatusExpired {
		t.Errorf("Expected expired status, got %s (found=%v)", status, ok)
	}

	// Dequeue should return nil (expired request is skipped)
	d := q.Dequeue()
	if d != nil {
		t.Errorf("Expected nil from queue with expired request, got %v", d)
	}
}

func TestBatchQueue_ExplicitExpiry(t *testing.T) {
	q := newTestBatchQueue(0, 0) // No default TTL

	req := newTestBatchRequest(BatchPriorityNormal)
	req.ExpiresAt = time.Now().Add(-1 * time.Second) // Already expired

	id, _ := q.Enqueue(req)

	// The explicit expiry should be honored even without default TTL
	status, ok := q.Status(id)
	if !ok || status != BatchStatusExpired {
		t.Errorf("Expected expired status for past-due request, got %s (found=%v)", status, ok)
	}
}

func TestBatchQueue_NoExpiry(t *testing.T) {
	q := newTestBatchQueue(0, 0) // No TTL

	req := newTestBatchRequest(BatchPriorityNormal)
	id, _ := q.Enqueue(req)

	// Without TTL, request should remain pending
	status, ok := q.Status(id)
	if !ok || status != BatchStatusPending {
		t.Errorf("Expected pending status (no TTL), got %s (found=%v)", status, ok)
	}
}

func TestBatchQueue_ExpireStale(t *testing.T) {
	q := newTestBatchQueue(0, 50*time.Millisecond)

	for i := 0; i < 5; i++ {
		req := newTestBatchRequest(BatchPriorityNormal)
		q.Enqueue(req)
	}

	time.Sleep(100 * time.Millisecond)

	expired := q.ExpireStale()
	if expired != 5 {
		t.Errorf("Expected 5 expired, got %d", expired)
	}

	if q.PendingCount() != 0 {
		t.Errorf("Expected 0 pending after expiry, got %d", q.PendingCount())
	}
}

// --- Cancel functionality ---

func TestBatchQueue_Cancel(t *testing.T) {
	q := newTestBatchQueue(0, 0)

	req := newTestBatchRequest(BatchPriorityNormal)
	id, _ := q.Enqueue(req)

	// Cancel should succeed
	err := q.Cancel(id)
	if err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}

	// Status should be cancelled
	status, ok := q.Status(id)
	if !ok || status != BatchStatusCancelled {
		t.Errorf("Expected cancelled status, got %s (found=%v)", status, ok)
	}

	// Dequeue should skip cancelled request
	d := q.Dequeue()
	if d != nil {
		t.Errorf("Expected nil from queue with cancelled request, got %v", d)
	}
}

func TestBatchQueue_CancelNonExistent(t *testing.T) {
	q := newTestBatchQueue(0, 0)

	err := q.Cancel("nonexistent-ticket")
	if err != ErrRequestNotFound {
		t.Errorf("Expected ErrRequestNotFound, got: %v", err)
	}
}

func TestBatchQueue_CancelAlreadyCancelled(t *testing.T) {
	q := newTestBatchQueue(0, 0)

	req := newTestBatchRequest(BatchPriorityNormal)
	id, _ := q.Enqueue(req)

	q.Cancel(id)

	err := q.Cancel(id)
	if err != ErrAlreadyCancelled {
		t.Errorf("Expected ErrAlreadyCancelled, got: %v", err)
	}
}

func TestBatchQueue_CancelProcessingRequest(t *testing.T) {
	q := newTestBatchQueue(0, 0)

	req := newTestBatchRequest(BatchPriorityNormal)
	id, _ := q.Enqueue(req)

	// Dequeue to move to processing
	q.Dequeue()

	err := q.Cancel(id)
	if err == nil {
		t.Error("Expected error when cancelling processing request")
	}
}

// --- Status tracking ---

func TestBatchQueue_StatusTracking(t *testing.T) {
	q := newTestBatchQueue(0, 0)

	req := newTestBatchRequest(BatchPriorityNormal)
	id, _ := q.Enqueue(req)

	// Initially pending
	status, ok := q.Status(id)
	if !ok || status != BatchStatusPending {
		t.Errorf("Expected pending, got %s", status)
	}

	// Dequeue moves to processing
	q.Dequeue()
	status, _ = q.Status(id)
	if status != BatchStatusProcessing {
		t.Errorf("Expected processing, got %s", status)
	}

	// Mark complete
	q.markComplete(id, &SimpleConversationResponse{Content: "done"}, nil)
	status, _ = q.Status(id)
	if status != BatchStatusCompleted {
		t.Errorf("Expected completed, got %s", status)
	}
}

func TestBatchQueue_StatusNotFound(t *testing.T) {
	q := newTestBatchQueue(0, 0)

	_, ok := q.Status("nonexistent")
	if ok {
		t.Error("Expected ok=false for nonexistent ticket")
	}
}

func TestBatchQueue_StatusDetail(t *testing.T) {
	q := newTestBatchQueue(0, 0)

	// Enqueue two requests
	req1 := newTestBatchRequest(BatchPriorityHigh)
	id1, _ := q.Enqueue(req1)

	req2 := newTestBatchRequest(BatchPriorityNormal)
	id2, _ := q.Enqueue(req2)

	// Check position
	detail1, ok := q.StatusDetail(id1)
	if !ok {
		t.Fatal("Expected to find request 1")
	}
	if detail1.Position != 1 {
		t.Errorf("Expected position 1 for high-priority request, got %d", detail1.Position)
	}

	detail2, ok := q.StatusDetail(id2)
	if !ok {
		t.Fatal("Expected to find request 2")
	}
	if detail2.Position != 2 {
		t.Errorf("Expected position 2 for normal-priority request, got %d", detail2.Position)
	}
}

func TestBatchQueue_MarkFailed(t *testing.T) {
	q := newTestBatchQueue(0, 0)

	req := newTestBatchRequest(BatchPriorityNormal)
	id, _ := q.Enqueue(req)

	q.Dequeue()
	q.markFailed(id, ErrRequestExpired)

	status, _ := q.Status(id)
	if status != BatchStatusFailed {
		t.Errorf("Expected failed, got %s", status)
	}

	detail, _ := q.StatusDetail(id)
	if detail.Error == "" {
		t.Error("Expected error message in status detail")
	}
}

// --- Len and PendingCount ---

func TestBatchQueue_LenAndPendingCount(t *testing.T) {
	q := newTestBatchQueue(0, 0)

	if q.Len() != 0 {
		t.Errorf("Expected empty queue, got %d", q.Len())
	}

	req1 := newTestBatchRequest(BatchPriorityNormal)
	req2 := newTestBatchRequest(BatchPriorityNormal)
	req3 := newTestBatchRequest(BatchPriorityNormal)

	q.Enqueue(req1)
	q.Enqueue(req2)
	id3, _ := q.Enqueue(req3)

	if q.Len() != 3 {
		t.Errorf("Expected Len=3, got %d", q.Len())
	}
	if q.PendingCount() != 3 {
		t.Errorf("Expected PendingCount=3, got %d", q.PendingCount())
	}

	// Dequeue one
	q.Dequeue()
	// Len is unchanged (request is still tracked), but PendingCount decreases
	if q.PendingCount() != 2 {
		t.Errorf("Expected PendingCount=2 after dequeue, got %d", q.PendingCount())
	}

	// Cancel one
	q.Cancel(id3)
	if q.PendingCount() != 1 {
		t.Errorf("Expected PendingCount=1 after cancel, got %d", q.PendingCount())
	}
}

// --- DrainAll ---

func TestBatchQueue_DrainAll(t *testing.T) {
	q := newTestBatchQueue(0, 0)

	for i := 0; i < 5; i++ {
		req := newTestBatchRequest(BatchPriorityNormal)
		q.Enqueue(req)
	}

	// Dequeue one to make it processing
	q.Dequeue()

	// Drain should only cancel pending ones
	drained := q.DrainAll()
	if len(drained) != 4 {
		t.Errorf("Expected 4 drained, got %d", len(drained))
	}

	for _, req := range drained {
		if req.status != BatchStatusCancelled {
			t.Errorf("Expected cancelled status after drain, got %s", req.status)
		}
		if req.err != ErrProcessorStopped {
			t.Errorf("Expected ErrProcessorStopped, got %v", req.err)
		}
	}

	if q.PendingCount() != 0 {
		t.Errorf("Expected 0 pending after drain, got %d", q.PendingCount())
	}
}

// --- Cleanup ---

func TestBatchQueue_Cleanup(t *testing.T) {
	q := newTestBatchQueue(0, 0)

	// Add requests and move them to terminal states
	req1 := newTestBatchRequest(BatchPriorityNormal)
	id1, _ := q.Enqueue(req1)
	q.Dequeue()
	q.markComplete(id1, &SimpleConversationResponse{Content: "done"}, nil)

	req2 := newTestBatchRequest(BatchPriorityNormal)
	id2, _ := q.Enqueue(req2)
	q.Cancel(id2)

	req3 := newTestBatchRequest(BatchPriorityNormal)
	q.Enqueue(req3) // Still pending

	// Cleanup with 0 duration removes all terminal entries
	removed := q.Cleanup(0)
	if removed != 2 {
		t.Errorf("Expected 2 removed, got %d", removed)
	}

	// Pending request should remain
	if q.Len() != 1 {
		t.Errorf("Expected 1 remaining, got %d", q.Len())
	}
}

func TestBatchQueue_Cleanup_OlderThan(t *testing.T) {
	q := newTestBatchQueue(0, 0)

	req := newTestBatchRequest(BatchPriorityNormal)
	id, _ := q.Enqueue(req)
	q.Dequeue()
	q.markComplete(id, &SimpleConversationResponse{Content: "done"}, nil)

	// Cleanup with 1 hour: nothing is old enough
	removed := q.Cleanup(1 * time.Hour)
	if removed != 0 {
		t.Errorf("Expected 0 removed (not old enough), got %d", removed)
	}
}

// --- Concurrent enqueue/dequeue safety ---

func TestBatchQueue_ConcurrentSafety(t *testing.T) {
	q := newTestBatchQueue(0, 0)
	const numGoroutines = 50
	const opsPerGoroutine = 20

	var wg sync.WaitGroup

	// Concurrent enqueue
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < opsPerGoroutine; j++ {
				req := newTestBatchRequest(BatchPriority(id % 4 * 10))
				q.Enqueue(req)
			}
		}(i)
	}
	wg.Wait()

	totalEnqueued := numGoroutines * opsPerGoroutine
	if q.Len() != totalEnqueued {
		t.Errorf("Expected %d enqueued, got %d", totalEnqueued, q.Len())
	}

	// Concurrent dequeue
	var dequeued int64
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			for {
				req := q.Dequeue()
				if req == nil {
					return
				}
				atomic.AddInt64(&dequeued, 1)
			}
		}()
	}
	wg.Wait()

	if dequeued != int64(totalEnqueued) {
		t.Errorf("Expected %d dequeued, got %d", totalEnqueued, dequeued)
	}
}

func TestBatchQueue_ConcurrentEnqueueDequeue(t *testing.T) {
	q := newTestBatchQueue(1000, 0)
	const workers = 20

	var wg sync.WaitGroup
	var enqueued int64
	var dequeued int64

	// Mix of enqueuers and dequeuers
	for i := 0; i < workers; i++ {
		wg.Add(2)

		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				req := newTestBatchRequest(BatchPriorityNormal)
				if _, err := q.Enqueue(req); err == nil {
					atomic.AddInt64(&enqueued, 1)
				}
			}
		}()

		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if q.Dequeue() != nil {
					atomic.AddInt64(&dequeued, 1)
				}
				time.Sleep(time.Microsecond) // Small delay to let enqueuers catch up
			}
		}()
	}
	wg.Wait()

	// All enqueued items should either be dequeued or still in queue
	remaining := int64(q.PendingCount())
	if dequeued+remaining != enqueued {
		t.Errorf("Accounting mismatch: dequeued=%d + pending=%d != enqueued=%d",
			dequeued, remaining, enqueued)
	}
}

// --- Processor lifecycle tests ---

func TestBatchProcessor_StartStop(t *testing.T) {
	q := newTestBatchQueue(0, 0)
	p := NewBatchProcessor(q, BatchProcessorConfig{
		PollInterval: 10 * time.Millisecond,
	})

	if p.IsRunning() {
		t.Error("Expected processor to not be running before Start()")
	}

	p.Start()
	if !p.IsRunning() {
		t.Error("Expected processor to be running after Start()")
	}

	// Double start should be a no-op
	p.Start()
	if !p.IsRunning() {
		t.Error("Expected processor still running after double Start()")
	}

	p.Stop()
	if p.IsRunning() {
		t.Error("Expected processor to not be running after Stop()")
	}

	// Double stop should be safe
	p.Stop()
}

func TestBatchProcessor_StopDrainsPending(t *testing.T) {
	q := newTestBatchQueue(0, 0)

	var callbackCalled int32
	for i := 0; i < 3; i++ {
		req := newTestBatchRequest(BatchPriorityNormal)
		req.Callback = func(resp ConversationResponse, rr *SmartRoutingResult, err error) {
			atomic.AddInt32(&callbackCalled, 1)
			if err != ErrProcessorStopped {
				t.Errorf("Expected ErrProcessorStopped in drain callback, got: %v", err)
			}
		}
		q.Enqueue(req)
	}

	// Create processor but don't start -- then stop (which drains)
	p := NewBatchProcessor(q, BatchProcessorConfig{
		PollInterval: 10 * time.Millisecond,
	})
	p.Start()

	// Stop immediately to drain
	p.Stop()

	// Give callbacks a moment to complete (they run synchronously in Stop)
	if callbackCalled != 3 {
		t.Errorf("Expected 3 drain callbacks, got %d", callbackCalled)
	}
}

func TestBatchProcessor_ProcessesRequests(t *testing.T) {
	// Create a router with a mock provider
	cfg := config.AIConfig{
		DefaultProvider: "mock",
		Providers:       []config.ProviderConfig{},
	}
	router, err := NewRouter(cfg, nil)
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}
	mock := NewMockProvider("mock")
	mock.AddResponse("batch result", nil)
	mock.AddResponse("batch result 2", nil)
	router.RegisterProvider("mock", mock)

	q := newTestBatchQueue(0, 0)
	p := NewBatchProcessor(q, BatchProcessorConfig{
		PollInterval:  10 * time.Millisecond,
		MaxConcurrent: 2,
		Router:        router,
	})

	resultCh := make(chan string, 1)
	req := newTestBatchRequest(BatchPriorityNormal)
	req.Callback = func(resp ConversationResponse, rr *SmartRoutingResult, err error) {
		if err != nil {
			resultCh <- "error: " + err.Error()
			return
		}
		resultCh <- resp.GetContent()
	}

	id, _ := q.Enqueue(req)

	p.Start()
	defer p.Stop()

	// Wait for processing
	select {
	case content := <-resultCh:
		if content != "batch result" {
			t.Errorf("Expected 'batch result', got %q", content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Timed out waiting for batch result")
	}

	// Verify status was updated
	status, ok := q.Status(id)
	if !ok {
		t.Fatal("Expected to find request after processing")
	}
	if status != BatchStatusCompleted {
		t.Errorf("Expected completed status, got %s", status)
	}
}

func TestBatchProcessor_CapacityChecker(t *testing.T) {
	q := newTestBatchQueue(0, 0)

	var capacityAvailable int32
	atomic.StoreInt32(&capacityAvailable, 0)

	p := NewBatchProcessor(q, BatchProcessorConfig{
		PollInterval: 10 * time.Millisecond,
	})
	p.SetCapacityChecker(func() bool {
		return atomic.LoadInt32(&capacityAvailable) == 1
	})

	req := newTestBatchRequest(BatchPriorityNormal)
	q.Enqueue(req)

	p.Start()
	defer p.Stop()

	// With capacity blocked, request should stay pending
	time.Sleep(50 * time.Millisecond)
	if q.PendingCount() != 1 {
		t.Errorf("Expected 1 pending (capacity blocked), got %d", q.PendingCount())
	}

	// Release capacity
	atomic.StoreInt32(&capacityAvailable, 1)

	// Wait for dequeue (processor has no router, so it will fail, but it will dequeue)
	time.Sleep(50 * time.Millisecond)
	if q.PendingCount() != 0 {
		t.Errorf("Expected 0 pending after capacity release, got %d", q.PendingCount())
	}
}

func TestBatchProcessor_Stats(t *testing.T) {
	q := newTestBatchQueue(0, 0)
	p := NewBatchProcessor(q, BatchProcessorConfig{
		PollInterval:  10 * time.Millisecond,
		MaxConcurrent: 3,
	})

	req := newTestBatchRequest(BatchPriorityNormal)
	q.Enqueue(req)

	stats := p.Stats()
	if stats.Running {
		t.Error("Expected Running=false before start")
	}
	if stats.QueuedPending != 1 {
		t.Errorf("Expected QueuedPending=1, got %d", stats.QueuedPending)
	}
	if stats.MaxConcurrent != 3 {
		t.Errorf("Expected MaxConcurrent=3, got %d", stats.MaxConcurrent)
	}
}

func TestBatchProcessor_NoRouterError(t *testing.T) {
	q := newTestBatchQueue(0, 0)
	p := NewBatchProcessor(q, BatchProcessorConfig{
		PollInterval: 10 * time.Millisecond,
		// No router configured
	})

	resultCh := make(chan error, 1)
	req := newTestBatchRequest(BatchPriorityNormal)
	req.Callback = func(resp ConversationResponse, rr *SmartRoutingResult, err error) {
		resultCh <- err
	}

	id, _ := q.Enqueue(req)

	p.Start()
	defer p.Stop()

	select {
	case err := <-resultCh:
		if err == nil {
			t.Fatal("Expected error when no router configured")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Timed out waiting for error callback")
	}

	status, _ := q.Status(id)
	if status != BatchStatusFailed {
		t.Errorf("Expected failed status, got %s", status)
	}
}

func TestBatchProcessor_DefaultConfig(t *testing.T) {
	q := newTestBatchQueue(0, 0)

	// Zero values for PollInterval and MaxConcurrent should get defaults
	p := NewBatchProcessor(q, BatchProcessorConfig{})

	if p.config.PollInterval != 5*time.Second {
		t.Errorf("Expected default PollInterval of 5s, got %v", p.config.PollInterval)
	}
	if p.config.MaxConcurrent != 1 {
		t.Errorf("Expected default MaxConcurrent of 1, got %d", p.config.MaxConcurrent)
	}
}

// --- EnqueueFromSmartRouting convenience ---

func TestEnqueueFromSmartRouting(t *testing.T) {
	q := newTestBatchQueue(0, 0)

	session := &sessions.Session{
		Key:       "smart-routing-session",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	var callbackInvoked bool
	callback := func(resp ConversationResponse, rr *SmartRoutingResult, err error) {
		callbackInvoked = true
	}

	id, err := EnqueueFromSmartRouting(q, session, "test message", "anthropic", BatchPriorityUrgent, callback)
	if err != nil {
		t.Fatalf("EnqueueFromSmartRouting failed: %v", err)
	}
	if id == "" {
		t.Fatal("Expected non-empty ticket ID")
	}

	// Verify the request is in the queue
	if q.PendingCount() != 1 {
		t.Errorf("Expected 1 pending, got %d", q.PendingCount())
	}

	// Verify the request fields
	req := q.Dequeue()
	if req == nil {
		t.Fatal("Expected to dequeue a request")
	}
	if req.Session.Key != "smart-routing-session" {
		t.Errorf("Expected session key 'smart-routing-session', got %q", req.Session.Key)
	}
	if req.UserMessage != "test message" {
		t.Errorf("Expected 'test message', got %q", req.UserMessage)
	}
	if req.ProviderName != "anthropic" {
		t.Errorf("Expected provider 'anthropic', got %q", req.ProviderName)
	}
	if req.Priority != BatchPriorityUrgent {
		t.Errorf("Expected urgent priority, got %d", req.Priority)
	}

	_ = callbackInvoked
}

// --- Custom ticket ID ---

func TestBatchQueue_CustomTicketID(t *testing.T) {
	q := newTestBatchQueue(0, 0)

	req := newTestBatchRequest(BatchPriorityNormal)
	req.TicketID = "custom-id-123"

	id, err := q.Enqueue(req)
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	if id != "custom-id-123" {
		t.Errorf("Expected custom ID 'custom-id-123', got %q", id)
	}

	status, ok := q.Status("custom-id-123")
	if !ok || status != BatchStatusPending {
		t.Errorf("Expected to find custom-id request, got found=%v status=%s", ok, status)
	}
}

// --- EnqueuedAt timestamp ---

func TestBatchQueue_SetsEnqueuedAt(t *testing.T) {
	q := newTestBatchQueue(0, 0)

	before := time.Now()
	req := newTestBatchRequest(BatchPriorityNormal)
	q.Enqueue(req)
	after := time.Now()

	if req.EnqueuedAt.Before(before) || req.EnqueuedAt.After(after) {
		t.Errorf("EnqueuedAt %v not between %v and %v", req.EnqueuedAt, before, after)
	}
}

// --- Error sentinel tests ---

func TestBatchErrors(t *testing.T) {
	if ErrQueueFull.Error() != "batch queue is full" {
		t.Errorf("Unexpected ErrQueueFull message: %s", ErrQueueFull.Error())
	}
	if ErrRequestExpired.Error() != "batch request expired" {
		t.Errorf("Unexpected ErrRequestExpired message: %s", ErrRequestExpired.Error())
	}
	if ErrRequestNotFound.Error() != "batch request not found" {
		t.Errorf("Unexpected ErrRequestNotFound message: %s", ErrRequestNotFound.Error())
	}
	if ErrProcessorStopped.Error() != "batch processor is stopped" {
		t.Errorf("Unexpected ErrProcessorStopped message: %s", ErrProcessorStopped.Error())
	}
	if ErrAlreadyCancelled.Error() != "batch request already cancelled" {
		t.Errorf("Unexpected ErrAlreadyCancelled message: %s", ErrAlreadyCancelled.Error())
	}
}
