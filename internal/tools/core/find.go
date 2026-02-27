package core

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"conduit/internal/tools/types"
)

// FindTool provides universal search across all indexed sources:
// - Memory (workspace documents)
// - Session messages
// - Beads issues
//
// It supports multiple search backends and gracefully degrades
// when backends are unavailable.
type FindTool struct {
	services *types.ToolServices
}

// NewFindTool creates a new Find tool instance.
func NewFindTool(services *types.ToolServices) *FindTool {
	return &FindTool{services: services}
}

// Name returns the tool name.
func (t *FindTool) Name() string {
	return "Find"
}

// Description returns the tool description.
func (t *FindTool) Description() string {
	return `Universal search across all indexed sources: workspace documents (memory), session messages, and beads issues.

Supports scope filtering:
- "all": Search all sources (default)
- "memory": Search workspace documents only
- "session": Search session messages only
- "beads": Search beads issues only

For beads searches, optionally filter by status: "open", "done", "in_progress", or "any".

Results are ranked by relevance using BM25 scoring and normalized for cross-source comparison.`
}

// Parameters returns the tool's parameter schema.
func (t *FindTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Search query - keywords or phrases to find",
			},
			"scope": map[string]interface{}{
				"type":        "string",
				"description": "Search scope: 'all' (default), 'memory', 'session', or 'beads'",
				"enum":        []string{"all", "memory", "session", "beads"},
				"default":     "all",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum number of results (1-50, default 10)",
				"default":     10,
				"minimum":     1,
				"maximum":     50,
			},
			"status": map[string]interface{}{
				"type":        "string",
				"description": "Filter beads by status: 'open', 'done', 'in_progress', or 'any' (default)",
				"enum":        []string{"any", "open", "done", "in_progress"},
				"default":     "any",
			},
			"semantic": map[string]interface{}{
				"type":        "boolean",
				"description": "Enable semantic/vector search if available (default false)",
				"default":     false,
			},
		},
		"required": []string{"query"},
	}
}

// FindResult represents a unified search result from any source.
type FindResult struct {
	Source      string  `json:"source"`       // "document", "message", or "beads"
	Score       float64 `json:"score"`        // Normalized 0-1 score (higher = better)
	Title       string  `json:"title"`        // Display title
	Summary     string  `json:"summary"`      // Content preview
	SourceID    string  `json:"source_id"`    // Unique ID within source
	BackendUsed string  `json:"backend_used"` // "fts5", "vector", or "fallback"
}

// Execute runs the Find search.
func (t *FindTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	// Parse parameters
	query, _ := args["query"].(string)
	if query == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "query parameter is required",
		}, nil
	}

	scope := "all"
	if s, ok := args["scope"].(string); ok && s != "" {
		scope = s
	}

	limit := 10
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 50 {
		limit = 50
	}

	status := "any"
	if s, ok := args["status"].(string); ok && s != "" {
		status = s
	}

	semantic, _ := args["semantic"].(bool)

	// Check if searcher is available
	if t.services == nil || t.services.Searcher == nil {
		return t.fallbackSearch(ctx, query, scope, limit)
	}

	// Run searches based on scope
	var results []FindResult
	var searchErrors []string
	var wg sync.WaitGroup
	var mu sync.Mutex

	// Document search
	if scope == "all" || scope == "memory" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			docs, err := t.services.Searcher.SearchDocuments(ctx, query, limit)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				searchErrors = append(searchErrors, fmt.Sprintf("document search: %v", err))
				return
			}
			for _, doc := range docs {
				results = append(results, FindResult{
					Source:      "document",
					Score:       normalizeRank(doc.Rank),
					Title:       doc.FilePath,
					Summary:     truncate(doc.Content, 200),
					SourceID:    fmt.Sprintf("%s#%s", doc.FilePath, doc.Heading),
					BackendUsed: "fts5",
				})
			}
		}()
	}

	// Message search
	if scope == "all" || scope == "session" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			msgs, err := t.services.Searcher.SearchMessages(ctx, query, limit)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				searchErrors = append(searchErrors, fmt.Sprintf("message search: %v", err))
				return
			}
			for _, msg := range msgs {
				results = append(results, FindResult{
					Source:      "message",
					Score:       normalizeRank(msg.Rank),
					Title:       fmt.Sprintf("[%s] %s", msg.Role, msg.SessionKey),
					Summary:     truncate(msg.Content, 200),
					SourceID:    msg.MessageID,
					BackendUsed: "fts5",
				})
			}
		}()
	}

	// Beads search
	if scope == "all" || scope == "beads" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			beads, err := t.services.Searcher.SearchBeads(ctx, query, limit, status)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				searchErrors = append(searchErrors, fmt.Sprintf("beads search: %v", err))
				return
			}
			for _, bead := range beads {
				summary := bead.Title
				if bead.Description != "" {
					summary = bead.Description
				}
				results = append(results, FindResult{
					Source:      "beads",
					Score:       normalizeRank(bead.Rank),
					Title:       fmt.Sprintf("[%s] %s", bead.Status, bead.IssueID),
					Summary:     truncate(summary, 200),
					SourceID:    bead.IssueID,
					BackendUsed: "fts5",
				})
			}
		}()
	}

	wg.Wait()

	// Vector/semantic search (if requested)
	if semantic {
		if t.services.VectorSearch != nil {
			vecResults, vecErr := t.services.VectorSearch.Search(ctx, query, limit)
			if vecErr != nil {
				searchErrors = append(searchErrors, fmt.Sprintf("vector search: %v", vecErr))
			} else {
				for _, vr := range vecResults {
					results = append(results, FindResult{
						Source:      sourceFromMetadata(vr.Metadata),
						Score:       vr.Score,
						Title:       titleFromMetadata(vr.Metadata, vr.ID),
						Summary:     truncate(vr.Content, 200),
						SourceID:    vr.ID,
						BackendUsed: "vector",
					})
				}
			}
		} else {
			searchErrors = append(searchErrors, "vector search requested but not available (enable vector in config)")
		}
	}

	// Sort by score (descending)
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	// Deduplicate by SourceID (keep highest score)
	results = deduplicateResults(results)

	// Trim to limit
	if len(results) > limit {
		results = results[:limit]
	}

	// Format output
	output := formatFindResults(query, scope, results, searchErrors)

	return &types.ToolResult{
		Success: true,
		Content: output,
		Data: map[string]interface{}{
			"result_count": len(results),
			"scope":        scope,
			"errors":       searchErrors,
		},
	}, nil
}

// fallbackSearch provides basic search when FTS is unavailable.
func (t *FindTool) fallbackSearch(ctx context.Context, query string, scope string, limit int) (*types.ToolResult, error) {
	return &types.ToolResult{
		Success: false,
		Content: "Search service unavailable. The FTS5 search index may not be initialized.",
		Error:   "search service not available",
		Data: map[string]interface{}{
			"backend_used": "fallback",
			"scope":        scope,
		},
	}, nil
}

// sourceFromMetadata extracts a display source type from vector result metadata.
func sourceFromMetadata(meta map[string]string) string {
	if s, ok := meta["source"]; ok && s != "" {
		return s
	}
	return "document"
}

// titleFromMetadata extracts a display title from vector result metadata.
func titleFromMetadata(meta map[string]string, fallbackID string) string {
	if t, ok := meta["title"]; ok && t != "" {
		return t
	}
	if p, ok := meta["path"]; ok && p != "" {
		return p
	}
	return fallbackID
}

// normalizeRank converts BM25 rank to a 0-1 score.
// BM25 ranks are negative, with more negative = better match.
// We normalize so 0 = worst, 1 = best.
func normalizeRank(rank float64) float64 {
	// BM25 ranks are typically in range [-30, 0]
	// More negative = better match
	// Convert to 0-1 where 1 = best match
	if rank >= 0 {
		return 0.0
	}
	// Normalize assuming typical range of -30 to 0
	normalized := -rank / 30.0
	if normalized > 1.0 {
		normalized = 1.0
	}
	return normalized
}

// deduplicateResults removes duplicate SourceIDs, keeping the highest-scored entry.
func deduplicateResults(results []FindResult) []FindResult {
	seen := make(map[string]bool)
	deduped := make([]FindResult, 0, len(results))
	for _, r := range results {
		if !seen[r.SourceID] {
			seen[r.SourceID] = true
			deduped = append(deduped, r)
		}
	}
	return deduped
}

// truncate shortens a string to maxLen, adding ellipsis if needed.
func truncate(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	// Collapse multiple spaces
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// formatFindResults generates human-readable output.
func formatFindResults(query, scope string, results []FindResult, errors []string) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("## Search Results for \"%s\"\n", query))
	sb.WriteString(fmt.Sprintf("Scope: %s | Found: %d results\n\n", scope, len(results)))

	if len(results) == 0 {
		sb.WriteString("No results found.\n")
		if len(errors) > 0 {
			sb.WriteString("\n### Search Errors\n")
			for _, err := range errors {
				sb.WriteString(fmt.Sprintf("- %s\n", err))
			}
		}
		return sb.String()
	}

	for i, r := range results {
		sb.WriteString(fmt.Sprintf("### %d. %s\n", i+1, r.Title))
		sb.WriteString(fmt.Sprintf("**Source:** %s | **Score:** %.2f\n", r.Source, r.Score))
		sb.WriteString(fmt.Sprintf("> %s\n\n", r.Summary))
	}

	if len(errors) > 0 {
		sb.WriteString("### Partial Errors\n")
		for _, err := range errors {
			sb.WriteString(fmt.Sprintf("- %s\n", err))
		}
	}

	return sb.String()
}

// GetSchemaHints implements types.EnhancedSchemaProvider.
func (t *FindTool) GetSchemaHints() map[string]interface{} {
	return map[string]interface{}{
		"query": map[string]interface{}{
			"examples": []string{"authentication", "database migration", "error handling"},
		},
		"scope": map[string]interface{}{
			"examples": []string{"all", "memory", "session", "beads"},
		},
	}
}

// GetUsageExamples implements types.UsageExampleProvider.
func (t *FindTool) GetUsageExamples() []types.ToolExample {
	return []types.ToolExample{
		{
			Name:        "Search all sources",
			Description: "Find 'authentication' across documents, messages, and beads",
			Args: map[string]interface{}{
				"query": "authentication",
			},
			Expected: "Results from memory files, session history, and beads issues",
		},
		{
			Name:        "Search only beads",
			Description: "Find open beads issues about testing",
			Args: map[string]interface{}{
				"query":  "testing",
				"scope":  "beads",
				"status": "open",
			},
			Expected: "Only beads issues matching 'testing' with open status",
		},
		{
			Name:        "Search session history",
			Description: "Find recent conversations about deployment",
			Args: map[string]interface{}{
				"query": "deployment",
				"scope": "session",
				"limit": 5,
			},
			Expected: "Up to 5 messages from session history about deployment",
		},
	}
}
