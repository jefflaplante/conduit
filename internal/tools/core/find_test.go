package core

import (
	"context"
	"testing"

	"conduit/internal/fts"
	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockSearchService implements types.SearchService for testing
type mockSearchService struct {
	documents []fts.DocumentResult
	messages  []fts.MessageResult
	beads     []fts.BeadsResult
	docErr    error
	msgErr    error
	beadsErr  error
}

func (m *mockSearchService) SearchDocuments(ctx context.Context, query string, limit int) ([]fts.DocumentResult, error) {
	if m.docErr != nil {
		return nil, m.docErr
	}
	if limit > 0 && len(m.documents) > limit {
		return m.documents[:limit], nil
	}
	return m.documents, nil
}

func (m *mockSearchService) SearchMessages(ctx context.Context, query string, limit int) ([]fts.MessageResult, error) {
	if m.msgErr != nil {
		return nil, m.msgErr
	}
	if limit > 0 && len(m.messages) > limit {
		return m.messages[:limit], nil
	}
	return m.messages, nil
}

func (m *mockSearchService) SearchBeads(ctx context.Context, query string, limit int, statusFilter string) ([]fts.BeadsResult, error) {
	if m.beadsErr != nil {
		return nil, m.beadsErr
	}
	result := m.beads
	if statusFilter != "" && statusFilter != "any" {
		filtered := make([]fts.BeadsResult, 0)
		for _, b := range result {
			if b.Status == statusFilter {
				filtered = append(filtered, b)
			}
		}
		result = filtered
	}
	if limit > 0 && len(result) > limit {
		return result[:limit], nil
	}
	return result, nil
}

func (m *mockSearchService) Search(ctx context.Context, query string, limit int) ([]fts.SearchResult, error) {
	return nil, nil // Not used by Find tool
}

func TestFindToolName(t *testing.T) {
	tool := NewFindTool(nil)
	assert.Equal(t, "Find", tool.Name())
}

func TestFindToolDescription(t *testing.T) {
	tool := NewFindTool(nil)
	desc := tool.Description()
	assert.Contains(t, desc, "Universal search")
	assert.Contains(t, desc, "workspace documents")
	assert.Contains(t, desc, "session messages")
	assert.Contains(t, desc, "beads issues")
}

func TestFindToolParameters(t *testing.T) {
	tool := NewFindTool(nil)
	params := tool.Parameters()

	// Check required fields
	required, ok := params["required"].([]string)
	require.True(t, ok)
	assert.Contains(t, required, "query")

	// Check properties
	props, ok := params["properties"].(map[string]interface{})
	require.True(t, ok)
	assert.Contains(t, props, "query")
	assert.Contains(t, props, "scope")
	assert.Contains(t, props, "limit")
	assert.Contains(t, props, "status")
	assert.Contains(t, props, "semantic")
}

func TestFindToolExecuteWithNoQuery(t *testing.T) {
	tool := NewFindTool(&types.ToolServices{})
	ctx := context.Background()

	result, err := tool.Execute(ctx, map[string]interface{}{})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "query parameter is required")
}

func TestFindToolExecuteWithNoSearcher(t *testing.T) {
	tool := NewFindTool(nil)
	ctx := context.Background()

	result, err := tool.Execute(ctx, map[string]interface{}{
		"query": "test",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "not available")
}

func TestFindToolExecuteAllScope(t *testing.T) {
	mockSearcher := &mockSearchService{
		documents: []fts.DocumentResult{
			{FilePath: "doc1.md", Heading: "Test", Content: "Document content", Rank: -10},
		},
		messages: []fts.MessageResult{
			{MessageID: "msg1", SessionKey: "session1", Role: "user", Content: "Message content", Rank: -5},
		},
		beads: []fts.BeadsResult{
			{IssueID: "test-1", Title: "Test issue", Status: "open", Rank: -8},
		},
	}

	tool := NewFindTool(&types.ToolServices{
		Searcher: mockSearcher,
	})

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]interface{}{
		"query": "test",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "Search Results")

	// Check data
	data := result.Data
	assert.Equal(t, 3, data["result_count"])
	assert.Equal(t, "all", data["scope"])
}

func TestFindToolExecuteMemoryScope(t *testing.T) {
	mockSearcher := &mockSearchService{
		documents: []fts.DocumentResult{
			{FilePath: "doc1.md", Content: "Test content", Rank: -10},
		},
		messages: []fts.MessageResult{
			{MessageID: "msg1", Content: "Should not appear", Rank: -5},
		},
	}

	tool := NewFindTool(&types.ToolServices{
		Searcher: mockSearcher,
	})

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]interface{}{
		"query": "test",
		"scope": "memory",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)

	data := result.Data
	assert.Equal(t, 1, data["result_count"])
	assert.Equal(t, "memory", data["scope"])
}

func TestFindToolExecuteSessionScope(t *testing.T) {
	mockSearcher := &mockSearchService{
		documents: []fts.DocumentResult{
			{FilePath: "doc1.md", Content: "Should not appear", Rank: -10},
		},
		messages: []fts.MessageResult{
			{MessageID: "msg1", SessionKey: "session1", Role: "user", Content: "Test message", Rank: -5},
		},
	}

	tool := NewFindTool(&types.ToolServices{
		Searcher: mockSearcher,
	})

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]interface{}{
		"query": "test",
		"scope": "session",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)

	data := result.Data
	assert.Equal(t, 1, data["result_count"])
	assert.Equal(t, "session", data["scope"])
}

func TestFindToolExecuteBeadsScope(t *testing.T) {
	mockSearcher := &mockSearchService{
		beads: []fts.BeadsResult{
			{IssueID: "test-1", Title: "Open issue", Status: "open", Rank: -10},
			{IssueID: "test-2", Title: "Done issue", Status: "done", Rank: -5},
		},
	}

	tool := NewFindTool(&types.ToolServices{
		Searcher: mockSearcher,
	})

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]interface{}{
		"query": "test",
		"scope": "beads",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)

	data := result.Data
	assert.Equal(t, 2, data["result_count"])
	assert.Equal(t, "beads", data["scope"])
}

func TestFindToolExecuteBeadsScopeWithStatusFilter(t *testing.T) {
	mockSearcher := &mockSearchService{
		beads: []fts.BeadsResult{
			{IssueID: "test-1", Title: "Open issue", Status: "open", Rank: -10},
			{IssueID: "test-2", Title: "Done issue", Status: "done", Rank: -5},
			{IssueID: "test-3", Title: "Another open", Status: "open", Rank: -8},
		},
	}

	tool := NewFindTool(&types.ToolServices{
		Searcher: mockSearcher,
	})

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]interface{}{
		"query":  "test",
		"scope":  "beads",
		"status": "open",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)

	data := result.Data
	assert.Equal(t, 2, data["result_count"]) // Only open issues
}

func TestFindToolExecuteWithLimit(t *testing.T) {
	mockSearcher := &mockSearchService{
		documents: []fts.DocumentResult{
			{FilePath: "doc1.md", Rank: -10},
			{FilePath: "doc2.md", Rank: -9},
			{FilePath: "doc3.md", Rank: -8},
		},
	}

	tool := NewFindTool(&types.ToolServices{
		Searcher: mockSearcher,
	})

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]interface{}{
		"query": "test",
		"scope": "memory",
		"limit": float64(2),
	})
	require.NoError(t, err)
	assert.True(t, result.Success)

	data := result.Data
	assert.Equal(t, 2, data["result_count"])
}

func TestFindToolExecuteLimitBounds(t *testing.T) {
	mockSearcher := &mockSearchService{
		documents: []fts.DocumentResult{
			{FilePath: "doc1.md", Rank: -10},
		},
	}

	tool := NewFindTool(&types.ToolServices{
		Searcher: mockSearcher,
	})
	ctx := context.Background()

	// Test limit < 1 defaults to 1
	result, err := tool.Execute(ctx, map[string]interface{}{
		"query": "test",
		"scope": "memory",
		"limit": float64(0),
	})
	require.NoError(t, err)
	assert.True(t, result.Success)

	// Test limit > 50 capped to 50
	result, err = tool.Execute(ctx, map[string]interface{}{
		"query": "test",
		"scope": "memory",
		"limit": float64(100),
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
}

func TestNormalizeRank(t *testing.T) {
	tests := []struct {
		name     string
		rank     float64
		expected float64
	}{
		{"perfect match", -30.0, 1.0},
		{"good match", -15.0, 0.5},
		{"weak match", -5.0, 0.16666666666666666},
		{"no match", 0.0, 0.0},
		{"positive rank", 5.0, 0.0},
		{"very negative", -60.0, 1.0}, // Capped at 1.0
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeRank(tt.rank)
			assert.InDelta(t, tt.expected, result, 0.001)
		})
	}
}

func TestDeduplicateResults(t *testing.T) {
	results := []FindResult{
		{SourceID: "id1", Score: 0.9},
		{SourceID: "id2", Score: 0.8},
		{SourceID: "id1", Score: 0.7}, // Duplicate - should be removed
		{SourceID: "id3", Score: 0.6},
	}

	deduped := deduplicateResults(results)
	assert.Len(t, deduped, 3)

	// First occurrence of id1 (score 0.9) should be kept
	found := false
	for _, r := range deduped {
		if r.SourceID == "id1" {
			assert.Equal(t, 0.9, r.Score)
			found = true
			break
		}
	}
	assert.True(t, found)
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxLen   int
		expected string
	}{
		{"short string", "hello", 10, "hello"},
		{"exact length", "hello", 5, "hello"},
		{"needs truncation", "hello world", 8, "hello..."},
		{"with newlines", "hello\nworld", 20, "hello world"},
		{"with multiple spaces", "hello   world", 20, "hello world"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncate(tt.input, tt.maxLen)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFormatFindResults(t *testing.T) {
	results := []FindResult{
		{Source: "document", Score: 0.9, Title: "doc1.md", Summary: "Test content"},
		{Source: "message", Score: 0.8, Title: "[user] session1", Summary: "Hello"},
	}

	output := formatFindResults("test", "all", results, nil)

	assert.Contains(t, output, "Search Results for \"test\"")
	assert.Contains(t, output, "Scope: all")
	assert.Contains(t, output, "Found: 2 results")
	assert.Contains(t, output, "doc1.md")
	assert.Contains(t, output, "[user] session1")
}

func TestFormatFindResultsEmpty(t *testing.T) {
	output := formatFindResults("test", "all", []FindResult{}, nil)
	assert.Contains(t, output, "No results found")
}

func TestFormatFindResultsWithErrors(t *testing.T) {
	results := []FindResult{
		{Source: "document", Score: 0.9, Title: "doc1.md", Summary: "Content"},
	}
	errors := []string{"message search: connection failed"}

	output := formatFindResults("test", "all", results, errors)

	assert.Contains(t, output, "Partial Errors")
	assert.Contains(t, output, "message search: connection failed")
}

// mockVectorService implements types.VectorService for testing.
type mockVectorService struct {
	results []types.VectorSearchResult
	err     error
}

func (m *mockVectorService) Search(ctx context.Context, query string, limit int) ([]types.VectorSearchResult, error) {
	if m.err != nil {
		return nil, m.err
	}
	if limit > 0 && len(m.results) > limit {
		return m.results[:limit], nil
	}
	return m.results, nil
}

func (m *mockVectorService) Index(ctx context.Context, id, content string, metadata map[string]string) error {
	return nil
}

func (m *mockVectorService) Remove(ctx context.Context, id string) error {
	return nil
}

func (m *mockVectorService) Close() error {
	return nil
}

func TestFindToolSemanticSearch(t *testing.T) {
	mockSearcher := &mockSearchService{
		documents: []fts.DocumentResult{
			{FilePath: "fts-doc.md", Heading: "FTS", Content: "FTS content", Rank: -10},
		},
	}
	mockVector := &mockVectorService{
		results: []types.VectorSearchResult{
			{ID: "vec-doc1", Score: 0.85, Content: "Vector matched content", Metadata: map[string]string{"source": "workspace", "title": "VecDoc"}},
			{ID: "vec-doc2", Score: 0.65, Content: "Another vector match", Metadata: map[string]string{"path": "notes/vec.md"}},
		},
	}

	tool := NewFindTool(&types.ToolServices{
		Searcher:     mockSearcher,
		VectorSearch: mockVector,
	})

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]interface{}{
		"query":    "test",
		"semantic": true,
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "VecDoc")
	assert.Contains(t, result.Content, "notes/vec.md")

	// Should include both FTS and vector results.
	data := result.Data
	count := data["result_count"].(int)
	assert.Equal(t, 3, count)
}

func TestFindToolSemanticSearchUnavailable(t *testing.T) {
	mockSearcher := &mockSearchService{
		documents: []fts.DocumentResult{
			{FilePath: "doc.md", Content: "Content", Rank: -10},
		},
	}

	// VectorSearch is nil — should degrade gracefully.
	tool := NewFindTool(&types.ToolServices{
		Searcher: mockSearcher,
	})

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]interface{}{
		"query":    "test",
		"semantic": true,
	})
	require.NoError(t, err)
	assert.True(t, result.Success)

	// Should still return FTS results.
	data := result.Data
	assert.Equal(t, 1, data["result_count"])

	// Error list should note vector search unavailability.
	errors := data["errors"].([]string)
	assert.Len(t, errors, 1)
	assert.Contains(t, errors[0], "vector search requested but not available")
}

func TestFindToolSemanticFalseSkipsVector(t *testing.T) {
	mockSearcher := &mockSearchService{
		documents: []fts.DocumentResult{
			{FilePath: "doc.md", Content: "Content", Rank: -10},
		},
	}
	mockVector := &mockVectorService{
		results: []types.VectorSearchResult{
			{ID: "vec1", Score: 0.9, Content: "Should not appear"},
		},
	}

	tool := NewFindTool(&types.ToolServices{
		Searcher:     mockSearcher,
		VectorSearch: mockVector,
	})

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]interface{}{
		"query":    "test",
		"semantic": false,
	})
	require.NoError(t, err)
	assert.True(t, result.Success)

	// Only FTS results — no vector results.
	data := result.Data
	assert.Equal(t, 1, data["result_count"])
}

func TestSourceFromMetadata(t *testing.T) {
	assert.Equal(t, "workspace", sourceFromMetadata(map[string]string{"source": "workspace"}))
	assert.Equal(t, "document", sourceFromMetadata(map[string]string{}))
	assert.Equal(t, "document", sourceFromMetadata(nil))
}

func TestTitleFromMetadata(t *testing.T) {
	assert.Equal(t, "My Title", titleFromMetadata(map[string]string{"title": "My Title"}, "fallback"))
	assert.Equal(t, "notes/file.md", titleFromMetadata(map[string]string{"path": "notes/file.md"}, "fallback"))
	assert.Equal(t, "fallback-id", titleFromMetadata(map[string]string{}, "fallback-id"))
}

func TestFindToolGetUsageExamples(t *testing.T) {
	tool := NewFindTool(nil)
	examples := tool.GetUsageExamples()

	assert.GreaterOrEqual(t, len(examples), 1)
	for _, ex := range examples {
		assert.NotEmpty(t, ex.Name)
		assert.NotEmpty(t, ex.Description)
		assert.NotNil(t, ex.Args)
	}
}
