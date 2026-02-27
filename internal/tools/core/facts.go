package core

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"conduit/internal/config"
	"conduit/internal/tools/types"
)

// Fact represents a single extracted fact
type Fact struct {
	Content  string `json:"content"`
	Category string `json:"category"`
	Source   string `json:"source"`
}

// FactsTool extracts structured facts from memory files
type FactsTool struct {
	services     *types.ToolServices
	sandboxCfg   config.SandboxConfig
	workspaceDir string
}

// knowledgeHeaders are words that indicate a header contains extractable facts.
var knowledgeHeaders = []string{
	"facts", "knowledge", "preferences", "notes", "learned",
	"remember", "context", "important", "key",
}

// keyValuePattern matches **key**: value or key: value at line start.
var keyValueBoldPattern = regexp.MustCompile(`^\*\*(.+?)\*\*:\s*(.+)`)
var keyValuePlainPattern = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9 _-]+):\s+(.+)`)

func NewFactsTool(services *types.ToolServices, sandboxCfg config.SandboxConfig) *FactsTool {
	return &FactsTool{
		services:     services,
		sandboxCfg:   sandboxCfg,
		workspaceDir: sandboxCfg.WorkspaceDir,
	}
}

func (t *FactsTool) Name() string {
	return "Facts"
}

func (t *FactsTool) Description() string {
	return "Extract structured facts and knowledge from memory files. Returns categorized facts from markdown files in the workspace memory system."
}

func (t *FactsTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"category": map[string]interface{}{
				"type":        "string",
				"description": "Filter facts by category (e.g., \"preferences\", \"technical\", \"people\")",
			},
			"maxFacts": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum number of facts to return",
				"default":     50,
			},
		},
	}
}

func (t *FactsTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	category := t.getStringArg(args, "category", "")
	maxFacts := t.getIntArg(args, "maxFacts", 50)

	// Get memory file paths (same pattern as MemorySearchTool)
	memoryPaths, err := t.getMemoryFilePaths()
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to get memory file paths: %v", err),
		}, nil
	}

	if len(memoryPaths) == 0 {
		return &types.ToolResult{
			Success: true,
			Content: "No memory files found in workspace.",
			Data: map[string]interface{}{
				"facts":      []Fact{},
				"total":      0,
				"categories": []string{},
			},
		}, nil
	}

	// Extract facts from all memory files
	var allFacts []Fact
	for _, path := range memoryPaths {
		facts, err := t.extractFactsFromFile(path)
		if err != nil {
			continue
		}
		allFacts = append(allFacts, facts...)
	}

	// Apply category filter
	if category != "" {
		categoryLower := strings.ToLower(category)
		var filtered []Fact
		for _, fact := range allFacts {
			if strings.ToLower(fact.Category) == categoryLower {
				filtered = append(filtered, fact)
			}
		}
		allFacts = filtered
	}

	// Limit results
	if len(allFacts) > maxFacts {
		allFacts = allFacts[:maxFacts]
	}

	// Collect unique categories
	categorySet := make(map[string]struct{})
	for _, fact := range allFacts {
		categorySet[fact.Category] = struct{}{}
	}
	var categories []string
	for cat := range categorySet {
		categories = append(categories, cat)
	}

	// Format output
	content := t.formatFacts(allFacts)

	return &types.ToolResult{
		Success: true,
		Content: content,
		Data: map[string]interface{}{
			"facts":      allFacts,
			"total":      len(allFacts),
			"categories": categories,
		},
	}, nil
}

// getMemoryFilePaths returns paths to memory files (MEMORY.md + memory/*.md)
func (t *FactsTool) getMemoryFilePaths() ([]string, error) {
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

// extractFactsFromFile parses a markdown file and extracts facts
func (t *FactsTool) extractFactsFromFile(filePath string) ([]Fact, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	relPath := filePath
	if strings.HasPrefix(filePath, t.workspaceDir) {
		relPath = strings.TrimPrefix(filePath, t.workspaceDir)
		if len(relPath) > 0 && relPath[0] == '/' {
			relPath = relPath[1:]
		}
	}

	lines := strings.Split(string(content), "\n")
	var facts []Fact
	currentHeader := ""
	isKnowledgeSection := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Check for headers (## or ###)
		if strings.HasPrefix(trimmed, "## ") || strings.HasPrefix(trimmed, "### ") {
			currentHeader = strings.TrimLeft(trimmed, "# ")
			currentHeader = strings.TrimSpace(currentHeader)
			isKnowledgeSection = t.isKnowledgeHeader(currentHeader)
			continue
		}

		// Extract bullet facts under knowledge headers
		if isKnowledgeSection {
			if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
				factContent := strings.TrimSpace(trimmed[2:])
				if factContent != "" {
					facts = append(facts, Fact{
						Content:  factContent,
						Category: t.normalizeCategory(currentHeader),
						Source:   relPath,
					})
				}
				continue
			}
		}

		// Extract key-value patterns anywhere in the file
		if matches := keyValueBoldPattern.FindStringSubmatch(trimmed); matches != nil {
			category := t.normalizeCategory(currentHeader)
			if category == "" {
				category = "general"
			}
			facts = append(facts, Fact{
				Content:  fmt.Sprintf("%s: %s", matches[1], matches[2]),
				Category: category,
				Source:   relPath,
			})
			continue
		}

		if matches := keyValuePlainPattern.FindStringSubmatch(trimmed); matches != nil {
			// Skip markdown headers and other non-fact patterns
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			category := t.normalizeCategory(currentHeader)
			if category == "" {
				category = "general"
			}
			facts = append(facts, Fact{
				Content:  fmt.Sprintf("%s: %s", matches[1], matches[2]),
				Category: category,
				Source:   relPath,
			})
		}
	}

	return facts, nil
}

// isKnowledgeHeader checks if a header text suggests it contains facts
func (t *FactsTool) isKnowledgeHeader(header string) bool {
	headerLower := strings.ToLower(header)
	for _, keyword := range knowledgeHeaders {
		if strings.Contains(headerLower, keyword) {
			return true
		}
	}
	return false
}

// normalizeCategory converts a header to a clean category name
func (t *FactsTool) normalizeCategory(header string) string {
	if header == "" {
		return ""
	}
	// Lowercase and remove punctuation
	lower := strings.ToLower(header)
	var cleaned strings.Builder
	for _, r := range lower {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' {
			cleaned.WriteRune(r)
		}
	}
	return strings.TrimSpace(cleaned.String())
}

// formatFacts formats facts for human-readable display
func (t *FactsTool) formatFacts(facts []Fact) string {
	if len(facts) == 0 {
		return "No facts found."
	}

	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("Found %d facts:\n\n", len(facts)))

	currentCategory := ""
	for i, fact := range facts {
		if fact.Category != currentCategory {
			currentCategory = fact.Category
			builder.WriteString(fmt.Sprintf("### %s\n", currentCategory))
		}
		builder.WriteString(fmt.Sprintf("%d. %s", i+1, fact.Content))
		builder.WriteString(fmt.Sprintf("  [source: %s]\n", fact.Source))
	}

	return builder.String()
}

// Helper methods (same pattern as MemorySearchTool)
func (t *FactsTool) getIntArg(args map[string]interface{}, key string, defaultVal int) int {
	if val, ok := args[key].(float64); ok {
		return int(val)
	}
	if val, ok := args[key].(int); ok {
		return val
	}
	return defaultVal
}

func (t *FactsTool) getStringArg(args map[string]interface{}, key string, defaultVal string) string {
	if val, ok := args[key].(string); ok {
		return val
	}
	return defaultVal
}
