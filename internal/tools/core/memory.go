package core

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"conduit/internal/config"
	toolargs "conduit/internal/tools/args"
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
				"default":     0.3,
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

	maxResults := toolargs.GetInt(args, "maxResults", 10)
	minScore := toolargs.GetFloat64(args, "minScore", 0.3)
	searchSessions := toolargs.GetBool(args, "searchSessions", true)
	sessionLimit := toolargs.GetInt(args, "sessionLimit", 50)
	searchMode := toolargs.GetString(args, "searchMode", "auto")

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

	// Search brain memory if available
	if t.services.Brain != nil {
		brainResults, brainErr := t.searchBrain(ctx, query)
		if brainErr != nil {
			log.Printf("Warning: brain search failed: %v", brainErr)
		} else {
			results = append(results, brainResults...)
		}
	}

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

	// Log score distribution for calibration
	if len(results) > 0 {
		log.Printf("[vecgo] MemorySearch: mode=%s query=%q results=%d scores=[%.3f..%.3f]",
			effectiveMode, query, len(results), results[len(results)-1].Score, results[0].Score)
	}

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
			"brainAvailable":  t.services.Brain != nil,
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

// SelfTest implements types.SelfTester for MemorySearchTool.
func (t *MemorySearchTool) SelfTest(ctx context.Context, opts *types.SelfTestOptions) *types.SelfTestResult {
	start := time.Now()

	if opts == nil {
		opts = types.DefaultSelfTestOptions()
	}

	result := &types.SelfTestResult{
		Status:       types.SelfTestStatusOK,
		Capabilities: []string{},
		TestedAt:     time.Now(),
	}

	deps := []types.DependencyStatus{}
	var unavailable []string

	// Check Searcher (FTS5) - primary search backend
	searcherDep := types.DependencyStatus{
		Name:     "SearchService",
		Required: false, // can fall back to grep
	}
	hasFTS5 := t.services != nil && t.services.Searcher != nil
	if hasFTS5 {
		searcherDep.Available = true
		searcherDep.Status = "available"
		result.Capabilities = append(result.Capabilities, "fts5_search", "document_search", "message_search")
	} else {
		searcherDep.Available = false
		searcherDep.Status = "not_configured"
		searcherDep.Message = "FTS5 search service not available; falling back to grep"
		unavailable = append(unavailable, "fts5_search")
	}
	deps = append(deps, searcherDep)

	// Check VectorSearch - semantic/vector search backend
	vectorDep := types.DependencyStatus{
		Name:     "VectorService",
		Required: false,
	}
	hasVector := t.services != nil && t.services.VectorSearch != nil
	if hasVector {
		vectorDep.Available = true
		vectorDep.Status = "available"
		result.Capabilities = append(result.Capabilities, "vector_search", "semantic_search")
	} else {
		vectorDep.Available = false
		vectorDep.Status = "not_configured"
		vectorDep.Message = "Vector search not available; semantic search disabled"
		unavailable = append(unavailable, "vector_search", "semantic_search")
	}
	deps = append(deps, vectorDep)

	// Hybrid search requires both FTS5 and Vector
	if hasFTS5 && hasVector {
		result.Capabilities = append(result.Capabilities, "hybrid_search")
	} else {
		unavailable = append(unavailable, "hybrid_search")
	}

	// Check Brain service for brain memory search
	brainDep := types.DependencyStatus{
		Name:     "BrainService",
		Required: false,
	}
	if t.services != nil && t.services.Brain != nil {
		brainDep.Available = true
		brainDep.Status = "available"
		result.Capabilities = append(result.Capabilities, "brain_search")
	} else {
		brainDep.Available = false
		brainDep.Status = "not_configured"
		brainDep.Message = "Brain service not available; brain memory search disabled"
		unavailable = append(unavailable, "brain_search")
	}
	deps = append(deps, brainDep)

	// Check SessionStore for session message search
	sessionDep := types.DependencyStatus{
		Name:     "SessionStore",
		Required: false,
	}
	if t.services != nil && t.services.SessionStore != nil {
		sessionDep.Available = true
		sessionDep.Status = "available"
		result.Capabilities = append(result.Capabilities, "session_search")
	} else {
		sessionDep.Available = false
		sessionDep.Status = "not_configured"
		sessionDep.Message = "Session store not available; session history search disabled"
		unavailable = append(unavailable, "session_search")
	}
	deps = append(deps, sessionDep)

	// Grep fallback is always available
	result.Capabilities = append(result.Capabilities, "grep_search")

	// Determine overall status
	if !hasFTS5 && !hasVector {
		result.Status = types.SelfTestStatusDegraded
		result.Message = "MemorySearch is degraded: only grep fallback available"
		result.Suggestions = []string{
			"Configure search.db for FTS5 document/message search",
			"Configure vector service for semantic search",
		}
	} else if !hasFTS5 || !hasVector {
		result.Status = types.SelfTestStatusDegraded
		if !hasFTS5 {
			result.Message = "MemorySearch is degraded: FTS5 not available, using vector-only"
		} else {
			result.Message = "MemorySearch is degraded: vector search not available, using FTS5-only"
		}
	} else {
		result.Status = types.SelfTestStatusOK
		result.Message = "MemorySearch is fully functional with hybrid search"
	}

	result.Dependencies = deps
	result.UnavailableCapabilities = unavailable
	result.TestDuration = time.Since(start)

	// Add verbose details
	if opts.Verbose {
		sandboxEnabled := t.sandboxCfg.WorkspaceDir != "" || len(t.sandboxCfg.AllowedPaths) > 0
		result.Details = map[string]interface{}{
			"workspace_dir":   t.workspaceDir,
			"has_fts5":        hasFTS5,
			"has_vector":      hasVector,
			"effective_mode":  t.resolveSearchMode("auto"),
			"sandbox_enabled": sandboxEnabled,
		}
	}

	// Add examples if requested and tool is functional
	if opts.IncludeExamples && result.IsFunctional() {
		result.Examples = t.GetUsageExamples()
	}

	return result
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
