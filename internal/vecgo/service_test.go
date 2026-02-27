package vecgo

import (
	"context"
	"path/filepath"
	"testing"

	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	assert.Equal(t, 500, cfg.ChunkSize)
	assert.Equal(t, 4096, cfg.EmbedDims)
	assert.Equal(t, 16, cfg.HNSWM)
	assert.Equal(t, 200, cfg.HNSWEfC)
	assert.Equal(t, 50, cfg.HNSWEfS)
	assert.Empty(t, cfg.DBPath)
}

func TestInterfaceCompliance(t *testing.T) {
	// Compile-time check is in service.go, but verify at runtime too.
	var _ types.VectorService = (*Service)(nil)
}

func TestNewServiceInMemory(t *testing.T) {
	svc, err := NewService(DefaultConfig())
	require.NoError(t, err)
	require.NotNil(t, svc)
	defer svc.Close()
}

func TestNewServiceWithSQLite(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.vector.db")
	cfg := DefaultConfig()
	cfg.DBPath = dbPath

	svc, err := NewService(cfg)
	require.NoError(t, err)
	require.NotNil(t, svc)
	defer svc.Close()
}

func TestIndexAndSearch(t *testing.T) {
	svc, err := NewService(DefaultConfig())
	require.NoError(t, err)
	defer svc.Close()

	ctx := context.Background()

	// Index some documents.
	require.NoError(t, svc.Index(ctx, "doc1", "The quick brown fox jumps over the lazy dog", map[string]string{"source": "test"}))
	require.NoError(t, svc.Index(ctx, "doc2", "A cat sat on the mat watching the birds fly by", map[string]string{"source": "test"}))
	require.NoError(t, svc.Index(ctx, "doc3", "The fox chased the rabbit through the forest", map[string]string{"source": "test"}))

	// Search for fox-related content.
	results, err := svc.Search(ctx, "fox", 5)
	require.NoError(t, err)
	require.NotEmpty(t, results)

	// Results should have valid fields.
	for _, r := range results {
		assert.NotEmpty(t, r.ID)
		assert.NotEmpty(t, r.Content)
		assert.GreaterOrEqual(t, r.Score, float64(0))
	}
}

func TestSearchEmptyIndex(t *testing.T) {
	svc, err := NewService(DefaultConfig())
	require.NoError(t, err)
	defer svc.Close()

	ctx := context.Background()
	results, err := svc.Search(ctx, "anything", 5)
	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestRemove(t *testing.T) {
	svc, err := NewService(DefaultConfig())
	require.NoError(t, err)
	defer svc.Close()

	ctx := context.Background()

	require.NoError(t, svc.Index(ctx, "doc1", "The quick brown fox", nil))
	require.NoError(t, svc.Index(ctx, "doc2", "A lazy dog sleeps", nil))

	// Remove doc1 — should not error.
	require.NoError(t, svc.Remove(ctx, "doc1"))

	// Search should still work without errors.
	results, err := svc.Search(ctx, "dog sleeps lazy", 5)
	require.NoError(t, err)

	// At least doc2 content should be findable.
	found := false
	for _, r := range results {
		if r.Content != "" {
			found = true
		}
	}
	assert.True(t, found, "should still have searchable results after removal")
}

func TestPersistAndReload(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "persist.vector.db")
	ctx := context.Background()

	// Create service, index data, save, close.
	cfg := DefaultConfig()
	cfg.DBPath = dbPath

	svc1, err := NewService(cfg)
	require.NoError(t, err)

	require.NoError(t, svc1.Index(ctx, "persist-doc", "Persistence is key to reliable vector search", map[string]string{"type": "test"}))
	require.NoError(t, svc1.Save(ctx))
	require.NoError(t, svc1.Close())

	// Reopen service — should load persisted state.
	svc2, err := NewService(cfg)
	require.NoError(t, err)
	defer svc2.Close()

	results, err := svc2.Search(ctx, "persistence vector search", 5)
	require.NoError(t, err)
	require.NotEmpty(t, results, "persisted documents should be searchable after reload")
}

func TestDefaultsAppliedForZeroConfig(t *testing.T) {
	// All-zero Config should get defaults filled in.
	svc, err := NewService(Config{})
	require.NoError(t, err)
	defer svc.Close()
	assert.Equal(t, 500, svc.cfg.ChunkSize)
	assert.Equal(t, 4096, svc.cfg.EmbedDims)
}
