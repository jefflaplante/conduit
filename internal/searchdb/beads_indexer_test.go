package searchdb

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseIssuesJSONL(t *testing.T) {
	jsonl := `{"id":"test-1","title":"First issue","status":"open","issue_type":"task"}
{"id":"test-2","title":"Second issue","description":"Some description","status":"done","issue_type":"bug","owner":"user@example.com"}
{"id":"test-3","title":"Third issue","status":"in_progress","issue_type":"feature"}`

	issues, err := parseIssuesJSONL([]byte(jsonl))
	require.NoError(t, err)
	assert.Len(t, issues, 3)

	// Verify first issue
	assert.Equal(t, "test-1", issues[0].ID)
	assert.Equal(t, "First issue", issues[0].Title)
	assert.Equal(t, "open", issues[0].Status)
	assert.Equal(t, "task", issues[0].IssueType)

	// Verify second issue with all fields
	assert.Equal(t, "test-2", issues[1].ID)
	assert.Equal(t, "Some description", issues[1].Description)
	assert.Equal(t, "user@example.com", issues[1].Owner)
	assert.Equal(t, "done", issues[1].Status)
}

func TestParseIssuesJSONLWithEmptyLines(t *testing.T) {
	jsonl := `{"id":"test-1","title":"First issue","status":"open","issue_type":"task"}

{"id":"test-2","title":"Second issue","status":"done","issue_type":"bug"}
`

	issues, err := parseIssuesJSONL([]byte(jsonl))
	require.NoError(t, err)
	assert.Len(t, issues, 2)
}

func TestParseIssuesJSONLWithInvalidLine(t *testing.T) {
	jsonl := `{"id":"test-1","title":"First issue","status":"open","issue_type":"task"}
not valid json
{"id":"test-2","title":"Second issue","status":"done","issue_type":"bug"}`

	issues, err := parseIssuesJSONL([]byte(jsonl))
	require.NoError(t, err)
	// Should skip invalid line and parse valid ones
	assert.Len(t, issues, 2)
}

func TestBeadsIndexerIndexBeads(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	require.NoError(t, os.MkdirAll(beadsDir, 0755))

	// Create test issues.jsonl
	issuesFile := filepath.Join(beadsDir, "issues.jsonl")
	jsonl := `{"id":"test-1","title":"Implement feature X","description":"Add new feature","status":"open","issue_type":"feature"}
{"id":"test-2","title":"Fix bug Y","status":"done","issue_type":"bug"}
{"id":"test-3","title":"Write tests","status":"in_progress","issue_type":"task"}`
	require.NoError(t, os.WriteFile(issuesFile, []byte(jsonl), 0644))

	// Create search database
	searchPath := filepath.Join(tmpDir, "search.db")
	gatewayPath := filepath.Join(tmpDir, "gateway.db")
	gatewayDB, err := createTestGatewayDB(gatewayPath)
	require.NoError(t, err)
	defer gatewayDB.Close()

	sdb, err := NewSearchDB(searchPath, gatewayPath, gatewayDB)
	require.NoError(t, err)
	defer sdb.Close()

	// Create indexer and index beads
	indexer := NewBeadsIndexer(sdb.DB(), beadsDir)
	ctx := context.Background()

	err = indexer.IndexBeads(ctx)
	require.NoError(t, err)

	// Verify count
	count, err := indexer.GetIndexedCount()
	require.NoError(t, err)
	assert.Equal(t, 3, count)
}

func TestBeadsIndexerSearchBeads(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	require.NoError(t, os.MkdirAll(beadsDir, 0755))

	// Create test issues.jsonl
	issuesFile := filepath.Join(beadsDir, "issues.jsonl")
	jsonl := `{"id":"test-1","title":"Implement feature X","description":"Add new feature","status":"open","issue_type":"feature"}
{"id":"test-2","title":"Fix bug Y","status":"done","issue_type":"bug"}
{"id":"test-3","title":"Write tests for feature","status":"in_progress","issue_type":"task"}`
	require.NoError(t, os.WriteFile(issuesFile, []byte(jsonl), 0644))

	// Setup database and indexer
	searchPath := filepath.Join(tmpDir, "search.db")
	gatewayPath := filepath.Join(tmpDir, "gateway.db")
	gatewayDB, err := createTestGatewayDB(gatewayPath)
	require.NoError(t, err)
	defer gatewayDB.Close()

	sdb, err := NewSearchDB(searchPath, gatewayPath, gatewayDB)
	require.NoError(t, err)
	defer sdb.Close()

	indexer := NewBeadsIndexer(sdb.DB(), beadsDir)
	ctx := context.Background()
	require.NoError(t, indexer.IndexBeads(ctx))

	// Test search
	results, err := indexer.SearchBeads(ctx, "feature", 10, "")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(results), 1)

	// First result should contain "feature"
	foundFeature := false
	for _, r := range results {
		if r.Title == "Implement feature X" || r.Title == "Write tests for feature" {
			foundFeature = true
			break
		}
	}
	assert.True(t, foundFeature, "Expected to find issue with 'feature' in title")
}

func TestBeadsIndexerSearchWithStatusFilter(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	require.NoError(t, os.MkdirAll(beadsDir, 0755))

	// Create test issues.jsonl with different statuses
	issuesFile := filepath.Join(beadsDir, "issues.jsonl")
	jsonl := `{"id":"test-1","title":"Open task","status":"open","issue_type":"task"}
{"id":"test-2","title":"Done task","status":"done","issue_type":"task"}
{"id":"test-3","title":"Another open task","status":"open","issue_type":"task"}`
	require.NoError(t, os.WriteFile(issuesFile, []byte(jsonl), 0644))

	// Setup
	searchPath := filepath.Join(tmpDir, "search.db")
	gatewayPath := filepath.Join(tmpDir, "gateway.db")
	gatewayDB, err := createTestGatewayDB(gatewayPath)
	require.NoError(t, err)
	defer gatewayDB.Close()

	sdb, err := NewSearchDB(searchPath, gatewayPath, gatewayDB)
	require.NoError(t, err)
	defer sdb.Close()

	indexer := NewBeadsIndexer(sdb.DB(), beadsDir)
	ctx := context.Background()
	require.NoError(t, indexer.IndexBeads(ctx))

	// Search for "task" with open status filter
	results, err := indexer.SearchBeads(ctx, "task", 10, "open")
	require.NoError(t, err)

	// All results should have status "open"
	for _, r := range results {
		assert.Equal(t, "open", r.Status)
	}
}

func TestBeadsIndexerSkipsUnchangedFile(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	require.NoError(t, os.MkdirAll(beadsDir, 0755))

	// Create test issues.jsonl
	issuesFile := filepath.Join(beadsDir, "issues.jsonl")
	jsonl := `{"id":"test-1","title":"Test issue","status":"open","issue_type":"task"}`
	require.NoError(t, os.WriteFile(issuesFile, []byte(jsonl), 0644))

	// Setup
	searchPath := filepath.Join(tmpDir, "search.db")
	gatewayPath := filepath.Join(tmpDir, "gateway.db")
	gatewayDB, err := createTestGatewayDB(gatewayPath)
	require.NoError(t, err)
	defer gatewayDB.Close()

	sdb, err := NewSearchDB(searchPath, gatewayPath, gatewayDB)
	require.NoError(t, err)
	defer sdb.Close()

	indexer := NewBeadsIndexer(sdb.DB(), beadsDir)
	ctx := context.Background()

	// First index
	require.NoError(t, indexer.IndexBeads(ctx))
	count1, _ := indexer.GetIndexedCount()

	// Second index - should skip since file unchanged
	require.NoError(t, indexer.IndexBeads(ctx))
	count2, _ := indexer.GetIndexedCount()

	assert.Equal(t, count1, count2)
}

func TestBeadsIndexerMissingDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	nonExistentDir := filepath.Join(tmpDir, "nonexistent", ".beads")

	// Setup database
	searchPath := filepath.Join(tmpDir, "search.db")
	gatewayPath := filepath.Join(tmpDir, "gateway.db")
	gatewayDB, err := createTestGatewayDB(gatewayPath)
	require.NoError(t, err)
	defer gatewayDB.Close()

	sdb, err := NewSearchDB(searchPath, gatewayPath, gatewayDB)
	require.NoError(t, err)
	defer sdb.Close()

	indexer := NewBeadsIndexer(sdb.DB(), nonExistentDir)
	ctx := context.Background()

	// Should not error on missing directory
	err = indexer.IndexBeads(ctx)
	assert.NoError(t, err)

	count, err := indexer.GetIndexedCount()
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestBuildBeadsFTSQuery(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "single word",
			input:    "test",
			expected: "test",
		},
		{
			name:     "multiple words",
			input:    "test feature",
			expected: "test OR feature",
		},
		{
			name:     "with special characters",
			input:    "test:feature (bug)",
			expected: "testfeature OR bug", // Special chars are stripped, not split
		},
		{
			name:     "empty query",
			input:    "",
			expected: "",
		},
		{
			name:     "only special characters",
			input:    ":()+-",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildBeadsFTSQuery(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
