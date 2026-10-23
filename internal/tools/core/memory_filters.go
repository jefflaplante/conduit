package core

import (
	"context"
	"fmt"
	"strings"

	"conduit/internal/fts"
	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
)

// bd-3o6h: frontmatter pre-filters and the summary scan. Filters resolve
// against search.db's document_metadata and constrain the FTS5/vector
// candidate set before ranking; with no filters none of this runs and
// MemorySearch behaves exactly as before.

const (
	// filteredFetchSize is how many candidates each backend returns for a
	// filtered search, matching the unfiltered FTS5/vector limits.
	filteredFetchSize = 20
	// filteredVectorFetch over-fetches vector hits because the vector
	// service cannot filter; hits outside the allowed paths are dropped
	// before fusion.
	filteredVectorFetch = 100
	// defaultSummaryResults is the summary_only limit when maxResults is
	// not given (summaries are one line each, so the default is generous).
	defaultSummaryResults = 100
)

// errNoMetadata explains why filters/summary_only are unavailable.
var errNoMetadata = fmt.Errorf("frontmatter filters and summary_only require the FTS5 search index (search.db)")

// memoryFilterFromArgs builds the frontmatter filter from tool args.
func memoryFilterFromArgs(args map[string]interface{}) (fts.DocumentFilter, error) {
	filter := fts.DocumentFilter{
		Tags:       toolargs.GetStringSlice(args, "tags"),
		People:     toolargs.GetStringSlice(args, "people"),
		Projects:   toolargs.GetStringSlice(args, "projects"),
		Category:   toolargs.GetString(args, "category", ""),
		DateAfter:  toolargs.GetString(args, "date_after", ""),
		DateBefore: toolargs.GetString(args, "date_before", ""),
	}
	if err := filter.Validate(); err != nil {
		return fts.DocumentFilter{}, err
	}
	return filter.Normalize(), nil
}

// metadataSearcher returns the metadata-capable search service, if any.
func (t *MemorySearchTool) metadataSearcher() (types.DocumentMetadataSearcher, error) {
	if ms, ok := t.services.Searcher.(types.DocumentMetadataSearcher); ok {
		return ms, nil
	}
	return nil, errNoMetadata
}

// searchMemoryFilesFiltered searches only files whose frontmatter matches
// filter. FTS5 applies the constraint inside its MATCH query; vector hits are
// restricted to the matching path set before RRF.
func (t *MemorySearchTool) searchMemoryFilesFiltered(ctx context.Context, query, mode string, filter fts.DocumentFilter) ([]MemoryResult, error) {
	ms, err := t.metadataSearcher()
	if err != nil {
		return nil, err
	}

	paths, err := ms.MatchingDocumentPaths(ctx, filter)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, nil
	}
	allowed := make(map[string]bool, len(paths))
	for _, p := range paths {
		allowed[p] = true
	}

	ftsSearch := func() ([]MemoryResult, error) {
		docs, err := ms.SearchDocumentsFiltered(ctx, query, filteredFetchSize, filter)
		if err != nil {
			return nil, fmt.Errorf("FTS5 document search failed: %w", err)
		}
		return documentMemoryResults(docs), nil
	}
	vectorSearch := func() ([]MemoryResult, error) {
		hits, err := t.searchMemoryFilesVectorRaw(ctx, query, filteredVectorFetch)
		if err != nil {
			return nil, err
		}
		var kept []MemoryResult
		for _, h := range hits {
			if allowed[h.Path] {
				kept = append(kept, h)
			}
		}
		if len(kept) > filteredFetchSize {
			kept = kept[:filteredFetchSize]
		}
		return kept, nil
	}

	switch mode {
	case "hybrid":
		return t.mergeHybrid(ftsSearch, vectorSearch)
	case "vector":
		return vectorSearch()
	case "fts5":
		return ftsSearch()
	default: // "grep": no Searcher, so metadataSearcher already failed
		return nil, errNoMetadata
	}
}

// executeSummaryScan returns one line per file with frontmatter (path, date,
// category, summary), optionally narrowed by filter, without searching.
func (t *MemorySearchTool) executeSummaryScan(ctx context.Context, args map[string]interface{}, filter fts.DocumentFilter) (*types.ToolResult, error) {
	maxResults := toolargs.GetInt(args, "maxResults", defaultSummaryResults)

	ms, err := t.metadataSearcher()
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	summaries, err := ms.ListDocumentMetadata(ctx, filter, maxResults)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("summary scan failed: %v", err),
		}, nil
	}

	data := map[string]interface{}{
		"summaries":   summaries,
		"total":       len(summaries),
		"maxResults":  maxResults,
		"summaryOnly": true,
	}
	if !filter.IsEmpty() {
		data["filters"] = filter
	}
	return &types.ToolResult{
		Success: true,
		Content: formatSummaryScan(summaries),
		Data:    data,
	}, nil
}

// formatSummaryScan renders one line per file:
// "- path (date) [category] summary".
func formatSummaryScan(summaries []fts.DocumentMetadata) string {
	if len(summaries) == 0 {
		return "No memory files with frontmatter matched."
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Found %d memory file summaries:\n\n", len(summaries))
	for _, s := range summaries {
		b.WriteString("- ")
		b.WriteString(s.Path)
		if s.Date != "" {
			fmt.Fprintf(&b, " (%s)", s.Date)
		}
		if s.Category != "" {
			fmt.Fprintf(&b, " [%s]", s.Category)
		}
		if summary := strings.Join(strings.Fields(s.Summary), " "); summary != "" {
			b.WriteString(" ")
			b.WriteString(summary)
		}
		b.WriteString("\n")
	}
	return b.String()
}
