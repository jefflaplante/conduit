package rem

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"conduit/internal/brain"
)

// Groom checks source files for changes and flags for re-extraction
func (r *REMCycle) Groom(ctx context.Context, dryRun bool) (*GroomResult, error) {
	result := &GroomResult{
		FilesChecked:       0,
		FilesChanged:       []string{},
		KeysUpdated:        0,
		EntriesMarkedStale: 0,
	}

	if err := r.groomFileSources(ctx, result, dryRun); err != nil {
		return result, fmt.Errorf("groom file sources: %w", err)
	}

	if err := r.groomAgeSources(ctx, result, dryRun); err != nil {
		return result, fmt.Errorf("groom age sources: %w", err)
	}

	return result, nil
}

// groomFileSources handles hash-based detection for file: sources
func (r *REMCycle) groomFileSources(ctx context.Context, result *GroomResult, dryRun bool) error {
	// 1. Get all unique source files from LTM (only file: sources)
	rows, err := r.db.QueryContext(ctx, `
		SELECT DISTINCT source
		FROM brain_ltm
		WHERE source != ''
		AND source LIKE 'file:%'
		ORDER BY source
	`)
	if err != nil {
		return fmt.Errorf("query sources: %w", err)
	}
	defer rows.Close()

	var sources []string
	for rows.Next() {
		var source string
		if err := rows.Scan(&source); err != nil {
			return fmt.Errorf("scan source: %w", err)
		}
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate sources: %w", err)
	}

	// 2. Process each source file
	for _, source := range sources {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		result.FilesChecked++

		// Extract file path from source (e.g., "file:MEMORY.md" -> "MEMORY.md")
		filePath := strings.TrimPrefix(source, "file:")
		if filePath == source {
			// No "file:" prefix, skip
			continue
		}

		// Resolve relative paths against workspace directory
		resolvedPath := filePath
		if !filepath.IsAbs(filePath) && r.config.WorkspaceDir != "" {
			resolvedPath = filepath.Join(r.config.WorkspaceDir, filePath)
		}

		// Compute current file hash
		currentHash, err := computeFileHash(resolvedPath)
		if err != nil {
			// File may be missing or unreadable, log and continue
			// In a real system, might want to flag these entries for archival
			continue
		}

		// Get stored hash for this source
		var storedHash string
		err = r.db.QueryRowContext(ctx, `
			SELECT source_hash
			FROM brain_ltm
			WHERE source = ?
			LIMIT 1
		`, source).Scan(&storedHash)
		if err != nil {
			return fmt.Errorf("get stored hash for %s: %w", source, err)
		}

		// Check if file has changed
		hashChanged := storedHash != "" && storedHash != currentHash
		if hashChanged {
			result.FilesChanged = append(result.FilesChanged, filePath)
			// Note: LLM re-extraction would happen here if config.GroomWithLLM was true
			// For now, we just record the change and mark entries as stale
		}

		if !dryRun {
			// Mark entries as stale if file changed
			if hashChanged {
				res, err := r.db.ExecContext(ctx, `
					UPDATE brain_ltm
					SET stale = 1
					WHERE source = ?
				`, source)
				if err != nil {
					return fmt.Errorf("mark stale for %s: %w", source, err)
				}
				staleCount, _ := res.RowsAffected()
				result.EntriesMarkedStale += int(staleCount)
			}

			// Update source_hash for this file (tracks it for next cycle)
			res, err := r.db.ExecContext(ctx, `
				UPDATE brain_ltm
				SET source_hash = ?
				WHERE source = ?
			`, currentHash, source)
			if err != nil {
				return fmt.Errorf("update hash for %s: %w", source, err)
			}

			rowsAffected, _ := res.RowsAffected()
			result.KeysUpdated += int(rowsAffected)
		}
	}

	return nil
}

// groomAgeSources handles age-based staleness for non-file sources
func (r *REMCycle) groomAgeSources(ctx context.Context, result *GroomResult, dryRun bool) error {
	// Query entries with non-file sources that aren't already marked stale
	rows, err := r.db.QueryContext(ctx, `
		SELECT key, source, accessed_at
		FROM brain_ltm
		WHERE source NOT LIKE 'file:%'
		AND source != ''
		AND stale = 0
	`)
	if err != nil {
		return fmt.Errorf("query non-file sources: %w", err)
	}
	defer rows.Close()

	now := time.Now()
	var staleKeys []string

	for rows.Next() {
		var key, source, accessedAtStr string
		if err := rows.Scan(&key, &source, &accessedAtStr); err != nil {
			return fmt.Errorf("scan entry: %w", err)
		}

		// Parse source prefix
		prefix, _ := brain.ParseSource(source)
		threshold := brain.StalenessThreshold(prefix)

		// Skip if this source type should never be stale
		if threshold == 0 {
			continue
		}

		// Parse accessed_at timestamp (SQLite may return as RFC3339 or our format)
		var accessedAt time.Time
		var err error
		// Try RFC3339 first (what SQLite returns from DATETIME columns)
		accessedAt, err = time.Parse(time.RFC3339, accessedAtStr)
		if err != nil {
			// Try our storage format
			accessedAt, err = time.ParseInLocation("2006-01-02 15:04:05", accessedAtStr, time.UTC)
			if err != nil {
				// Skip entries with invalid timestamps
				continue
			}
		}

		// Check if entry is stale
		age := now.Sub(accessedAt)
		if age > threshold {
			staleKeys = append(staleKeys, key)
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate entries: %w", err)
	}

	// Mark stale entries
	if !dryRun {
		for _, key := range staleKeys {
			_, err := r.db.ExecContext(ctx, `
				UPDATE brain_ltm
				SET stale = 1
				WHERE key = ?
			`, key)
			if err != nil {
				return fmt.Errorf("mark stale for %s: %w", key, err)
			}
		}
	}

	result.EntriesMarkedStale += len(staleKeys)

	return nil
}

// computeFileHash computes SHA256 hash of a file
func computeFileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
