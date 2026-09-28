package core

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// searchMemoryFilesGrep is the fallback line-by-line grep search.
func (t *MemorySearchTool) searchMemoryFilesGrep(_ context.Context, query string, minScore float64) ([]MemoryResult, error) {
	var results []MemoryResult

	memoryPaths, err := t.getMemoryFilePaths()
	if err != nil {
		return nil, fmt.Errorf("failed to get memory file paths: %w", err)
	}

	for _, path := range memoryPaths {
		fileResults, err := t.searchInFile(path, query, minScore)
		if err != nil {
			continue
		}
		results = append(results, fileResults...)
	}

	return results, nil
}

// searchMemoryFilesFTS uses the FTS5 searcher for document chunk search.
func (t *MemorySearchTool) searchMemoryFilesFTS(ctx context.Context, query string) ([]MemoryResult, error) {
	docResults, err := t.services.Searcher.SearchDocuments(ctx, query, 20)
	if err != nil {
		return nil, fmt.Errorf("FTS5 document search failed: %w", err)
	}

	var results []MemoryResult
	for _, dr := range docResults {
		// Normalize BM25 rank to a 0-1 score. BM25 rank is negative (more negative = better).
		// We map the range roughly: -20 -> 1.0, 0 -> 0.0
		score := -dr.Rank / 20.0
		if score > 1.0 {
			score = 1.0
		}
		if score < 0.0 {
			score = 0.0
		}

		content := dr.Content
		if len(content) > 200 {
			content = content[:200] + "..."
		}

		heading := dr.Heading
		if heading == "" {
			heading = "(no heading)"
		}

		results = append(results, MemoryResult{
			Path:       dr.FilePath,
			Content:    content,
			Score:      score,
			Context:    heading + "\n" + dr.Content,
			Source:     "file",
			SearchType: "fts5",
		})
	}

	return results, nil
}

// searchSessionMessages searches for messages in session history.
// Uses FTS5 when a Searcher is available, falls back to LIKE-based search otherwise.
func (t *MemorySearchTool) searchSessionMessages(ctx context.Context, query string, limit int, minScore float64) ([]MemoryResult, error) {
	// Prefer FTS5 path
	if t.services.Searcher != nil {
		return t.searchSessionMessagesFTS(ctx, query, limit)
	}

	if t.services.SessionStore == nil {
		return []MemoryResult{}, nil
	}

	// Fallback: LIKE-based search
	searchResults, err := t.services.SessionStore.SearchMessages(query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to search session messages: %w", err)
	}

	var results []MemoryResult
	for _, sr := range searchResults {
		if sr.MatchScore >= minScore {
			timestamp := sr.Message.Timestamp.Format("2006-01-02 15:04")

			content := sr.Message.Content
			if len(content) > 200 {
				content = content[:200] + "..."
			}

			sessionPath := fmt.Sprintf("session:%s", sr.SessionKey)

			results = append(results, MemoryResult{
				Path:       sessionPath,
				Content:    content,
				Score:      sr.MatchScore,
				Source:     "session",
				SessionKey: sr.SessionKey,
				Role:       sr.Message.Role,
				Timestamp:  timestamp,
				Context:    sr.Message.Content,
			})
		}
	}

	return results, nil
}

// searchSessionMessagesFTS uses the FTS5 searcher for message search.
func (t *MemorySearchTool) searchSessionMessagesFTS(ctx context.Context, query string, limit int) ([]MemoryResult, error) {
	msgResults, err := t.services.Searcher.SearchMessages(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("FTS5 message search failed: %w", err)
	}

	var results []MemoryResult
	for _, mr := range msgResults {
		// Normalize BM25 rank to a 0-1 score
		score := -mr.Rank / 20.0
		if score > 1.0 {
			score = 1.0
		}
		if score < 0.0 {
			score = 0.0
		}

		content := mr.Content
		if len(content) > 200 {
			content = content[:200] + "..."
		}

		sessionPath := fmt.Sprintf("session:%s", mr.SessionKey)

		results = append(results, MemoryResult{
			Path:       sessionPath,
			Content:    content,
			Score:      score,
			Source:     "session",
			SessionKey: mr.SessionKey,
			Role:       mr.Role,
			Context:    mr.Content,
		})
	}

	return results, nil
}

// searchBrain queries the Brain service for matching entries across all tiers.
// Prefers FTS5 search if a brain indexer is available, falls back to LIKE-based Recall.
func (t *MemorySearchTool) searchBrain(ctx context.Context, query string) ([]MemoryResult, error) {
	// Prefer FTS5 search if brain indexer is available
	if t.services.BrainFTS != nil {
		return t.searchBrainFTS(ctx, query)
	}
	// Fallback to direct Brain.Recall (LIKE-based)
	entries, err := t.services.Brain.Recall(ctx, query, 10)
	if err != nil {
		return nil, err
	}

	var results []MemoryResult
	for _, e := range entries {
		results = append(results, MemoryResult{
			Path:       fmt.Sprintf("brain:%s", e.Key),
			Content:    fmt.Sprintf("%s = %s", e.Key, e.Value),
			Score:      e.Salience,
			Source:     "brain",
			SearchType: string(e.Tier),
		})
	}
	return results, nil
}

// searchBrainFTS uses the FTS5-backed brain indexer for BM25-ranked search.
func (t *MemorySearchTool) searchBrainFTS(ctx context.Context, query string) ([]MemoryResult, error) {
	results, err := t.services.BrainFTS.SearchBrain(ctx, query, 10)
	if err != nil {
		return nil, err
	}
	var memResults []MemoryResult
	for _, r := range results {
		score := -r.Rank / 20.0
		if score > 1.0 {
			score = 1.0
		}
		if score < 0.0 {
			score = 0.0
		}
		memResults = append(memResults, MemoryResult{
			Path:       fmt.Sprintf("brain:%s", r.Key),
			Content:    fmt.Sprintf("%s = %s", r.Key, r.Value),
			Score:      score,
			Source:     "brain",
			SearchType: "fts5",
		})
	}
	return memResults, nil
}

// getMemoryFilePaths returns paths to memory files
func (t *MemorySearchTool) getMemoryFilePaths() ([]string, error) {
	var paths []string

	// Add MEMORY.md if it exists
	memoryPath := filepath.Join(t.workspaceDir, "MEMORY.md")
	if _, err := os.Stat(memoryPath); err == nil {
		paths = append(paths, memoryPath)
	}

	// Add files from memory/ directory
	memoryDir := filepath.Join(t.workspaceDir, "memory")
	if _, err := os.Stat(memoryDir); err == nil {
		err := filepath.WalkDir(memoryDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
				paths = append(paths, path)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("failed to walk memory directory: %w", err)
		}
	}

	return paths, nil
}

// searchInFile searches for the query within a specific file
func (t *MemorySearchTool) searchInFile(filePath, query string, minScore float64) ([]MemoryResult, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	text := string(content)
	lines := strings.Split(text, "\n")

	var results []MemoryResult

	// Simple keyword-based search (in production, this would use vector embeddings)
	queryLower := strings.ToLower(query)
	keywords := strings.Fields(queryLower)

	for lineNum, line := range lines {
		lineLower := strings.ToLower(line)
		score := t.calculateRelevanceScore(lineLower, keywords)

		if score >= minScore {
			// Get context around the matching line
			context := t.getContext(lines, lineNum, 2)

			relPath := strings.TrimPrefix(filePath, t.workspaceDir)
			if len(relPath) > 0 && relPath[0] == '/' {
				relPath = relPath[1:]
			}

			results = append(results, MemoryResult{
				Path:    relPath,
				Content: strings.TrimSpace(line),
				Score:   score,
				LineNum: lineNum + 1,
				Context: context,
				Source:  "file",
			})
		}
	}

	return results, nil
}

// calculateRelevanceScore calculates a simple relevance score
func (t *MemorySearchTool) calculateRelevanceScore(text string, keywords []string) float64 {
	if len(keywords) == 0 {
		return 0.0
	}

	matches := 0
	for _, keyword := range keywords {
		if strings.Contains(text, keyword) {
			matches++
		}
	}

	return float64(matches) / float64(len(keywords))
}

// getContext returns context lines around a given line number
func (t *MemorySearchTool) getContext(lines []string, lineNum, contextLines int) string {
	start := lineNum - contextLines
	if start < 0 {
		start = 0
	}

	end := lineNum + contextLines + 1
	if end > len(lines) {
		end = len(lines)
	}

	contextSlice := lines[start:end]
	return strings.Join(contextSlice, "\n")
}
