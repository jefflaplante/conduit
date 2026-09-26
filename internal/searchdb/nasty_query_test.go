package searchdb

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// conduit-31jg.31: bead IDs used to be mangled to "conduit3dru" (no match);
// punctuation used to raise "fts5: syntax error".
func TestBeadsIndexer_NastyQueries(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	require.NoError(t, os.MkdirAll(beadsDir, 0755))
	jsonl := `{"id":"conduit-3dru","title":"Fix brain recall tokenizer","description":"stopwords, OR logic","status":"closed","issue_type":"bug"}
{"id":"conduit-9xyz","title":"Unrelated conduit work","description":"gardening","status":"open","issue_type":"task"}
{"id":"conduit-31jg.31","title":"FTS5 MATCH syntax errors","description":"queries like e@x.com and don't fail","status":"open","issue_type":"bug"}`
	require.NoError(t, os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(jsonl), 0644))

	gatewayPath := filepath.Join(tmpDir, "gateway.db")
	gatewayDB, err := createTestGatewayDB(gatewayPath)
	require.NoError(t, err)
	defer gatewayDB.Close()
	sdb, err := NewSearchDB(filepath.Join(tmpDir, "search.db"), gatewayPath, gatewayDB)
	require.NoError(t, err)
	defer sdb.Close()

	idx := NewBeadsIndexer(sdb.DB(), beadsDir)
	ctx := context.Background()
	require.NoError(t, idx.IndexBeads(ctx))

	for q, want := range map[string]string{
		"conduit-3dru":    "conduit-3dru",
		"conduit-31jg.31": "conduit-31jg.31",
		"e@x.com":         "conduit-31jg.31",
		"don't":           "conduit-31jg.31",
	} {
		res, err := idx.SearchBeads(ctx, q, 10, "")
		require.NoError(t, err, q)
		require.NotEmpty(t, res, q)
		assert.Equal(t, want, res[0].IssueID, "query %q", q)
	}
}

func TestBrainIndexer_NastyQueries(t *testing.T) {
	idx, brainDB, cleanup := setupBrainIndexerTest(t)
	defer cleanup()
	for k, v := range map[string]string{
		"jeff.birthday":     "March 3",
		"contact.email":     "reach Jeff at e@x.com",
		"food.dislikes":     "Jeff doesn't like cilantro",
		"task.conduit-3dru": "recall tokenizer bead",
		"garden.plan":       "tomatoes and basil",
	} {
		_, err := brainDB.Exec(`INSERT INTO brain_ltm (key, value) VALUES (?, ?)`, k, v)
		require.NoError(t, err)
	}
	ctx := context.Background()
	require.NoError(t, idx.IndexBrain(ctx))

	for q, want := range map[string]string{
		"jeff.birthday":         "jeff.birthday",
		"e@x.com":               "contact.email",
		"doesn't":               "food.dislikes",
		"conduit-3dru":          "task.conduit-3dru",
		"when is jeff birthday": "jeff.birthday", // 179p: stopwords + OR
		"100%":                  "",
		"c#":                    "",
	} {
		res, err := idx.SearchBrain(ctx, q, 10)
		require.NoError(t, err, q)
		if want == "" {
			continue
		}
		require.NotEmpty(t, res, q)
		assert.Equal(t, want, res[0].Key, "query %q", q)
	}
}
