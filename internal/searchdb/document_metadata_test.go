package searchdb

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"conduit/internal/fts"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bd-3o6h: frontmatter metadata indexed into search.db and used to pre-filter
// document search. These run the real search.db migrations, fts.Indexer and
// fts.Searcher together.

type metadataFixture struct {
	sdb       *SearchDB
	workspace string
	indexer   *fts.Indexer
	searcher  *fts.Searcher
}

func setupMetadataFixture(t *testing.T) *metadataFixture {
	t.Helper()
	dir := t.TempDir()
	gatewayPath := filepath.Join(dir, "gateway.db")
	gatewayDB, err := createTestGatewayDB(gatewayPath)
	require.NoError(t, err)
	t.Cleanup(func() { gatewayDB.Close() })

	sdb, err := NewSearchDB(filepath.Join(dir, "gateway.search.db"), gatewayPath, gatewayDB)
	require.NoError(t, err)
	t.Cleanup(func() { sdb.Close() })

	workspace := filepath.Join(dir, "workspace")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "memory"), 0o755))

	return &metadataFixture{
		sdb:       sdb,
		workspace: workspace,
		indexer:   fts.NewIndexer(sdb.DB(), workspace),
		searcher:  fts.NewSearcher(sdb.DB()),
	}
}

func (f *metadataFixture) write(t *testing.T, rel, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(f.workspace, rel), []byte(content), 0o644))
}

func (f *metadataFixture) index(t *testing.T) {
	t.Helper()
	require.NoError(t, f.indexer.IndexWorkspace(context.Background()))
}

// writeCorpus writes a small synthetic memory corpus; every file mentions
// "widget" so a single query hits all of them.
func (f *metadataFixture) writeCorpus(t *testing.T) {
	f.write(t, "memory/a.md", "---\ndate: 2026-01-05\ntags: [alpha, cron]\npeople: [Rex]\nprojects: [projects.alpha]\ncategory: Ops\nsummary: Alpha widget rollout\n---\n# A\n\nThe widget rollout for alpha.\n")
	f.write(t, "memory/b.md", "---\ndate: 2026-02-10\ntags: [beta]\npeople: [Acme, Rex]\ncategory: notes\nsummary: Beta widget notes\n---\n# B\n\nNotes about the beta widget.\n")
	f.write(t, "memory/c.md", "---\ndate: someday\ntags: [alpha]\ncategory: Ops\nsummary: Undated widget idea\n---\n# C\n\nA widget idea with no real date.\n")
	f.write(t, "memory/plain.md", "# Plain\n\nA widget mentioned in a file without frontmatter.\n")
	f.write(t, "memory/broken.md", "---\ntags: [alpha\n---\n# Broken\n\nA widget in a file with malformed frontmatter.\n")
}

func docPaths(results []fts.DocumentResult) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range results {
		if !seen[r.FilePath] {
			seen[r.FilePath] = true
			out = append(out, r.FilePath)
		}
	}
	sort.Strings(out)
	return out
}

func TestDocumentMetadata_MigrationCreatesTable(t *testing.T) {
	f := setupMetadataFixture(t)
	var n int
	require.NoError(t, f.sdb.DB().QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='document_metadata'`).Scan(&n))
	assert.Equal(t, 1, n)
}

func TestDocumentMetadata_IndexStoresRowPerFile(t *testing.T) {
	f := setupMetadataFixture(t)
	f.writeCorpus(t)
	f.index(t)

	type row struct {
		has      bool
		date     string
		tags     string
		category string
	}
	rows := map[string]row{}
	rs, err := f.sdb.DB().Query(`SELECT path, has_frontmatter, date, tags, category FROM document_metadata`)
	require.NoError(t, err)
	defer rs.Close()
	for rs.Next() {
		var p string
		var r row
		require.NoError(t, rs.Scan(&p, &r.has, &r.date, &r.tags, &r.category))
		rows[p] = r
	}
	require.NoError(t, rs.Err())

	assert.Len(t, rows, 5, "every indexed file has exactly one metadata row")
	assert.Equal(t, row{true, "2026-01-05", `["alpha","cron"]`, "Ops"}, rows["memory/a.md"])
	assert.Equal(t, row{true, "someday", `["alpha"]`, "Ops"}, rows["memory/c.md"])
	assert.Equal(t, row{false, "", "[]", ""}, rows["memory/plain.md"])
	assert.Equal(t, row{false, "", "[]", ""}, rows["memory/broken.md"], "malformed frontmatter is treated as none")
}

func TestDocumentMetadata_BodyChunkedWithoutFrontmatter(t *testing.T) {
	f := setupMetadataFixture(t)
	f.writeCorpus(t)
	f.index(t)
	ctx := context.Background()

	// Frontmatter-only text is not in the FTS index...
	res, err := f.searcher.SearchDocuments(ctx, "rollout", 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"memory/a.md"}, docPaths(res), "body text still indexed")
	res, err = f.searcher.SearchDocuments(ctx, "Undated", 10)
	require.NoError(t, err)
	assert.Empty(t, res, "summary lives in metadata, not chunks")

	// ...but malformed frontmatter is kept as content, like any other text.
	res, err = f.searcher.SearchDocuments(ctx, "malformed", 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"memory/broken.md"}, docPaths(res))
}

func TestDocumentMetadata_FilesWithoutFrontmatterStillSearchable(t *testing.T) {
	f := setupMetadataFixture(t)
	f.writeCorpus(t)
	f.index(t)

	res, err := f.searcher.SearchDocuments(context.Background(), "widget", 50)
	require.NoError(t, err)
	assert.Equal(t,
		[]string{"memory/a.md", "memory/b.md", "memory/broken.md", "memory/c.md", "memory/plain.md"},
		docPaths(res))
}

func TestDocumentMetadata_FilteredSearch(t *testing.T) {
	f := setupMetadataFixture(t)
	f.writeCorpus(t)
	f.index(t)
	ctx := context.Background()

	tests := []struct {
		name   string
		filter fts.DocumentFilter
		want   []string
	}{
		{"single tag", fts.DocumentFilter{Tags: []string{"beta"}}, []string{"memory/b.md"}},
		{"tags OR within field", fts.DocumentFilter{Tags: []string{"cron", "beta"}}, []string{"memory/a.md", "memory/b.md"}},
		{"tag case-insensitive", fts.DocumentFilter{Tags: []string{"ALPHA"}}, []string{"memory/a.md", "memory/c.md"}},
		{"people", fts.DocumentFilter{People: []string{"rex"}}, []string{"memory/a.md", "memory/b.md"}},
		{"projects", fts.DocumentFilter{Projects: []string{"projects.alpha"}}, []string{"memory/a.md"}},
		{"category case-insensitive", fts.DocumentFilter{Category: "ops"}, []string{"memory/a.md", "memory/c.md"}},
		{"AND across fields", fts.DocumentFilter{Tags: []string{"alpha"}, People: []string{"Rex"}}, []string{"memory/a.md"}},
		{"AND across fields no match", fts.DocumentFilter{Tags: []string{"beta"}, Category: "Ops"}, nil},
		{"date_after inclusive", fts.DocumentFilter{DateAfter: "2026-02-10"}, []string{"memory/b.md"}},
		{"date_before inclusive", fts.DocumentFilter{DateBefore: "2026-01-05"}, []string{"memory/a.md"}},
		{"date range", fts.DocumentFilter{DateAfter: "2026-01-01", DateBefore: "2026-12-31"}, []string{"memory/a.md", "memory/b.md"}},
		{"unparseable date never matches", fts.DocumentFilter{Category: "Ops", DateAfter: "2000-01-01"}, []string{"memory/a.md"}},
		{"unknown tag", fts.DocumentFilter{Tags: []string{"nope"}}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := f.searcher.SearchDocumentsFiltered(ctx, "widget", 50, tc.filter)
			require.NoError(t, err)
			assert.Equal(t, tc.want, docPaths(res))

			paths, err := f.searcher.MatchingDocumentPaths(ctx, tc.filter)
			require.NoError(t, err)
			assert.Equal(t, tc.want, paths, "path pre-filter agrees with filtered search")
		})
	}
}

func TestDocumentMetadata_EmptyFilterMatchesUnfiltered(t *testing.T) {
	f := setupMetadataFixture(t)
	f.writeCorpus(t)
	f.index(t)
	ctx := context.Background()

	plain, err := f.searcher.SearchDocuments(ctx, "widget", 3)
	require.NoError(t, err)
	for _, filter := range []fts.DocumentFilter{{}, {Tags: []string{""}, Category: "  "}} {
		got, err := f.searcher.SearchDocumentsFiltered(ctx, "widget", 3, filter)
		require.NoError(t, err)
		assert.Equal(t, plain, got)
	}
}

func TestDocumentMetadata_ListSummaries(t *testing.T) {
	f := setupMetadataFixture(t)
	f.writeCorpus(t)
	f.index(t)
	ctx := context.Background()

	all, err := f.searcher.ListDocumentMetadata(ctx, fts.DocumentFilter{}, 100)
	require.NoError(t, err)
	var paths []string
	for _, m := range all {
		paths = append(paths, m.Path)
	}
	// Only files with frontmatter; newest date first, undated/unparseable last.
	assert.Equal(t, []string{"memory/b.md", "memory/a.md", "memory/c.md"}, paths)
	assert.Equal(t, fts.DocumentMetadata{
		Path: "memory/a.md",
		Frontmatter: fts.Frontmatter{
			Date: "2026-01-05", Tags: []string{"alpha", "cron"}, People: []string{"Rex"},
			Projects: []string{"projects.alpha"}, Category: "Ops", Summary: "Alpha widget rollout",
		},
	}, all[1])

	limited, err := f.searcher.ListDocumentMetadata(ctx, fts.DocumentFilter{Tags: []string{"alpha"}}, 1)
	require.NoError(t, err)
	require.Len(t, limited, 1)
	assert.Equal(t, "memory/a.md", limited[0].Path)
}

func TestDocumentMetadata_ReindexOnChangeAndRemoval(t *testing.T) {
	f := setupMetadataFixture(t)
	ctx := context.Background()
	f.write(t, "memory/a.md", "---\ntags: [alpha]\n---\nwidget body\n")
	f.write(t, "memory/gone.md", "---\ntags: [alpha]\n---\nwidget gone\n")
	f.index(t)

	f.write(t, "memory/a.md", "---\ntags: [beta]\n---\nwidget body\n")
	require.NoError(t, os.Remove(filepath.Join(f.workspace, "memory/gone.md")))
	f.index(t)

	paths, err := f.searcher.MatchingDocumentPaths(ctx, fts.DocumentFilter{Tags: []string{"alpha"}})
	require.NoError(t, err)
	assert.Empty(t, paths, "changed tags and removed files are reflected")
	paths, err = f.searcher.MatchingDocumentPaths(ctx, fts.DocumentFilter{Tags: []string{"beta"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"memory/a.md"}, paths)

	require.NoError(t, f.indexer.RemoveFile(ctx, "memory/a.md"))
	paths, err = f.searcher.MatchingDocumentPaths(ctx, fts.DocumentFilter{})
	require.NoError(t, err)
	assert.Empty(t, paths, "RemoveFile drops the metadata row")
}

func TestDocumentMetadata_LegacyChunksBackfilled(t *testing.T) {
	f := setupMetadataFixture(t)
	ctx := context.Background()
	f.write(t, "memory/a.md", "---\ntags: [alpha]\n---\nwidget body\n")
	f.index(t)

	// Simulate a file indexed before bd-3o6h: chunks present, no metadata.
	_, err := f.sdb.DB().Exec(`DELETE FROM document_metadata`)
	require.NoError(t, err)
	f.index(t)

	paths, err := f.searcher.MatchingDocumentPaths(ctx, fts.DocumentFilter{Tags: []string{"alpha"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"memory/a.md"}, paths, "unchanged file without metadata row is re-indexed")
}

func TestDocumentMetadata_FrontmatterOnlyFileNotReindexed(t *testing.T) {
	f := setupMetadataFixture(t)
	f.write(t, "memory/meta.md", "---\nsummary: only metadata\n---\n")
	f.index(t)

	// A sentinel survives the second pass only if the file was skipped.
	_, err := f.sdb.DB().Exec(`UPDATE document_metadata SET summary = 'sentinel'`)
	require.NoError(t, err)
	f.index(t)

	var summary string
	require.NoError(t, f.sdb.DB().QueryRow(
		`SELECT summary FROM document_metadata WHERE path = 'memory/meta.md'`).Scan(&summary))
	assert.Equal(t, "sentinel", summary, "unchanged file with zero chunks is skipped by hash")
}
