package fts

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Indexer handles indexing workspace markdown files into FTS5.
type Indexer struct {
	db           *sql.DB
	workspaceDir string
	maxTokens    int
	mu           sync.Mutex

	// metaChecked/metaEnabled cache whether db has document_metadata
	// (search.db only); guarded by mu.
	metaChecked bool
	metaEnabled bool
}

// NewIndexer creates a new workspace file indexer.
func NewIndexer(db *sql.DB, workspaceDir string) *Indexer {
	return &Indexer{
		db:           db,
		workspaceDir: workspaceDir,
		maxTokens:    500,
	}
}

// IndexWorkspace scans all .md files in the workspace directory, chunks them,
// and upserts into document_chunks. Files whose SHA256 hash hasn't changed
// since the last index are skipped.
func (idx *Indexer) IndexWorkspace(ctx context.Context) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if idx.workspaceDir == "" {
		return nil
	}

	// Collect all .md files
	var mdFiles []string
	err := filepath.WalkDir(idx.workspaceDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip inaccessible paths
		}
		if !d.IsDir() && strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			mdFiles = append(mdFiles, path)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to walk workspace directory: %w", err)
	}

	// Build a set of relative paths we've seen so we can remove stale entries
	seenPaths := make(map[string]bool)

	for _, fullPath := range mdFiles {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		relPath, err := filepath.Rel(idx.workspaceDir, fullPath)
		if err != nil {
			continue
		}
		seenPaths[relPath] = true

		if err := idx.indexFile(ctx, fullPath, relPath); err != nil {
			log.Printf("FTS indexer: error indexing %s: %v", relPath, err)
		}
	}

	// Remove chunks for files that no longer exist
	if err := idx.removeStaleFiles(ctx, seenPaths); err != nil {
		log.Printf("FTS indexer: error removing stale files: %v", err)
	}

	return nil
}

// IndexFile processes a single file: hash check, chunk, upsert.
func (idx *Indexer) IndexFile(ctx context.Context, relativePath string) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	fullPath := filepath.Join(idx.workspaceDir, relativePath)
	return idx.indexFile(ctx, fullPath, relativePath)
}

// RemoveFile removes all chunks for a file path.
func (idx *Indexer) RemoveFile(ctx context.Context, relativePath string) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if _, err := idx.db.ExecContext(ctx,
		`DELETE FROM document_chunks WHERE file_path = ?`, relativePath); err != nil {
		return err
	}
	return idx.removeMetadata(ctx, relativePath)
}

// metadataEnabled reports (once, then cached) whether frontmatter metadata
// is stored alongside chunks. Caller holds mu.
func (idx *Indexer) metadataEnabled(ctx context.Context) bool {
	if !idx.metaChecked {
		idx.metaEnabled = hasMetadataTable(ctx, idx.db)
		idx.metaChecked = true
	}
	return idx.metaEnabled
}

// removeMetadata deletes path's metadata row when metadata is enabled.
// Caller holds mu.
func (idx *Indexer) removeMetadata(ctx context.Context, relPath string) error {
	if !idx.metadataEnabled(ctx) {
		return nil
	}
	_, err := idx.db.ExecContext(ctx, `DELETE FROM document_metadata WHERE path = ?`, relPath)
	return err
}

func (idx *Indexer) indexFile(ctx context.Context, fullPath, relPath string) error {
	// Read file content
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", relPath, err)
	}
	content := string(data)

	// Compute hash
	hash := fmt.Sprintf("%x", sha256.Sum256(data))

	// Check if file hash has changed
	if idx.metadataEnabled(ctx) {
		// The metadata row is written in the same transaction as the chunks,
		// so its hash alone proves the file is current (even with zero
		// chunks), and a missing row re-indexes files indexed before bd-3o6h.
		if metadataHash(ctx, idx.db, relPath) == hash {
			return nil // unchanged
		}
	} else {
		var existingHash string
		err = idx.db.QueryRowContext(ctx,
			`SELECT file_hash FROM document_chunks WHERE file_path = ? LIMIT 1`, relPath,
		).Scan(&existingHash)
		if err == nil && existingHash == hash {
			return nil // unchanged
		}
	}

	// bd-3o6h: frontmatter is stored as per-file metadata and only the body
	// is chunked. Without (or with malformed) frontmatter body == content.
	fm, body, hasFrontmatter := ParseFrontmatter(content)

	// File is new or changed — re-index
	tx, err := idx.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Delete old chunks (triggers handle FTS5 cleanup)
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM document_chunks WHERE file_path = ?`, relPath); err != nil {
		return fmt.Errorf("failed to delete old chunks: %w", err)
	}

	// Chunk the content
	chunks := ChunkMarkdown(body, idx.maxTokens)

	// Insert new chunks
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO document_chunks (file_path, heading, chunk_index, content, file_hash) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("failed to prepare insert: %w", err)
	}
	defer stmt.Close()

	for _, chunk := range chunks {
		if _, err := stmt.ExecContext(ctx, relPath, chunk.Heading, chunk.Index, chunk.Content, hash); err != nil {
			return fmt.Errorf("failed to insert chunk: %w", err)
		}
	}

	if idx.metadataEnabled(ctx) {
		if err := upsertMetadata(ctx, tx, relPath, hash, fm, hasFrontmatter); err != nil {
			return fmt.Errorf("failed to upsert metadata: %w", err)
		}
	}

	return tx.Commit()
}

func (idx *Indexer) removeStaleFiles(ctx context.Context, seenPaths map[string]bool) error {
	rows, err := idx.db.QueryContext(ctx,
		`SELECT DISTINCT file_path FROM document_chunks`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var stalePaths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			continue
		}
		if !seenPaths[path] {
			stalePaths = append(stalePaths, path)
		}
	}

	for _, path := range stalePaths {
		if _, err := idx.db.ExecContext(ctx,
			`DELETE FROM document_chunks WHERE file_path = ?`, path); err != nil {
			log.Printf("FTS indexer: failed to remove stale path %s: %v", path, err)
		}
	}

	return idx.removeStaleMetadata(ctx, seenPaths)
}

// removeStaleMetadata drops metadata rows for files no longer in the
// workspace, including files that never produced a chunk.
func (idx *Indexer) removeStaleMetadata(ctx context.Context, seenPaths map[string]bool) error {
	if !idx.metadataEnabled(ctx) {
		return nil
	}
	rows, err := idx.db.QueryContext(ctx, `SELECT path FROM document_metadata`)
	if err != nil {
		return err
	}
	var stalePaths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			continue
		}
		if !seenPaths[path] {
			stalePaths = append(stalePaths, path)
		}
	}
	rows.Close()

	for _, path := range stalePaths {
		if _, err := idx.db.ExecContext(ctx,
			`DELETE FROM document_metadata WHERE path = ?`, path); err != nil {
			log.Printf("FTS indexer: failed to remove stale metadata %s: %v", path, err)
		}
	}

	return nil
}
