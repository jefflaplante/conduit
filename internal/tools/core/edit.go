package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"conduit/internal/tools/schema"
	"conduit/internal/tools/types"
)

// EditTool performs surgical text replacement in files
type EditTool struct {
	services *types.ToolServices
}

func NewEditTool(services *types.ToolServices) *EditTool {
	return &EditTool{services: services}
}

func (t *EditTool) Name() string {
	return "Edit"
}

func (t *EditTool) Description() string {
	return "Edit a file by replacing exact text. The oldText must match exactly (including whitespace). Use this for precise, surgical edits."
}

func (t *EditTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Path to the file to edit (relative or absolute)",
			},
			"old_string": map[string]interface{}{
				"type":        "string",
				"description": "Exact text to find and replace (must match exactly)",
			},
			"new_string": map[string]interface{}{
				"type":        "string",
				"description": "New text to replace the old text with",
			},
		},
		"required": []string{"path", "old_string", "new_string"},
	}
}

func (t *EditTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	path := t.getStringArg(args, "path", "")
	if path == "" {
		path = t.getStringArg(args, "file_path", "") // Alternative param name
	}

	oldText := t.getStringArg(args, "old_string", "")
	if oldText == "" {
		oldText = t.getStringArg(args, "oldText", "") // Alternative param name
	}

	newText := t.getStringArg(args, "new_string", "")
	if newText == "" {
		newText = t.getStringArg(args, "newText", "") // Alternative param name
	}

	// Validate required parameters with rich errors
	if path == "" {
		workspaceDir := "./workspace"
		if t.services != nil && t.services.ConfigMgr != nil {
			workspaceDir = t.services.ConfigMgr.Tools.Sandbox.WorkspaceDir
		}
		return types.NewErrorResult("missing_parameter",
			"Path parameter is required").
			WithParameter("path", nil).
			WithExamples([]string{
				filepath.Join(workspaceDir, "file.txt"),
				"./document.md",
				"README.md",
			}).
			WithSuggestions([]string{
				"Provide the path to the file to edit",
				"Use absolute or relative path within workspace",
			}), nil
	}

	if oldText == "" {
		return types.NewErrorResult("missing_parameter",
			"old_string parameter is required").
			WithParameter("old_string", nil).
			WithExamples([]string{
				"old text to replace",
				"// TODO: implement this",
			}).
			WithSuggestions([]string{
				"Provide the exact text to find and replace",
				"Text must match exactly including whitespace",
			}), nil
	}

	// Check if file exists with rich error
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return types.NewErrorResult("file_not_found",
			fmt.Sprintf("File does not exist: %s", path)).
			WithParameter("path", path).
			WithSuggestions([]string{
				"Check the file path spelling",
				"Use 'Glob' tool to list available files",
				"Use absolute path or path relative to workspace",
				"Create the file first if needed",
			}), nil
	}

	// Read the file with rich error handling
	data, err := os.ReadFile(path)
	if err != nil {
		errorType := "permission_denied"
		suggestions := []string{
			"Check file permissions",
			"Ensure file is readable",
		}

		if strings.Contains(err.Error(), "permission denied") {
			suggestions = append(suggestions, "Run with appropriate permissions")
		} else if strings.Contains(err.Error(), "no such file") {
			errorType = "file_not_found"
			suggestions = []string{
				"Check if file path is correct",
				"Use 'Glob' tool to find the file",
			}
		}

		return types.NewErrorResult(errorType,
			fmt.Sprintf("Failed to read file: %v", err)).
			WithParameter("path", path).
			WithSuggestions(suggestions), nil
	}

	content := string(data)

	// Strip BOM if present
	bom := ""
	if strings.HasPrefix(content, "\uFEFF") {
		bom = "\uFEFF"
		content = content[len("\uFEFF"):]
	}

	// Detect original line ending
	originalEnding := detectLineEnding(content)

	// Normalize to LF for matching
	normalizedContent := normalizeToLF(content)
	normalizedOldText := normalizeToLF(oldText)
	normalizedNewText := normalizeToLF(newText)

	// Try exact match first
	matchResult := fuzzyFindText(normalizedContent, normalizedOldText)

	if !matchResult.found {
		// Rich error for text not found
		lines := strings.Split(normalizedContent, "\n")
		contextLines := []string{}
		if len(lines) > 0 {
			// Show first few lines as context
			end := len(lines)
			if end > 5 {
				end = 5
			}
			contextLines = lines[:end]
		}

		return types.NewErrorResult("resource_not_found",
			fmt.Sprintf("Could not find the exact text in %s", path)).
			WithParameter("old_string", oldText).
			WithContext(map[string]interface{}{
				"file_preview": contextLines,
				"file_size":    len(content),
				"line_count":   len(lines),
			}).
			WithSuggestions([]string{
				"Check the text matches exactly including whitespace",
				"Use 'Read' tool to view file content first",
				"Copy text directly from the file to ensure exact match",
				"Text search is case-sensitive and whitespace-sensitive",
			}), nil
	}

	// Count occurrences for uniqueness check
	occurrences := countOccurrences(matchResult.contentForReplacement, normalizedOldText)
	if occurrences > 1 {
		return types.NewErrorResult("resource_conflict",
			fmt.Sprintf("Found %d occurrences of the text in %s", occurrences, path)).
			WithParameter("old_string", oldText).
			WithContext(map[string]interface{}{
				"occurrences": occurrences,
			}).
			WithSuggestions([]string{
				"Provide more context to make the text unique",
				"Include surrounding lines in the old_string",
				"Use a more specific text pattern",
			}), nil
	}

	// Perform the replacement
	baseContent := matchResult.contentForReplacement
	newContent := baseContent[:matchResult.index] + normalizedNewText + baseContent[matchResult.index+matchResult.matchLength:]

	// Check if anything changed
	if baseContent == newContent {
		return types.NewErrorResult("resource_conflict",
			fmt.Sprintf("No changes made to %s", path)).
			WithParameter("new_string", newText).
			WithSuggestions([]string{
				"Provide different replacement text",
				"Check if old_string and new_string are identical",
			}), nil
	}

	// Restore line endings and BOM
	finalContent := bom + restoreLineEndings(newContent, originalEnding)

	// Write the file with rich error handling
	if err := os.WriteFile(path, []byte(finalContent), 0644); err != nil {
		errorType := "permission_denied"
		suggestions := []string{
			"Check file write permissions",
			"Ensure directory is writable",
		}

		if strings.Contains(err.Error(), "permission denied") {
			suggestions = append(suggestions, "Run with appropriate permissions")
		} else if strings.Contains(err.Error(), "no space left") {
			errorType = "insufficient_quota"
			suggestions = []string{"Free up disk space", "Use a different location"}
		}

		return types.NewErrorResult(errorType,
			fmt.Sprintf("Failed to write file: %v", err)).
			WithParameter("path", path).
			WithSuggestions(suggestions), nil
	}

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Successfully replaced text in %s.", path),
		Data: map[string]interface{}{
			"path":           path,
			"usedFuzzyMatch": matchResult.usedFuzzyMatch,
		},
	}, nil
}

// matchResult holds the result of text search
type matchResult struct {
	found                 bool
	index                 int
	matchLength           int
	usedFuzzyMatch        bool
	contentForReplacement string
}

// detectLineEnding detects the line ending style used in the content
func detectLineEnding(content string) string {
	crlfIdx := strings.Index(content, "\r\n")
	lfIdx := strings.Index(content, "\n")

	if lfIdx == -1 {
		return "\n"
	}
	if crlfIdx == -1 {
		return "\n"
	}
	if crlfIdx < lfIdx {
		return "\r\n"
	}
	return "\n"
}

// normalizeToLF converts all line endings to LF
func normalizeToLF(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return text
}

// restoreLineEndings converts LF back to the original line ending style
func restoreLineEndings(text string, ending string) string {
	if ending == "\r\n" {
		return strings.ReplaceAll(text, "\n", "\r\n")
	}
	return text
}

// normalizeForFuzzyMatch normalizes text for fuzzy matching
func normalizeForFuzzyMatch(text string) string {
	// Strip trailing whitespace per line
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	text = strings.Join(lines, "\n")

	// Smart single quotes → '
	smartSingleQuotes := regexp.MustCompile(`[\x{2018}\x{2019}\x{201A}\x{201B}]`)
	text = smartSingleQuotes.ReplaceAllString(text, "'")

	// Smart double quotes → "
	smartDoubleQuotes := regexp.MustCompile(`[\x{201C}\x{201D}\x{201E}\x{201F}]`)
	text = smartDoubleQuotes.ReplaceAllString(text, "\"")

	// Various dashes/hyphens → -
	dashes := regexp.MustCompile(`[\x{2010}\x{2011}\x{2012}\x{2013}\x{2014}\x{2015}\x{2212}]`)
	text = dashes.ReplaceAllString(text, "-")

	// Special spaces → regular space
	spaces := regexp.MustCompile(`[\x{00A0}\x{2002}-\x{200A}\x{202F}\x{205F}\x{3000}]`)
	text = spaces.ReplaceAllString(text, " ")

	return text
}

// fuzzyFindText tries exact match first, then fuzzy match
func fuzzyFindText(content, oldText string) matchResult {
	// Try exact match first
	exactIndex := strings.Index(content, oldText)
	if exactIndex != -1 {
		return matchResult{
			found:                 true,
			index:                 exactIndex,
			matchLength:           len(oldText),
			usedFuzzyMatch:        false,
			contentForReplacement: content,
		}
	}

	// Try fuzzy match
	fuzzyContent := normalizeForFuzzyMatch(content)
	fuzzyOldText := normalizeForFuzzyMatch(oldText)

	fuzzyIndex := strings.Index(fuzzyContent, fuzzyOldText)
	if fuzzyIndex == -1 {
		return matchResult{
			found:                 false,
			index:                 -1,
			matchLength:           0,
			usedFuzzyMatch:        false,
			contentForReplacement: content,
		}
	}

	// When fuzzy matching, work in normalized space for replacement
	return matchResult{
		found:                 true,
		index:                 fuzzyIndex,
		matchLength:           len(fuzzyOldText),
		usedFuzzyMatch:        true,
		contentForReplacement: fuzzyContent,
	}
}

// countOccurrences counts how many times needle appears in haystack (using fuzzy matching)
func countOccurrences(haystack, needle string) int {
	fuzzyHaystack := normalizeForFuzzyMatch(haystack)
	fuzzyNeedle := normalizeForFuzzyMatch(needle)

	if len(fuzzyNeedle) == 0 {
		return 0
	}

	return strings.Count(fuzzyHaystack, fuzzyNeedle)
}

func (t *EditTool) getStringArg(args map[string]interface{}, key, defaultVal string) string {
	if val, ok := args[key].(string); ok {
		return val
	}
	return defaultVal
}

// GetSchemaHints implements types.EnhancedSchemaProvider.
func (t *EditTool) GetSchemaHints() map[string]schema.SchemaHints {
	return map[string]schema.SchemaHints{
		"path": {
			Examples: []interface{}{
				"README.md",
				"src/main.go",
				"/absolute/path/to/file.txt",
			},
			DiscoveryType:     "workspace_paths",
			EnumFromDiscovery: false,
			ValidationHints: []string{
				"Relative paths resolve from workspace directory",
				"File must exist and be readable",
				"Use Read tool first to see current content",
			},
		},
		"old_string": {
			Examples: []interface{}{
				"function oldName() {",
				"const VERSION = \"1.0.0\"",
				"// TODO: implement this",
			},
			ValidationHints: []string{
				"Must match exactly including whitespace and newlines",
				"Text must be unique in the file",
				"Use exact copy from Read tool output",
				"Fuzzy matching handles smart quotes and special characters",
			},
		},
		"new_string": {
			Examples: []interface{}{
				"function newName() {",
				"const VERSION = \"2.0.0\"",
				"// DONE: implemented successfully",
			},
			ValidationHints: []string{
				"Replacement text for the old_string",
				"Can be empty string for deletion",
				"Maintains file's original line ending style",
			},
		},
	}
}
