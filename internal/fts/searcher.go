package fts

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
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
	`, "content:"+ftsQuery, limit)
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

// fts5Operators are FTS5 query operators that should not be treated as search terms.
// This includes both standard boolean operators and advanced FTS5 syntax that could
// be abused for query manipulation.
var fts5Operators = map[string]bool{
	"and": true, "or": true, "not": true, "near": true,
}

// maxQueryTerms limits the number of terms in a single FTS5 query to prevent
// resource exhaustion from extremely long inputs.
const maxQueryTerms = 50

// maxTermLength limits individual term length to prevent abuse with extremely long tokens.
const maxTermLength = 200

// buildFTSQuery converts a user query string into an FTS5 MATCH expression.
// Terms are joined with OR for broad matching. Special FTS5 characters are escaped.
// Prevents injection by stripping operators and quoting terms that need it.
func buildFTSQuery(query string) string {
	// Strip null bytes and other control characters from the entire query first
	query = stripControlChars(query)

	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return ""
	}

	var terms []string
	for _, w := range words {
		// Enforce max term length to prevent abuse
		if len(w) > maxTermLength {
			w = w[:maxTermLength]
		}

		// Skip FTS5 operators to prevent query manipulation
		if fts5Operators[w] {
			continue
		}

		// Block advanced FTS5 operator patterns before cleaning.
		// NEAR/N patterns like "NEAR/3" bypass the simple operator check.
		if isBlockedPattern(w) {
			continue
		}

		// Strip FTS5 special characters to prevent syntax errors
		cleaned := cleanFTSTerm(w)
		if cleaned == "" {
			continue
		}

		// Re-check for operators after cleaning, since stripping special chars
		// may reveal a hidden operator (e.g., "\"AND\"" becomes "and")
		if fts5Operators[cleaned] {
			continue
		}

		// If term still contains any risky characters after cleaning, quote it
		if needsQuoting(cleaned) {
			cleaned = `"` + cleaned + `"`
		}

		terms = append(terms, cleaned)

		// Enforce max query terms
		if len(terms) >= maxQueryTerms {
			break
		}
	}

	if len(terms) == 0 {
		return ""
	}

	return strings.Join(terms, " OR ")
}

// stripControlChars removes null bytes and other ASCII control characters
// (except common whitespace like space, tab, newline) from input.
func stripControlChars(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, ch := range s {
		if ch < 32 && ch != '\t' && ch != '\n' && ch != '\r' {
			continue // strip null bytes and other control chars
		}
		b.WriteRune(ch)
	}
	return b.String()
}

// isBlockedPattern checks if a word matches advanced FTS5 syntax patterns
// that should never appear in user search queries.
func isBlockedPattern(word string) bool {
	// Block NEAR/N syntax (e.g., "near/3", "near/10")
	if strings.HasPrefix(word, "near/") {
		return true
	}

	// Block column filter syntax (e.g., "title:", "content:")
	// These contain colons which cleanFTSTerm strips, but check explicitly
	// for the pattern before cleaning to ensure defense in depth.
	if strings.Contains(word, ":") {
		return true
	}

	// Block start-of-column marker
	if strings.HasPrefix(word, "^") {
		return true
	}

	return false
}

// cleanFTSTerm removes characters that have special meaning in FTS5 queries.
// This is the primary defense against FTS5 injection.
func cleanFTSTerm(term string) string {
	var b strings.Builder
	for _, ch := range term {
		switch ch {
		case '"', '*', '(', ')', ':', '^', '{', '}', '+', '-', '~', '<', '>', '[', ']':
			// skip special FTS5 characters and potential injection vectors
		default:
			b.WriteRune(ch)
		}
	}
	return strings.TrimSpace(b.String())
}

// needsQuoting returns true if the term contains characters that might cause
// issues even after cleaning (e.g., embedded spaces from multi-byte chars).
func needsQuoting(term string) bool {
	for _, ch := range term {
		if ch < 32 || ch == '\'' {
			return true
		}
	}
	return false
}
