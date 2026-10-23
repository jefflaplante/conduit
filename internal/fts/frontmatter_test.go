package fts

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseFrontmatter_Valid(t *testing.T) {
	content := "---\n" +
		"date: 2026-01-01\n" +
		"tags: [alpha, Beta, alpha]\n" +
		"people:\n  - Rex\n  - Acme\n" +
		"projects: projects.alpha, projects.beta\n" +
		"category: Notes\n" +
		"summary: >\n  Weekly sync\n  recap.\n" +
		"unknown: ignored\n" +
		"---\n" +
		"# Heading\n\nBody text.\n"

	fm, body, ok := ParseFrontmatter(content)
	assert.True(t, ok)
	assert.Equal(t, "# Heading\n\nBody text.\n", body)
	assert.Equal(t, Frontmatter{
		Date:     "2026-01-01",
		Tags:     []string{"alpha", "Beta"}, // case-insensitive dedupe keeps first spelling
		People:   []string{"Rex", "Acme"},
		Projects: []string{"projects.alpha", "projects.beta"},
		Category: "Notes",
		Summary:  "Weekly sync recap.",
	}, fm)
}

func TestParseFrontmatter_DateForms(t *testing.T) {
	tests := []struct {
		name, yaml, want string
	}{
		{"unquoted timestamp", "date: 2026-01-01", "2026-01-01"},
		{"quoted", `date: "2026-01-02"`, "2026-01-02"},
		{"rfc3339", "date: 2026-01-03T10:00:00Z", "2026-01-03"},
		{"datetime with space", `date: "2026-01-04 09:30"`, "2026-01-04"},
		{"unparseable kept raw", "date: last tuesday", "last tuesday"},
		{"missing", "category: x", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fm, _, ok := ParseFrontmatter("---\n" + tc.yaml + "\n---\nbody\n")
			assert.True(t, ok)
			assert.Equal(t, tc.want, fm.Date)
		})
	}
}

func TestParseFrontmatter_CRLFAndDotsTerminator(t *testing.T) {
	fm, body, ok := ParseFrontmatter("---\r\ncategory: notes\r\n...\r\nbody\r\n")
	assert.True(t, ok)
	assert.Equal(t, "notes", fm.Category)
	assert.Equal(t, "body\r\n", body)
}

func TestParseFrontmatter_EmptyBlock(t *testing.T) {
	fm, body, ok := ParseFrontmatter("---\n---\nbody\n")
	assert.True(t, ok)
	assert.Equal(t, Frontmatter{}, fm)
	assert.Equal(t, "body\n", body)
}

func TestParseFrontmatter_Missing(t *testing.T) {
	for _, content := range []string{
		"",
		"# Heading\n\nNo frontmatter.\n",
		"---",
		"text\n---\ncategory: x\n---\n",
	} {
		fm, body, ok := ParseFrontmatter(content)
		assert.False(t, ok, "content %q", content)
		assert.Equal(t, content, body)
		assert.Equal(t, Frontmatter{}, fm)
	}
}

func TestParseFrontmatter_Malformed(t *testing.T) {
	tests := map[string]string{
		"unclosed":         "---\ncategory: x\n\n# Heading\n",
		"invalid yaml":     "---\ntags: [a, b\n---\nbody\n",
		"not a mapping":    "---\njust a sentence\n---\nbody\n",
		"list not mapping": "---\n- a\n- b\n---\nbody\n",
		"tags is mapping":  "---\ntags:\n  a: b\n---\nbody\n",
		"nested list item": "---\ntags:\n  - [a]\n---\nbody\n",
		"summary is list":  "---\nsummary: [a, b]\n---\nbody\n",
		"date is list":     "---\ndate: [2026-01-01]\n---\nbody\n",
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			fm, body, ok := ParseFrontmatter(content)
			assert.False(t, ok)
			assert.Equal(t, content, body, "malformed frontmatter must leave content intact")
			assert.Equal(t, Frontmatter{}, fm)
		})
	}
}

// The gateway.db fallback has no document_metadata table: indexing still
// works (body only, no metadata) and is skipped by chunk hash as before.
func TestIndexer_WithoutMetadataTable(t *testing.T) {
	db := setupTestDB(t)
	workspaceDir := t.TempDir()
	writeFile(t, workspaceDir, "note.md", "---\nsummary: secretword\n---\n# Note\n\nwidget body\n")

	idx := NewIndexer(db, workspaceDir)
	for i := 0; i < 2; i++ {
		if err := idx.IndexWorkspace(context.Background()); err != nil {
			t.Fatalf("IndexWorkspace pass %d: %v", i, err)
		}
	}

	s := NewSearcher(db)
	res, err := s.SearchDocuments(context.Background(), "widget", 10)
	assert.NoError(t, err)
	assert.Len(t, res, 1)
	res, err = s.SearchDocuments(context.Background(), "secretword", 10)
	assert.NoError(t, err)
	assert.Empty(t, res, "frontmatter is not chunked")

	_, err = s.MatchingDocumentPaths(context.Background(), DocumentFilter{Tags: []string{"x"}})
	assert.Error(t, err, "metadata queries fail without the table")
}

func TestDocumentFilter_NormalizeAndIsEmpty(t *testing.T) {
	assert.True(t, DocumentFilter{}.IsEmpty())
	assert.True(t, DocumentFilter{Tags: []string{"", "  "}, Category: " "}.IsEmpty())
	assert.False(t, DocumentFilter{People: []string{"Rex"}}.IsEmpty())
	assert.False(t, DocumentFilter{DateBefore: "2026-01-01"}.IsEmpty())

	f := DocumentFilter{
		Tags:      []string{" alpha ", "ALPHA", "beta"},
		Category:  " notes ",
		DateAfter: "2026-01-02T00:00:00Z",
	}.Normalize()
	assert.Equal(t, []string{"alpha", "beta"}, f.Tags)
	assert.Equal(t, "notes", f.Category)
	assert.Equal(t, "2026-01-02", f.DateAfter)
}

func TestDocumentFilter_Validate(t *testing.T) {
	assert.NoError(t, DocumentFilter{}.Validate())
	assert.NoError(t, DocumentFilter{DateAfter: "2026-01-01", DateBefore: "2026-12-31"}.Validate())
	assert.ErrorContains(t, DocumentFilter{DateAfter: "yesterday"}.Validate(), "date_after")
	assert.ErrorContains(t, DocumentFilter{DateBefore: "2026/01/01"}.Validate(), "date_before")
}
