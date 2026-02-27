package orchestration

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSharedContextSetGet(t *testing.T) {
	ctx := NewSharedContext(ScopeGlobal)

	ctx.Set("key1", "value1")
	ctx.Set("key2", 42)

	val, ok := ctx.Get("key1")
	require.True(t, ok)
	assert.Equal(t, "value1", val)

	val, ok = ctx.Get("key2")
	require.True(t, ok)
	assert.Equal(t, 42, val)

	_, ok = ctx.Get("nonexistent")
	assert.False(t, ok)
}

func TestSharedContextGetString(t *testing.T) {
	ctx := NewSharedContext(ScopeGlobal)

	ctx.Set("str", "hello")
	ctx.Set("num", 42)

	assert.Equal(t, "hello", ctx.GetString("str"))
	assert.Equal(t, "42", ctx.GetString("num"))
	assert.Equal(t, "", ctx.GetString("missing"))
}

func TestSharedContextDelete(t *testing.T) {
	ctx := NewSharedContext(ScopeGlobal)

	ctx.Set("key", "value")
	assert.Equal(t, 1, ctx.Len())

	ctx.Delete("key")
	_, ok := ctx.Get("key")
	assert.False(t, ok)
	assert.Equal(t, 0, ctx.Len())

	// Deleting nonexistent key is a no-op
	ctx.Delete("nonexistent")
}

func TestSharedContextVersion(t *testing.T) {
	ctx := NewSharedContext(ScopeGlobal)
	assert.Equal(t, uint64(0), ctx.Version())

	ctx.Set("a", 1)
	assert.Equal(t, uint64(1), ctx.Version())

	ctx.Set("b", 2)
	assert.Equal(t, uint64(2), ctx.Version())

	ctx.Delete("a")
	assert.Equal(t, uint64(3), ctx.Version())

	// Deleting nonexistent key does not increment version
	ctx.Delete("nonexistent")
	assert.Equal(t, uint64(3), ctx.Version())
}

func TestSharedContextScope(t *testing.T) {
	global := NewSharedContext(ScopeGlobal)
	group := NewSharedContext(ScopeGroup)

	assert.Equal(t, ScopeGlobal, global.Scope())
	assert.Equal(t, ScopeGroup, group.Scope())
}

func TestSharedContextSnapshot(t *testing.T) {
	ctx := NewSharedContext(ScopeGlobal)
	ctx.Set("a", 1)
	ctx.Set("b", "two")

	snapshot, version := ctx.Snapshot()
	assert.Equal(t, uint64(2), version)
	assert.Equal(t, 1, snapshot["a"])
	assert.Equal(t, "two", snapshot["b"])

	// Mutating snapshot does not affect original
	snapshot["c"] = 3
	_, ok := ctx.Get("c")
	assert.False(t, ok)
}

func TestSharedContextKeys(t *testing.T) {
	ctx := NewSharedContext(ScopeGlobal)
	ctx.Set("alpha", 1)
	ctx.Set("beta", 2)
	ctx.Set("gamma", 3)

	keys := ctx.Keys()
	assert.Len(t, keys, 3)
	assert.ElementsMatch(t, []string{"alpha", "beta", "gamma"}, keys)
}

func TestSharedContextOnChange(t *testing.T) {
	ctx := NewSharedContext(ScopeGlobal)

	var events []ContextChangeEvent
	var mu sync.Mutex

	ctx.OnChange(func(event ContextChangeEvent) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	})

	ctx.Set("key", "val1")
	ctx.Set("key", "val2")
	ctx.Delete("key")

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, events, 3)

	assert.Equal(t, "set", events[0].ChangeType)
	assert.Equal(t, "key", events[0].Key)
	assert.Equal(t, "val1", events[0].NewValue)
	assert.Nil(t, events[0].OldValue)
	assert.Equal(t, uint64(1), events[0].Version)

	assert.Equal(t, "set", events[1].ChangeType)
	assert.Equal(t, "val1", events[1].OldValue)
	assert.Equal(t, "val2", events[1].NewValue)
	assert.Equal(t, uint64(2), events[1].Version)

	assert.Equal(t, "delete", events[2].ChangeType)
	assert.Equal(t, "val2", events[2].OldValue)
	assert.Equal(t, uint64(3), events[2].Version)
}

func TestSharedContextMerge(t *testing.T) {
	src := NewSharedContext(ScopeGlobal)
	src.Set("x", 10)
	src.Set("y", 20)

	dst := NewSharedContext(ScopeGroup)
	dst.Set("y", 99)

	dst.Merge(src)

	assert.Equal(t, 10, func() interface{} { v, _ := dst.Get("x"); return v }())
	// y should be overwritten by merge
	assert.Equal(t, 20, func() interface{} { v, _ := dst.Get("y"); return v }())
}

func TestSharedContextConcurrentAccess(t *testing.T) {
	ctx := NewSharedContext(ScopeGlobal)

	const goroutines = 50
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(goroutines * 2) // half writers, half readers

	// Writers
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				key := "key" + string(rune('A'+id%26))
				ctx.Set(key, i)
			}
		}(g)
	}

	// Readers
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				key := "key" + string(rune('A'+id%26))
				ctx.Get(key)
				ctx.Version()
				ctx.Snapshot()
				ctx.Keys()
			}
		}(g)
	}

	wg.Wait()
	// No race detector failures means success
	assert.True(t, ctx.Len() > 0)
}

func TestSharedContextMultipleListeners(t *testing.T) {
	ctx := NewSharedContext(ScopeGlobal)

	var count1, count2 int32

	ctx.OnChange(func(event ContextChangeEvent) {
		atomic.AddInt32(&count1, 1)
	})
	ctx.OnChange(func(event ContextChangeEvent) {
		atomic.AddInt32(&count2, 1)
	})

	ctx.Set("a", 1)
	ctx.Set("b", 2)

	assert.Equal(t, int32(2), atomic.LoadInt32(&count1))
	assert.Equal(t, int32(2), atomic.LoadInt32(&count2))
}
