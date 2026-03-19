package mqtt

import (
	"encoding/json"
	"strings"
	"sync"
	"time"
)

// RetainedMessage holds the latest retained value for a topic.
type RetainedMessage struct {
	Topic     string          `json:"topic"`
	Payload   json.RawMessage `json:"payload"`
	Timestamp time.Time       `json:"timestamp"`
}

// RetainedStore is a persistent in-memory store for retained messages,
// separate from the ring buffer. The ring buffer prunes old events;
// the retained store keeps the latest retained value per topic indefinitely
// (mirrors what the broker holds).
type RetainedStore struct {
	mu     sync.RWMutex
	topics map[string]RetainedMessage
}

// NewRetainedStore creates an empty retained message store.
func NewRetainedStore() *RetainedStore {
	return &RetainedStore{
		topics: make(map[string]RetainedMessage),
	}
}

// Set stores or overwrites the retained message for a topic.
func (rs *RetainedStore) Set(topic string, payload []byte, timestamp time.Time) {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	// Defensive copy of payload
	copied := make(json.RawMessage, len(payload))
	copy(copied, payload)

	rs.topics[topic] = RetainedMessage{
		Topic:     topic,
		Payload:   copied,
		Timestamp: timestamp,
	}
}

// Get returns the retained message for a single topic, or nil if not found.
func (rs *RetainedStore) Get(topic string) *RetainedMessage {
	rs.mu.RLock()
	defer rs.mu.RUnlock()

	if msg, ok := rs.topics[topic]; ok {
		return &msg
	}
	return nil
}

// GetByPrefix returns all retained messages whose topic starts with the given prefix.
func (rs *RetainedStore) GetByPrefix(prefix string) []RetainedMessage {
	rs.mu.RLock()
	defer rs.mu.RUnlock()

	var result []RetainedMessage
	for topic, msg := range rs.topics {
		if strings.HasPrefix(topic, prefix) {
			result = append(result, msg)
		}
	}
	return result
}

// All returns all retained messages.
func (rs *RetainedStore) All() []RetainedMessage {
	rs.mu.RLock()
	defer rs.mu.RUnlock()

	result := make([]RetainedMessage, 0, len(rs.topics))
	for _, msg := range rs.topics {
		result = append(result, msg)
	}
	return result
}

// Count returns the number of retained topics.
func (rs *RetainedStore) Count() int {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	return len(rs.topics)
}

// Prefixes returns unique top-level prefixes (the first path segment of each topic).
func (rs *RetainedStore) Prefixes() []string {
	rs.mu.RLock()
	defer rs.mu.RUnlock()

	seen := make(map[string]struct{})
	for topic := range rs.topics {
		if idx := strings.Index(topic, "/"); idx > 0 {
			seen[topic[:idx]] = struct{}{}
		} else {
			seen[topic] = struct{}{}
		}
	}

	result := make([]string, 0, len(seen))
	for prefix := range seen {
		result = append(result, prefix)
	}
	return result
}
