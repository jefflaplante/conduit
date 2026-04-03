package mqtt

import (
	"encoding/json"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetainedStore_SetAndGet(t *testing.T) {
	rs := NewRetainedStore()

	// Initially empty
	assert.Nil(t, rs.Get("nonexistent"))
	assert.Equal(t, 0, rs.Count())

	// Set a message
	now := time.Now()
	rs.Set("zigbee2mqtt/sensor1", []byte(`{"temperature":22.5}`), now)

	msg := rs.Get("zigbee2mqtt/sensor1")
	require.NotNil(t, msg)
	assert.Equal(t, "zigbee2mqtt/sensor1", msg.Topic)
	assert.Equal(t, json.RawMessage(`{"temperature":22.5}`), msg.Payload)
	assert.Equal(t, now, msg.Timestamp)
	assert.Equal(t, 1, rs.Count())
}

func TestRetainedStore_Overwrite(t *testing.T) {
	rs := NewRetainedStore()

	t1 := time.Now()
	rs.Set("zigbee2mqtt/sensor1", []byte(`{"temperature":20}`), t1)

	t2 := t1.Add(time.Minute)
	rs.Set("zigbee2mqtt/sensor1", []byte(`{"temperature":25}`), t2)

	msg := rs.Get("zigbee2mqtt/sensor1")
	require.NotNil(t, msg)
	assert.Equal(t, json.RawMessage(`{"temperature":25}`), msg.Payload)
	assert.Equal(t, t2, msg.Timestamp)
	assert.Equal(t, 1, rs.Count()) // Still just one entry
}

func TestRetainedStore_DefensiveCopy(t *testing.T) {
	rs := NewRetainedStore()

	original := []byte(`{"value":1}`)
	rs.Set("test/topic", original, time.Now())

	// Modify the original slice
	original[9] = '9' // Change "1" to "9"

	// The stored value should be unchanged
	msg := rs.Get("test/topic")
	require.NotNil(t, msg)
	assert.Equal(t, json.RawMessage(`{"value":1}`), msg.Payload)
}

func TestRetainedStore_GetByPrefix(t *testing.T) {
	rs := NewRetainedStore()
	now := time.Now()

	rs.Set("zigbee2mqtt/sensor1", []byte(`{"t":1}`), now)
	rs.Set("zigbee2mqtt/sensor2", []byte(`{"t":2}`), now)
	rs.Set("homeassistant/state", []byte(`{"s":"on"}`), now)
	rs.Set("zigbee2mqtt/bridge/state", []byte(`{"s":"ok"}`), now)

	// Get all zigbee2mqtt topics
	msgs := rs.GetByPrefix("zigbee2mqtt/")
	assert.Len(t, msgs, 3)

	// Get only sensors
	msgs = rs.GetByPrefix("zigbee2mqtt/sensor")
	assert.Len(t, msgs, 2)

	// Get homeassistant
	msgs = rs.GetByPrefix("homeassistant/")
	assert.Len(t, msgs, 1)
	assert.Equal(t, "homeassistant/state", msgs[0].Topic)

	// Empty prefix returns all
	msgs = rs.GetByPrefix("")
	assert.Len(t, msgs, 4)

	// Non-matching prefix
	msgs = rs.GetByPrefix("nonexistent/")
	assert.Len(t, msgs, 0)
}

func TestRetainedStore_All(t *testing.T) {
	rs := NewRetainedStore()
	now := time.Now()

	// Empty store
	assert.Len(t, rs.All(), 0)

	rs.Set("topic1", []byte(`{}`), now)
	rs.Set("topic2", []byte(`{}`), now)
	rs.Set("topic3", []byte(`{}`), now)

	all := rs.All()
	assert.Len(t, all, 3)

	topics := make([]string, len(all))
	for i, m := range all {
		topics[i] = m.Topic
	}
	sort.Strings(topics)
	assert.Equal(t, []string{"topic1", "topic2", "topic3"}, topics)
}

func TestRetainedStore_Prefixes(t *testing.T) {
	rs := NewRetainedStore()
	now := time.Now()

	// Empty store
	assert.Len(t, rs.Prefixes(), 0)

	rs.Set("zigbee2mqtt/sensor1", []byte(`{}`), now)
	rs.Set("zigbee2mqtt/sensor2", []byte(`{}`), now)
	rs.Set("homeassistant/switch/light", []byte(`{}`), now)
	rs.Set("tasmota/device1/state", []byte(`{}`), now)

	prefixes := rs.Prefixes()
	sort.Strings(prefixes)
	assert.Equal(t, []string{"homeassistant", "tasmota", "zigbee2mqtt"}, prefixes)
}

func TestRetainedStore_Prefixes_TopLevelTopic(t *testing.T) {
	rs := NewRetainedStore()
	now := time.Now()

	// Topic without a slash
	rs.Set("singletopic", []byte(`{}`), now)
	rs.Set("zigbee2mqtt/sensor", []byte(`{}`), now)

	prefixes := rs.Prefixes()
	sort.Strings(prefixes)
	assert.Equal(t, []string{"singletopic", "zigbee2mqtt"}, prefixes)
}

func TestRetainedStore_Count(t *testing.T) {
	rs := NewRetainedStore()

	assert.Equal(t, 0, rs.Count())

	rs.Set("a", []byte(`{}`), time.Now())
	assert.Equal(t, 1, rs.Count())

	rs.Set("b", []byte(`{}`), time.Now())
	assert.Equal(t, 2, rs.Count())

	// Overwrite doesn't increase count
	rs.Set("a", []byte(`{"new":"value"}`), time.Now())
	assert.Equal(t, 2, rs.Count())
}

func TestRetainedStore_ConcurrentAccess(t *testing.T) {
	rs := NewRetainedStore()
	done := make(chan struct{})

	// Writer goroutine
	go func() {
		for i := 0; i < 100; i++ {
			rs.Set("topic", []byte(`{"i":1}`), time.Now())
		}
		done <- struct{}{}
	}()

	// Reader goroutines
	for i := 0; i < 5; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				rs.Get("topic")
				rs.GetByPrefix("")
				rs.All()
				rs.Count()
				rs.Prefixes()
			}
			done <- struct{}{}
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 6; i++ {
		<-done
	}
}
