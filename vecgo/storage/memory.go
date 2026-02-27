package storage

import (
	"context"
	"sync"
)

// Memory is an in-memory storage implementation.
type Memory struct {
	vectors map[string]Vector
	graph   []byte
	mu      sync.RWMutex
}

// NewMemory creates a new in-memory storage.
func NewMemory() *Memory {
	return &Memory{
		vectors: make(map[string]Vector),
	}
}

// Save stores vectors in memory.
func (m *Memory) Save(ctx context.Context, vectors []Vector) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, v := range vectors {
		m.vectors[v.ID] = v
	}
	return nil
}

// Load returns all stored vectors.
func (m *Memory) Load(ctx context.Context) ([]Vector, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]Vector, 0, len(m.vectors))
	for _, v := range m.vectors {
		result = append(result, v)
	}
	return result, nil
}

// Delete removes vectors by ID.
func (m *Memory) Delete(ctx context.Context, ids []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range ids {
		delete(m.vectors, id)
	}
	return nil
}

// SaveGraph stores the HNSW graph data.
func (m *Memory) SaveGraph(ctx context.Context, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.graph = make([]byte, len(data))
	copy(m.graph, data)
	return nil
}

// LoadGraph returns the stored HNSW graph data.
func (m *Memory) LoadGraph(ctx context.Context) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.graph == nil {
		return nil, nil
	}
	result := make([]byte, len(m.graph))
	copy(result, m.graph)
	return result, nil
}

// Close is a no-op for memory storage.
func (m *Memory) Close() error {
	return nil
}
