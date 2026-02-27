package ai

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"conduit/internal/sessions"
)

// Batch processing errors.
var (
	ErrQueueFull        = errors.New("batch queue is full")
	ErrRequestExpired   = errors.New("batch request expired")
	ErrRequestNotFound  = errors.New("batch request not found")
	ErrProcessorStopped = errors.New("batch processor is stopped")
	ErrAlreadyCancelled = errors.New("batch request already cancelled")
)

// BatchPriority defines the priority level for batch requests.
// Higher values are processed first.
type BatchPriority int

const (
	// BatchPriorityLow is for background/deferrable requests.
	BatchPriorityLow BatchPriority = 0

	// BatchPriorityNormal is the default priority.
	BatchPriorityNormal BatchPriority = 10

	// BatchPriorityHigh is for important requests.
	BatchPriorityHigh BatchPriority = 20

	// BatchPriorityUrgent is for requests that should be processed ASAP.
	BatchPriorityUrgent BatchPriority = 30
)

// BatchStatus represents the current state of a batch request.
type BatchStatus int

const (
	// BatchStatusPending means the request is queued and waiting.
	BatchStatusPending BatchStatus = iota

	// BatchStatusProcessing means the request is currently being executed.
	BatchStatusProcessing

	// BatchStatusCompleted means the request finished successfully.
	BatchStatusCompleted

	// BatchStatusFailed means the request failed with an error.
	BatchStatusFailed

	// BatchStatusCancelled means the request was cancelled before processing.
	BatchStatusCancelled

	// BatchStatusExpired means the request timed out while waiting in the queue.
	BatchStatusExpired
)

// String returns a human-readable name for the status.
func (s BatchStatus) String() string {
	switch s {
	case BatchStatusPending:
		return "pending"
	case BatchStatusProcessing:
		return "processing"
	case BatchStatusCompleted:
		return "completed"
	case BatchStatusFailed:
		return "failed"
	case BatchStatusCancelled:
		return "cancelled"
	case BatchStatusExpired:
		return "expired"
	default:
		return "unknown"
	}
}

// BatchResultCallback is called when a batch request completes.
// It receives the response (or nil on error) and any error that occurred.
type BatchResultCallback func(resp ConversationResponse, routingResult *SmartRoutingResult, err error)

// BatchRequest wraps a GenerateRequest with batch-specific metadata.
type BatchRequest struct {
	// TicketID is the unique identifier for this batch request.
	TicketID string

	// Session is the session context for the request.
	Session *sessions.Session

	// UserMessage is the user's message text.
	UserMessage string

	// ProviderName is the target AI provider.
	ProviderName string

	// Priority determines processing order (higher = sooner).
	Priority BatchPriority

	// Callback is invoked when the request completes or fails.
	// May be nil if the caller only polls via Status().
	Callback BatchResultCallback

	// EnqueuedAt is when the request was added to the queue.
	EnqueuedAt time.Time

	// ExpiresAt is the deadline after which the request should not be processed.
	// Zero value means no expiry.
	ExpiresAt time.Time

	// status tracks the current state of this request.
	status BatchStatus

	// result stores the response after completion.
	result ConversationResponse

	// routingResult stores the smart routing metadata after completion.
	routingResult *SmartRoutingResult

	// err stores any error that occurred during processing.
	err error
}

// BatchQueueConfig holds configuration for the batch queue.
type BatchQueueConfig struct {
	// MaxSize is the maximum number of requests that can be queued.
	// Zero means unlimited (not recommended in production).
	MaxSize int

	// DefaultTTL is the default time-to-live for queued requests.
	// Requests that exceed this duration are expired. Zero means no expiry.
	DefaultTTL time.Duration
}

// BatchQueue is a thread-safe priority queue for pending batch requests.
type BatchQueue struct {
	mu       sync.Mutex
	requests []*BatchRequest
	index    map[string]*BatchRequest // ticketID -> request for O(1) lookup
	config   BatchQueueConfig
	nextID   int64
}

// NewBatchQueue creates a new batch queue with the given configuration.
func NewBatchQueue(cfg BatchQueueConfig) *BatchQueue {
	return &BatchQueue{
		requests: make([]*BatchRequest, 0),
		index:    make(map[string]*BatchRequest),
		config:   cfg,
	}
}

// Enqueue adds a request to the queue and returns a ticket ID.
// Returns ErrQueueFull if the queue is at capacity.
func (q *BatchQueue) Enqueue(req *BatchRequest) (string, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	// Check capacity
	if q.config.MaxSize > 0 && len(q.requests) >= q.config.MaxSize {
		return "", ErrQueueFull
	}

	// Assign ticket ID if not already set
	if req.TicketID == "" {
		q.nextID++
		req.TicketID = fmt.Sprintf("batch-%d-%d", time.Now().UnixNano(), q.nextID)
	}

	// Set enqueue time
	req.EnqueuedAt = time.Now()
	req.status = BatchStatusPending

	// Apply default TTL if no explicit expiry
	if req.ExpiresAt.IsZero() && q.config.DefaultTTL > 0 {
		req.ExpiresAt = req.EnqueuedAt.Add(q.config.DefaultTTL)
	}

	// Insert in priority order (highest priority first, then FIFO within same priority)
	inserted := false
	for i, existing := range q.requests {
		if req.Priority > existing.Priority {
			// Insert before this element
			q.requests = append(q.requests, nil)
			copy(q.requests[i+1:], q.requests[i:])
			q.requests[i] = req
			inserted = true
			break
		}
	}
	if !inserted {
		q.requests = append(q.requests, req)
	}

	q.index[req.TicketID] = req

	return req.TicketID, nil
}

// Dequeue returns the highest-priority pending request, marking it as processing.
// The request remains in the queue for status tracking and cleanup.
// Returns nil if the queue is empty or all requests are expired/cancelled.
// Expired requests are automatically marked and skipped.
func (q *BatchQueue) Dequeue() *BatchRequest {
	q.mu.Lock()
	defer q.mu.Unlock()

	now := time.Now()

	for i := 0; i < len(q.requests); i++ {
		req := q.requests[i]

		// Skip non-pending requests
		if req.status != BatchStatusPending {
			continue
		}

		// Check expiry
		if !req.ExpiresAt.IsZero() && now.After(req.ExpiresAt) {
			req.status = BatchStatusExpired
			req.err = ErrRequestExpired
			continue
		}

		// Found a valid request: mark as processing (keep in slice for tracking)
		req.status = BatchStatusProcessing
		return req
	}

	return nil
}

// Status returns the current status of a request by ticket ID.
// Returns the status and true if found, or BatchStatusPending and false if not found.
func (q *BatchQueue) Status(ticketID string) (BatchStatus, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	req, ok := q.index[ticketID]
	if !ok {
		return BatchStatusPending, false
	}

	// Lazily check expiry
	if req.status == BatchStatusPending && !req.ExpiresAt.IsZero() && time.Now().After(req.ExpiresAt) {
		req.status = BatchStatusExpired
		req.err = ErrRequestExpired
	}

	return req.status, true
}

// StatusDetail returns full status information for a batch request.
func (q *BatchQueue) StatusDetail(ticketID string) (*BatchRequestStatus, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	req, ok := q.index[ticketID]
	if !ok {
		return nil, false
	}

	// Lazily check expiry
	if req.status == BatchStatusPending && !req.ExpiresAt.IsZero() && time.Now().After(req.ExpiresAt) {
		req.status = BatchStatusExpired
		req.err = ErrRequestExpired
	}

	detail := &BatchRequestStatus{
		TicketID:   req.TicketID,
		Status:     req.status,
		Priority:   req.Priority,
		EnqueuedAt: req.EnqueuedAt,
		ExpiresAt:  req.ExpiresAt,
	}

	if req.status == BatchStatusPending {
		detail.Position = q.positionOf(ticketID)
	}

	if req.err != nil {
		detail.Error = req.err.Error()
	}

	return detail, true
}

// Cancel cancels a pending request. Returns an error if the request is not
// found or is not in a cancellable state.
func (q *BatchQueue) Cancel(ticketID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	req, ok := q.index[ticketID]
	if !ok {
		return ErrRequestNotFound
	}

	if req.status != BatchStatusPending {
		if req.status == BatchStatusCancelled {
			return ErrAlreadyCancelled
		}
		return fmt.Errorf("cannot cancel request in state %s", req.status)
	}

	req.status = BatchStatusCancelled
	return nil
}

// Len returns the total number of requests in the queue (all states).
func (q *BatchQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.requests)
}

// PendingCount returns the number of requests still in pending state.
func (q *BatchQueue) PendingCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()

	count := 0
	for _, req := range q.requests {
		if req.status == BatchStatusPending {
			count++
		}
	}
	return count
}

// DrainAll removes all pending requests from the queue, marking them as cancelled.
// Returns the cancelled requests so callbacks can be invoked by the caller.
func (q *BatchQueue) DrainAll() []*BatchRequest {
	q.mu.Lock()
	defer q.mu.Unlock()

	var drained []*BatchRequest
	for _, req := range q.requests {
		if req.status == BatchStatusPending {
			req.status = BatchStatusCancelled
			req.err = ErrProcessorStopped
			drained = append(drained, req)
		}
	}
	return drained
}

// ExpireStale checks all pending requests and marks expired ones.
// Returns the number of requests expired.
func (q *BatchQueue) ExpireStale() int {
	q.mu.Lock()
	defer q.mu.Unlock()

	now := time.Now()
	count := 0
	for _, req := range q.requests {
		if req.status == BatchStatusPending && !req.ExpiresAt.IsZero() && now.After(req.ExpiresAt) {
			req.status = BatchStatusExpired
			req.err = ErrRequestExpired
			count++
		}
	}
	return count
}

// Cleanup removes completed, failed, cancelled, and expired requests that are
// older than the given age from both the queue slice and the index map.
// Returns the number of removed entries.
func (q *BatchQueue) Cleanup(olderThan time.Duration) int {
	q.mu.Lock()
	defer q.mu.Unlock()

	cutoff := time.Now().Add(-olderThan)
	removed := 0

	remaining := q.requests[:0]
	for _, req := range q.requests {
		terminal := req.status == BatchStatusCompleted ||
			req.status == BatchStatusFailed ||
			req.status == BatchStatusCancelled ||
			req.status == BatchStatusExpired

		if terminal && req.EnqueuedAt.Before(cutoff) {
			delete(q.index, req.TicketID)
			removed++
		} else {
			remaining = append(remaining, req)
		}
	}
	q.requests = remaining

	return removed
}

// positionOf returns the 1-based position of a ticket in the pending queue.
// Must be called with the mutex held. Returns 0 if not found.
func (q *BatchQueue) positionOf(ticketID string) int {
	pos := 0
	for _, req := range q.requests {
		if req.status == BatchStatusPending {
			pos++
			if req.TicketID == ticketID {
				return pos
			}
		}
	}
	return 0
}

// markComplete updates a request's status to completed with the given result.
func (q *BatchQueue) markComplete(ticketID string, resp ConversationResponse, routingResult *SmartRoutingResult) {
	q.mu.Lock()
	defer q.mu.Unlock()

	req, ok := q.index[ticketID]
	if !ok {
		return
	}
	req.status = BatchStatusCompleted
	req.result = resp
	req.routingResult = routingResult
}

// markFailed updates a request's status to failed with the given error.
func (q *BatchQueue) markFailed(ticketID string, err error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	req, ok := q.index[ticketID]
	if !ok {
		return
	}
	req.status = BatchStatusFailed
	req.err = err
}

// BatchRequestStatus provides detailed status information about a batch request.
type BatchRequestStatus struct {
	TicketID   string        `json:"ticket_id"`
	Status     BatchStatus   `json:"status"`
	Priority   BatchPriority `json:"priority"`
	Position   int           `json:"position,omitempty"` // queue position (1-based, 0 if not queued)
	EnqueuedAt time.Time     `json:"enqueued_at"`
	ExpiresAt  time.Time     `json:"expires_at,omitempty"`
	Error      string        `json:"error,omitempty"`
}

// BatchProcessorConfig holds configuration for the batch processor.
type BatchProcessorConfig struct {
	// PollInterval is how often the processor checks for available capacity.
	PollInterval time.Duration

	// MaxConcurrent is the maximum number of requests to process simultaneously.
	// Zero or negative means process one at a time.
	MaxConcurrent int

	// Router is the AI router used to execute requests.
	Router *Router
}

// BatchProcessor is a background worker that processes queued batch requests
// when model capacity becomes available.
type BatchProcessor struct {
	queue  *BatchQueue
	config BatchProcessorConfig

	// running indicates whether the processor is active
	running bool
	mu      sync.Mutex

	// stop channel signals the processor to shut down
	stopCh chan struct{}

	// done channel signals that the processor has finished
	doneCh chan struct{}

	// sem limits concurrent request processing
	sem chan struct{}
	wg  sync.WaitGroup

	// capacityChecker is an optional function to determine if processing capacity
	// is available. If nil, the processor always attempts to process.
	capacityChecker func() bool
}

// NewBatchProcessor creates a new batch processor.
func NewBatchProcessor(queue *BatchQueue, cfg BatchProcessorConfig) *BatchProcessor {
	maxConcurrent := cfg.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}

	pollInterval := cfg.PollInterval
	if pollInterval <= 0 {
		pollInterval = 5 * time.Second
	}

	return &BatchProcessor{
		queue: queue,
		config: BatchProcessorConfig{
			PollInterval:  pollInterval,
			MaxConcurrent: maxConcurrent,
			Router:        cfg.Router,
		},
		sem: make(chan struct{}, maxConcurrent),
	}
}

// SetCapacityChecker sets a function that the processor calls before dequeuing.
// If the function returns false, the processor skips that poll cycle.
func (p *BatchProcessor) SetCapacityChecker(fn func() bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.capacityChecker = fn
}

// Start begins the background processing loop. It is safe to call Start
// multiple times; subsequent calls are no-ops if the processor is already running.
func (p *BatchProcessor) Start() {
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return
	}
	p.running = true
	p.stopCh = make(chan struct{})
	p.doneCh = make(chan struct{})
	p.mu.Unlock()

	go p.run()
	log.Printf("[BatchProcessor] Started (poll=%v, concurrency=%d)", p.config.PollInterval, p.config.MaxConcurrent)
}

// Stop gracefully shuts down the processor. It signals the processing loop to
// stop, waits for in-flight requests to finish, and drains remaining queued
// requests (invoking their callbacks with ErrProcessorStopped).
func (p *BatchProcessor) Stop() {
	p.mu.Lock()
	if !p.running {
		p.mu.Unlock()
		return
	}
	p.running = false
	close(p.stopCh)
	p.mu.Unlock()

	// Wait for the run loop to finish
	<-p.doneCh

	// Wait for any in-flight request processing to complete
	p.wg.Wait()

	// Drain remaining pending requests
	drained := p.queue.DrainAll()
	for _, req := range drained {
		if req.Callback != nil {
			req.Callback(nil, nil, ErrProcessorStopped)
		}
	}

	log.Printf("[BatchProcessor] Stopped (drained %d pending requests)", len(drained))
}

// IsRunning returns whether the processor is currently active.
func (p *BatchProcessor) IsRunning() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}

// Stats returns current processor statistics.
func (p *BatchProcessor) Stats() BatchProcessorStats {
	return BatchProcessorStats{
		Running:       p.IsRunning(),
		QueuedTotal:   p.queue.Len(),
		QueuedPending: p.queue.PendingCount(),
		InFlight:      len(p.sem),
		MaxConcurrent: p.config.MaxConcurrent,
	}
}

// BatchProcessorStats holds current processor statistics.
type BatchProcessorStats struct {
	Running       bool `json:"running"`
	QueuedTotal   int  `json:"queued_total"`
	QueuedPending int  `json:"queued_pending"`
	InFlight      int  `json:"in_flight"`
	MaxConcurrent int  `json:"max_concurrent"`
}

// run is the main processing loop.
func (p *BatchProcessor) run() {
	defer close(p.doneCh)

	ticker := time.NewTicker(p.config.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-p.stopCh:
			return
		case <-ticker.C:
			p.processBatch()
		}
	}
}

// processBatch attempts to dequeue and process pending requests.
func (p *BatchProcessor) processBatch() {
	// First expire any stale requests
	expired := p.queue.ExpireStale()
	if expired > 0 {
		log.Printf("[BatchProcessor] Expired %d stale request(s)", expired)
	}

	// Check capacity
	p.mu.Lock()
	checker := p.capacityChecker
	p.mu.Unlock()

	if checker != nil && !checker() {
		return // No capacity available
	}

	// Dequeue and process requests up to concurrency limit
	for {
		// Try to acquire a concurrency slot
		select {
		case p.sem <- struct{}{}:
			// Got a slot, try to dequeue
		default:
			// All slots busy
			return
		}

		req := p.queue.Dequeue()
		if req == nil {
			// Nothing to process; release the slot
			<-p.sem
			return
		}

		p.wg.Add(1)
		go p.processRequest(req)
	}
}

// processRequest executes a single batch request and handles the result.
func (p *BatchProcessor) processRequest(req *BatchRequest) {
	defer func() {
		<-p.sem // Release concurrency slot
		p.wg.Done()
	}()

	log.Printf("[BatchProcessor] Processing ticket=%s priority=%d", req.TicketID, req.Priority)

	if p.config.Router == nil {
		err := fmt.Errorf("no router configured for batch processing")
		p.queue.markFailed(req.TicketID, err)
		if req.Callback != nil {
			req.Callback(nil, nil, err)
		}
		return
	}

	// Check if the request has expired between dequeue and now
	if !req.ExpiresAt.IsZero() && time.Now().After(req.ExpiresAt) {
		p.queue.markFailed(req.TicketID, ErrRequestExpired)
		if req.Callback != nil {
			req.Callback(nil, nil, ErrRequestExpired)
		}
		return
	}

	// Execute via smart routing if available, otherwise regular
	ctx := context.Background()
	var resp ConversationResponse
	var routingResult *SmartRoutingResult
	var err error

	if p.config.Router.IsSmartRoutingEnabled() {
		resp, routingResult, err = p.config.Router.GenerateResponseSmart(ctx, req.Session, req.UserMessage, req.ProviderName)
	} else {
		resp, err = p.config.Router.GenerateResponseWithTools(ctx, req.Session, req.UserMessage, req.ProviderName, "")
	}

	if err != nil {
		log.Printf("[BatchProcessor] Ticket %s failed: %v", req.TicketID, err)
		p.queue.markFailed(req.TicketID, err)
		if req.Callback != nil {
			req.Callback(nil, routingResult, err)
		}
		return
	}

	log.Printf("[BatchProcessor] Ticket %s completed successfully", req.TicketID)
	p.queue.markComplete(req.TicketID, resp, routingResult)
	if req.Callback != nil {
		req.Callback(resp, routingResult, nil)
	}
}

// EnqueueFromSmartRouting is a convenience method for enqueueing a request
// that was rejected by smart routing (e.g., all models rate-limited).
// It creates a BatchRequest with the given parameters and enqueues it.
func EnqueueFromSmartRouting(queue *BatchQueue, session *sessions.Session, userMessage string, providerName string, priority BatchPriority, callback BatchResultCallback) (string, error) {
	req := &BatchRequest{
		Session:      session,
		UserMessage:  userMessage,
		ProviderName: providerName,
		Priority:     priority,
		Callback:     callback,
	}
	return queue.Enqueue(req)
}
