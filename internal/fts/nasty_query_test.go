package fts

import (
	"context"
	"testing"
)

// conduit-31jg.31: ordinary queries with punctuation must not produce
// "fts5: syntax error" and must find the obvious hit, across every Searcher
// method (documents, messages, beads).
func TestSearcher_NastyQueries(t *testing.T) {
	db := setupTestDB(t)
	if _, err := db.Exec(`CREATE VIRTUAL TABLE beads_fts USING fts5(
		issue_id, title, description, status, issue_type, owner, tokenize='porter unicode61')`); err != nil {
		t.Fatal(err)
	}
	docs := []struct{ path, content string }{
		{"a.md", "jeff.birthday is March 3"},
		{"b.md", "email e@x.com for access"},
		{"c.md", "config in foo/bar/baz.json"},
		{"d.md", "disk at 100% full"},
		{"e.md", "rewrite it in c# maybe"},
		{"f.md", "I don't like cilantro"},
		{"g.md", "unrelated filler text about gardening"},
	}
	for i, d := range docs {
		if _, err := db.Exec(`INSERT INTO document_chunks(file_path, heading, chunk_index, content, file_hash) VALUES (?, '', ?, ?, 'h')`,
			d.path, i, d.content); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO messages_fts(message_id, session_key, role, content) VALUES (?, 's', 'user', ?)`,
			d.path, d.content); err != nil {
			t.Fatal(err)
		}
	}
	for _, b := range [][3]string{
		{"conduit-3dru", "Fix brain recall tokenizer", "stopwords and OR logic"},
		{"conduit-9xyz", "Unrelated work", "gardening"},
	} {
		if _, err := db.Exec(`INSERT INTO beads_fts(issue_id, title, description, status, issue_type, owner) VALUES (?, ?, ?, 'open', 'bug', 'jules')`,
			b[0], b[1], b[2]); err != nil {
			t.Fatal(err)
		}
	}

	s := NewSearcher(db)
	ctx := context.Background()
	cases := map[string]string{
		"jeff.birthday": "a.md",
		"e@x.com":       "b.md",
		"foo/bar":       "c.md",
		"100%":          "d.md",
		"c#":            "e.md",
		"don't":         "f.md",
	}
	for q, want := range cases {
		dr, err := s.SearchDocuments(ctx, q, 5)
		if err != nil {
			t.Errorf("SearchDocuments(%q): %v", q, err)
		} else if len(dr) == 0 || dr[0].FilePath != want {
			t.Errorf("SearchDocuments(%q) = %+v, want top %s", q, dr, want)
		}
		mr, err := s.SearchMessages(ctx, q, 5)
		if err != nil {
			t.Errorf("SearchMessages(%q): %v", q, err)
		} else if len(mr) == 0 || mr[0].MessageID != want {
			t.Errorf("SearchMessages(%q) = %+v, want top %s", q, mr, want)
		}
	}

	br, err := s.SearchBeads(ctx, "conduit-3dru", 5, "")
	if err != nil {
		t.Fatalf("SearchBeads: %v", err)
	}
	if len(br) == 0 || br[0].IssueID != "conduit-3dru" {
		t.Fatalf("SearchBeads(conduit-3dru) = %+v, want conduit-3dru first", br)
	}

	// Messages: the content: column filter must scope every OR'd term, not
	// just the first ("user" is every row's role, not message content).
	mr, err := s.SearchMessages(ctx, "gardening user", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(mr) != 1 || mr[0].MessageID != "g.md" {
		t.Fatalf("SearchMessages(gardening user) = %+v, want only g.md", mr)
	}
}
