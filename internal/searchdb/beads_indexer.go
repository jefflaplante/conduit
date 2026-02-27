package searchdb

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// BeadsIssue represents a beads issue from issues.jsonl
type BeadsIssue struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status"`
	IssueType   string `json:"issue_type"`
	Owner       string `json:"owner,omitempty"`
	Priority    int    `json:"priority,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

// BeadsResult represents a search result from beads FTS.
type BeadsResult struct {
	IssueID     string  `json:"issue_id"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Status      string  `json:"status"`
	IssueType   string  `json:"issue_type"`
	Owner       string  `json:"owner"`
	Rank        float64 `json:"rank"`
}

// BeadsIndexer indexes beads issues into FTS5.
type BeadsIndexer struct {
	db       *sql.DB
	beadsDir string
	lastHash string
	mu       sync.Mutex
}

// NewBeadsIndexer creates a new beads indexer.
func NewBeadsIndexer(db *sql.DB, beadsDir string) *BeadsIndexer {
	return &BeadsIndexer{
		db:       db,
		beadsDir: beadsDir,
	}
}

// IndexBeads parses .beads/issues.jsonl and indexes into beads_fts.
// Uses SHA256 hash to skip re-indexing if file hasn't changed.
func (idx *BeadsIndexer) IndexBeads(ctx context.Context) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	jsonlPath := filepath.Join(idx.beadsDir, "issues.jsonl")

	// Check if file exists
	if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
		// No beads directory - not an error, just nothing to index
		return nil
	}

	// Read file and compute hash
	data, err := os.ReadFile(jsonlPath)
	if err != nil {
		return fmt.Errorf("failed to read issues.jsonl: %w", err)
	}

	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	if hash == idx.lastHash {
		// File unchanged, skip re-indexing
		return nil
	}

	// Parse issues
	issues, err := parseIssuesJSONL(data)
	if err != nil {
		return fmt.Errorf("failed to parse issues.jsonl: %w", err)
	}

	// Re-index all issues (full rebuild since beads_fts is small)
	tx, err := idx.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Clear existing index
	if _, err := tx.ExecContext(ctx, "DELETE FROM beads_fts"); err != nil {
		return fmt.Errorf("failed to clear beads_fts: %w", err)
	}

	// Insert all issues
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO beads_fts(issue_id, title, description, status, issue_type, owner) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("failed to prepare insert: %w", err)
	}
	defer stmt.Close()

	for _, issue := range issues {
		if _, err := stmt.ExecContext(ctx,
			issue.ID,
			issue.Title,
			issue.Description,
			issue.Status,
			issue.IssueType,
			issue.Owner,
		); err != nil {
			log.Printf("Warning: failed to index beads issue %s: %v", issue.ID, err)
			continue
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	idx.lastHash = hash
	log.Printf("BeadsIndexer: indexed %d issues from %s", len(issues), jsonlPath)
	return nil
}

// SearchBeads queries beads_fts with BM25 ranking.
func (idx *BeadsIndexer) SearchBeads(ctx context.Context, query string, limit int, statusFilter string) ([]BeadsResult, error) {
	if limit <= 0 {
		limit = 10
	}

	ftsQuery := buildBeadsFTSQuery(query)
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

	rows, err := idx.db.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("beads search failed: %w", err)
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

// GetIndexedCount returns the number of beads issues currently indexed.
func (idx *BeadsIndexer) GetIndexedCount() (int, error) {
	var count int
	err := idx.db.QueryRow("SELECT COUNT(*) FROM beads_fts").Scan(&count)
	return count, err
}

// parseIssuesJSONL parses the JSONL format beads issues file.
func parseIssuesJSONL(data []byte) ([]BeadsIssue, error) {
	var issues []BeadsIssue
	scanner := bufio.NewScanner(strings.NewReader(string(data)))

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var issue BeadsIssue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			log.Printf("Warning: failed to parse beads issue at line %d: %v", lineNum, err)
			continue
		}

		issues = append(issues, issue)
	}

	return issues, scanner.Err()
}

// buildBeadsFTSQuery converts a user query into an FTS5 MATCH expression.
// Searches across title, description, and issue_id fields.
func buildBeadsFTSQuery(query string) string {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return ""
	}

	var terms []string
	for _, w := range words {
		cleaned := cleanFTSTerm(w)
		if cleaned != "" {
			// Search across multiple columns: title, description, issue_id
			terms = append(terms, cleaned)
		}
	}

	if len(terms) == 0 {
		return ""
	}

	// Use OR to match any term across any indexed column
	return strings.Join(terms, " OR ")
}

// cleanFTSTerm removes characters that have special meaning in FTS5 queries.
func cleanFTSTerm(term string) string {
	var b strings.Builder
	for _, ch := range term {
		switch ch {
		case '"', '*', '(', ')', ':', '^', '{', '}', '+', '-':
			// skip special FTS5 characters
		default:
			b.WriteRune(ch)
		}
	}
	return strings.TrimSpace(b.String())
}
