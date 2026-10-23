package core

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"conduit/internal/config"
	"conduit/internal/fts"
	"conduit/internal/searchdb"
	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockMetadataSearchService adds types.DocumentMetadataSearcher to
// mockSearchService and records the filter it was called with.
type mockMetadataSearchService struct {
	mockSearchService
	paths        []string
	filteredDocs []fts.DocumentResult
	summaries    []fts.DocumentMetadata

	gotFilter fts.DocumentFilter
	gotLimit  int
	calls     int
}

func (m *mockMetadataSearchService) SearchDocumentsFiltered(_ context.Context, _ string, limit int, filter fts.DocumentFilter) ([]fts.DocumentResult, error) {
	m.calls++
	m.gotFilter, m.gotLimit = filter, limit
	return m.filteredDocs, nil
}

func (m *mockMetadataSearchService) MatchingDocumentPaths(_ context.Context, filter fts.DocumentFilter) ([]string, error) {
	m.calls++
	m.gotFilter = filter
	return m.paths, nil
}

func (m *mockMetadataSearchService) ListDocumentMetadata(_ context.Context, filter fts.DocumentFilter, limit int) ([]fts.DocumentMetadata, error) {
	m.calls++
	m.gotFilter, m.gotLimit = filter, limit
	if limit > 0 && len(m.summaries) > limit {
		return m.summaries[:limit], nil
	}
	return m.summaries, nil
}

func TestMemoryFilterFromArgs(t *testing.T) {
	filter, err := memoryFilterFromArgs(map[string]interface{}{
		"tags":        []interface{}{" alpha ", "ALPHA", "", "beta"},
		"people":      "Rex, Acme",
		"projects":    []string{"projects.alpha"},
		"category":    " notes ",
		"date_after":  "2026-01-01",
		"date_before": "2026-02-01T00:00:00Z",
	})
	require.NoError(t, err)
	assert.Equal(t, fts.DocumentFilter{
		Tags:       []string{"alpha", "beta"},
		People:     []string{"Rex", "Acme"},
		Projects:   []string{"projects.alpha"},
		Category:   "notes",
		DateAfter:  "2026-01-01",
		DateBefore: "2026-02-01",
	}, filter)

	filter, err = memoryFilterFromArgs(map[string]interface{}{"query": "x"})
	require.NoError(t, err)
	assert.True(t, filter.IsEmpty())

	_, err = memoryFilterFromArgs(map[string]interface{}{"date_after": "last week"})
	assert.ErrorContains(t, err, "date_after")
}

func TestMemorySearchTool_ParametersIncludeFilters(t *testing.T) {
	props := setupTestMemoryTool(t, nil).Parameters()["properties"].(map[string]interface{})
	for _, name := range []string{"tags", "people", "projects", "category", "date_after", "date_before", "summary_only"} {
		assert.Contains(t, props, name)
	}
}

// hybridServices returns fresh vector+FTS mocks with one hit each.
func hybridServices() (*types.ToolServices, *mockMetadataSearchService) {
	searcher := &mockMetadataSearchService{
		mockSearchService: mockSearchService{documents: []fts.DocumentResult{
			{FilePath: "memory/fts.md", Heading: "## A", Content: "FTS hit about widgets", Rank: -8},
		}},
	}
	vec := &mockVectorService{results: []types.VectorSearchResult{
		{ID: "memory/vec.md", Score: 0.7, Content: "Vector hit about widgets", Metadata: map[string]string{"path": "memory/vec.md"}},
	}}
	return &types.ToolServices{Searcher: searcher, VectorSearch: vec}, searcher
}

// Regression (bd-3o6h): empty filter args must behave exactly like no
// filter args, and must not touch the metadata methods at all.
func TestExecute_EmptyFilterArgsIdenticalToNoFilters(t *testing.T) {
	base := map[string]interface{}{"query": "widgets", "minScore": 0.0}
	withEmpty := map[string]interface{}{
		"query": "widgets", "minScore": 0.0,
		"tags": []interface{}{}, "people": []interface{}{""}, "projects": "",
		"category": "", "date_after": "", "date_before": "", "summary_only": false,
	}

	for _, mode := range []string{"auto", "fts5", "vector"} {
		t.Run(mode, func(t *testing.T) {
			servicesA, searcherA := hybridServices()
			servicesB, searcherB := hybridServices()
			toolA := setupTestMemoryTool(t, servicesA)
			toolB := setupTestMemoryTool(t, servicesB)
			toolB.workspaceDir = toolA.workspaceDir

			a, b := copyArgs(base), copyArgs(withEmpty)
			a["searchMode"], b["searchMode"] = mode, mode

			resA, err := toolA.Execute(context.Background(), a)
			require.NoError(t, err)
			resB, err := toolB.Execute(context.Background(), b)
			require.NoError(t, err)

			require.True(t, resA.Success)
			require.NotZero(t, resA.Data["total"], "comparison must cover real results")
			assert.Equal(t, resA, resB)
			assert.NotContains(t, resB.Data, "filters")
			assert.Zero(t, searcherA.calls)
			assert.Zero(t, searcherB.calls, "metadata methods are not used without filters")
		})
	}
}

func copyArgs(in map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func TestExecute_FilterRequiresMetadataSearcher(t *testing.T) {
	for name, services := range map[string]*types.ToolServices{
		"grep (no searcher)":        {},
		"searcher without metadata": {Searcher: &mockSearchService{}},
		"vector only (no searcher)": {VectorSearch: &mockVectorService{}},
	} {
		t.Run(name, func(t *testing.T) {
			tool := setupTestMemoryTool(t, services)
			res, err := tool.Execute(context.Background(), map[string]interface{}{
				"query": "widgets", "tags": []interface{}{"alpha"},
			})
			require.NoError(t, err)
			assert.False(t, res.Success)
			assert.Contains(t, res.Error, "search.db")

			res, err = tool.Execute(context.Background(), map[string]interface{}{"summary_only": true})
			require.NoError(t, err)
			assert.False(t, res.Success)
			assert.Contains(t, res.Error, "search.db")
		})
	}
}

func TestExecute_InvalidDateFilter(t *testing.T) {
	services, _ := hybridServices()
	res, err := setupTestMemoryTool(t, services).Execute(context.Background(), map[string]interface{}{
		"query": "widgets", "date_before": "soon",
	})
	require.NoError(t, err)
	assert.False(t, res.Success)
	assert.Contains(t, res.Error, "date_before")
}

func TestExecute_FilteredFTS5(t *testing.T) {
	searcher := &mockMetadataSearchService{
		paths: []string{"memory/a.md"},
		filteredDocs: []fts.DocumentResult{
			{FilePath: "memory/a.md", Heading: "## A", Content: "alpha widget", Rank: -10},
		},
		mockSearchService: mockSearchService{
			documents: []fts.DocumentResult{{FilePath: "memory/other.md", Content: "unfiltered", Rank: -19}},
			messages:  []fts.MessageResult{{SessionKey: "s1", Content: "widget chat", Rank: -19}},
		},
	}
	tool := setupTestMemoryTool(t, &types.ToolServices{Searcher: searcher})

	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"query": "widget", "searchMode": "fts5", "minScore": 0.0,
		"tags": []interface{}{"Alpha"}, "category": "Ops",
	})
	require.NoError(t, err)
	require.True(t, res.Success, res.Error)

	assert.Equal(t, fts.DocumentFilter{Tags: []string{"Alpha"}, Category: "Ops"}, searcher.gotFilter)
	assert.Equal(t, filteredFetchSize, searcher.gotLimit)
	assert.Equal(t, searcher.gotFilter, res.Data["filters"])

	results := res.Data["results"].([]MemoryResult)
	require.Len(t, results, 1, "only filtered file results; no session results")
	assert.Equal(t, MemoryResult{
		Path: "memory/a.md", Content: "alpha widget", Score: 0.5,
		Context: "## A\nalpha widget", Source: "file", SearchType: "fts5",
	}, results[0])
	assert.Equal(t, 0, res.Data["sessionResults"])
}

func TestExecute_FilteredHybridDropsVectorHitsOutsideFilter(t *testing.T) {
	searcher := &mockMetadataSearchService{
		paths: []string{"memory/a.md"},
		filteredDocs: []fts.DocumentResult{
			{FilePath: "memory/a.md", Heading: "## A", Content: "alpha widget", Rank: -10},
		},
	}
	vec := &mockVectorService{results: []types.VectorSearchResult{
		{ID: "memory/b.md", Score: 0.9, Content: "beta widget", Metadata: map[string]string{"path": "memory/b.md"}},
		{ID: "memory/a.md", Score: 0.8, Content: "alpha widget semantic", Metadata: map[string]string{"path": "memory/a.md"}},
	}}
	tool := setupTestMemoryTool(t, &types.ToolServices{Searcher: searcher, VectorSearch: vec})

	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"query": "widget", "minScore": 0.0, "tags": []interface{}{"alpha"},
	})
	require.NoError(t, err)
	require.True(t, res.Success, res.Error)
	assert.Equal(t, "hybrid", res.Data["effectiveMode"])

	results := res.Data["results"].([]MemoryResult)
	require.Len(t, results, 2)
	for _, r := range results {
		assert.Equal(t, "memory/a.md", r.Path)
		assert.Equal(t, "hybrid", r.SearchType)
	}
}

func TestExecute_FilterWithNoMatchingFiles(t *testing.T) {
	searcher := &mockMetadataSearchService{}
	tool := setupTestMemoryTool(t, &types.ToolServices{Searcher: searcher})

	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"query": "widget", "people": []interface{}{"Nobody"},
	})
	require.NoError(t, err)
	assert.True(t, res.Success)
	assert.Equal(t, 0, res.Data["total"])
	assert.Contains(t, res.Content, "No results found")
}

func TestExecute_SummaryOnly(t *testing.T) {
	searcher := &mockMetadataSearchService{summaries: []fts.DocumentMetadata{
		{Path: "memory/b.md", Frontmatter: fts.Frontmatter{Date: "2026-02-10", Category: "notes", Summary: "Beta\n  widget notes"}},
		{Path: "memory/a.md", Frontmatter: fts.Frontmatter{Date: "2026-01-05", Summary: "Alpha rollout"}},
		{Path: "memory/c.md", Frontmatter: fts.Frontmatter{Category: "Ops"}},
	}}
	tool := setupTestMemoryTool(t, &types.ToolServices{Searcher: searcher})

	// No query needed.
	res, err := tool.Execute(context.Background(), map[string]interface{}{"summary_only": true})
	require.NoError(t, err)
	require.True(t, res.Success, res.Error)
	assert.Equal(t, defaultSummaryResults, searcher.gotLimit)
	assert.Equal(t, "Found 3 memory file summaries:\n\n"+
		"- memory/b.md (2026-02-10) [notes] Beta widget notes\n"+
		"- memory/a.md (2026-01-05) Alpha rollout\n"+
		"- memory/c.md [Ops]\n", res.Content)
	assert.Equal(t, true, res.Data["summaryOnly"])
	assert.Equal(t, 3, res.Data["total"])
	assert.Equal(t, searcher.summaries, res.Data["summaries"])
	assert.NotContains(t, res.Data, "filters")

	// Filters and maxResults are honored.
	res, err = tool.Execute(context.Background(), map[string]interface{}{
		"summary_only": true, "query": "ignored", "maxResults": 1, "category": "notes",
	})
	require.NoError(t, err)
	require.True(t, res.Success, res.Error)
	assert.Equal(t, 1, searcher.gotLimit)
	assert.Equal(t, fts.DocumentFilter{Category: "notes"}, searcher.gotFilter)
	assert.Equal(t, 1, res.Data["total"])
	assert.Equal(t, fts.DocumentFilter{Category: "notes"}, res.Data["filters"])

	searcher.summaries = nil
	res, err = tool.Execute(context.Background(), map[string]interface{}{"summary_only": true})
	require.NoError(t, err)
	assert.True(t, res.Success)
	assert.Equal(t, "No memory files with frontmatter matched.", res.Content)
}

// End to end through the real search.db, indexer and searcher.
func TestExecute_FrontmatterFiltersEndToEnd(t *testing.T) {
	dir := t.TempDir()
	workspace := filepath.Join(dir, "workspace")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "memory"), 0o755))
	files := map[string]string{
		"memory/a.md":     "---\ndate: 2026-01-05\ntags: [alpha]\nsummary: Alpha notes\n---\n# A\n\nwidget alpha details\n",
		"memory/b.md":     "---\ndate: 2026-02-10\ntags: [beta]\nsummary: Beta notes\n---\n# B\n\nwidget beta details\n",
		"memory/plain.md": "# Plain\n\nwidget plain details\n",
	}
	for rel, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(workspace, rel), []byte(content), 0o644))
	}

	gatewayDB, err := sql.Open("sqlite", filepath.Join(dir, "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { gatewayDB.Close() })
	sdb, err := searchdb.NewSearchDB(filepath.Join(dir, "gateway.search.db"), filepath.Join(dir, "gateway.db"), gatewayDB)
	require.NoError(t, err)
	t.Cleanup(func() { sdb.Close() })
	require.NoError(t, fts.NewIndexer(sdb.DB(), workspace).IndexWorkspace(context.Background()))

	tool := NewMemorySearchTool(&types.ToolServices{Searcher: fts.NewSearcher(sdb.DB())},
		config.SandboxConfig{WorkspaceDir: workspace})

	paths := func(res *types.ToolResult) []string {
		var out []string
		for _, r := range res.Data["results"].([]MemoryResult) {
			out = append(out, r.Path)
		}
		return out
	}

	res, err := tool.Execute(context.Background(), map[string]interface{}{"query": "widget", "minScore": 0.0})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"memory/a.md", "memory/b.md", "memory/plain.md"}, paths(res))

	res, err = tool.Execute(context.Background(), map[string]interface{}{
		"query": "widget", "minScore": 0.0, "tags": []interface{}{"beta"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"memory/b.md"}, paths(res))

	res, err = tool.Execute(context.Background(), map[string]interface{}{
		"query": "widget", "minScore": 0.0, "date_before": "2026-01-31",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"memory/a.md"}, paths(res))

	res, err = tool.Execute(context.Background(), map[string]interface{}{"summary_only": true})
	require.NoError(t, err)
	assert.Equal(t, "Found 2 memory file summaries:\n\n"+
		"- memory/b.md (2026-02-10) Beta notes\n"+
		"- memory/a.md (2026-01-05) Alpha notes\n", res.Content)
}
