package searchdb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"sync"

	"conduit/internal/tools/types"
)

// BrainIndexer indexes brain LTM entries into FTS5 for ranked full-text search.
type BrainIndexer struct {
	db       *sql.DB // search.db
	brainDB  *sql.DB // brain.db (read-only)
	lastHash string
	mu       sync.Mutex
}

// NewBrainIndexer creates a new brain indexer.
func NewBrainIndexer(searchDB, brainDB *sql.DB) *BrainIndexer {
	return &BrainIndexer{
		db:      searchDB,
		brainDB: brainDB,
	}
}

// IndexBrain reads all LTM entries from brain.db and indexes them into brain_ltm_fts.
// Uses SHA256 hash to skip re-indexing if data hasn't changed.
func (idx *BrainIndexer) IndexBrain(ctx context.Context) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	// Query all LTM entries from brain.db
	rows, err := idx.brainDB.QueryContext(ctx,
		`SELECT key, value, COALESCE(source,'') FROM brain_ltm ORDER BY key`)
	if err != nil {
		return fmt.Errorf("failed to query brain_ltm: %w", err)
	}
	defer rows.Close()

	type entry struct {
		key, value, source string
	}
	var entries []entry
	var hashBuilder strings.Builder

	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.key, &e.value, &e.source); err != nil {
			return fmt.Errorf("failed to scan brain_ltm row: %w", err)
		}
		entries = append(entries, e)
		hashBuilder.WriteString(e.key)
		hashBuilder.WriteString(e.value)
		hashBuilder.WriteString(e.source)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("brain_ltm row iteration error: %w", err)
	}

	// Compute hash and skip if unchanged
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(hashBuilder.String())))
	if hash == idx.lastHash {
		return nil
	}

	// Re-index: full rebuild in a transaction
	tx, err := idx.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "DELETE FROM brain_ltm_fts"); err != nil {
		return fmt.Errorf("failed to clear brain_ltm_fts: %w", err)
	}

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO brain_ltm_fts(key, value, source) VALUES (?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("failed to prepare insert: %w", err)
	}
	defer stmt.Close()

	for _, e := range entries {
		if _, err := stmt.ExecContext(ctx, e.key, e.value, e.source); err != nil {
			log.Printf("Warning: failed to index brain entry %q: %v", e.key, err)
			continue
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	idx.lastHash = hash
	log.Printf("BrainIndexer: indexed %d LTM entries", len(entries))
	return nil
}

// SearchBrain queries brain_ltm_fts with BM25 ranking.
func (idx *BrainIndexer) SearchBrain(ctx context.Context, query string, limit int) ([]types.BrainFTSResult, error) {
	if limit <= 0 {
		limit = 10
	}

	ftsQuery := buildBrainFTSQuery(query)
	if ftsQuery == "" {
		return nil, nil
	}

	rows, err := idx.db.QueryContext(ctx,
		`SELECT key, value, source, rank FROM brain_ltm_fts WHERE brain_ltm_fts MATCH ? ORDER BY rank LIMIT ?`,
		ftsQuery, limit)
	if err != nil {
		return nil, fmt.Errorf("brain FTS search failed: %w", err)
	}
	defer rows.Close()

	var results []types.BrainFTSResult
	for rows.Next() {
		var r types.BrainFTSResult
		if err := rows.Scan(&r.Key, &r.Value, &r.Source, &r.Rank); err != nil {
			return nil, fmt.Errorf("failed to scan brain FTS result: %w", err)
		}
		results = append(results, r)
	}

	return results, rows.Err()
}

// GetIndexedCount returns the number of brain LTM entries currently indexed.
func (idx *BrainIndexer) GetIndexedCount() (int, error) {
	var count int
	err := idx.db.QueryRow("SELECT COUNT(*) FROM brain_ltm_fts").Scan(&count)
	return count, err
}

// buildBrainFTSQuery converts a user query into an FTS5 MATCH expression.
func buildBrainFTSQuery(query string) string {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return ""
	}

	var terms []string
	for _, w := range words {
		cleaned := cleanFTSTerm(w)
		if cleaned != "" {
			terms = append(terms, cleaned)
		}
	}

	if len(terms) == 0 {
		return ""
	}

	return strings.Join(terms, " OR ")
}
