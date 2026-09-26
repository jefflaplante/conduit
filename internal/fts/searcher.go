package fts

import (
	"context"
	"database/sql"
	"fmt"

	"conduit/internal/ftsquery"
)

// DocumentResult represents a search result from workspace document chunks.
type DocumentResult struct {
	FilePath string  `json:"file_path"`
	Heading  string  `json:"heading"`
	Content  string  `json:"content"`
	Rank     float64 `json:"rank"` // BM25 rank (lower/more negative = more relevant)
}

// MessageResult represents a search result from session messages.
type MessageResult struct {
	MessageID  string  `json:"message_id"`
	SessionKey string  `json:"session_key"`
	Role       string  `json:"role"`
	Content    string  `json:"content"`
	Rank       float64 `json:"rank"`
}

// BeadsResult represents a search result from beads issues.
type BeadsResult struct {
	IssueID     string  `json:"issue_id"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Status      string  `json:"status"`
	IssueType   string  `json:"issue_type"`
	Owner       string  `json:"owner"`
	Rank        float64 `json:"rank"`
}

// SearchResult is a unified result from document, message, and beads search.
type SearchResult struct {
	Source   string          `json:"source"` // "document", "message", or "beads"
	Rank     float64         `json:"rank"`
	Document *DocumentResult `json:"document,omitempty"`
	Message  *MessageResult  `json:"message,omitempty"`
	Beads    *BeadsResult    `json:"beads,omitempty"`
}

// Searcher provides FTS5-backed search across documents and messages.
type Searcher struct {
	db *sql.DB
}

// NewSearcher creates a new FTS5 searcher.
func NewSearcher(db *sql.DB) *Searcher {
	return &Searcher{db: db}
}

// SearchDocuments queries document_chunks_fts with BM25 ranking.
func (s *Searcher) SearchDocuments(ctx context.Context, query string, limit int) ([]DocumentResult, error) {
	if limit <= 0 {
		limit = 10
	}

	ftsQuery := buildFTSQuery(query)
	if ftsQuery == "" {
		return nil, nil
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT dc.file_path, dc.heading, dc.content, rank
		FROM document_chunks_fts fts
		JOIN document_chunks dc ON dc.id = fts.rowid
		WHERE document_chunks_fts MATCH ?
		ORDER BY rank
		LIMIT ?
	`, ftsQuery, limit)
	if err != nil {
		return nil, fmt.Errorf("document search failed: %w", err)
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

// SearchMessages queries messages_fts with BM25 ranking.
func (s *Searcher) SearchMessages(ctx context.Context, query string, limit int) ([]MessageResult, error) {
	if limit <= 0 {
		limit = 10
	}

	ftsQuery := buildFTSQuery(query)
	if ftsQuery == "" {
		return nil, nil
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT message_id, session_key, role, content, rank
		FROM messages_fts
		WHERE messages_fts MATCH ?
		ORDER BY rank
		LIMIT ?
	`, ftsquery.Column("content", ftsQuery), limit) // conduit-31jg.31: scope every OR'd phrase
	if err != nil {
		return nil, fmt.Errorf("message search failed: %w", err)
	}
	defer rows.Close()

	var results []MessageResult
	for rows.Next() {
		var r MessageResult
		if err := rows.Scan(&r.MessageID, &r.SessionKey, &r.Role, &r.Content, &r.Rank); err != nil {
			return nil, fmt.Errorf("failed to scan message result: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// SearchBeads queries beads_fts with BM25 ranking.
// StatusFilter can be "open", "done", "in_progress", or "" for any status.
func (s *Searcher) SearchBeads(ctx context.Context, query string, limit int, statusFilter string) ([]BeadsResult, error) {
	if limit <= 0 {
		limit = 10
	}

	ftsQuery := buildFTSQuery(query)
	if ftsQuery == "" {
		return nil, nil
	}

	// Build query with optional status filter
	sqlQuery := `
		SELECT issue_id, title, description, status, issue_type, owner, rank
		FROM beads_fts
		WHERE beads_fts MATCH ?
	`
	args := []interface{}{ftsQuery}

	if statusFilter != "" && statusFilter != "any" {
		sqlQuery += " AND status = ?"
		args = append(args, statusFilter)
	}

	sqlQuery += " ORDER BY rank LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		// beads_fts might not exist in this database - return empty results
		return nil, nil
	}
	defer rows.Close()

	var results []BeadsResult
	for rows.Next() {
		var r BeadsResult
		if err := rows.Scan(&r.IssueID, &r.Title, &r.Description, &r.Status, &r.IssueType, &r.Owner, &r.Rank); err != nil {
			return nil, fmt.Errorf("failed to scan beads result: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// Search runs both document and message searches and returns unified results
// ordered by BM25 rank.
func (s *Searcher) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 10
	}

	// Run both searches with double the limit, then merge and trim
	docResults, docErr := s.SearchDocuments(ctx, query, limit)
	msgResults, msgErr := s.SearchMessages(ctx, query, limit)

	if docErr != nil && msgErr != nil {
		return nil, fmt.Errorf("both searches failed: docs=%v, msgs=%v", docErr, msgErr)
	}

	var results []SearchResult

	for i := range docResults {
		results = append(results, SearchResult{
			Source:   "document",
			Rank:     docResults[i].Rank,
			Document: &docResults[i],
		})
	}

	for i := range msgResults {
		results = append(results, SearchResult{
			Source:  "message",
			Rank:    msgResults[i].Rank,
			Message: &msgResults[i],
		})
	}

	// Sort by rank (BM25: more negative = better match)
	sortByRank(results)

	if len(results) > limit {
		results = results[:limit]
	}

	return results, nil
}

// sortByRank sorts results by BM25 rank (ascending, since lower = better).
func sortByRank(results []SearchResult) {
	for i := 1; i < len(results); i++ {
		for j := i; j > 0 && results[j].Rank < results[j-1].Rank; j-- {
			results[j], results[j-1] = results[j-1], results[j]
		}
	}
}

// buildFTSQuery converts a user query string into an FTS5 MATCH expression
// via the shared ftsquery builder: every term is a quoted phrase (so no
// operator, column filter or punctuation can break or alter the query),
// OR-joined, with conduit-179p stopword stripping and delimiter splitting.
//
// conduit-31jg.31: replaces the old denylist cleaner (cleanFTSTerm /
// isBlockedPattern / needsQuoting), which let . / @ % # through unquoted
// ("fts5: syntax error") and mangled bead IDs like conduit-3dru.
func buildFTSQuery(query string) string {
	return ftsquery.Build(query)
}
