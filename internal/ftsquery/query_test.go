package ftsquery

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// newFTS creates an in-memory FTS5 table with the same tokenizer every
// Conduit FTS table uses and loads docs (id -> text).
func newFTS(t *testing.T, docs map[string]string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`CREATE VIRTUAL TABLE docs USING fts5(id, body, tokenize='porter unicode61')`)
	require.NoError(t, err)
	for id, body := range docs {
		_, err := db.Exec(`INSERT INTO docs(id, body) VALUES (?, ?)`, id, body)
		require.NoError(t, err)
	}
	return db
}

func match(t *testing.T, db *sql.DB, expr string) ([]string, error) {
	t.Helper()
	rows, err := db.Query(`SELECT id FROM docs WHERE docs MATCH ? ORDER BY rank`, expr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

var corpus = map[string]string{
	"bead3dru":  "conduit-3dru: Fix brain recall tokenizer",
	"beadother": "conduit-9xyz: unrelated conduit work item",
	"birthday":  "jeff.birthday = March 3",
	"email":     "contact e@x.com for access",
	"path":      "config lives in foo/bar/baz.json",
	"percent":   "disk usage at 100% on the NAS",
	"csharp":    "rewrite the parser in c# someday",
	"dont":      "I don't like cilantro",
	"url":       "see https://example.com/docs?q=1 for details",
	"helm":      "deploy with helm and kustomize overlays",
	"solar":     "solar panel count is 30",
	"quote":     `he said "hello world" loudly`,
	"near":      "the store is near the park",
	"colon":     "meeting at 10:30 in title room",
}

// conduit-31jg.31: every one of these used to fail with "fts5: syntax error"
// (or silently match nothing). Each must parse and return the expected hit
// first.
func TestBuild_NastyQueriesAgainstRealFTS5(t *testing.T) {
	db := newFTS(t, corpus)
	tests := []struct {
		query string
		first string // expected top hit; "" = only require no error
	}{
		{"conduit-3dru", "bead3dru"},
		{"CONDUIT-3DRU", "bead3dru"},
		{"jeff.birthday", "birthday"},
		{"e@x.com", "email"},
		{"foo/bar", "path"},
		{"foo/bar/baz.json", "path"},
		{"100%", "percent"},
		{"c#", "csharp"},
		{"don't", "dont"},
		{"https://example.com/docs?q=1", "url"},
		{"helm/kustomize", "helm"},
		{`"hello world"`, "quote"},
		{`say "hello`, "quote"},
		{"10:30", "colon"},
		{"title:room", "colon"},
		{"near", "near"},
		{"NEAR/3 park", "near"},
		{"solar AND panel", "solar"},
		{"NOT solar", "solar"},
		{"solar*", "solar"},
		{"^solar", "solar"},
		{"(solar OR panel)", "solar"},
		{"{body}: solar", "solar"},
		{"'; DROP TABLE docs; --", ""},
		{"what is the solar panel count", "solar"},
		{"#", ""},
		{"%%%", ""},
		{"\x00solar", "solar"},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			expr := Build(tt.query)
			if expr == "" {
				assert.Empty(t, tt.first, "query unexpectedly produced no terms")
				return
			}
			ids, err := match(t, db, expr)
			require.NoError(t, err, "MATCH %q", expr)
			if tt.first != "" {
				require.NotEmpty(t, ids, "MATCH %q found nothing", expr)
				assert.Equal(t, tt.first, ids[0], "MATCH %q ranked %v", expr, ids)
			}
		})
	}

	// Column-scoped expressions parse too and apply to every phrase.
	ids, err := match(t, db, Column("body", Build("solar panel")))
	require.NoError(t, err)
	assert.Equal(t, []string{"solar"}, ids)
	ids, err = match(t, db, Column("id", Build("solar panel")))
	require.NoError(t, err)
	assert.Equal(t, []string{"solar"}, ids, "id column holds 'solar' too")
	ids, err = match(t, db, Column("id", Build("panel")))
	require.NoError(t, err)
	assert.Empty(t, ids, "column filter must scope the whole OR list")
}

// conduit-179p semantics survive: multi-word natural-language queries use
// OR (not AND), stopwords are stripped, compound tokens are split.
func TestBuild_Preserves179pSemantics(t *testing.T) {
	assert.Equal(t, `"bourbon" OR "jeff" OR "drink"`, Build("what bourbon does Jeff drink"))
	assert.Equal(t, `"helm/kustomize" OR "helm" OR "kustomize"`, Build("helm/kustomize"))
	assert.Equal(t, `"what" OR "is" OR "it"`, Build("what is it"), "all-stopword fallback")
	assert.Equal(t, `"my_project" OR "project"`, Build("my_project"))
	assert.Equal(t, "", Build("   "))

	db := newFTS(t, corpus)
	// OR: a doc matching only some terms is still found.
	ids, err := match(t, db, Build("what does the solar inverter say"))
	require.NoError(t, err)
	assert.Contains(t, ids, "solar")
	// Delimiter split: each half can match on its own.
	ids, err = match(t, db, Build("kustomize/argo"))
	require.NoError(t, err)
	assert.Equal(t, []string{"helm"}, ids)
}

func TestQuote(t *testing.T) {
	assert.Equal(t, `"abc"`, Quote("abc"))
	assert.Equal(t, `"a""b"`, Quote(`a"b`))
	assert.Equal(t, `""""`, Quote(`"`))
}

func TestTerms_Limits(t *testing.T) {
	var words []string
	for i := 0; i < MaxTerms+30; i++ {
		words = append(words, "w"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+string(rune('a'+(i/26)%26)))
	}
	assert.LessOrEqual(t, len(Terms(strings.Join(words, " "))), MaxTerms)

	long := strings.Repeat("é", MaxTermBytes) // 2 bytes per rune
	terms := Terms(long)
	require.Len(t, terms, 1)
	assert.LessOrEqual(t, len(terms[0]), MaxTermBytes)
	assert.True(t, strings.HasPrefix(long, terms[0]), "must not split a UTF-8 sequence")

	assert.NotContains(t, Build("hello\x00world"), "\x00")
}

// FuzzBuild: any input must yield an expression FTS5 accepts.
func FuzzBuild(f *testing.F) {
	for _, s := range []string{"hello world", `"a" AND "b"`, "NEAR/3 title:x ^y * (z) {w}", "\x00", "é\"'%#@./-_,", ""} {
		f.Add(s)
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		f.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE VIRTUAL TABLE docs USING fts5(id, body, tokenize='porter unicode61')`); err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, in string) {
		expr := Build(in)
		if expr == "" {
			return
		}
		rows, err := db.Query(`SELECT id FROM docs WHERE docs MATCH ?`, Column("body", expr))
		if err != nil {
			t.Fatalf("Build(%q) = %q rejected by FTS5: %v", in, expr, err)
		}
		rows.Close()
	})
}
