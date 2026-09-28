package tools

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/schema"
	"conduit/internal/tools/types"
)

// ReadFileTool implements file reading functionality
type ReadFileTool struct {
	registry *Registry
}

func (t *ReadFileTool) Name() string {
	return "Read"
}

func (t *ReadFileTool) Description() string {
	// conduit-31jg.39
	return "Read a text file. Returns lines in `cat -n` format (line number, tab, content); " +
		"the number prefix is not part of the file, so strip it before using text in Edit. " +
		"Relative paths resolve from the workspace. Large files are returned a page at a time: " +
		"when output is cut, a marker gives the offset to continue from. Use offset/limit to read " +
		"a specific region (e.g. around a line from a grep hit) instead of re-reading the whole file."
}

func (t *ReadFileTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Path to the file to read (absolute, or relative to the workspace)",
			},
			"offset": map[string]interface{}{
				"type":        "integer",
				"description": "1-based line number to start reading from (default 1)",
				"minimum":     1,
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": fmt.Sprintf("Maximum number of lines to return (default %d). Output is also capped by size; follow the continuation marker.", DefaultReadLimit),
				"minimum":     1,
			},
		},
		"required": []string{"path"},
	}
}

func (t *ReadFileTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		// Models trained on Claude Code send file_path (conduit-31jg.39).
		if fp, fok := args["file_path"].(string); fok && fp != "" {
			path, ok = fp, true
		}
	}
	if !ok {
		return types.NewErrorResult("missing_parameter",
			"Path parameter is required and must be a string").
			WithParameter("path", args["path"]).
			WithExamples([]string{"README.md", "./config.json", "/absolute/path.txt"}).
			WithSuggestions([]string{
				"Provide a file path to read",
				"Use relative paths from workspace or absolute paths within sandbox",
			}), nil
	}

	// Resolve relative paths against workspace context directory
	resolvedPath := t.resolvePath(path)

	// conduit-31jg.6: symlink-aware check; do I/O on the canonical path.
	realPath, allowed := t.registry.sandboxResolve(resolvedPath)
	if !allowed {
		return types.NewErrorResult("path_not_allowed",
			fmt.Sprintf("Path '%s' is not allowed in sandbox", path)).
			WithParameter("path", path).
			WithAvailableValues(t.registry.sandboxCfg.AllowedPaths).
			WithContext(map[string]interface{}{
				"resolved_path": resolvedPath,
				"sandbox_mode":  true,
				"allowed_paths": t.registry.sandboxCfg.AllowedPaths,
			}).
			WithSuggestions([]string{
				"Use a path within the allowed sandbox directories",
				"Check workspace configuration if using relative paths",
			}), nil
	}

	resolvedPath = realPath

	info, err := os.Stat(resolvedPath)
	if err == nil && info.IsDir() {
		return types.NewErrorResult("is_directory",
			fmt.Sprintf("'%s' is a directory, not a file", path)).
			WithParameter("path", path).
			WithSuggestions([]string{"Use Glob to list or search a directory"}), nil
	}
	var content []byte
	var reader io.Reader
	var file *os.File
	if err == nil && info.Size() > maxWholeReadBytes {
		// Stream very large files instead of loading them (conduit-31jg.39).
		file, err = os.Open(resolvedPath)
		if file != nil {
			defer file.Close()
			reader = file
		}
	} else if err == nil {
		content, err = os.ReadFile(resolvedPath)
		reader = bytes.NewReader(content)
	}
	if err != nil {
		// Enhanced error categorization
		errorType := "file_not_found"
		suggestions := []string{"Check if the file exists", "Verify the file path"}

		if os.IsNotExist(err) {
			errorType = "file_not_found"
			suggestions = []string{
				"Check if the file exists",
				"Verify the file path is correct",
				"Use 'Glob' tool to list available files",
			}
		} else if os.IsPermission(err) {
			errorType = "permission_denied"
			suggestions = []string{
				"Check file permissions",
				"Ensure read access to the file",
				"Run with appropriate permissions",
			}
		}

		return types.NewErrorResult(errorType,
			fmt.Sprintf("Failed to read file '%s': %v", path, err)).
			WithParameter("path", path).
			WithContext(map[string]interface{}{
				"resolved_path": resolvedPath,
				"error_detail":  err.Error(),
			}).
			WithSuggestions(suggestions), nil
	}

	// Auto-extract brain-extract hints from textual files. Failures here must
	// NEVER fail the read — we log and move on.
	if content != nil {
		t.maybeExtractBrainFacts(ctx, path, resolvedPath, content)
	}

	data := map[string]interface{}{
		"path":          path,
		"resolved_path": resolvedPath,
		"file_size":     info.Size(),
	}

	// conduit-31jg.39: binary files are summarised, not dumped.
	sniff := content
	if sniff == nil && file != nil {
		buf := make([]byte, 8192)
		n, _ := io.ReadFull(file, buf)
		sniff = buf[:n]
		if _, serr := file.Seek(0, io.SeekStart); serr != nil {
			return types.NewErrorResult("read_error", fmt.Sprintf("Failed to read file '%s': %v", path, serr)), nil
		}
	}
	if looksBinary(sniff) {
		return &types.ToolResult{
			Success: true,
			Content: fmt.Sprintf("'%s' is a binary file (%d bytes); contents not shown. Use Bash (file, xxd, strings) or the Image tool to inspect it.", path, info.Size()),
			Data:    data,
		}, nil
	}

	// conduit-31jg.39: line-numbered paging (offset/limit), sized to the
	// engine's result budget so the middle of a file is reachable instead
	// of being cut by head/tail truncation.
	offset := toolargs.GetInt(args, "offset", 1)
	limit := toolargs.GetInt(args, "limit", DefaultReadLimit)
	if offset < 1 {
		offset = 1
	}
	if limit < 1 {
		limit = DefaultReadLimit
	}
	budget := t.registry.maxResultChars() - readMarkerReserve
	page, err := renderLinePage(reader, offset, limit, budget)
	if err != nil {
		return types.NewErrorResult("read_error", fmt.Sprintf("Failed to read file '%s': %v", path, err)).
			WithParameter("path", path), nil
	}
	data["total_lines"] = page.TotalLines
	data["start_line"] = page.StartLine
	data["end_line"] = page.EndLine
	data["truncated"] = page.Truncated

	text := page.Text
	switch {
	case page.TotalLines == 0:
		text = fmt.Sprintf("'%s' is empty.", path)
	case page.EndLine == 0:
		text = fmt.Sprintf("'%s' has only %d lines; offset %d is past the end. Use a smaller offset.", path, page.TotalLines, offset)
	case page.Truncated:
		reason := "limit reached"
		if page.ByBudget {
			reason = "output size cap reached"
		}
		text += fmt.Sprintf("\n[... truncated (%s): showed lines %d-%d of %d. Continue with offset=%d ...]\n",
			reason, page.StartLine, page.EndLine, page.TotalLines, page.EndLine+1)
	}

	return &types.ToolResult{
		Success: true,
		Content: text,
		Data:    data,
	}, nil
}

// resolvePath resolves relative paths against workspace context directory
func (t *ReadFileTool) resolvePath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}

	// Try to get workspace context directory from config
	if t.registry.services != nil && t.registry.services.ConfigMgr != nil {
		contextDir := t.registry.services.ConfigMgr.Workspace.ContextDir
		if contextDir != "" {
			return filepath.Join(contextDir, path)
		}
	}

	// Fallback to sandbox workspace directory
	return filepath.Join(t.registry.sandboxCfg.WorkspaceDir, path)
}

// GetSchemaHints implements types.EnhancedSchemaProvider.
func (t *ReadFileTool) GetSchemaHints() map[string]schema.SchemaHints {
	hints := map[string]schema.SchemaHints{
		"path": {
			DiscoveryType:     "workspace_paths",
			EnumFromDiscovery: false, // Show allowed paths as examples, don't restrict
			ValidationHints: []string{
				"Relative paths resolve from workspace directory",
				"Absolute paths must be within allowed sandbox paths",
			},
		},
	}

	// Add workspace path example if available
	if t.registry.services != nil && t.registry.services.ConfigMgr != nil {
		contextDir := t.registry.services.ConfigMgr.Workspace.ContextDir
		if contextDir != "" {
			hints["path"] = schema.SchemaHints{
				Examples:          []interface{}{"README.md", "src/main.go", contextDir},
				DiscoveryType:     "workspace_paths",
				EnumFromDiscovery: false,
				ValidationHints: []string{
					fmt.Sprintf("Workspace root: %s", contextDir),
					"Relative paths resolve from workspace directory",
				},
			}
		}
	}

	return hints
}

// GetUsageExamples implements types.UsageExampleProvider for ReadFileTool.
func (t *ReadFileTool) GetUsageExamples() []types.ToolExample {
	return []types.ToolExample{
		{
			Name:        "Read configuration file",
			Description: "Read the contents of a JSON configuration file",
			Args: map[string]interface{}{
				"path": "config.json",
			},
			Expected: "Returns the JSON configuration file contents as text",
		},
		{
			Name:        "Read code file",
			Description: "Read a source code file from the project",
			Args: map[string]interface{}{
				"path": "src/main.go",
			},
			Expected: "Returns the Go source code file contents",
		},
		{
			Name:        "Read memory file",
			Description: "Read from the AI's memory system",
			Args: map[string]interface{}{
				"path": "MEMORY.md",
			},
			Expected: "Returns the main memory file with historical context",
		},
	}
}

// SelfTest implements types.SelfTester for ReadFileTool.
func (t *ReadFileTool) SelfTest(ctx context.Context, opts *types.SelfTestOptions) *types.SelfTestResult {
	start := time.Now()

	if opts == nil {
		opts = types.DefaultSelfTestOptions()
	}

	result := &types.SelfTestResult{
		Status:       types.SelfTestStatusOK,
		Message:      "Read tool is functional",
		Capabilities: []string{"read_file", "resolve_relative_paths", "sandbox_enforcement"},
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
		result.Suggestions = []string{"Ensure ReadFileTool is registered with a valid Registry"}
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
					result.Message = "Read tool functional but workspace directory not found"
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
