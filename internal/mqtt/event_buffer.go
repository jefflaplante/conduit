package mqtt

import (
	"encoding/json"
	"log"
	"path"
	"sort"
	"sync"
	"time"
)

// Event represents a single MQTT message received on a topic.
type Event struct {
	Topic     string          `json:"topic"`
	Payload   json.RawMessage `json:"payload"`
	Timestamp time.Time       `json:"timestamp"`
	Retained  bool            `json:"retained,omitempty"`
}

// TopicSummary provides an overview of activity on a single topic.
type TopicSummary struct {
	Topic      string          `json:"topic"`
	EventCount int             `json:"event_count"`
	LastEvent  time.Time       `json:"last_event"`
	LastValue  json.RawMessage `json:"last_value"`
}

// topicBuffer is a bounded ring buffer for a single topic.
type topicBuffer struct {
	events []Event
	head   int // next write position
	count  int
	cap    int
}

func newTopicBuffer(capacity int) *topicBuffer {
	return &topicBuffer{
		events: make([]Event, capacity),
		cap:    capacity,
	}
}

func (tb *topicBuffer) add(e Event) {
	tb.events[tb.head] = e
	tb.head = (tb.head + 1) % tb.cap
	if tb.count < tb.cap {
		tb.count++
	}
}

// newest returns up to limit events, newest first.
func (tb *topicBuffer) newest(limit int) []Event {
	if limit <= 0 || tb.count == 0 {
		return nil
	}
	if limit > tb.count {
		limit = tb.count
	}
	result := make([]Event, limit)
	idx := (tb.head - 1 + tb.cap) % tb.cap
	for i := 0; i < limit; i++ {
		result[i] = tb.events[idx]
		idx = (idx - 1 + tb.cap) % tb.cap
	}
	return result
}

// last returns the most recent event, or nil.
func (tb *topicBuffer) last() *Event {
	if tb.count == 0 {
		return nil
	}
	idx := (tb.head - 1 + tb.cap) % tb.cap
	e := tb.events[idx]
	return &e
}

// prune removes events older than cutoff and returns how many were removed.
func (tb *topicBuffer) prune(cutoff time.Time) int {
	if tb.count == 0 {
		return 0
	}

	// Walk from oldest to newest, zeroing out old entries.
	removed := 0
	oldest := (tb.head - tb.count + tb.cap) % tb.cap
	for i := 0; i < tb.count; i++ {
		idx := (oldest + i) % tb.cap
		if tb.events[idx].Timestamp.Before(cutoff) {
			tb.events[idx] = Event{} // zero out
			removed++
		} else {
			break // events are in chronological order within the ring
		}
	}
	tb.count -= removed
	return removed
}

// EventBuffer holds per-topic ring buffers with a global topic cap.
type EventBuffer struct {
	mu            sync.RWMutex
	topics        map[string]*topicBuffer
	maxAge        time.Duration
	maxEvents     int // per topic
	maxTopics     int
	totalEvents   int64
	droppedTopics int64
}

// NewEventBuffer creates a new event buffer with the given limits.
func NewEventBuffer(maxAgeSec, maxEventsPerTopic, maxTopics int) *EventBuffer {
	return &EventBuffer{
		topics:    make(map[string]*topicBuffer),
		maxAge:    time.Duration(maxAgeSec) * time.Second,
		maxEvents: maxEventsPerTopic,
		maxTopics: maxTopics,
	}
}

// Add stores an event in the appropriate topic buffer.
func (eb *EventBuffer) Add(e Event) {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	tb, ok := eb.topics[e.Topic]
	if !ok {
		// Enforce topic cap: if at limit, drop the event.
		if len(eb.topics) >= eb.maxTopics {
			eb.droppedTopics++
			if eb.droppedTopics == 1 || eb.droppedTopics%100 == 0 {
				log.Printf("[MQTT] Topic cap reached (%d): dropping topic %s (total dropped: %d). Consider increasing buffer_max_topics.",
					eb.maxTopics, e.Topic, eb.droppedTopics)
			}
			return
		}
		tb = newTopicBuffer(eb.maxEvents)
		eb.topics[e.Topic] = tb
	}
	tb.add(e)
	eb.totalEvents++
}

// Recent returns the most recent events across all topics, newest first.
func (eb *EventBuffer) Recent(limit int) []Event {
	eb.mu.RLock()
	defer eb.mu.RUnlock()

	// Collect the last event from each topic, sort by timestamp descending.
	// For a general "recent" across all topics, we merge.
	var all []Event
	for _, tb := range eb.topics {
		all = append(all, tb.newest(limit)...)
	}
	// Sort newest first
	sortEventsDesc(all)
	if len(all) > limit {
		all = all[:limit]
	}
	return all
}

// RecentForTopic returns recent events for a specific topic.
func (eb *EventBuffer) RecentForTopic(topic string, limit int) []Event {
	eb.mu.RLock()
	defer eb.mu.RUnlock()

	tb, ok := eb.topics[topic]
	if !ok {
		return nil
	}
	return tb.newest(limit)
}

// RecentMatching returns recent events for topics matching a glob pattern.
func (eb *EventBuffer) RecentMatching(pattern string, limit int) []Event {
	eb.mu.RLock()
	defer eb.mu.RUnlock()

	var all []Event
	for topic, tb := range eb.topics {
		matched, _ := path.Match(pattern, topic)
		if matched {
			all = append(all, tb.newest(limit)...)
		}
	}
	sortEventsDesc(all)
	if len(all) > limit {
		all = all[:limit]
	}
	return all
}

// Topics returns a summary of all active topics.
func (eb *EventBuffer) Topics() []TopicSummary {
	eb.mu.RLock()
	defer eb.mu.RUnlock()

	summaries := make([]TopicSummary, 0, len(eb.topics))
	for topic, tb := range eb.topics {
		if tb.count == 0 {
			continue
		}
		last := tb.last()
		summaries = append(summaries, TopicSummary{
			Topic:      topic,
			EventCount: tb.count,
			LastEvent:  last.Timestamp,
			LastValue:  last.Payload,
		})
	}
	return summaries
}

// Prune removes events older than maxAge and removes empty topic buffers.
func (eb *EventBuffer) Prune() int {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	cutoff := time.Now().Add(-eb.maxAge)
	total := 0
	for topic, tb := range eb.topics {
		total += tb.prune(cutoff)
		if tb.count == 0 {
			delete(eb.topics, topic)
		}
	}
	return total
}

// TotalEvents returns the total number of events ever received.
func (eb *EventBuffer) TotalEvents() int64 {
	eb.mu.RLock()
	defer eb.mu.RUnlock()
	return eb.totalEvents
}

// ActiveTopics returns the number of active topics.
func (eb *EventBuffer) ActiveTopics() int {
	eb.mu.RLock()
	defer eb.mu.RUnlock()
	return len(eb.topics)
}

// sortEventsDesc sorts events by timestamp descending (newest first).
func sortEventsDesc(events []Event) {
	sort.Slice(events, func(i, j int) bool {
		return events[i].Timestamp.After(events[j].Timestamp)
	})
}
