package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"conduit/internal/tools/schema"
	"conduit/internal/tools/types"
)

// WriteFileTool implements file writing functionality
type WriteFileTool struct {
	registry *Registry
}

func (t *WriteFileTool) Name() string {
	return "Write"
}

func (t *WriteFileTool) Description() string {
	return "Write content to a file"
}

func (t *WriteFileTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Path to the file to write",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "Content to write to the file",
			},
		},
		"required": []string{"path", "content"},
	}
}

func (t *WriteFileTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	path, ok := args["path"].(string)
	if !ok {
		return types.NewErrorResult("missing_parameter",
			"Path parameter is required and must be a string").
			WithParameter("path", args["path"]).
			WithExamples([]string{"output.txt", "./data/results.json", "notes.md"}).
			WithSuggestions([]string{
				"Provide a file path to write to",
				"Use relative paths from workspace or absolute paths within sandbox",
			}), nil
	}

	content, ok := args["content"].(string)
	if !ok {
		return types.NewErrorResult("missing_parameter",
			"Content parameter is required and must be a string").
			WithParameter("content", args["content"]).
			WithExamples([]string{"Hello, World!", "# Title\n\nContent", "{ \"key\": \"value\" }"}).
			WithSuggestions([]string{
				"Provide content to write to the file",
				"Content can be text, JSON, code, or any string data",
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

	// Ensure directory exists
	dir := filepath.Dir(resolvedPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return types.NewErrorResult("permission_denied",
			fmt.Sprintf("Failed to create parent directory '%s': %v", dir, err)).
			WithParameter("path", path).
			WithContext(map[string]interface{}{
				"parent_directory": dir,
				"resolved_path":    resolvedPath,
				"error_detail":     err.Error(),
			}).
			WithSuggestions([]string{
				"Check permissions on the parent directory",
				"Ensure the directory path is writable",
				"Use a different path if current one has permission issues",
			}), nil
	}

	if err := os.WriteFile(resolvedPath, []byte(content), 0644); err != nil {
		// Enhanced error categorization
		errorType := "permission_denied"
		suggestions := []string{"Check file permissions", "Ensure write access to the directory"}

		if os.IsPermission(err) {
			errorType = "permission_denied"
			suggestions = []string{
				"Check file and directory permissions",
				"Ensure write access to the target location",
				"Run with appropriate permissions",
			}
		} else if strings.Contains(err.Error(), "no space") {
			errorType = "insufficient_storage"
			suggestions = []string{
				"Free up disk space",
				"Use a different storage location",
				"Reduce content size if possible",
			}
		} else if strings.Contains(err.Error(), "read-only") {
			errorType = "permission_denied"
			suggestions = []string{
				"The file system is read-only",
				"Use a writable location",
				"Check mount options",
			}
		}

		return types.NewErrorResult(errorType,
			fmt.Sprintf("Failed to write file '%s': %v", path, err)).
			WithParameter("path", path).
			WithContext(map[string]interface{}{
				"resolved_path":  resolvedPath,
				"content_length": len(content),
				"error_detail":   err.Error(),
			}).
			WithSuggestions(suggestions), nil
	}

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Successfully wrote %d bytes to %s", len(content), resolvedPath),
		Data: map[string]interface{}{
			"path":           path,
			"resolved_path":  resolvedPath,
			"bytes_written":  len(content),
			"content_length": len(content),
		},
	}, nil
}

// resolvePath resolves relative paths against workspace context directory
func (t *WriteFileTool) resolvePath(path string) string {
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
func (t *WriteFileTool) GetSchemaHints() map[string]schema.SchemaHints {
	hints := map[string]schema.SchemaHints{
		"path": {
			DiscoveryType:     "workspace_paths",
			EnumFromDiscovery: false,
			ValidationHints: []string{
				"Parent directories created automatically",
				"Relative paths resolve from workspace directory",
				"Absolute paths must be within allowed sandbox paths",
			},
		},
		"content": {
			ValidationHints: []string{
				"Content is written as-is (no encoding)",
				"Use appropriate line endings for the platform",
			},
		},
	}

	// Add workspace path example if available
	if t.registry.services != nil && t.registry.services.ConfigMgr != nil {
		contextDir := t.registry.services.ConfigMgr.Workspace.ContextDir
		if contextDir != "" {
			hints["path"] = schema.SchemaHints{
				Examples:          []interface{}{"output.txt", "data/results.json", contextDir + "/notes.md"},
				DiscoveryType:     "workspace_paths",
				EnumFromDiscovery: false,
				ValidationHints: []string{
					fmt.Sprintf("Workspace root: %s", contextDir),
					"Parent directories created automatically",
				},
			}
		}
	}

	return hints
}

// GetUsageExamples implements types.UsageExampleProvider for WriteFileTool.
func (t *WriteFileTool) GetUsageExamples() []types.ToolExample {
	return []types.ToolExample{
		{
			Name:        "Create a text file",
			Description: "Write simple text content to a new file",
			Args: map[string]interface{}{
				"path":    "notes.txt",
				"content": "These are my notes for today.",
			},
			Expected: "Creates notes.txt with the specified content",
		},
		{
			Name:        "Save JSON data",
			Description: "Write structured data to a JSON file",
			Args: map[string]interface{}{
				"path":    "data/results.json",
				"content": "{\n  \"status\": \"success\",\n  \"count\": 42\n}",
			},
			Expected: "Creates results.json in the data directory with JSON content",
		},
		{
			Name:        "Update configuration",
			Description: "Update an existing configuration file",
			Args: map[string]interface{}{
				"path":    "config.yaml",
				"content": "database:\n  host: localhost\n  port: 5432\n  name: myapp",
			},
			Expected: "Updates config.yaml with new database configuration",
		},
	}
}

// SelfTest implements types.SelfTester for WriteFileTool.
func (t *WriteFileTool) SelfTest(ctx context.Context, opts *types.SelfTestOptions) *types.SelfTestResult {
	start := time.Now()

	if opts == nil {
		opts = types.DefaultSelfTestOptions()
	}

	result := &types.SelfTestResult{
		Status:       types.SelfTestStatusOK,
		Message:      "Write tool is functional",
		Capabilities: []string{"write_file", "create_directories", "resolve_relative_paths", "sandbox_enforcement"},
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
		result.Suggestions = []string{"Ensure WriteFileTool is registered with a valid Registry"}
	}
	deps = append(deps, registryStatus)

	// Check workspace directory existence and writability
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

				// Check if writable by attempting to create a temp file
				testFile := filepath.Join(workspaceDir, ".selftest_write_check")
				if err := os.WriteFile(testFile, []byte("test"), 0644); err == nil {
					os.Remove(testFile)
					workspaceStatus.Status = "writable"
				} else {
					workspaceStatus.Status = "read_only"
					if result.Status == types.SelfTestStatusOK {
						result.Status = types.SelfTestStatusDegraded
						result.Message = "Write tool functional but workspace directory is not writable"
					}
					result.Suggestions = append(result.Suggestions, "Check write permissions on workspace directory")
				}
			} else {
				workspaceStatus.Available = false
				workspaceStatus.Status = "not_found"
				workspaceStatus.Message = workspaceDir
				if result.Status == types.SelfTestStatusOK {
					result.Status = types.SelfTestStatusDegraded
					result.Message = "Write tool functional but workspace directory not found"
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
