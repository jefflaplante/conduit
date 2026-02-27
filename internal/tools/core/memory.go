package core

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"conduit/internal/config"
	"conduit/internal/tools/types"
)

// MemorySearchTool implements hybrid vector+FTS5 search across memory files.
// When vector search is available, it runs both semantic (vector) and keyword
// (FTS5) searches in parallel and merges results using Reciprocal Rank Fusion.
// Falls back gracefully to FTS5-only or line-by-line grep when services are
// unavailable.
type MemorySearchTool struct {
	services     *types.ToolServices
	sandboxCfg   config.SandboxConfig
	workspaceDir string
}

// MemoryResult represents a search result from memory files or session history
type MemoryResult struct {
	Path       string  `json:"path"`
	Content    string  `json:"content"`
	Score      float64 `json:"score"`
	LineNum    int     `json:"line_num,omitempty"`
	Context    string  `json:"context,omitempty"`
	Source     string  `json:"source"` // "file" or "session"
	SessionKey string  `json:"session_key,omitempty"`
	Role       string  `json:"role,omitempty"`
	Timestamp  string  `json:"timestamp,omitempty"`
	SearchType string  `json:"search_type,omitempty"` // "fts5", "vector", or "hybrid"
}

// rrfK is the constant used in Reciprocal Rank Fusion scoring: score = 1 / (k + rank).
// A value of 60 is standard in information retrieval literature.
const rrfK = 60

func NewMemorySearchTool(services *types.ToolServices, sandboxCfg config.SandboxConfig) *MemorySearchTool {
	return &MemorySearchTool{
		services:     services,
		sandboxCfg:   sandboxCfg,
		workspaceDir: sandboxCfg.WorkspaceDir,
	}
}

func (t *MemorySearchTool) Name() string {
	return "MemorySearch"
}

func (t *MemorySearchTool) Description() string {
	return "Search across memory files using hybrid vector (semantic) and FTS5 (keyword) matching"
}

func (t *MemorySearchTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Search query to find relevant content",
			},
			"maxResults": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum number of results to return",
				"default":     10,
			},
			"minScore": map[string]interface{}{
				"type":        "number",
				"description": "Minimum relevance score (0.0-1.0)",
				"default":     0.1,
			},
			"searchSessions": map[string]interface{}{
				"type":        "boolean",
				"description": "Whether to search session history in addition to memory files",
				"default":     true,
			},
			"sessionLimit": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum number of messages to search from session history",
				"default":     50,
			},
			"searchMode": map[string]interface{}{
				"type":        "string",
				"description": "Search strategy: 'auto' (hybrid when vector available, FTS5 otherwise), 'hybrid' (both vector+FTS5), 'vector' (semantic only), 'fts5' (keyword only)",
				"enum":        []string{"auto", "hybrid", "vector", "fts5"},
				"default":     "auto",
			},
		},
		"required": []string{"query"},
	}
}

func (t *MemorySearchTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	query, ok := args["query"].(string)
	if !ok {
		return &types.ToolResult{
			Success: false,
			Error:   "query parameter is required and must be a string",
		}, nil
	}

	maxResults := t.getIntArg(args, "maxResults", 10)
	minScore := t.getFloatArg(args, "minScore", 0.1)
	searchSessions := t.getBoolArg(args, "searchSessions", true)
	sessionLimit := t.getIntArg(args, "sessionLimit", 50)
	searchMode := t.getStringArg(args, "searchMode", "auto")

	// Resolve search mode
	effectiveMode := t.resolveSearchMode(searchMode)

	// Search memory files using the resolved mode
	fileResults, err := t.searchMemoryFiles(ctx, query, minScore, effectiveMode)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("file search failed: %v", err),
		}, nil
	}

	var results []MemoryResult
	results = append(results, fileResults...)

	// Search session messages if requested
	var sessionResults []MemoryResult
	if searchSessions && t.services.SessionStore != nil {
		sessionResults, err = t.searchSessionMessages(ctx, query, sessionLimit, minScore)
		if err != nil {
			log.Printf("Warning: session search failed: %v", err)
		} else {
			results = append(results, sessionResults...)
		}
	}

	// Filter by minScore
	var filtered []MemoryResult
	for _, r := range results {
		if r.Score >= minScore {
			filtered = append(filtered, r)
		}
	}
	results = filtered

	// Sort by score (descending)
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	// Limit results
	if len(results) > maxResults {
		results = results[:maxResults]
	}

	// Format results
	content := t.formatSearchResults(results, query)

	return &types.ToolResult{
		Success: true,
		Content: content,
		Data: map[string]interface{}{
			"results":         results,
			"query":           query,
			"total":           len(results),
			"fileResults":     len(fileResults),
			"sessionResults":  len(sessionResults),
			"minScore":        minScore,
			"maxResults":      maxResults,
			"searchSessions":  searchSessions,
			"sessionLimit":    sessionLimit,
			"searchMode":      searchMode,
			"effectiveMode":   effectiveMode,
			"vectorAvailable": t.services.VectorSearch != nil,
		},
	}, nil
}

// resolveSearchMode determines the effective search mode based on the requested
// mode and available services. "auto" resolves to "hybrid" when vector search
// is available, or "fts5" otherwise.
func (t *MemorySearchTool) resolveSearchMode(requested string) string {
	hasVector := t.services.VectorSearch != nil
	hasFTS5 := t.services.Searcher != nil

	switch requested {
	case "hybrid":
		if hasVector && hasFTS5 {
			return "hybrid"
		}
		if hasVector {
			return "vector"
		}
		if hasFTS5 {
			return "fts5"
		}
		return "grep"
	case "vector":
		if hasVector {
			return "vector"
		}
		// Graceful fallback
		if hasFTS5 {
			return "fts5"
		}
		return "grep"
	case "fts5":
		if hasFTS5 {
			return "fts5"
		}
		return "grep"
	default: // "auto"
		if hasVector && hasFTS5 {
			return "hybrid"
		}
		if hasVector {
			return "vector"
		}
		if hasFTS5 {
			return "fts5"
		}
		return "grep"
	}
}

// searchMemoryFiles dispatches to the appropriate search strategy based on mode.
// In "hybrid" mode, it runs vector and FTS5 searches in parallel and merges
// results using Reciprocal Rank Fusion.
func (t *MemorySearchTool) searchMemoryFiles(ctx context.Context, query string, minScore float64, mode string) ([]MemoryResult, error) {
	switch mode {
	case "hybrid":
		return t.searchMemoryFilesHybrid(ctx, query)
	case "vector":
		return t.searchMemoryFilesVector(ctx, query)
	case "fts5":
		return t.searchMemoryFilesFTS(ctx, query)
	default: // "grep" fallback
		return t.searchMemoryFilesGrep(ctx, query, minScore)
	}
}

// searchMemoryFilesHybrid runs both vector (semantic) and FTS5 (keyword) searches
// in parallel, then merges results using Reciprocal Rank Fusion (RRF).
func (t *MemorySearchTool) searchMemoryFilesHybrid(ctx context.Context, query string) ([]MemoryResult, error) {
	// Fetch size is larger than what we return so RRF has good input
	const fetchSize = 30

	var (
		ftsResults    []MemoryResult
		vectorResults []MemoryResult
		ftsErr        error
		vectorErr     error
		wg            sync.WaitGroup
	)

	// Run both searches in parallel
	wg.Add(2)
	go func() {
		defer wg.Done()
		ftsResults, ftsErr = t.searchMemoryFilesFTS(ctx, query)
	}()
	go func() {
		defer wg.Done()
		vectorResults, vectorErr = t.searchMemoryFilesVectorRaw(ctx, query, fetchSize)
	}()
	wg.Wait()

	// Handle errors gracefully: if one fails, use the other
	if ftsErr != nil && vectorErr != nil {
		return nil, fmt.Errorf("both searches failed: fts5=%v, vector=%v", ftsErr, vectorErr)
	}
	if ftsErr != nil {
		log.Printf("Hybrid search: FTS5 failed (%v), using vector results only", ftsErr)
		return vectorResults, nil
	}
	if vectorErr != nil {
		log.Printf("Hybrid search: vector failed (%v), using FTS5 results only", vectorErr)
		return ftsResults, nil
	}

	// Merge using Reciprocal Rank Fusion
	merged := reciprocalRankFusion(ftsResults, vectorResults)
	return merged, nil
}

// searchMemoryFilesVector performs semantic vector search only.
func (t *MemorySearchTool) searchMemoryFilesVector(ctx context.Context, query string) ([]MemoryResult, error) {
	return t.searchMemoryFilesVectorRaw(ctx, query, 20)
}

// searchMemoryFilesVectorRaw performs semantic vector search with a specified limit.
func (t *MemorySearchTool) searchMemoryFilesVectorRaw(ctx context.Context, query string, limit int) ([]MemoryResult, error) {
	if t.services.VectorSearch == nil {
		return nil, fmt.Errorf("vector search service not available")
	}

	vecResults, err := t.services.VectorSearch.Search(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("vector search failed: %w", err)
	}

	var results []MemoryResult
	for _, vr := range vecResults {
		content := vr.Content
		if len(content) > 200 {
			content = content[:200] + "..."
		}

		// Build context from metadata
		contextStr := vr.Content
		if title, ok := vr.Metadata["title"]; ok && title != "" {
			contextStr = title + "\n" + vr.Content
		}

		path := vr.ID
		if p, ok := vr.Metadata["path"]; ok && p != "" {
			path = p
		}

		results = append(results, MemoryResult{
			Path:       path,
			Content:    content,
			Score:      vr.Score,
			Context:    contextStr,
			Source:     "file",
			SearchType: "vector",
		})
	}

	return results, nil
}

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

// reciprocalRankFusion merges two ranked result lists using Reciprocal Rank Fusion.
// Each result's fused score is the sum of 1/(k + rank) across all lists where it appears.
// Results are identified by their content+path to handle deduplication.
func reciprocalRankFusion(lists ...[]MemoryResult) []MemoryResult {
	type fusedEntry struct {
		result MemoryResult
		score  float64
	}

	// Map from dedup key to fused entry
	fused := make(map[string]*fusedEntry)

	for _, list := range lists {
		for rank, result := range list {
			key := resultDeduplicationKey(result)
			rrfScore := 1.0 / float64(rrfK+rank+1) // rank is 0-based, so +1

			if existing, ok := fused[key]; ok {
				existing.score += rrfScore
				// Keep the result with more context or better metadata
				if len(result.Context) > len(existing.result.Context) {
					existing.result.Context = result.Context
				}
			} else {
				fused[key] = &fusedEntry{
					result: result,
					score:  rrfScore,
				}
			}
		}
	}

	// Collect and sort by fused score
	results := make([]MemoryResult, 0, len(fused))
	for _, entry := range fused {
		entry.result.Score = entry.score
		entry.result.SearchType = "hybrid"
		results = append(results, entry.result)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	return results
}

// resultDeduplicationKey generates a key for deduplicating results across search methods.
// For file results, we use path + a content prefix; for session results, session key + content prefix.
func resultDeduplicationKey(r MemoryResult) string {
	contentKey := r.Content
	if len(contentKey) > 100 {
		contentKey = contentKey[:100]
	}
	if r.Source == "session" {
		return "session:" + r.SessionKey + ":" + contentKey
	}
	return "file:" + r.Path + ":" + contentKey
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
			if relPath[0] == '/' {
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

// formatSearchResults formats search results for display
func (t *MemorySearchTool) formatSearchResults(results []MemoryResult, query string) string {
	if len(results) == 0 {
		return fmt.Sprintf("No results found for query: '%s'", query)
	}

	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("Found %d results for '%s':\n\n", len(results), query))

	for i, result := range results {
		searchTag := ""
		if result.SearchType != "" {
			searchTag = fmt.Sprintf(" [%s]", result.SearchType)
		}

		if result.Source == "session" {
			builder.WriteString(fmt.Sprintf("%d. **Session History** (%s) - Score: %.4f%s\n",
				i+1, result.Timestamp, result.Score, searchTag))
			builder.WriteString(fmt.Sprintf("   Session: %s | Role: %s\n", result.SessionKey, result.Role))
			builder.WriteString(fmt.Sprintf("   %s\n", result.Content))
		} else {
			if result.LineNum > 0 {
				builder.WriteString(fmt.Sprintf("%d. **%s** (line %d) - Score: %.4f%s\n",
					i+1, result.Path, result.LineNum, result.Score, searchTag))
			} else {
				builder.WriteString(fmt.Sprintf("%d. **%s** - Score: %.4f%s\n",
					i+1, result.Path, result.Score, searchTag))
			}
			builder.WriteString(fmt.Sprintf("   %s\n", result.Content))
			if result.Context != "" && result.Context != result.Content {
				builder.WriteString(fmt.Sprintf("   Context: %s\n",
					strings.ReplaceAll(result.Context, "\n", " | ")))
			}
		}
		builder.WriteString("\n")
	}

	return builder.String()
}

// Helper methods
func (t *MemorySearchTool) getIntArg(args map[string]interface{}, key string, defaultVal int) int {
	if val, ok := args[key].(float64); ok {
		return int(val)
	}
	if val, ok := args[key].(int); ok {
		return val
	}
	return defaultVal
}

func (t *MemorySearchTool) getFloatArg(args map[string]interface{}, key string, defaultVal float64) float64 {
	if val, ok := args[key].(float64); ok {
		return val
	}
	return defaultVal
}

func (t *MemorySearchTool) getBoolArg(args map[string]interface{}, key string, defaultVal bool) bool {
	if val, ok := args[key].(bool); ok {
		return val
	}
	return defaultVal
}

func (t *MemorySearchTool) getStringArg(args map[string]interface{}, key string, defaultVal string) string {
	if val, ok := args[key].(string); ok && val != "" {
		return val
	}
	return defaultVal
}

// MemoryGetTool retrieves specific snippets from memory files
type MemoryGetTool struct {
	services     *types.ToolServices
	sandboxCfg   config.SandboxConfig
	workspaceDir string
}

func NewMemoryGetTool(services *types.ToolServices, sandboxCfg config.SandboxConfig) *MemoryGetTool {
	return &MemoryGetTool{
		services:     services,
		sandboxCfg:   sandboxCfg,
		workspaceDir: sandboxCfg.WorkspaceDir,
	}
}

func (t *MemoryGetTool) Name() string {
	return "MemoryGet"
}

func (t *MemoryGetTool) Description() string {
	return "Retrieve specific content from memory files by path and line range"
}

func (t *MemoryGetTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Path to memory file (MEMORY.md or memory/*.md)",
			},
			"from": map[string]interface{}{
				"type":        "integer",
				"description": "Starting line number (1-based)",
				"default":     1,
			},
			"lines": map[string]interface{}{
				"type":        "integer",
				"description": "Number of lines to retrieve (0 = all lines)",
				"default":     0,
			},
		},
		"required": []string{"path"},
	}
}

func (t *MemoryGetTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	path, ok := args["path"].(string)
	if !ok {
		return &types.ToolResult{
			Success: false,
			Error:   "path parameter is required and must be a string",
		}, nil
	}

	// Validate path is a memory file
	if !t.isMemoryPath(path) {
		return &types.ToolResult{
			Success: false,
			Error:   "path must be MEMORY.md or memory/*.md",
		}, nil
	}

	from := t.getIntArg(args, "from", 1)
	lines := t.getIntArg(args, "lines", 0)

	// Read the content
	content, err := t.readMemorySnippet(path, from, lines)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to read memory content: %v", err),
		}, nil
	}

	return &types.ToolResult{
		Success: true,
		Content: content,
		Data: map[string]interface{}{
			"path":  path,
			"from":  from,
			"lines": lines,
		},
	}, nil
}

// isMemoryPath validates that the path is a memory file
func (t *MemoryGetTool) isMemoryPath(path string) bool {
	return path == "MEMORY.md" || strings.HasPrefix(path, "memory/")
}

// readMemorySnippet reads content from a memory file
func (t *MemoryGetTool) readMemorySnippet(path string, from, lines int) (string, error) {
	fullPath := filepath.Join(t.workspaceDir, path)

	content, err := os.ReadFile(fullPath)
	if err != nil {
		return "", fmt.Errorf("failed to read file %s: %w", path, err)
	}

	fileLines := strings.Split(string(content), "\n")

	// Adjust for 1-based indexing
	startIdx := from - 1
	if startIdx < 0 {
		startIdx = 0
	}
	if startIdx >= len(fileLines) {
		return "", fmt.Errorf("starting line %d is beyond file length (%d lines)", from, len(fileLines))
	}

	var endIdx int
	if lines == 0 {
		// Read all lines from start
		endIdx = len(fileLines)
	} else {
		endIdx = startIdx + lines
		if endIdx > len(fileLines) {
			endIdx = len(fileLines)
		}
	}

	selectedLines := fileLines[startIdx:endIdx]
	return strings.Join(selectedLines, "\n"), nil
}

func (t *MemoryGetTool) getIntArg(args map[string]interface{}, key string, defaultVal int) int {
	if val, ok := args[key].(float64); ok {
		return int(val)
	}
	if val, ok := args[key].(int); ok {
		return val
	}
	return defaultVal
}

// GetUsageExamples implements types.UsageExampleProvider for MemorySearchTool.
func (t *MemorySearchTool) GetUsageExamples() []types.ToolExample {
	return []types.ToolExample{
		{
			Name:        "Search for project information",
			Description: "Find information about specific projects or tasks using hybrid search",
			Args: map[string]interface{}{
				"query":      "Conduit project status",
				"maxResults": 5,
			},
			Expected: "Returns relevant memory entries about Conduit project progress and status (uses hybrid vector+FTS5 when available)",
		},
		{
			Name:        "Semantic search for concepts",
			Description: "Use vector search to find semantically related content even without exact keyword matches",
			Args: map[string]interface{}{
				"query":      "how to deploy the application",
				"searchMode": "vector",
				"maxResults": 10,
			},
			Expected: "Returns semantically related content about deployment procedures",
		},
		{
			Name:        "Keyword-only search",
			Description: "Use FTS5 keyword search for exact term matching",
			Args: map[string]interface{}{
				"query":      "database configuration settings",
				"searchMode": "fts5",
				"minScore":   0.5,
			},
			Expected: "Returns high-relevance keyword matches for database configuration information",
		},
		{
			Name:        "Search with session history",
			Description: "Search across both memory files and recent conversation history",
			Args: map[string]interface{}{
				"query":          "latest deployment",
				"searchSessions": true,
				"sessionLimit":   20,
				"maxResults":     5,
			},
			Expected: "Returns recent session messages and memory entries about deployments",
		},
	}
}
