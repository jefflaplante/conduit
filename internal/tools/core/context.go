package core

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"conduit/internal/tools/types"
	"conduit/internal/version"
)

// contextCache provides two-tier caching for context data.
// Static data (git repo info, project structure) uses a longer TTL (5 minutes),
// while dynamic data (active status, uncommitted changes) uses a shorter TTL (30 seconds).
type contextCache struct {
	mu      sync.RWMutex
	entries map[string]*cacheEntry
}

// cacheEntry holds a cached value with its expiration time.
type cacheEntry struct {
	value     interface{}
	expiresAt time.Time
}

// cacheTierStatic is the TTL for data that changes infrequently (5 minutes).
const cacheTierStatic = 5 * time.Minute

// cacheTierDynamic is the TTL for data that changes frequently (30 seconds).
const cacheTierDynamic = 30 * time.Second

func newContextCache() *contextCache {
	return &contextCache{
		entries: make(map[string]*cacheEntry),
	}
}

// get retrieves a cached value if it exists and has not expired.
func (c *contextCache) get(key string) (interface{}, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[key]
	if !ok || time.Now().After(entry.expiresAt) {
		return nil, false
	}
	return entry.value, true
}

// set stores a value in the cache with the given TTL.
func (c *contextCache) set(key string, value interface{}, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = &cacheEntry{
		value:     value,
		expiresAt: time.Now().Add(ttl),
	}
}

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
	section := t.getStringArg(args, "section", "")
	verbose := t.getBoolArg(args, "verbose", false)

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

// collectWorkspace returns workspace directory and sandbox configuration.
func (t *ContextTool) collectWorkspace(verbose bool) (map[string]interface{}, string) {
	result := make(map[string]interface{})
	var builder strings.Builder
	builder.WriteString("## Workspace\n\n")

	if t.services.ConfigMgr != nil {
		workspaceDir := t.services.ConfigMgr.Tools.Sandbox.WorkspaceDir
		contextDir := t.services.ConfigMgr.Workspace.ContextDir
		allowedPaths := t.services.ConfigMgr.Tools.Sandbox.AllowedPaths

		result["workspace_dir"] = workspaceDir
		result["context_dir"] = contextDir
		result["allowed_paths"] = allowedPaths

		builder.WriteString(fmt.Sprintf("Workspace Dir: %s\n", workspaceDir))
		if contextDir != "" {
			builder.WriteString(fmt.Sprintf("Context Dir: %s\n", contextDir))
		}

		if verbose && len(allowedPaths) > 0 {
			builder.WriteString(fmt.Sprintf("Allowed Paths: %s\n", strings.Join(allowedPaths, ", ")))
		}

		// Check if workspace dir exists
		if info, err := os.Stat(workspaceDir); err == nil && info.IsDir() {
			result["workspace_exists"] = true
		} else {
			result["workspace_exists"] = false
			builder.WriteString("  (workspace directory does not exist)\n")
		}
	} else {
		builder.WriteString("Configuration not available.\n")
		result["error"] = "config not available"
	}

	builder.WriteString("\n")
	return result, builder.String()
}

// collectProject returns git repository information by shelling out to git.
// Uses caching: branch/remote are static (5min), status/commits are dynamic (30s).
func (t *ContextTool) collectProject(verbose bool) (map[string]interface{}, string) {
	result := make(map[string]interface{})
	var builder strings.Builder
	builder.WriteString("## Project\n\n")

	workDir := t.getWorkspaceDir()

	// (.3) Graceful error handling: if git fails, return basic info
	branch, err := t.runGitCached(workDir, cacheTierStatic, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		result["git_available"] = false
		result["error"] = "not a git repository or git not available"
		builder.WriteString("Git: not available or not a repository\n\n")

		// (.3) Fallback: provide basic directory info instead
		t.appendBasicDirInfo(&builder, result, workDir)

		return result, builder.String()
	}

	result["git_available"] = true
	result["branch"] = branch
	builder.WriteString(fmt.Sprintf("Branch: %s\n", branch))

	// Check dirty status (dynamic cache - changes frequently)
	status, err := t.runGitCached(workDir, cacheTierDynamic, "status", "--porcelain")
	if err == nil {
		dirty := len(strings.TrimSpace(status)) > 0
		result["dirty"] = dirty
		if dirty {
			lines := strings.Split(strings.TrimSpace(status), "\n")
			result["changed_files"] = len(lines)
			builder.WriteString(fmt.Sprintf("Status: dirty (%d changed files)\n", len(lines)))
		} else {
			result["changed_files"] = 0
			builder.WriteString("Status: clean\n")
		}
	}

	// Get recent commits (dynamic cache)
	commitCount := "5"
	if verbose {
		commitCount = "10"
	}
	logFormat := "--oneline"
	if verbose {
		logFormat = "--format=%h %s (%cr)"
	}
	commits, err := t.runGitCached(workDir, cacheTierDynamic, "log", logFormat, "-n", commitCount)
	if err == nil && strings.TrimSpace(commits) != "" {
		commitLines := strings.Split(strings.TrimSpace(commits), "\n")
		result["recent_commits"] = commitLines
		builder.WriteString(fmt.Sprintf("\nRecent Commits (%d):\n", len(commitLines)))
		for _, line := range commitLines {
			builder.WriteString(fmt.Sprintf("  %s\n", line))
		}
	}

	// Get remote info (static cache)
	remote, err := t.runGitCached(workDir, cacheTierStatic, "remote", "get-url", "origin")
	if err == nil && strings.TrimSpace(remote) != "" {
		result["remote_origin"] = strings.TrimSpace(remote)
		if verbose {
			builder.WriteString(fmt.Sprintf("Remote: %s\n", strings.TrimSpace(remote)))
		}
	}

	// Get last commit timestamp for staleness detection (.4 intelligence)
	lastCommitTime, err := t.runGit(workDir, "log", "-1", "--format=%ct")
	if err == nil && strings.TrimSpace(lastCommitTime) != "" {
		result["last_commit_timestamp"] = strings.TrimSpace(lastCommitTime)
	}

	builder.WriteString("\n")
	return result, builder.String()
}

// collectSession returns current session context from the request.
func (t *ContextTool) collectSession(ctx context.Context) (map[string]interface{}, string) {
	result := make(map[string]interface{})
	var builder strings.Builder
	builder.WriteString("## Session\n\n")

	channelID := types.RequestChannelID(ctx)
	userID := types.RequestUserID(ctx)
	sessionKey := types.RequestSessionKey(ctx)

	result["channel_id"] = channelID
	result["user_id"] = userID
	result["session_key"] = sessionKey

	if sessionKey != "" {
		builder.WriteString(fmt.Sprintf("Session Key: %s\n", sessionKey))
	} else {
		builder.WriteString("Session Key: (not set)\n")
	}
	if channelID != "" {
		builder.WriteString(fmt.Sprintf("Channel ID: %s\n", channelID))
	}
	if userID != "" {
		builder.WriteString(fmt.Sprintf("User ID: %s\n", userID))
	}

	// If we have a session store and a session key, get additional info
	if t.services.SessionStore != nil && sessionKey != "" {
		session, err := t.services.SessionStore.GetSession(sessionKey)
		if err == nil && session != nil {
			result["message_count"] = session.MessageCount
			result["created_at"] = session.CreatedAt
			result["updated_at"] = session.UpdatedAt
			builder.WriteString(fmt.Sprintf("Messages: %d\n", session.MessageCount))
			builder.WriteString(fmt.Sprintf("Created: %s\n", session.CreatedAt.Format("2006-01-02 15:04:05")))
			builder.WriteString(fmt.Sprintf("Last Active: %s\n", session.UpdatedAt.Format("2006-01-02 15:04:05")))
		}
	}

	builder.WriteString("\n")
	return result, builder.String()
}

// collectGateway returns gateway status, version, and health.
func (t *ContextTool) collectGateway(verbose bool) (map[string]interface{}, string) {
	result := make(map[string]interface{})
	var builder strings.Builder
	builder.WriteString("## Gateway\n\n")

	// Build info from version package
	buildInfo := version.GetBuildInfo()
	result["version"] = buildInfo.Version
	result["git_commit"] = buildInfo.GitCommit
	result["build_date"] = buildInfo.BuildDate
	result["go_version"] = buildInfo.GoVersion

	builder.WriteString(fmt.Sprintf("Version: %s\n", buildInfo.Version))
	if verbose {
		builder.WriteString(fmt.Sprintf("Git Commit: %s\n", buildInfo.GitCommit))
		builder.WriteString(fmt.Sprintf("Build Date: %s\n", buildInfo.BuildDate))
		builder.WriteString(fmt.Sprintf("Go Version: %s\n", buildInfo.GoVersion))
	}

	// Port from config
	if t.services.ConfigMgr != nil {
		result["port"] = t.services.ConfigMgr.Port
		builder.WriteString(fmt.Sprintf("Port: %d\n", t.services.ConfigMgr.Port))

		// Agent name
		if t.services.ConfigMgr.Agent.Name != "" {
			result["agent_name"] = t.services.ConfigMgr.Agent.Name
			builder.WriteString(fmt.Sprintf("Agent: %s\n", t.services.ConfigMgr.Agent.Name))
		}

		// AI provider
		result["ai_provider"] = t.services.ConfigMgr.AI.DefaultProvider
		builder.WriteString(fmt.Sprintf("AI Provider: %s\n", t.services.ConfigMgr.AI.DefaultProvider))

		// SSH status
		result["ssh_enabled"] = t.services.ConfigMgr.SSH.Enabled
		if t.services.ConfigMgr.SSH.Enabled {
			builder.WriteString(fmt.Sprintf("SSH: enabled (%s)\n", t.services.ConfigMgr.SSH.ListenAddr))
		}
	}

	// Gateway status from service
	if t.services.Gateway != nil {
		gwStatus, err := t.services.Gateway.GetGatewayStatus()
		if err == nil {
			if uptime, ok := gwStatus["uptime"]; ok {
				result["uptime"] = fmt.Sprintf("%v", uptime)
				builder.WriteString(fmt.Sprintf("Uptime: %v\n", uptime))
			}
			if health, ok := gwStatus["health"].(string); ok {
				result["health"] = health
				builder.WriteString(fmt.Sprintf("Health: %s\n", health))
			}
			if activeConns, ok := gwStatus["active_connections"]; ok {
				result["active_connections"] = activeConns
				builder.WriteString(fmt.Sprintf("Active Connections: %v\n", activeConns))
			}
		}

		if verbose {
			metrics, err := t.services.Gateway.GetMetrics()
			if err == nil {
				result["metrics"] = metrics
				if rpm, ok := metrics["requests_per_minute"].(float64); ok {
					builder.WriteString(fmt.Sprintf("Requests/min: %.1f\n", rpm))
				}
				if totalTokens, ok := metrics["total_tokens"].(int64); ok {
					builder.WriteString(fmt.Sprintf("Total Tokens: %d\n", totalTokens))
				}
			}
		}
	}

	builder.WriteString("\n")
	return result, builder.String()
}

// collectChannels returns channel adapter status.
func (t *ContextTool) collectChannels() (map[string]interface{}, string) {
	result := make(map[string]interface{})
	var builder strings.Builder
	builder.WriteString("## Channels\n\n")

	if t.services.Gateway != nil {
		channels, err := t.services.Gateway.GetChannelStatus()
		if err == nil && len(channels) > 0 {
			result["channels"] = channels
			for name, info := range channels {
				if channelInfo, ok := info.(map[string]interface{}); ok {
					status := "unknown"
					if s, ok := channelInfo["status"].(string); ok {
						status = s
					}
					enabled := false
					if e, ok := channelInfo["enabled"].(bool); ok {
						enabled = e
					}
					builder.WriteString(fmt.Sprintf("  %s: %s (enabled: %t)\n", name, status, enabled))
				} else {
					builder.WriteString(fmt.Sprintf("  %s: %v\n", name, info))
				}
			}
		} else if err != nil {
			result["error"] = fmt.Sprintf("failed to get channel status: %v", err)
			builder.WriteString(fmt.Sprintf("Error: %v\n", err))
		} else {
			builder.WriteString("No channels configured.\n")
		}
	} else {
		// Fall back to config if gateway service not available
		if t.services.ConfigMgr != nil {
			configChannels := t.services.ConfigMgr.Channels
			result["configured"] = len(configChannels)
			builder.WriteString(fmt.Sprintf("Configured channels: %d\n", len(configChannels)))
			for _, ch := range configChannels {
				builder.WriteString(fmt.Sprintf("  %s (%s): enabled=%t\n", ch.Name, ch.Type, ch.Enabled))
			}
		} else {
			builder.WriteString("Channel status not available.\n")
		}
	}

	builder.WriteString("\n")
	return result, builder.String()
}

// collectTools returns the list of enabled tools from configuration.
func (t *ContextTool) collectTools() (map[string]interface{}, string) {
	result := make(map[string]interface{})
	var builder strings.Builder
	builder.WriteString("## Tools\n\n")

	if t.services.ConfigMgr != nil {
		enabledTools := t.services.ConfigMgr.Tools.EnabledTools
		maxChains := t.services.ConfigMgr.Tools.MaxToolChains

		result["enabled_tools"] = enabledTools
		result["count"] = len(enabledTools)
		result["max_tool_chains"] = maxChains

		builder.WriteString(fmt.Sprintf("Enabled: %d tools\n", len(enabledTools)))
		builder.WriteString(fmt.Sprintf("Max Tool Chains: %d\n", maxChains))

		if len(enabledTools) > 0 {
			builder.WriteString(fmt.Sprintf("Tools: %s\n", strings.Join(enabledTools, ", ")))
		}
	} else {
		builder.WriteString("Configuration not available.\n")
		result["error"] = "config not available"
	}

	builder.WriteString("\n")
	return result, builder.String()
}

// collectBeads detects beads and reports issue status by parsing JSONL files
// directly, with optional enrichment from br CLI commands when available.
func (t *ContextTool) collectBeads(verbose bool) (map[string]interface{}, string) {
	result := make(map[string]interface{})
	var builder strings.Builder
	builder.WriteString("## Beads\n\n")

	workDir := t.getWorkspaceDir()
	beadsDir := filepath.Join(workDir, ".beads")

	// Check if .beads directory exists
	info, err := os.Stat(beadsDir)
	if err != nil || !info.IsDir() {
		result["detected"] = false
		builder.WriteString("No .beads directory detected.\n\n")
		return result, builder.String()
	}

	result["detected"] = true
	result["path"] = beadsDir
	builder.WriteString(fmt.Sprintf("Beads Dir: %s\n", beadsDir))

	// Load metadata.json if present
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	var metadata map[string]interface{}
	if data, err := os.ReadFile(metadataPath); err == nil {
		if json.Unmarshal(data, &metadata) == nil && len(metadata) > 0 {
			result["metadata"] = metadata
			if backend, ok := metadata["backend"].(string); ok {
				builder.WriteString(fmt.Sprintf("Backend: %s\n", backend))
			}
		}
	}

	// Determine the JSONL file path.
	// Priority: metadata.jsonl_export (relative to workDir) -> .beads/issues.jsonl -> workspace root issues.jsonl
	jsonlPath := ""
	if metadata != nil {
		if exportPath, ok := metadata["jsonl_export"].(string); ok && exportPath != "" {
			candidate := exportPath
			if !filepath.IsAbs(candidate) {
				candidate = filepath.Join(workDir, candidate)
			}
			if _, err := os.Stat(candidate); err == nil {
				jsonlPath = candidate
			}
		}
	}
	if jsonlPath == "" {
		candidate := filepath.Join(beadsDir, "issues.jsonl")
		if _, err := os.Stat(candidate); err == nil {
			jsonlPath = candidate
		}
	}
	if jsonlPath == "" {
		candidate := filepath.Join(workDir, "issues.jsonl")
		if _, err := os.Stat(candidate); err == nil {
			jsonlPath = candidate
		}
	}

	// Parse JSONL issues
	if jsonlPath != "" {
		issues, allParsed := t.parseIssuesJSONL(jsonlPath)
		issuesData := t.summarizeIssues(issues, allParsed, verbose)
		result["issues"] = issuesData

		total, _ := issuesData["total"].(int)
		openCount, _ := issuesData["open"].(int)
		inProgressCount, _ := issuesData["in_progress"].(int)
		closedCount, _ := issuesData["closed"].(int)

		builder.WriteString(fmt.Sprintf("%d total, %d open, %d in-progress, %d closed\n",
			total, openCount, inProgressCount, closedCount))

		if verbose {
			if openIssues, ok := issuesData["open_issues"].([]map[string]interface{}); ok && len(openIssues) > 0 {
				builder.WriteString("\nOpen Issues:\n")
				for _, oi := range openIssues {
					line := fmt.Sprintf("  %s: %s", oi["id"], oi["title"])
					if assignee, ok := oi["assignee"].(string); ok && assignee != "" {
						line += fmt.Sprintf(" (@%s)", assignee)
					}
					builder.WriteString(line + "\n")
				}
			}
		}
	}

	// Optionally enrich with br CLI output (non-fatal if unavailable)
	if ver, err := t.runBeadsCommand(workDir, "--version"); err == nil {
		result["version"] = strings.TrimSpace(ver)
	}

	builder.WriteString("\n")
	return result, builder.String()
}

// ---------- Intelligence (.4) ----------

// generateSuggestions analyzes collected context data and produces actionable suggestions.
// It examines project state, beads issues, and workspace configuration to detect
// common patterns that merit the agent's attention.
func (t *ContextTool) generateSuggestions(data map[string]interface{}) []string {
	var suggestions []string

	// Pattern: many dirty files + stale last commit => suggest committing
	if projData, ok := data["project"].(map[string]interface{}); ok {
		dirty, _ := projData["dirty"].(bool)
		changedFiles, _ := projData["changed_files"].(int)

		if dirty && changedFiles >= 5 {
			suggestions = append(suggestions, fmt.Sprintf(
				"You have %d uncommitted changes. Consider committing or stashing your work.", changedFiles))
		}

		// Detect stale commit (more than 24 hours since last commit while dirty)
		if dirty {
			if tsStr, ok := projData["last_commit_timestamp"].(string); ok && tsStr != "" {
				var ts int64
				if _, err := fmt.Sscanf(tsStr, "%d", &ts); err == nil {
					lastCommit := time.Unix(ts, 0)
					if time.Since(lastCommit) > 24*time.Hour {
						suggestions = append(suggestions, fmt.Sprintf(
							"Last commit was %s ago with uncommitted changes. Consider committing.",
							formatDuration(time.Since(lastCommit))))
					}
				}
			}
		}
	}

	// Pattern: beads with blocked/stalled in-progress tasks
	if beadsData, ok := data["beads"].(map[string]interface{}); ok {
		if issuesData, ok := beadsData["issues"].(map[string]interface{}); ok {
			inProgress, _ := issuesData["in_progress"].(int)
			openCount, _ := issuesData["open"].(int)

			if inProgress > 3 {
				suggestions = append(suggestions, fmt.Sprintf(
					"%d tasks are in-progress. Consider finishing some before starting new work.", inProgress))
			}
			if openCount > 10 {
				suggestions = append(suggestions, fmt.Sprintf(
					"%d open tasks in backlog. Consider prioritizing or triaging.", openCount))
			}
		}
	}

	// Pattern: workspace directory does not exist
	if wsData, ok := data["workspace"].(map[string]interface{}); ok {
		if exists, ok := wsData["workspace_exists"].(bool); ok && !exists {
			suggestions = append(suggestions, "Workspace directory does not exist. Run 'make init' to set up.")
		}
	}

	return suggestions
}

// ---------- Memory Integration (.5) ----------

// gatherMemoryInsights cross-references current context with the memory/search system.
// If a SearchService is available, it queries for related content based on active beads tasks.
func (t *ContextTool) gatherMemoryInsights(ctx context.Context, data map[string]interface{}) []string {
	if t.services.Searcher == nil {
		return nil
	}

	var insights []string

	// Cross-reference in-progress beads tasks with memory
	if beadsData, ok := data["beads"].(map[string]interface{}); ok {
		if issuesData, ok := beadsData["issues"].(map[string]interface{}); ok {
			if openIssues, ok := issuesData["open_issues"].([]map[string]interface{}); ok {
				// Search for the first few in-progress issues to find related context
				searched := 0
				for _, issue := range openIssues {
					if searched >= 3 {
						break
					}
					title, _ := issue["title"].(string)
					id, _ := issue["id"].(string)
					if title == "" {
						continue
					}

					results, err := t.services.Searcher.Search(ctx, title, 2)
					if err != nil || len(results) == 0 {
						continue
					}

					for _, r := range results {
						insights = append(insights, fmt.Sprintf(
							"Task %s (%s): related content found in %s", id, title, r.Source))
					}
					searched++
				}
			}
		}
	}

	return insights
}

// ---------- Output Formatting (.6) ----------

// formatSuggestions renders the suggestions and memory insights section with rich formatting.
func (t *ContextTool) formatSuggestions(suggestions []string, memoryInsights []string) string {
	var builder strings.Builder

	if len(suggestions) > 0 {
		builder.WriteString("## Suggestions\n\n")
		for _, s := range suggestions {
			builder.WriteString(fmt.Sprintf("- **Action:** %s\n", s))
		}
		builder.WriteString("\n")
	}

	if len(memoryInsights) > 0 {
		builder.WriteString("## Memory Insights\n\n")
		for _, insight := range memoryInsights {
			builder.WriteString(fmt.Sprintf("- %s\n", insight))
		}
		builder.WriteString("\n")
	}

	return builder.String()
}

// ---------- Caching (.2) ----------

// runGitCached runs a git command and caches the result with the given TTL.
// The cache key is derived from the working directory and the command arguments.
func (t *ContextTool) runGitCached(dir string, ttl time.Duration, gitArgs ...string) (string, error) {
	cacheKey := "git:" + dir + ":" + strings.Join(gitArgs, " ")

	if cached, ok := t.cache.get(cacheKey); ok {
		if result, ok := cached.(string); ok {
			return result, nil
		}
	}

	result, err := t.runGit(dir, gitArgs...)
	if err != nil {
		return "", err
	}

	t.cache.set(cacheKey, result, ttl)
	return result, nil
}

// ---------- Graceful Error Handling (.3) ----------

// appendBasicDirInfo provides fallback directory information when git is not available.
func (t *ContextTool) appendBasicDirInfo(builder *strings.Builder, result map[string]interface{}, dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	fileCount := 0
	dirCount := 0
	for _, e := range entries {
		if e.IsDir() {
			dirCount++
		} else {
			fileCount++
		}
	}

	result["file_count"] = fileCount
	result["dir_count"] = dirCount
	builder.WriteString(fmt.Sprintf("Directory contains %d files and %d subdirectories\n", fileCount, dirCount))
}

// ---------- Parsing & Helpers ----------

// parseIssuesJSONL reads a JSONL file and returns parsed issue maps.
// Malformed lines and blank lines are silently skipped.
func (t *ContextTool) parseIssuesJSONL(path string) ([]map[string]interface{}, []map[string]interface{}) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil
	}
	defer f.Close()

	var issues []map[string]interface{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var issue map[string]interface{}
		if json.Unmarshal([]byte(line), &issue) == nil {
			issues = append(issues, issue)
		}
	}
	return issues, issues
}

// classifyStatus normalizes a beads issue status into one of: "open", "in_progress", "closed".
func classifyStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "in_progress", "in-progress", "active", "doing":
		return "in_progress"
	case "closed", "done", "resolved", "completed":
		return "closed"
	default:
		// open, new, backlog, todo, and anything unrecognized count as open
		return "open"
	}
}

// summarizeIssues builds the issues data map from parsed issues.
func (t *ContextTool) summarizeIssues(issues []map[string]interface{}, _ []map[string]interface{}, verbose bool) map[string]interface{} {
	data := make(map[string]interface{})
	total := len(issues)
	data["total"] = total

	var openCount, inProgressCount, closedCount int
	var openIssues []map[string]interface{}

	for _, issue := range issues {
		status, _ := issue["status"].(string)
		cat := classifyStatus(status)
		switch cat {
		case "open":
			openCount++
			openIssues = append(openIssues, issue)
		case "in_progress":
			inProgressCount++
			openIssues = append(openIssues, issue)
		case "closed":
			closedCount++
		}
	}

	data["open"] = openCount
	data["in_progress"] = inProgressCount
	data["closed"] = closedCount

	if verbose && len(openIssues) > 0 {
		var summaries []map[string]interface{}
		for _, oi := range openIssues {
			summary := map[string]interface{}{
				"id":    oi["id"],
				"title": oi["title"],
			}
			if status, ok := oi["status"].(string); ok {
				summary["status"] = status
			}
			if priority, ok := oi["priority"]; ok {
				summary["priority"] = priority
			}
			if assignee, ok := oi["assignee"].(string); ok && assignee != "" {
				summary["assignee"] = assignee
			}
			summaries = append(summaries, summary)
		}
		data["open_issues"] = summaries
	}

	return data
}

// runBeadsCommand executes a br command in the given directory and returns stdout.
func (t *ContextTool) runBeadsCommand(dir string, beadsArgs ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "br", beadsArgs...)
	cmd.Dir = dir

	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(output)), nil
}

// parseBeadsOutput parses br command output into individual task lines.
func (t *ContextTool) parseBeadsOutput(output string) []string {
	var tasks []string
	lines := strings.Split(output, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Skip header lines or status info
		if strings.HasPrefix(line, "ID") ||
			strings.HasPrefix(line, "--") ||
			strings.HasPrefix(line, "Backend:") ||
			strings.HasPrefix(line, "Database:") ||
			strings.Contains(line, "ready tasks") {
			continue
		}
		tasks = append(tasks, line)
	}

	return tasks
}

// runGit executes a git command in the given directory and returns trimmed stdout.
func (t *ContextTool) runGit(dir string, gitArgs ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", gitArgs...)
	cmd.Dir = dir

	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(output)), nil
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

// Helper methods for argument extraction

func (t *ContextTool) getStringArg(args map[string]interface{}, key, defaultVal string) string {
	if val, ok := args[key].(string); ok {
		return val
	}
	return defaultVal
}

func (t *ContextTool) getBoolArg(args map[string]interface{}, key string, defaultVal bool) bool {
	if val, ok := args[key].(bool); ok {
		return val
	}
	return defaultVal
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
