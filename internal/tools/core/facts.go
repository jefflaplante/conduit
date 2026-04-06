package core

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"conduit/internal/config"
	toolargs "conduit/internal/tools/args"
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
	category := toolargs.GetString(args, "category", "")
	maxFacts := toolargs.GetInt(args, "maxFacts", 50)

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

// SelfTest implements types.SelfTester for FactsTool.
func (t *FactsTool) SelfTest(ctx context.Context, opts *types.SelfTestOptions) *types.SelfTestResult {
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

	// Check workspace directory configuration
	workspaceDep := types.DependencyStatus{
		Name:     "WorkspaceDir",
		Required: true,
	}

	if t.workspaceDir == "" {
		workspaceDep.Available = false
		workspaceDep.Status = "not_configured"
		workspaceDep.Message = "Workspace directory not configured"
		result.Status = types.SelfTestStatusFailed
		result.Message = "Facts tool requires a workspace directory"
		result.Suggestions = []string{
			"Configure workspace_dir in sandbox settings",
			"Check config.json tools.sandbox.workspace_dir",
		}
	} else {
		// Check if workspace directory exists
		if info, err := os.Stat(t.workspaceDir); err != nil {
			workspaceDep.Available = false
			workspaceDep.Status = "error"
			workspaceDep.Message = fmt.Sprintf("Workspace directory error: %v", err)
			result.Status = types.SelfTestStatusFailed
			result.Message = "Workspace directory is not accessible"
			result.Suggestions = []string{
				"Check workspace directory exists",
				"Verify read permissions on workspace",
			}
		} else if !info.IsDir() {
			workspaceDep.Available = false
			workspaceDep.Status = "invalid"
			workspaceDep.Message = "Workspace path is not a directory"
			result.Status = types.SelfTestStatusFailed
			result.Message = "Workspace path is not a directory"
		} else {
			workspaceDep.Available = true
			workspaceDep.Status = "available"
			result.Capabilities = []string{
				"extract_markdown_facts",
				"categorize_facts",
				"filter_by_category",
				"parse_key_value_patterns",
			}
		}
	}
	deps = append(deps, workspaceDep)

	// Check for memory files (informational, not required)
	if result.Status != types.SelfTestStatusFailed {
		memoryPaths, err := t.getMemoryFilePaths()
		if err == nil && len(memoryPaths) > 0 {
			result.Capabilities = append(result.Capabilities, "memory_files_found")
			if opts.Verbose {
				result.Details = map[string]interface{}{
					"workspace_dir":      t.workspaceDir,
					"memory_files_count": len(memoryPaths),
					"memory_files":       memoryPaths,
				}
			}
		} else {
			// Memory files not found — still functional but note it
			if opts.Verbose {
				result.Details = map[string]interface{}{
					"workspace_dir":      t.workspaceDir,
					"memory_files_count": 0,
					"note":               "No MEMORY.md or memory/*.md files found",
				}
			}
		}
	}

	result.Dependencies = deps

	// Set final status and message if not already failed
	if result.Status != types.SelfTestStatusFailed {
		result.Status = types.SelfTestStatusOK
		result.Message = "Facts tool is fully functional"
	}

	result.TestDuration = time.Since(start)

	if opts.IncludeExamples && result.IsFunctional() {
		result.Examples = []types.ToolExample{
			{
				Name:        "Extract all facts",
				Description: "Get all facts from memory files",
				Args:        map[string]interface{}{},
				Expected:    "List of categorized facts from MEMORY.md and memory/*.md",
			},
			{
				Name:        "Filter by category",
				Description: "Get only facts in the 'preferences' category",
				Args: map[string]interface{}{
					"category": "preferences",
				},
				Expected: "Facts filtered to preferences category only",
			},
			{
				Name:        "Limit results",
				Description: "Get at most 10 facts",
				Args: map[string]interface{}{
					"maxFacts": 10,
				},
				Expected: "Up to 10 facts from memory files",
			},
		}
	}

	return result
}

