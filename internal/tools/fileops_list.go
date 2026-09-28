package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"conduit/internal/tools/types"
)

// ListFilesTool implements directory listing functionality
type ListFilesTool struct {
	registry *Registry
}

func (t *ListFilesTool) Name() string {
	return "Glob"
}

func (t *ListFilesTool) Description() string {
	// conduit-31jg.39
	return "Find files by glob pattern, or list a directory. With `pattern` (e.g. \"**/*.go\", \"src/**/*.{ts,tsx}\", \"*.md\"), " +
		"returns matching file paths (absolute, newest first, up to 100); `**` matches any number of directories and " +
		"patterns are relative to `path` (default: the workspace). Without `pattern`, lists the entries of `path`. " +
		"Use this instead of Bash find/ls."
}

func (t *ListFilesTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"pattern": map[string]interface{}{
				"type":        "string",
				"description": "Glob pattern to match files against, e.g. \"**/*.go\" or \"internal/**/*_test.go\". Supports *, ?, [...], {a,b} and ** (any depth). Omit to list the directory at path.",
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Directory to search or list (absolute, or relative to the workspace; defaults to the workspace)",
			},
		},
	}
}

func (t *ListFilesTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	// conduit-31jg.39: resolve relative to the workspace (was process cwd).
	path, _ := args["path"].(string)
	dir := t.resolveDir(path)

	// conduit-31jg.6: symlink-aware check; list the canonical directory.
	realDir, allowed := t.registry.sandboxResolve(dir)
	if !allowed {
		return &types.ToolResult{
			Success: false,
			Error:   "path is not allowed in sandbox",
		}, nil
	}
	dir = realDir

	if pattern, _ := args["pattern"].(string); strings.TrimSpace(pattern) != "" {
		return t.globPattern(ctx, dir, pattern)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to read directory: %v", err),
		}, nil
	}

	// Compact one-entry-per-line listing for the model; the structured list
	// stays in Data for programmatic callers (not sent to the model).
	var files []map[string]interface{}
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%d entries):\n", dir, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}

		files = append(files, map[string]interface{}{
			"name":  entry.Name(),
			"type":  getFileType(entry),
			"size":  info.Size(),
			"mtime": info.ModTime(),
		})
		if entry.IsDir() {
			fmt.Fprintf(&b, "%s/\n", entry.Name())
		} else {
			fmt.Fprintf(&b, "%s  %d bytes\n", entry.Name(), info.Size())
		}
	}

	return &types.ToolResult{
		Success: true,
		Content: b.String(),
		Data: map[string]interface{}{
			"files": files,
			"count": len(files),
			"path":  dir,
		},
	}, nil
}

// globPattern runs a doublestar pattern search rooted at dir (conduit-31jg.39).
func (t *ListFilesTool) globPattern(ctx context.Context, dir, pattern string) (*types.ToolResult, error) {
	res, err := globFiles(ctx, dir, pattern, DefaultGlobLimit, t.registry.isPathAllowed)
	if err != nil {
		if os.IsNotExist(err) {
			return &types.ToolResult{Success: true, Content: fmt.Sprintf("No files found: directory %s does not exist.", dir),
				Data: map[string]interface{}{"pattern": pattern, "path": dir, "count": 0}}, nil
		}
		return types.NewErrorResult("glob_failed", fmt.Sprintf("Glob %q in %s failed: %v", pattern, dir, err)).
			WithParameter("pattern", pattern), nil
	}

	var b strings.Builder
	if res.Total == 0 {
		fmt.Fprintf(&b, "No files found matching %q in %s.", pattern, dir)
	} else {
		b.WriteString(strings.Join(res.Matches, "\n"))
		b.WriteString("\n")
		if res.Total > len(res.Matches) {
			fmt.Fprintf(&b, "(showing %d of %d matches, newest first; use a more specific pattern or path)\n", len(res.Matches), res.Total)
		}
	}
	if res.Stopped {
		fmt.Fprintf(&b, "(search stopped after %d entries; narrow the path or pattern)\n", maxGlobVisited)
	}

	return &types.ToolResult{
		Success: true,
		Content: b.String(),
		Data: map[string]interface{}{
			"pattern":   pattern,
			"path":      dir,
			"count":     res.Total,
			"truncated": res.Total > len(res.Matches) || res.Stopped,
		},
	}, nil
}

// resolveDir resolves a Glob directory: empty means the workspace context
// dir (falling back to the sandbox workspace); relative paths resolve
// against it, not the process cwd (conduit-31jg.39).
func (t *ListFilesTool) resolveDir(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	base := t.registry.sandboxCfg.WorkspaceDir
	if t.registry.services != nil && t.registry.services.ConfigMgr != nil && t.registry.services.ConfigMgr.Workspace.ContextDir != "" {
		base = t.registry.services.ConfigMgr.Workspace.ContextDir
	}
	if p == "" {
		return base
	}
	return filepath.Join(base, p)
}

// GetUsageExamples implements types.UsageExampleProvider for ListFilesTool.
func (t *ListFilesTool) GetUsageExamples() []types.ToolExample {
	return []types.ToolExample{
		{
			Name:        "List workspace files",
			Description: "List all files and directories in the current workspace",
			Args:        map[string]interface{}{},
			Expected:    "Returns JSON array of files with names, types, sizes, and modification times",
		},
		{
			Name:        "List specific directory",
			Description: "List contents of a specific directory",
			Args: map[string]interface{}{
				"path": "src",
			},
			Expected: "Returns JSON array of files in the src directory",
		},
		{
			Name:        "List project root",
			Description: "List files in the project root directory",
			Args: map[string]interface{}{
				"path": ".",
			},
			Expected: "Returns all files and directories in the project root",
		},
	}
}

// SelfTest implements types.SelfTester for ListFilesTool (Glob).
func (t *ListFilesTool) SelfTest(ctx context.Context, opts *types.SelfTestOptions) *types.SelfTestResult {
	start := time.Now()

	if opts == nil {
		opts = types.DefaultSelfTestOptions()
	}

	result := &types.SelfTestResult{
		Status:       types.SelfTestStatusOK,
		Message:      "Glob tool is functional",
		Capabilities: []string{"list_directory", "file_metadata", "sandbox_enforcement"},
		TestedAt:     time.Now(),
	}

	deps := []types.DependencyStatus{}

	// Check registry availability (for sandbox config)
	registryStatus := types.DependencyStatus{
		Name:     "Registry",
		Required: true,
	}
	if t.registry != nil {
		registryStatus.Available = true
		registryStatus.Status = "ready"
	} else {
		registryStatus.Available = false
		registryStatus.Status = "not_configured"
		result.Status = types.SelfTestStatusFailed
		result.Message = "Registry not available"
		result.Suggestions = []string{"Ensure ListFilesTool is registered with a valid Registry"}
	}
	deps = append(deps, registryStatus)

	// Check workspace directory existence
	workspaceStatus := types.DependencyStatus{
		Name:     "WorkspaceDir",
		Required: false,
	}
	if t.registry != nil {
		workspaceDir := t.registry.sandboxCfg.WorkspaceDir
		if workspaceDir != "" {
			if info, err := os.Stat(workspaceDir); err == nil && info.IsDir() {
				workspaceStatus.Available = true
				workspaceStatus.Status = "exists"
				workspaceStatus.Message = workspaceDir
			} else {
				workspaceStatus.Available = false
				workspaceStatus.Status = "not_found"
				workspaceStatus.Message = workspaceDir
				if result.Status == types.SelfTestStatusOK {
					result.Status = types.SelfTestStatusDegraded
					result.Message = "Glob tool functional but workspace directory not found"
				}
				result.Suggestions = append(result.Suggestions, "Create workspace directory or update sandbox configuration")
			}
		} else {
			workspaceStatus.Available = false
			workspaceStatus.Status = "not_configured"
		}
	}
	deps = append(deps, workspaceStatus)

	result.Dependencies = deps
	result.TestDuration = time.Since(start)

	if opts.IncludeExamples && result.IsFunctional() {
		result.Examples = t.GetUsageExamples()
	}

	return result
}

// getFileType returns the type of a directory entry
func getFileType(entry os.DirEntry) string {
	if entry.IsDir() {
		return "directory"
	}
	return "file"
}
