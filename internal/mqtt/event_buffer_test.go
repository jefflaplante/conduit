package mqtt

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEventBuffer_AddAndRecent(t *testing.T) {
	buf := NewEventBuffer(3600, 100, 500)

	// Add events across topics
	for i := 0; i < 5; i++ {
		buf.Add(Event{
			Topic:     fmt.Sprintf("zigbee2mqtt/sensor%d", i),
			Payload:   json.RawMessage(fmt.Sprintf(`{"temperature":%d}`, 20+i)),
			Timestamp: time.Now().Add(time.Duration(i) * time.Second),
		})
	}

	assert.Equal(t, int64(5), buf.TotalEvents())
	assert.Equal(t, 5, buf.ActiveTopics())

	recent := buf.Recent(3)
	require.Len(t, recent, 3)
	// Newest first
	assert.Equal(t, "zigbee2mqtt/sensor4", recent[0].Topic)
}

func TestEventBuffer_RecentForTopic(t *testing.T) {
	buf := NewEventBuffer(3600, 100, 500)

	for i := 0; i < 5; i++ {
		buf.Add(Event{
			Topic:     "zigbee2mqtt/temp",
			Payload:   json.RawMessage(fmt.Sprintf(`{"val":%d}`, i)),
			Timestamp: time.Now().Add(time.Duration(i) * time.Second),
		})
	}

	events := buf.RecentForTopic("zigbee2mqtt/temp", 3)
	require.Len(t, events, 3)
	// Newest first
	assert.Contains(t, string(events[0].Payload), `"val":4`)

	// Non-existent topic
	events = buf.RecentForTopic("nonexistent", 10)
	assert.Len(t, events, 0)
}

func TestEventBuffer_RecentMatching(t *testing.T) {
	buf := NewEventBuffer(3600, 100, 500)

	buf.Add(Event{Topic: "zigbee2mqtt/living_room", Payload: json.RawMessage(`{}`), Timestamp: time.Now()})
	buf.Add(Event{Topic: "zigbee2mqtt/bedroom", Payload: json.RawMessage(`{}`), Timestamp: time.Now()})
	buf.Add(Event{Topic: "homeassistant/state", Payload: json.RawMessage(`{}`), Timestamp: time.Now()})

	events := buf.RecentMatching("zigbee2mqtt/*", 10)
	assert.Len(t, events, 2)

	events = buf.RecentMatching("homeassistant/*", 10)
	assert.Len(t, events, 1)
}

func TestEventBuffer_RingBufferOverflow(t *testing.T) {
	buf := NewEventBuffer(3600, 3, 500) // only 3 events per topic

	for i := 0; i < 5; i++ {
		buf.Add(Event{
			Topic:     "test/topic",
			Payload:   json.RawMessage(fmt.Sprintf(`{"i":%d}`, i)),
			Timestamp: time.Now().Add(time.Duration(i) * time.Second),
		})
	}

	events := buf.RecentForTopic("test/topic", 10)
	require.Len(t, events, 3)
	// Should have events 2, 3, 4 (oldest evicted)
	assert.Contains(t, string(events[0].Payload), `"i":4`)
	assert.Contains(t, string(events[1].Payload), `"i":3`)
	assert.Contains(t, string(events[2].Payload), `"i":2`)
}

func TestEventBuffer_TopicCap(t *testing.T) {
	buf := NewEventBuffer(3600, 100, 2) // only 2 topics

	buf.Add(Event{Topic: "topic1", Payload: json.RawMessage(`{}`), Timestamp: time.Now()})
	buf.Add(Event{Topic: "topic2", Payload: json.RawMessage(`{}`), Timestamp: time.Now()})
	buf.Add(Event{Topic: "topic3", Payload: json.RawMessage(`{}`), Timestamp: time.Now()}) // should be dropped

	assert.Equal(t, 2, buf.ActiveTopics())
	assert.Equal(t, int64(2), buf.TotalEvents()) // 3rd was not counted because it was dropped
}

func TestEventBuffer_Topics(t *testing.T) {
	buf := NewEventBuffer(3600, 100, 500)

	buf.Add(Event{Topic: "a", Payload: json.RawMessage(`{"x":1}`), Timestamp: time.Now()})
	buf.Add(Event{Topic: "b", Payload: json.RawMessage(`{"x":2}`), Timestamp: time.Now()})

	summaries := buf.Topics()
	assert.Len(t, summaries, 2)

	found := map[string]bool{}
	for _, s := range summaries {
		found[s.Topic] = true
		assert.Equal(t, 1, s.EventCount)
	}
	assert.True(t, found["a"])
	assert.True(t, found["b"])
}

func TestEventBuffer_Prune(t *testing.T) {
	buf := NewEventBuffer(1, 100, 500) // 1 second max age

	buf.Add(Event{
		Topic:     "old",
		Payload:   json.RawMessage(`{}`),
		Timestamp: time.Now().Add(-5 * time.Second), // 5s ago
	})
	buf.Add(Event{
		Topic:     "new",
		Payload:   json.RawMessage(`{}`),
		Timestamp: time.Now(),
	})

	pruned := buf.Prune()
	assert.Equal(t, 1, pruned)
	assert.Equal(t, 1, buf.ActiveTopics()) // "old" topic should be removed
}
