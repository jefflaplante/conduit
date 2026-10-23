package fts

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// DocumentMetadata is one row of document_metadata: a file's parsed
// frontmatter keyed by workspace-relative path (bd-3o6h).
type DocumentMetadata struct {
	Path string `json:"path"`
	Frontmatter
}

// DocumentFilter narrows document search to files whose frontmatter
// matches. Fields AND together; values within one list field OR together.
// String comparisons are case-insensitive. DateAfter/DateBefore are
// inclusive YYYY-MM-DD bounds; files whose date is missing or unparseable
// never match a date bound.
type DocumentFilter struct {
	Tags       []string `json:"tags,omitempty"`
	People     []string `json:"people,omitempty"`
	Projects   []string `json:"projects,omitempty"`
	Category   string   `json:"category,omitempty"`
	DateAfter  string   `json:"date_after,omitempty"`
	DateBefore string   `json:"date_before,omitempty"`
}

// Normalize trims all values and drops empty list entries, so a filter
// built from empty arguments reports IsEmpty.
func (f DocumentFilter) Normalize() DocumentFilter {
	return DocumentFilter{
		Tags:       cleanStringList(f.Tags),
		People:     cleanStringList(f.People),
		Projects:   cleanStringList(f.Projects),
		Category:   strings.TrimSpace(f.Category),
		DateAfter:  normalizeBound(f.DateAfter),
		DateBefore: normalizeBound(f.DateBefore),
	}
}

func normalizeBound(s string) string {
	d, _ := NormalizeDate(s)
	return d
}

// Validate reports a date bound that is not a recognizable date.
func (f DocumentFilter) Validate() error {
	bounds := []struct{ name, value string }{
		{"date_after", f.DateAfter},
		{"date_before", f.DateBefore},
	}
	for _, b := range bounds {
		if strings.TrimSpace(b.value) == "" {
			continue
		}
		if _, ok := NormalizeDate(b.value); !ok {
			return fmt.Errorf("%s must be a YYYY-MM-DD date, got %q", b.name, b.value)
		}
	}
	return nil
}

// IsEmpty reports whether the filter constrains nothing.
func (f DocumentFilter) IsEmpty() bool {
	f = f.Normalize()
	return len(f.Tags) == 0 && len(f.People) == 0 && len(f.Projects) == 0 &&
		f.Category == "" && f.DateAfter == "" && f.DateBefore == ""
}

// datePattern matches the canonical stored YYYY-MM-DD form.
const datePattern = "[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]"

// whereSQL renders the filter as a SQL condition over document_metadata
// aliased as dm, with its bind arguments.
func (f DocumentFilter) whereSQL() (string, []interface{}) {
	f = f.Normalize()
	conds := []string{"1=1"}
	var args []interface{}

	listCond := func(column string, values []string) {
		if len(values) == 0 {
			return
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(values)), ",")
		conds = append(conds, fmt.Sprintf(
			"EXISTS (SELECT 1 FROM json_each(dm.%s) j WHERE lower(j.value) IN (%s))", column, placeholders))
		for _, v := range values {
			args = append(args, strings.ToLower(v))
		}
	}
	listCond("tags", f.Tags)
	listCond("people", f.People)
	listCond("projects", f.Projects)

	if f.Category != "" {
		conds = append(conds, "lower(dm.category) = ?")
		args = append(args, strings.ToLower(f.Category))
	}
	if f.DateAfter != "" {
		conds = append(conds, "dm.date GLOB '"+datePattern+"' AND dm.date >= ?")
		args = append(args, f.DateAfter)
	}
	if f.DateBefore != "" {
		conds = append(conds, "dm.date GLOB '"+datePattern+"' AND dm.date <= ?")
		args = append(args, f.DateBefore)
	}
	return strings.Join(conds, " AND "), args
}

// hasMetadataTable reports whether db has the document_metadata table. It is
// created by the search.db migrations only; the gateway.db fallback lacks it,
// and the indexer then skips metadata writes.
func hasMetadataTable(ctx context.Context, db *sql.DB) bool {
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'document_metadata'`).Scan(&n)
	return err == nil && n > 0
}

// metadataHash returns the file hash recorded with path's metadata row, or ""
// when there is none.
func metadataHash(ctx context.Context, db *sql.DB, path string) string {
	var hash string
	if err := db.QueryRowContext(ctx,
		`SELECT file_hash FROM document_metadata WHERE path = ?`, path).Scan(&hash); err != nil {
		return ""
	}
	return hash
}

// upsertMetadata writes path's metadata row. Files without frontmatter get a
// row with has_frontmatter = 0 and empty fields, so every indexed file has
// exactly one row.
func upsertMetadata(ctx context.Context, tx *sql.Tx, path, hash string, fm Frontmatter, hasFrontmatter bool) error {
	tags, people, projects := jsonList(fm.Tags), jsonList(fm.People), jsonList(fm.Projects)
	_, err := tx.ExecContext(ctx, `
		INSERT INTO document_metadata
			(path, has_frontmatter, date, tags, people, projects, category, summary, file_hash, indexed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(path) DO UPDATE SET
			has_frontmatter = excluded.has_frontmatter,
			date = excluded.date,
			tags = excluded.tags,
			people = excluded.people,
			projects = excluded.projects,
			category = excluded.category,
			summary = excluded.summary,
			file_hash = excluded.file_hash,
			indexed_at = excluded.indexed_at
	`, path, hasFrontmatter, fm.Date, tags, people, projects, fm.Category, fm.Summary, hash)
	return err
}

func jsonList(values []string) string {
	if len(values) == 0 {
		return "[]"
	}
	b, err := json.Marshal(values)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// SearchDocumentsFiltered is SearchDocuments restricted to files whose
// frontmatter matches filter; the path constraint is applied inside the
// MATCH query, before BM25 ranking and LIMIT. An empty filter is exactly
// SearchDocuments.
func (s *Searcher) SearchDocumentsFiltered(ctx context.Context, query string, limit int, filter DocumentFilter) ([]DocumentResult, error) {
	if filter.IsEmpty() {
		return s.SearchDocuments(ctx, query, limit)
	}
	if limit <= 0 {
		limit = 10
	}

	ftsQuery := buildFTSQuery(query)
	if ftsQuery == "" {
		return nil, nil
	}

	where, whereArgs := filter.whereSQL()
	args := append([]interface{}{ftsQuery}, whereArgs...)
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, `
		SELECT dc.file_path, dc.heading, dc.content, rank
		FROM document_chunks_fts fts
		JOIN document_chunks dc ON dc.id = fts.rowid
		WHERE document_chunks_fts MATCH ?
		  AND dc.file_path IN (SELECT dm.path FROM document_metadata dm WHERE `+where+`)
		ORDER BY rank
		LIMIT ?
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("filtered document search failed: %w", err)
	}
	defer rows.Close()

	var results []DocumentResult
	for rows.Next() {
		var r DocumentResult
		if err := rows.Scan(&r.FilePath, &r.Heading, &r.Content, &r.Rank); err != nil {
			return nil, fmt.Errorf("failed to scan document result: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// MatchingDocumentPaths returns the paths of all indexed files whose
// frontmatter matches filter, sorted. With an empty filter it returns every
// indexed path.
func (s *Searcher) MatchingDocumentPaths(ctx context.Context, filter DocumentFilter) ([]string, error) {
	where, args := filter.whereSQL()
	rows, err := s.db.QueryContext(ctx,
		`SELECT dm.path FROM document_metadata dm WHERE `+where+` ORDER BY dm.path`, args...)
	if err != nil {
		return nil, fmt.Errorf("document metadata query failed: %w", err)
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("failed to scan document path: %w", err)
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}

// ListDocumentMetadata returns the frontmatter of files that have it and
// match filter, newest date first (missing or unparseable dates last), then
// by path.
func (s *Searcher) ListDocumentMetadata(ctx context.Context, filter DocumentFilter, limit int) ([]DocumentMetadata, error) {
	if limit <= 0 {
		limit = 100
	}
	where, args := filter.whereSQL()
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, `
		SELECT dm.path, dm.date, dm.tags, dm.people, dm.projects, dm.category, dm.summary
		FROM document_metadata dm
		WHERE dm.has_frontmatter = 1 AND `+where+`
		ORDER BY dm.date GLOB '`+datePattern+`' DESC, dm.date DESC, dm.path
		LIMIT ?
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("document metadata query failed: %w", err)
	}
	defer rows.Close()

	var results []DocumentMetadata
	for rows.Next() {
		var m DocumentMetadata
		var tags, people, projects string
		if err := rows.Scan(&m.Path, &m.Date, &tags, &people, &projects, &m.Category, &m.Summary); err != nil {
			return nil, fmt.Errorf("failed to scan document metadata: %w", err)
		}
		m.Tags = decodeJSONList(tags)
		m.People = decodeJSONList(people)
		m.Projects = decodeJSONList(projects)
		results = append(results, m)
	}
	return results, rows.Err()
}

func decodeJSONList(s string) []string {
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil || len(out) == 0 {
		return nil
	}
	return out
}
