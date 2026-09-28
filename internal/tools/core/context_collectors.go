package core

import (
	"context"
	"fmt"
	"os"
	"strings"

	"conduit/internal/tools/types"
	"conduit/internal/version"
)

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
