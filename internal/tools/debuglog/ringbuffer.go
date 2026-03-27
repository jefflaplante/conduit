package debuglog

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// EntryType identifies what kind of event was captured.
type EntryType string

const (
	EntryToolStart    EntryType = "tool_start"
	EntryToolComplete EntryType = "tool_complete"
	EntryToolError    EntryType = "tool_error"
	EntryThinking     EntryType = "thinking"
	EntryLLMRequest   EntryType = "llm_request"
	EntryLLMResponse  EntryType = "llm_response"
)

// Entry is a single event captured in the ring buffer.
type Entry struct {
	Timestamp time.Time              `json:"timestamp"`
	Type      EntryType              `json:"type"`
	ToolName  string                 `json:"tool_name,omitempty"`
	Args      map[string]interface{} `json:"args,omitempty"`
	Result    string                 `json:"result,omitempty"`
	Error     string                 `json:"error,omitempty"`
	Duration  time.Duration          `json:"duration,omitempty"`
	Meta      map[string]string      `json:"meta,omitempty"`
}

// MarshalJSON customises JSON for Duration (renders as string).
func (e Entry) MarshalJSON() ([]byte, error) {
	type Alias Entry
	return json.Marshal(&struct {
		Alias
		Duration string `json:"duration,omitempty"`
	}{
		Alias:    Alias(e),
		Duration: durationStr(e.Duration),
	})
}

func durationStr(d time.Duration) string {
	if d == 0 {
		return ""
	}
	return d.String()
}

// DefaultCapacity is the default number of entries kept.
const DefaultCapacity = 500

// RingBuffer is a thread-safe circular buffer for debug log entries.
type RingBuffer struct {
	mu       sync.Mutex
	entries  []Entry
	head     int  // next write position
	full     bool // whether we've wrapped
	capacity int
}

// NewRingBuffer creates a ring buffer with the given capacity.
// Pass 0 to use DefaultCapacity.
func NewRingBuffer(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &RingBuffer{
		entries:  make([]Entry, capacity),
		capacity: capacity,
	}
}

// Add appends an entry to the buffer, overwriting the oldest if full.
func (rb *RingBuffer) Add(e Entry) {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
	rb.mu.Lock()
	rb.entries[rb.head] = e
	rb.head = (rb.head + 1) % rb.capacity
	if rb.head == 0 && !rb.full {
		rb.full = true
	}
	// Once we've wrapped once, full stays true
	if !rb.full && rb.head == 0 {
		rb.full = true
	}
	rb.mu.Unlock()
}

// Len returns the number of entries currently stored.
func (rb *RingBuffer) Len() int {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	if rb.full {
		return rb.capacity
	}
	return rb.head
}

// Entries returns all stored entries in chronological order.
// An optional filter function can restrict which entries are returned.
func (rb *RingBuffer) Entries(filter func(Entry) bool) []Entry {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	n := rb.capacity
	if !rb.full {
		n = rb.head
	}

	result := make([]Entry, 0, n)
	for i := 0; i < n; i++ {
		idx := i
		if rb.full {
			idx = (rb.head + i) % rb.capacity
		}
		e := rb.entries[idx]
		if filter == nil || filter(e) {
			result = append(result, e)
		}
	}
	return result
}

// Last returns the most recent `limit` entries (chronological order).
func (rb *RingBuffer) Last(limit int) []Entry {
	all := rb.Entries(nil)
	if limit <= 0 || limit >= len(all) {
		return all
	}
	return all[len(all)-limit:]
}

// Clear resets the buffer.
func (rb *RingBuffer) Clear() {
	rb.mu.Lock()
	rb.head = 0
	rb.full = false
	rb.entries = make([]Entry, rb.capacity)
	rb.mu.Unlock()
}

// --- Convenience constructors ---

// ToolStart records the start of a tool execution.
func ToolStart(name string, args map[string]interface{}) Entry {
	return Entry{
		Timestamp: time.Now(),
		Type:      EntryToolStart,
		ToolName:  name,
		Args:      args,
	}
}

// ToolComplete records the successful completion of a tool.
func ToolComplete(name string, duration time.Duration, resultSummary string) Entry {
	return Entry{
		Timestamp: time.Now(),
		Type:      EntryToolComplete,
		ToolName:  name,
		Duration:  duration,
		Result:    resultSummary,
	}
}

// ToolError records a tool execution failure.
func ToolError(name string, duration time.Duration, err string) Entry {
	return Entry{
		Timestamp: time.Now(),
		Type:      EntryToolError,
		ToolName:  name,
		Duration:  duration,
		Error:     err,
	}
}

// Thinking records an LLM thinking indicator.
func Thinking(msg string) Entry {
	return Entry{
		Timestamp: time.Now(),
		Type:      EntryThinking,
		Result:    msg,
	}
}

// LLMRequest records an outbound LLM API request.
func LLMRequest(model string, meta map[string]string) Entry {
	return Entry{
		Timestamp: time.Now(),
		Type:      EntryLLMRequest,
		Meta:      meta,
		Result:    fmt.Sprintf("model=%s", model),
	}
}

// LLMResponse records an LLM API response.
func LLMResponse(model string, stopReason string, duration time.Duration) Entry {
	return Entry{
		Timestamp: time.Now(),
		Type:      EntryLLMResponse,
		Duration:  duration,
		Result:    fmt.Sprintf("model=%s stop=%s", model, stopReason),
	}
}
