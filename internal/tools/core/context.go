package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
)

// sectionResult holds the output from collecting a single section.
type sectionResult struct {
	name    string
	data    map[string]interface{}
	content string
	err     error
}

// ContextTool provides workspace, session, project, and gateway context for agent orientation.
// It aggregates status information from multiple sources into a single tool call, giving the
// agent a complete picture of its operating environment.
//
// Features:
//   - Parallel execution: sections are gathered concurrently via goroutines
//   - Smart caching: two-tier cache (static 5m, dynamic 30s) for repeated calls
//   - Graceful error handling: individual section failures produce partial results, never a full failure
//   - Context-aware intelligence: pattern recognition and actionable suggestions
//   - Memory integration: cross-references beads tasks with memory/search when available
//   - Rich output formatting: markdown tables, highlighted suggestions, progress indicators
type ContextTool struct {
	services *types.ToolServices
	cache    *contextCache
}

func NewContextTool(services *types.ToolServices) *ContextTool {
	return &ContextTool{
		services: services,
		cache:    newContextCache(),
	}
}

func (t *ContextTool) Name() string {
	return "Context"
}

func (t *ContextTool) Description() string {
	return "Get workspace, session, project, and gateway context for orientation. Returns environment status including git info, active session, enabled tools, beads tickets, and gateway health. Use with no arguments for a full overview, or specify a section for focused detail."
}

func (t *ContextTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"section": map[string]interface{}{
				"type": "string",
				"enum": []string{
					"workspace", "project", "session", "gateway", "channels", "tools", "beads",
				},
				"description": "Specific section to return. Omit for all sections.",
			},
			"verbose": map[string]interface{}{
				"type":        "boolean",
				"description": "Include extra detail in output (e.g., full commit messages, all config fields)",
				"default":     false,
			},
		},
	}
}

func (t *ContextTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	section := toolargs.GetString(args, "section", "")
	verbose := toolargs.GetBool(args, "verbose", false)

	// Determine which sections to collect
	sections := []string{"workspace", "project", "session", "gateway", "channels", "tools", "beads"}
	if section != "" {
		sections = []string{section}
	}

	// (.1) Parallel execution: collect all sections concurrently
	results := t.collectSectionsParallel(ctx, sections, verbose)

	data := make(map[string]interface{})
	var contentParts []string

	// Assemble results in order (preserving section ordering).
	// Skip entries that have nil data (e.g. unrecognized section names).
	for _, s := range sections {
		if res, ok := results[s]; ok && res.data != nil {
			data[s] = res.data
			contentParts = append(contentParts, res.content)
		}
	}

	content := strings.Join(contentParts, "\n")

	// (.4/.5) Intelligence and memory: add suggestions section when returning all sections
	if section == "" {
		suggestions := t.generateSuggestions(data)
		memoryInsights := t.gatherMemoryInsights(ctx, data)
		if len(suggestions) > 0 || len(memoryInsights) > 0 {
			suggestionsContent := t.formatSuggestions(suggestions, memoryInsights)
			content += "\n" + suggestionsContent
			data["suggestions"] = suggestions
			if len(memoryInsights) > 0 {
				data["memory_insights"] = memoryInsights
			}
		}
	}

	return &types.ToolResult{
		Success: true,
		Content: content,
		Data:    data,
	}, nil
}

// collectSectionsParallel runs all section collectors concurrently using goroutines.
// Each section collection is independent and errors in one section do not affect others.
func (t *ContextTool) collectSectionsParallel(ctx context.Context, sections []string, verbose bool) map[string]*sectionResult {
	resultsCh := make(chan *sectionResult, len(sections))
	var wg sync.WaitGroup

	for _, s := range sections {
		wg.Add(1)
		go func(sectionName string) {
			defer wg.Done()
			res := t.collectSectionSafe(ctx, sectionName, verbose)
			resultsCh <- res
		}(s)
	}

	// Wait for all goroutines, then close channel
	wg.Wait()
	close(resultsCh)

	// Collect results
	results := make(map[string]*sectionResult)
	for res := range resultsCh {
		results[res.name] = res
	}

	return results
}

// collectSectionSafe wraps section collection with panic recovery for graceful error handling.
// If a section panics or returns an error, a degraded result is returned instead of failing.
func (t *ContextTool) collectSectionSafe(ctx context.Context, sectionName string, verbose bool) (res *sectionResult) {
	// (.3) Graceful error handling: recover from panics
	defer func() {
		if r := recover(); r != nil {
			res = &sectionResult{
				name:    sectionName,
				data:    map[string]interface{}{"error": fmt.Sprintf("internal error: %v", r)},
				content: fmt.Sprintf("## %s\n\n(section unavailable due to internal error)\n\n", titleCase(sectionName)),
				err:     fmt.Errorf("panic in %s: %v", sectionName, r),
			}
		}
	}()

	var sectionData map[string]interface{}
	var content string

	switch sectionName {
	case "workspace":
		sectionData, content = t.collectWorkspace(verbose)
	case "project":
		sectionData, content = t.collectProject(verbose)
	case "session":
		sectionData, content = t.collectSession(ctx)
	case "gateway":
		sectionData, content = t.collectGateway(verbose)
	case "channels":
		sectionData, content = t.collectChannels()
	case "tools":
		sectionData, content = t.collectTools()
	case "beads":
		sectionData, content = t.collectBeads(verbose)
	default:
		// Unknown section: return nil data so it is excluded from final output
		return &sectionResult{name: sectionName, data: nil, content: ""}
	}

	return &sectionResult{
		name:    sectionName,
		data:    sectionData,
		content: content,
	}
}

// getWorkspaceDir returns the workspace directory from config, falling back to cwd.
func (t *ContextTool) getWorkspaceDir() string {
	if t.services.ConfigMgr != nil {
		dir := t.services.ConfigMgr.Tools.Sandbox.WorkspaceDir
		if dir != "" {
			// Resolve to absolute path
			if !filepath.IsAbs(dir) {
				if abs, err := filepath.Abs(dir); err == nil {
					return abs
				}
			}
			return dir
		}
	}

	// Fallback to current working directory
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}

	return "."
}

// titleCase converts the first character of a string to upper case. This avoids the
// deprecated strings.Title function which has been obsoleted since Go 1.18.
func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// formatDuration formats a time.Duration into a human-readable string like "2h 30m" or "3d".
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		hours := int(d.Hours())
		mins := int(d.Minutes()) % 60
		if mins > 0 {
			return fmt.Sprintf("%dh %dm", hours, mins)
		}
		return fmt.Sprintf("%dh", hours)
	}
	days := int(d.Hours()) / 24
	return fmt.Sprintf("%dd", days)
}

// GetUsageExamples implements types.UsageExampleProvider.
func (t *ContextTool) GetUsageExamples() []types.ToolExample {
	return []types.ToolExample{
		{
			Name:        "Full context overview",
			Description: "Get a complete overview of workspace, project, session, and gateway status",
			Args:        map[string]interface{}{},
			Expected:    "Returns all context sections: workspace, project (git), session, gateway, channels, tools, beads",
		},
		{
			Name:        "Check project git status",
			Description: "Get git branch, dirty status, and recent commits",
			Args: map[string]interface{}{
				"section": "project",
			},
			Expected: "Returns git branch name, dirty/clean status, and recent commit history",
		},
		{
			Name:        "Verbose beads ticket status",
			Description: "Get detailed beads issue tracker information with open issue list",
			Args: map[string]interface{}{
				"section": "beads",
				"verbose": true,
			},
			Expected: "Returns beads detection, issue counts, and a list of all open/in-progress issues",
		},
		{
			Name:        "Check current session",
			Description: "Get information about the current active session",
			Args: map[string]interface{}{
				"section": "session",
			},
			Expected: "Returns session key, channel ID, user ID, and message count",
		},
	}
}

// SelfTest implements types.SelfTester for ContextTool.
func (t *ContextTool) SelfTest(ctx context.Context, opts *types.SelfTestOptions) *types.SelfTestResult {
	start := time.Now()

	if opts == nil {
		opts = types.DefaultSelfTestOptions()
	}

	result := &types.SelfTestResult{
		Status:       types.SelfTestStatusOK,
		Capabilities: []string{"workspace", "project", "session", "gateway", "channels", "tools", "beads"},
		TestedAt:     time.Now(),
	}

	deps := []types.DependencyStatus{}

	// Check ConfigMgr
	configDep := types.DependencyStatus{
		Name:     "ConfigMgr",
		Required: false,
	}
	if t.services != nil && t.services.ConfigMgr != nil {
		configDep.Available = true
		configDep.Status = "available"
	} else {
		configDep.Available = false
		configDep.Status = "not_configured"
	}
	deps = append(deps, configDep)

	// Check workspace directory
	workspaceDep := types.DependencyStatus{
		Name:     "WorkspaceDir",
		Required: false,
	}
	workDir := t.getWorkspaceDir()
	if info, err := os.Stat(workDir); err == nil && info.IsDir() {
		workspaceDep.Available = true
		workspaceDep.Status = "exists"
	} else {
		workspaceDep.Available = false
		workspaceDep.Status = "not_found"
		workspaceDep.Message = "Workspace directory does not exist"
	}
	deps = append(deps, workspaceDep)

	// Check git availability
	gitDep := types.DependencyStatus{
		Name:     "Git",
		Required: false,
	}
	if _, err := t.runGit(workDir, "rev-parse", "--is-inside-work-tree"); err == nil {
		gitDep.Available = true
		gitDep.Status = "available"
	} else {
		gitDep.Available = false
		gitDep.Status = "not_available"
		gitDep.Message = "Not a git repository or git not installed"
	}
	deps = append(deps, gitDep)

	// Check Gateway service
	gatewayDep := types.DependencyStatus{
		Name:     "Gateway",
		Required: false,
	}
	if t.services != nil && t.services.Gateway != nil {
		gatewayDep.Available = true
		gatewayDep.Status = "available"
	} else {
		gatewayDep.Available = false
		gatewayDep.Status = "not_configured"
	}
	deps = append(deps, gatewayDep)

	result.Dependencies = deps
	result.TestDuration = time.Since(start)

	// Determine overall status
	result.Status = types.SelfTestStatusOK
	result.Message = "Context tool is fully functional"

	if opts.Verbose {
		result.Details = map[string]interface{}{
			"workspace_dir":  workDir,
			"cache_entries":  len(t.cache.entries),
			"config_present": t.services != nil && t.services.ConfigMgr != nil,
		}
	}

	if opts.IncludeExamples && result.IsFunctional() {
		result.Examples = t.GetUsageExamples()
	}

	return result
}
