package rem

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"strings"
)

// Groom checks source files for changes and flags for re-extraction
func (r *REMCycle) Groom(ctx context.Context, dryRun bool) (*GroomResult, error) {
	result := &GroomResult{
		FilesChecked: 0,
		FilesChanged: []string{},
		KeysUpdated:  0,
	}

	// 1. Get all unique source files from LTM (only file: sources)
	rows, err := r.db.QueryContext(ctx, `
		SELECT DISTINCT source
		FROM brain_ltm
		WHERE source != ''
		AND source LIKE 'file:%'
		ORDER BY source
	`)
	if err != nil {
		return nil, fmt.Errorf("query sources: %w", err)
	}
	defer rows.Close()

	var sources []string
	for rows.Next() {
		var source string
		if err := rows.Scan(&source); err != nil {
			return nil, fmt.Errorf("scan source: %w", err)
		}
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sources: %w", err)
	}

	// 2. Process each source file
	for _, source := range sources {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		result.FilesChecked++

		// Extract file path from source (e.g., "file:MEMORY.md" -> "MEMORY.md")
		filePath := strings.TrimPrefix(source, "file:")
		if filePath == source {
			// No "file:" prefix, skip
			continue
		}

		// Compute current file hash
		currentHash, err := computeFileHash(filePath)
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
			return nil, fmt.Errorf("get stored hash for %s: %w", source, err)
		}

		// Check if file has changed
		if storedHash != "" && storedHash != currentHash {
			result.FilesChanged = append(result.FilesChanged, filePath)
			// Note: LLM re-extraction would happen here if config.GroomWithLLM was true
			// For now, we just record the change
		}

		// Update source_hash for this file (tracks it for next cycle)
		if !dryRun {
			res, err := r.db.ExecContext(ctx, `
				UPDATE brain_ltm
				SET source_hash = ?
				WHERE source = ?
			`, currentHash, source)
			if err != nil {
				return nil, fmt.Errorf("update hash for %s: %w", source, err)
			}

			rowsAffected, _ := res.RowsAffected()
			result.KeysUpdated += int(rowsAffected)
		}
	}

	return result, nil
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
