package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"conduit/internal/config"
	"conduit/internal/fts"
	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- helpers ---

func setupTestMemoryTool(t *testing.T, services *types.ToolServices) *MemorySearchTool {
	t.Helper()
	workspaceDir := t.TempDir()

	// Create memory files
	memDir := filepath.Join(workspaceDir, "memory")
	require.NoError(t, os.MkdirAll(memDir, 0755))

	require.NoError(t, os.WriteFile(
		filepath.Join(workspaceDir, "MEMORY.md"),
		[]byte("# Memory\n\nThis is the main memory file.\n\n## Projects\n\nConduit gateway project is in progress.\n"),
		0644,
	))

	require.NoError(t, os.WriteFile(
		filepath.Join(memDir, "notes.md"),
		[]byte("## Notes\n\nDatabase configuration uses SQLite with WAL mode.\nThe deployment process involves docker compose.\n"),
		0644,
	))

	sandboxCfg := config.SandboxConfig{
		WorkspaceDir: workspaceDir,
	}

	if services == nil {
		services = &types.ToolServices{}
	}

	return NewMemorySearchTool(services, sandboxCfg)
}

// --- Tests ---

func TestMemorySearchTool_Name(t *testing.T) {
	tool := setupTestMemoryTool(t, nil)
	assert.Equal(t, "MemorySearch", tool.Name())
}

func TestMemorySearchTool_Description(t *testing.T) {
	tool := setupTestMemoryTool(t, nil)
	assert.Contains(t, tool.Description(), "hybrid")
}

func TestMemorySearchTool_Parameters(t *testing.T) {
	tool := setupTestMemoryTool(t, nil)
	params := tool.Parameters()

	props, ok := params["properties"].(map[string]interface{})
	require.True(t, ok)

	// Verify searchMode parameter exists
	searchMode, ok := props["searchMode"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "string", searchMode["type"])

	enumValues, ok := searchMode["enum"].([]string)
	require.True(t, ok)
	assert.Contains(t, enumValues, "auto")
	assert.Contains(t, enumValues, "hybrid")
	assert.Contains(t, enumValues, "vector")
	assert.Contains(t, enumValues, "fts5")
}

func TestResolveSearchMode_Auto(t *testing.T) {
	tests := []struct {
		name      string
		hasVector bool
		hasFTS5   bool
		expected  string
	}{
		{"both available", true, true, "hybrid"},
		{"vector only", true, false, "vector"},
		{"fts5 only", false, true, "fts5"},
		{"neither", false, false, "grep"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			services := &types.ToolServices{}
			if tc.hasVector {
				services.VectorSearch = &mockVectorService{}
			}
			if tc.hasFTS5 {
				services.Searcher = &mockSearchService{}
			}

			tool := &MemorySearchTool{services: services}
			mode := tool.resolveSearchMode("auto")
			assert.Equal(t, tc.expected, mode)
		})
	}
}

func TestResolveSearchMode_Hybrid_Fallback(t *testing.T) {
	tests := []struct {
		name      string
		hasVector bool
		hasFTS5   bool
		expected  string
	}{
		{"both available", true, true, "hybrid"},
		{"vector only", true, false, "vector"},
		{"fts5 only", false, true, "fts5"},
		{"neither", false, false, "grep"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			services := &types.ToolServices{}
			if tc.hasVector {
				services.VectorSearch = &mockVectorService{}
			}
			if tc.hasFTS5 {
				services.Searcher = &mockSearchService{}
			}

			tool := &MemorySearchTool{services: services}
			mode := tool.resolveSearchMode("hybrid")
			assert.Equal(t, tc.expected, mode)
		})
	}
}

func TestResolveSearchMode_VectorFallback(t *testing.T) {
	// If vector requested but unavailable, fall back to fts5
	services := &types.ToolServices{
		Searcher: &mockSearchService{},
	}
	tool := &MemorySearchTool{services: services}
	assert.Equal(t, "fts5", tool.resolveSearchMode("vector"))

	// If neither available, fall back to grep
	tool2 := &MemorySearchTool{services: &types.ToolServices{}}
	assert.Equal(t, "grep", tool2.resolveSearchMode("vector"))
}

func TestResolveSearchMode_FTS5Fallback(t *testing.T) {
	// If fts5 requested but unavailable, fall back to grep
	tool := &MemorySearchTool{services: &types.ToolServices{}}
	assert.Equal(t, "grep", tool.resolveSearchMode("fts5"))
}

func TestReciprocalRankFusion_BasicMerge(t *testing.T) {
	list1 := []MemoryResult{
		{Path: "a.md", Content: "content A", Source: "file", Score: 0.9},
		{Path: "b.md", Content: "content B", Source: "file", Score: 0.7},
		{Path: "c.md", Content: "content C", Source: "file", Score: 0.5},
	}
	list2 := []MemoryResult{
		{Path: "b.md", Content: "content B", Source: "file", Score: 0.95},
		{Path: "a.md", Content: "content A", Source: "file", Score: 0.6},
		{Path: "d.md", Content: "content D", Source: "file", Score: 0.4},
	}

	merged := reciprocalRankFusion(list1, list2)

	// All 4 unique items should be present
	assert.Len(t, merged, 4)

	// All results should have "hybrid" search type
	for _, r := range merged {
		assert.Equal(t, "hybrid", r.SearchType)
	}

	// Items appearing in both lists should have higher scores than those in one
	scoreMap := make(map[string]float64)
	for _, r := range merged {
		scoreMap[r.Path] = r.Score
	}

	// b.md and a.md appear in both lists, so should have higher RRF scores
	assert.Greater(t, scoreMap["b.md"], scoreMap["d.md"], "b.md (in both lists) should score higher than d.md (in one list)")
	assert.Greater(t, scoreMap["a.md"], scoreMap["d.md"], "a.md (in both lists) should score higher than d.md (in one list)")
}

func TestReciprocalRankFusion_SingleList(t *testing.T) {
	list := []MemoryResult{
		{Path: "a.md", Content: "content A", Source: "file", Score: 0.9},
		{Path: "b.md", Content: "content B", Source: "file", Score: 0.7},
	}

	merged := reciprocalRankFusion(list)
	assert.Len(t, merged, 2)

	// Order should be preserved (first item has higher RRF score)
	assert.Equal(t, "a.md", merged[0].Path)
	assert.Equal(t, "b.md", merged[1].Path)
}

func TestReciprocalRankFusion_EmptyLists(t *testing.T) {
	merged := reciprocalRankFusion([]MemoryResult{}, []MemoryResult{})
	assert.Empty(t, merged)
}

func TestReciprocalRankFusion_Deduplication(t *testing.T) {
	// Same content in both lists should be merged, not duplicated
	list1 := []MemoryResult{
		{Path: "a.md", Content: "exact same content", Source: "file"},
	}
	list2 := []MemoryResult{
		{Path: "a.md", Content: "exact same content", Source: "file"},
	}

	merged := reciprocalRankFusion(list1, list2)
	assert.Len(t, merged, 1, "duplicate results should be merged")

	// Score should be sum of both lists' contributions
	expectedScore := 1.0/float64(rrfK+1) + 1.0/float64(rrfK+1)
	assert.InDelta(t, expectedScore, merged[0].Score, 0.0001)
}

func TestResultDeduplicationKey_FilesVsSessions(t *testing.T) {
	fileResult := MemoryResult{Path: "test.md", Content: "some content", Source: "file"}
	sessionResult := MemoryResult{SessionKey: "sess1", Content: "some content", Source: "session"}

	fileKey := resultDeduplicationKey(fileResult)
	sessionKey := resultDeduplicationKey(sessionResult)

	assert.NotEqual(t, fileKey, sessionKey, "file and session results should have different keys")
	assert.Contains(t, fileKey, "file:")
	assert.Contains(t, sessionKey, "session:")
}

func TestExecute_GrepFallback(t *testing.T) {
	// No FTS5, no vector -- should fall back to grep
	tool := setupTestMemoryTool(t, &types.ToolServices{})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query":          "memory",
		"searchSessions": false,
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "memory")

	data := result.Data
	assert.Equal(t, "auto", data["searchMode"])
	assert.Equal(t, "grep", data["effectiveMode"])
	assert.Equal(t, false, data["vectorAvailable"])
}

func TestExecute_VectorOnly(t *testing.T) {
	mockVec := &mockVectorService{
		results: []types.VectorSearchResult{
			{
				ID:       "test.md",
				Score:    0.85,
				Content:  "Conduit gateway is a Go application",
				Metadata: map[string]string{"path": "test.md", "title": "Test"},
			},
		},
	}

	services := &types.ToolServices{
		VectorSearch: mockVec,
	}
	tool := setupTestMemoryTool(t, services)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query":          "Go application",
		"searchMode":     "vector",
		"searchSessions": false,
		"minScore":       0.0,
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, "vector", result.Data["effectiveMode"])
	assert.Equal(t, true, result.Data["vectorAvailable"])
}

func TestExecute_VectorError_ReturnsFailure(t *testing.T) {
	// Vector requested and available but errors at runtime
	mockVec := &mockVectorService{
		err: fmt.Errorf("connection refused"),
	}

	services := &types.ToolServices{
		VectorSearch: mockVec,
	}
	tool := setupTestMemoryTool(t, services)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query":          "test",
		"searchMode":     "vector",
		"searchSessions": false,
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "search failed")
}

func TestExecute_HybridMode(t *testing.T) {
	mockVec := &mockVectorService{
		results: []types.VectorSearchResult{
			{
				ID:       "vec-doc.md",
				Score:    0.9,
				Content:  "Vector found this semantic result about databases",
				Metadata: map[string]string{"path": "vec-doc.md", "source": "workspace"},
			},
		},
	}

	mockFTS := &mockSearchService{
		documents: []fts.DocumentResult{
			{
				FilePath: "fts-doc.md",
				Heading:  "## Database",
				Content:  "FTS5 found this keyword match about databases",
				Rank:     -10.5,
			},
		},
	}

	services := &types.ToolServices{
		VectorSearch: mockVec,
		Searcher:     mockFTS,
	}
	tool := setupTestMemoryTool(t, services)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query":          "database",
		"searchMode":     "hybrid",
		"searchSessions": false,
		"minScore":       0.0,
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, "hybrid", result.Data["effectiveMode"])

	// Should have results from both sources (2 unique results merged)
	total := result.Data["total"].(int)
	assert.Equal(t, 2, total, "should have 2 merged results from vector + FTS5")
}

func TestExecute_HybridFallsBackWhenVectorFails(t *testing.T) {
	mockVec := &mockVectorService{
		err: fmt.Errorf("vector index empty"),
	}

	mockFTS := &mockSearchService{
		documents: []fts.DocumentResult{
			{
				FilePath: "fallback.md",
				Heading:  "## Fallback",
				Content:  "This was found by FTS5 after vector failed",
				Rank:     -8.0,
			},
		},
	}

	services := &types.ToolServices{
		VectorSearch: mockVec,
		Searcher:     mockFTS,
	}
	tool := setupTestMemoryTool(t, services)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query":          "fallback test",
		"searchSessions": false,
		"minScore":       0.0,
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	// Should succeed with FTS5 results even though vector failed
	assert.Contains(t, result.Content, "FTS5")
}

func TestExecute_HybridFallsBackWhenFTS5Fails(t *testing.T) {
	mockVec := &mockVectorService{
		results: []types.VectorSearchResult{
			{
				ID:       "vec-only.md",
				Score:    0.75,
				Content:  "Only vector found this",
				Metadata: map[string]string{"path": "vec-only.md"},
			},
		},
	}

	mockFTS := &mockSearchService{
		docErr: fmt.Errorf("FTS5 table not found"),
	}

	services := &types.ToolServices{
		VectorSearch: mockVec,
		Searcher:     mockFTS,
	}
	tool := setupTestMemoryTool(t, services)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query":          "vector only",
		"searchSessions": false,
		"minScore":       0.0,
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "vector")
}

func TestExecute_MinScoreFilter(t *testing.T) {
	mockVec := &mockVectorService{
		results: []types.VectorSearchResult{
			{ID: "high.md", Score: 0.9, Content: "High score", Metadata: map[string]string{"path": "high.md"}},
			{ID: "low.md", Score: 0.01, Content: "Low score", Metadata: map[string]string{"path": "low.md"}},
		},
	}

	services := &types.ToolServices{
		VectorSearch: mockVec,
	}
	tool := setupTestMemoryTool(t, services)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query":          "test",
		"searchMode":     "vector",
		"minScore":       0.5,
		"searchSessions": false,
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, 1, result.Data["total"].(int), "low-score result should be filtered out")
}

func TestExecute_MaxResultsLimit(t *testing.T) {
	var vecResults []types.VectorSearchResult
	for i := 0; i < 20; i++ {
		vecResults = append(vecResults, types.VectorSearchResult{
			ID:       fmt.Sprintf("doc%d.md", i),
			Score:    0.9 - float64(i)*0.01,
			Content:  fmt.Sprintf("Document %d content", i),
			Metadata: map[string]string{"path": fmt.Sprintf("doc%d.md", i)},
		})
	}

	services := &types.ToolServices{
		VectorSearch: &mockVectorService{results: vecResults},
	}
	tool := setupTestMemoryTool(t, services)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query":          "document",
		"searchMode":     "vector",
		"maxResults":     5,
		"minScore":       0.0,
		"searchSessions": false,
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, 5, result.Data["total"].(int))
}

func TestExecute_InvalidQuery(t *testing.T) {
	tool := setupTestMemoryTool(t, nil)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query": 123, // not a string
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "query parameter is required")
}

func TestSearchMemoryFilesVector_NilService(t *testing.T) {
	tool := &MemorySearchTool{
		services: &types.ToolServices{},
	}

	_, err := tool.searchMemoryFilesVector(context.Background(), "test")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not available")
}

func TestMemoryFormatSearchResults_Empty(t *testing.T) {
	tool := setupTestMemoryTool(t, nil)
	output := tool.formatSearchResults(nil, "test query")
	assert.Contains(t, output, "No results found")
}

func TestMemoryFormatSearchResults_WithSearchType(t *testing.T) {
	tool := setupTestMemoryTool(t, nil)
	results := []MemoryResult{
		{
			Path:       "test.md",
			Content:    "Test content",
			Score:      0.0328,
			Source:     "file",
			SearchType: "hybrid",
		},
	}
	output := tool.formatSearchResults(results, "test")
	assert.Contains(t, output, "[hybrid]")
	assert.Contains(t, output, "0.0328")
}

func TestMemoryFormatSearchResults_SessionResult(t *testing.T) {
	tool := setupTestMemoryTool(t, nil)
	results := []MemoryResult{
		{
			Path:       "session:sess1",
			Content:    "Session content",
			Score:      0.5,
			Source:     "session",
			SessionKey: "sess1",
			Role:       "user",
			Timestamp:  "2025-01-01 10:00",
			SearchType: "fts5",
		},
	}
	output := tool.formatSearchResults(results, "test")
	assert.Contains(t, output, "Session History")
	assert.Contains(t, output, "[fts5]")
	assert.Contains(t, output, "sess1")
}

func TestMemoryFormatSearchResults_FileWithLineNum(t *testing.T) {
	tool := setupTestMemoryTool(t, nil)
	results := []MemoryResult{
		{
			Path:    "notes.md",
			Content: "Some content",
			Score:   0.5,
			LineNum: 42,
			Source:  "file",
		},
	}
	output := tool.formatSearchResults(results, "test")
	assert.Contains(t, output, "line 42")
}

func TestMemoryFormatSearchResults_FileWithoutLineNum(t *testing.T) {
	tool := setupTestMemoryTool(t, nil)
	results := []MemoryResult{
		{
			Path:       "notes.md",
			Content:    "Some content",
			Score:      0.5,
			Source:     "file",
			SearchType: "vector",
		},
	}
	output := tool.formatSearchResults(results, "test")
	// Should not contain "line 0"
	assert.NotContains(t, output, "line 0")
}

func TestMemoryGetUsageExamples(t *testing.T) {
	tool := setupTestMemoryTool(t, nil)
	examples := tool.GetUsageExamples()
	assert.GreaterOrEqual(t, len(examples), 3)

	// At least one example should mention searchMode
	found := false
	for _, ex := range examples {
		if _, ok := ex.Args["searchMode"]; ok {
			found = true
			break
		}
	}
	assert.True(t, found, "at least one example should demonstrate searchMode parameter")
}

func TestExecute_AutoModeUsesHybridWhenBothAvailable(t *testing.T) {
	mockVec := &mockVectorService{
		results: []types.VectorSearchResult{
			{
				ID:       "vec.md",
				Score:    0.8,
				Content:  "Vector result",
				Metadata: map[string]string{"path": "vec.md"},
			},
		},
	}

	mockFTS := &mockSearchService{
		documents: []fts.DocumentResult{
			{
				FilePath: "fts.md",
				Heading:  "## Test",
				Content:  "FTS result",
				Rank:     -5.0,
			},
		},
	}

	services := &types.ToolServices{
		VectorSearch: mockVec,
		Searcher:     mockFTS,
	}
	tool := setupTestMemoryTool(t, services)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query":          "test",
		"searchSessions": false,
		"minScore":       0.0,
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	// Default searchMode is "auto", which resolves to "hybrid" when both are available
	assert.Equal(t, "auto", result.Data["searchMode"])
	assert.Equal(t, "hybrid", result.Data["effectiveMode"])
}
