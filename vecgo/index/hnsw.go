package index

import (
	"bytes"
	"encoding/gob"
	"math"
	"math/rand"
	"sort"
	"sync"

	"github.com/jefflaplante/vecgo/internal/mathutil"
	"github.com/jefflaplante/vecgo/storage"
)

// HNSWConfig configures the HNSW index.
type HNSWConfig struct {
	M              int     // Max connections per node (default 16)
	EfConstruction int     // Construction search depth (default 200)
	EfSearch       int     // Query search depth (default 50)
	LevelMult      float64 // Level multiplier (default 1/ln(M))
}

func (c *HNSWConfig) withDefaults() HNSWConfig {
	cfg := *c
	if cfg.M == 0 {
		cfg.M = 16
	}
	if cfg.EfConstruction == 0 {
		cfg.EfConstruction = 200
	}
	if cfg.EfSearch == 0 {
		cfg.EfSearch = 50
	}
	if cfg.LevelMult == 0 {
		cfg.LevelMult = 1.0 / math.Log(float64(cfg.M))
	}
	return cfg
}

// hnswNode is an HNSW graph node. Fields are exported for gob serialization.
type hnswNode struct {
	ID        string
	Vector    []float32
	Metadata  map[string]string
	Level     int
	Neighbors [][]uint32 // Neighbors[level] = list of neighbor indices
}

// HNSW is a Hierarchical Navigable Small World graph index.
type HNSW struct {
	nodes      []hnswNode
	idToIndex  map[string]uint32
	entryPoint int32 // -1 if empty
	maxLevel   int
	cfg        HNSWConfig
	mu         sync.RWMutex
}

// NewHNSW creates a new HNSW index.
func NewHNSW(cfg HNSWConfig) *HNSW {
	cfg = cfg.withDefaults()
	return &HNSW{
		idToIndex:  make(map[string]uint32),
		entryPoint: -1,
		cfg:        cfg,
	}
}

// Add inserts vectors into the index.
func (h *HNSW) Add(vectors []storage.Vector) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, v := range vectors {
		h.addOne(v)
	}
	return nil
}

func (h *HNSW) addOne(v storage.Vector) {
	level := h.randomLevel()
	idx := uint32(len(h.nodes))

	n := hnswNode{
		ID:        v.ID,
		Vector:    v.Embedding,
		Metadata:  v.Metadata,
		Level:     level,
		Neighbors: make([][]uint32, level+1),
	}
	for i := range n.Neighbors {
		n.Neighbors[i] = make([]uint32, 0, h.cfg.M)
	}

	h.nodes = append(h.nodes, n)
	h.idToIndex[v.ID] = idx

	if h.entryPoint < 0 {
		h.entryPoint = int32(idx)
		h.maxLevel = level
		return
	}

	// Find entry point at top level and descend
	currNode := uint32(h.entryPoint)
	for l := h.maxLevel; l > level; l-- {
		currNode = h.searchLayerOne(v.Embedding, currNode, l)
	}

	// Insert at each level from level down to 0
	for l := min(level, h.maxLevel); l >= 0; l-- {
		neighbors := h.searchLayer(v.Embedding, currNode, h.cfg.EfConstruction, l)
		h.selectAndConnect(idx, neighbors, l)
		if len(neighbors) > 0 {
			currNode = neighbors[0]
		}
	}

	if level > h.maxLevel {
		h.maxLevel = level
		h.entryPoint = int32(idx)
	}
}

func (h *HNSW) randomLevel() int {
	r := rand.Float64()
	return int(-math.Log(r) * h.cfg.LevelMult)
}

func (h *HNSW) searchLayerOne(query []float32, entry uint32, level int) uint32 {
	curr := entry
	currDist := mathutil.CosineDistance(query, h.nodes[curr].Vector)

	for {
		changed := false
		if level < len(h.nodes[curr].Neighbors) {
			for _, neighbor := range h.nodes[curr].Neighbors[level] {
				dist := mathutil.CosineDistance(query, h.nodes[neighbor].Vector)
				if dist < currDist {
					curr = neighbor
					currDist = dist
					changed = true
				}
			}
		}
		if !changed {
			break
		}
	}
	return curr
}

func (h *HNSW) searchLayer(query []float32, entry uint32, ef, level int) []uint32 {
	visited := make(map[uint32]bool)
	candidates := &distHeap{}
	results := &distHeap{}

	dist := mathutil.CosineDistance(query, h.nodes[entry].Vector)
	candidates.push(distItem{idx: entry, dist: dist})
	results.push(distItem{idx: entry, dist: dist})
	visited[entry] = true

	for candidates.len() > 0 {
		curr := candidates.pop()

		if results.len() > 0 && curr.dist > results.peek().dist && results.len() >= ef {
			break
		}

		if level < len(h.nodes[curr.idx].Neighbors) {
			for _, neighbor := range h.nodes[curr.idx].Neighbors[level] {
				if visited[neighbor] {
					continue
				}
				visited[neighbor] = true

				nDist := mathutil.CosineDistance(query, h.nodes[neighbor].Vector)
				if results.len() < ef || nDist < results.peek().dist {
					candidates.push(distItem{idx: neighbor, dist: nDist})
					results.push(distItem{idx: neighbor, dist: nDist})
					if results.len() > ef {
						results.popLast()
					}
				}
			}
		}
	}

	result := make([]uint32, results.len())
	for i := range result {
		result[i] = results.items[i].idx
	}
	return result
}

func (h *HNSW) selectAndConnect(idx uint32, neighbors []uint32, level int) {
	m := h.cfg.M
	if level == 0 {
		m = h.cfg.M * 2
	}

	// Select up to M neighbors
	selected := neighbors
	if len(selected) > m {
		selected = selected[:m]
	}

	// Connect bidirectionally
	h.nodes[idx].Neighbors[level] = append(h.nodes[idx].Neighbors[level], selected...)
	for _, n := range selected {
		if level < len(h.nodes[n].Neighbors) {
			h.nodes[n].Neighbors[level] = append(h.nodes[n].Neighbors[level], idx)
			// Prune if too many
			if len(h.nodes[n].Neighbors[level]) > m {
				h.pruneConnections(n, level, m)
			}
		}
	}
}

func (h *HNSW) pruneConnections(idx uint32, level, m int) {
	neighbors := h.nodes[idx].Neighbors[level]
	if len(neighbors) <= m {
		return
	}

	// Sort by distance to idx and keep closest M
	type nd struct {
		n    uint32
		dist float32
	}
	nds := make([]nd, len(neighbors))
	for i, n := range neighbors {
		nds[i] = nd{n: n, dist: mathutil.CosineDistance(h.nodes[idx].Vector, h.nodes[n].Vector)}
	}
	sort.Slice(nds, func(i, j int) bool { return nds[i].dist < nds[j].dist })

	h.nodes[idx].Neighbors[level] = make([]uint32, m)
	for i := 0; i < m; i++ {
		h.nodes[idx].Neighbors[level][i] = nds[i].n
	}
}

// Search returns the k nearest neighbors to the query.
func (h *HNSW) Search(query []float32, k int) ([]SearchResult, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.entryPoint < 0 {
		return nil, nil
	}

	// Descend from top to level 0
	currNode := uint32(h.entryPoint)
	for l := h.maxLevel; l > 0; l-- {
		currNode = h.searchLayerOne(query, currNode, l)
	}

	// Search at level 0
	neighbors := h.searchLayer(query, currNode, max(h.cfg.EfSearch, k), 0)

	// Convert to results
	results := make([]SearchResult, 0, k)
	for _, idx := range neighbors {
		if len(results) >= k {
			break
		}
		n := h.nodes[idx]
		results = append(results, SearchResult{
			ID:       n.ID,
			Distance: mathutil.CosineDistance(query, n.Vector),
			Metadata: n.Metadata,
		})
	}

	// Sort by distance
	sort.Slice(results, func(i, j int) bool {
		return results[i].Distance < results[j].Distance
	})

	if len(results) > k {
		results = results[:k]
	}

	return results, nil
}

// Remove removes vectors by ID.
func (h *HNSW) Remove(ids []string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	// Lazy deletion - mark as deleted
	for _, id := range ids {
		delete(h.idToIndex, id)
	}
	return nil
}

// Len returns the number of vectors in the index.
func (h *HNSW) Len() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.idToIndex)
}

// hnswData is the serializable representation of the HNSW index.
type hnswData struct {
	Nodes      []hnswNode
	IdToIndex  map[string]uint32
	EntryPoint int32
	MaxLevel   int
	Cfg        HNSWConfig
}

// Marshal serializes the index.
func (h *HNSW) Marshal() ([]byte, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	data := hnswData{
		Nodes:      h.nodes,
		IdToIndex:  h.idToIndex,
		EntryPoint: h.entryPoint,
		MaxLevel:   h.maxLevel,
		Cfg:        h.cfg,
	}

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Unmarshal deserializes the index.
func (h *HNSW) Unmarshal(data []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	var d hnswData
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&d); err != nil {
		return err
	}

	h.nodes = d.Nodes
	h.idToIndex = d.IdToIndex
	h.entryPoint = d.EntryPoint
	h.maxLevel = d.MaxLevel
	h.cfg = d.Cfg

	return nil
}

// distItem for priority queue
type distItem struct {
	idx  uint32
	dist float32
}

// distHeap is a simple min-heap for search
type distHeap struct {
	items []distItem
}

func (h *distHeap) len() int { return len(h.items) }

func (h *distHeap) push(item distItem) {
	h.items = append(h.items, item)
	// Bubble up
	i := len(h.items) - 1
	for i > 0 {
		parent := (i - 1) / 2
		if h.items[i].dist >= h.items[parent].dist {
			break
		}
		h.items[i], h.items[parent] = h.items[parent], h.items[i]
		i = parent
	}
}

func (h *distHeap) pop() distItem {
	item := h.items[0]
	h.items[0] = h.items[len(h.items)-1]
	h.items = h.items[:len(h.items)-1]
	h.bubbleDown(0)
	return item
}

func (h *distHeap) peek() distItem {
	return h.items[0]
}

func (h *distHeap) popLast() {
	// Remove the max item (for results pruning)
	if len(h.items) == 0 {
		return
	}
	maxIdx := 0
	for i := 1; i < len(h.items); i++ {
		if h.items[i].dist > h.items[maxIdx].dist {
			maxIdx = i
		}
	}
	h.items = append(h.items[:maxIdx], h.items[maxIdx+1:]...)
}

func (h *distHeap) bubbleDown(i int) {
	for {
		left := 2*i + 1
		right := 2*i + 2
		smallest := i

		if left < len(h.items) && h.items[left].dist < h.items[smallest].dist {
			smallest = left
		}
		if right < len(h.items) && h.items[right].dist < h.items[smallest].dist {
			smallest = right
		}
		if smallest == i {
			break
		}
		h.items[i], h.items[smallest] = h.items[smallest], h.items[i]
		i = smallest
	}
}
