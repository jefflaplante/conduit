package orchestration

import (
	"fmt"
	"sync"
)

// SharedContext provides a thread-safe key-value store with change notifications
// and context versioning for consistency across coordinated agents.
type SharedContext struct {
	mu        sync.RWMutex
	data      map[string]interface{}
	version   uint64
	listeners []ContextChangeListener
	scope     ContextScope
}

// ContextScope identifies whether a context is global or scoped to an agent group.
type ContextScope string

const (
	// ScopeGlobal indicates context visible to all agents.
	ScopeGlobal ContextScope = "global"
	// ScopeGroup indicates context scoped to a specific agent group.
	ScopeGroup ContextScope = "group"
)

// ContextChangeEvent describes a change to the shared context.
type ContextChangeEvent struct {
	Key        string      `json:"key"`
	OldValue   interface{} `json:"old_value,omitempty"`
	NewValue   interface{} `json:"new_value"`
	Version    uint64      `json:"version"`
	ChangeType string      `json:"change_type"` // "set", "delete"
}

// ContextChangeListener is called when a shared context value changes.
type ContextChangeListener func(event ContextChangeEvent)

// NewSharedContext creates a new SharedContext with the given scope.
func NewSharedContext(scope ContextScope) *SharedContext {
	return &SharedContext{
		data:      make(map[string]interface{}),
		version:   0,
		listeners: make([]ContextChangeListener, 0),
		scope:     scope,
	}
}

// Set stores a key-value pair, increments the version, and notifies listeners.
func (sc *SharedContext) Set(key string, value interface{}) {
	sc.mu.Lock()
	oldValue, existed := sc.data[key]
	sc.data[key] = value
	sc.version++
	currentVersion := sc.version

	// Copy listeners for notification outside the lock
	listeners := make([]ContextChangeListener, len(sc.listeners))
	copy(listeners, sc.listeners)
	sc.mu.Unlock()

	event := ContextChangeEvent{
		Key:        key,
		NewValue:   value,
		Version:    currentVersion,
		ChangeType: "set",
	}
	if existed {
		event.OldValue = oldValue
	}
	for _, listener := range listeners {
		listener(event)
	}
}

// Get retrieves a value by key. Returns the value and true if found, nil and false otherwise.
func (sc *SharedContext) Get(key string) (interface{}, bool) {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	val, ok := sc.data[key]
	return val, ok
}

// GetString retrieves a string value by key. Returns empty string if not found
// or if the value is not a string.
func (sc *SharedContext) GetString(key string) string {
	val, ok := sc.Get(key)
	if !ok {
		return ""
	}
	s, ok := val.(string)
	if !ok {
		return fmt.Sprintf("%v", val)
	}
	return s
}

// Delete removes a key, increments the version, and notifies listeners.
func (sc *SharedContext) Delete(key string) {
	sc.mu.Lock()
	oldValue, existed := sc.data[key]
	if !existed {
		sc.mu.Unlock()
		return
	}
	delete(sc.data, key)
	sc.version++
	currentVersion := sc.version

	listeners := make([]ContextChangeListener, len(sc.listeners))
	copy(listeners, sc.listeners)
	sc.mu.Unlock()

	event := ContextChangeEvent{
		Key:        key,
		OldValue:   oldValue,
		Version:    currentVersion,
		ChangeType: "delete",
	}
	for _, listener := range listeners {
		listener(event)
	}
}

// Version returns the current version of the context.
func (sc *SharedContext) Version() uint64 {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	return sc.version
}

// Scope returns the scope of this context.
func (sc *SharedContext) Scope() ContextScope {
	return sc.scope
}

// Snapshot returns a point-in-time copy of all key-value pairs and the version.
func (sc *SharedContext) Snapshot() (map[string]interface{}, uint64) {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	snapshot := make(map[string]interface{}, len(sc.data))
	for k, v := range sc.data {
		snapshot[k] = v
	}
	return snapshot, sc.version
}

// OnChange registers a listener that will be called when context values change.
func (sc *SharedContext) OnChange(listener ContextChangeListener) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.listeners = append(sc.listeners, listener)
}

// Len returns the number of entries in the context.
func (sc *SharedContext) Len() int {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	return len(sc.data)
}

// Keys returns all keys currently in the context.
func (sc *SharedContext) Keys() []string {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	keys := make([]string, 0, len(sc.data))
	for k := range sc.data {
		keys = append(keys, k)
	}
	return keys
}

// Merge copies all entries from another SharedContext into this one.
// Each key set increments the version and notifies listeners individually.
func (sc *SharedContext) Merge(other *SharedContext) {
	snapshot, _ := other.Snapshot()
	for k, v := range snapshot {
		sc.Set(k, v)
	}
}
