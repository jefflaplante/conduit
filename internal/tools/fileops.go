package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

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
	return "Read the contents of a file"
}

func (t *ReadFileTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Path to the file to read",
			},
		},
		"required": []string{"path"},
	}
}

func (t *ReadFileTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	path, ok := args["path"].(string)
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

	if !t.registry.isPathAllowed(resolvedPath) {
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

	content, err := os.ReadFile(resolvedPath)
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
	t.maybeExtractBrainFacts(ctx, path, resolvedPath, content)

	return &types.ToolResult{
		Success: true,
		Content: string(content),
		Data: map[string]interface{}{
			"path":          path,
			"resolved_path": resolvedPath,
			"file_size":     len(content),
		},
	}, nil
}

// brainExtractMarker is a cheap substring check before running the real regex.
// The extract parser itself is also defensive but this avoids any work for the
// overwhelmingly common case of files that contain no hints.
const brainExtractMarker = "brain-extract"

// extractLineRegex mirrors internal/brain.extract.go — duplicated here to avoid
// importing the brain package from the tools package (we already depend on the
// BrainService interface, not the concrete type).
var extractLineRegex = regexp.MustCompile(`^\s*([a-zA-Z0-9_.-]+)\s*:\s*"(.*)"\s*$`)
var extractBlockRegex = regexp.MustCompile(`(?s)<!--\s*brain-extract\s*(.*?)\s*/brain-extract\s*-->`)

// textualExtensions lists file extensions we will scan for brain-extract hints.
// An empty extension is also allowed (e.g. MEMORY, LICENSE); for that case we
// additionally require the content to be valid UTF-8 via a simple ASCII sniff.
var textualExtensions = map[string]bool{
	".md":   true,
	".txt":  true,
	".yaml": true,
	".yml":  true,
	".json": true,
}

// maybeExtractBrainFacts scans textual file content for brain-extract blocks and
// forwards the parsed entries to BrainService.StoreBulk. All errors are logged
// (slog) and swallowed; the read must not fail because of a parsing or storage
// issue in this hook.
func (t *ReadFileTool) maybeExtractBrainFacts(ctx context.Context, userPath, resolvedPath string, content []byte) {
	if t.registry == nil || t.registry.services == nil || t.registry.services.Brain == nil {
		return
	}
	// Cheap substring check — skips binaries and normal text files alike.
	if !strings.Contains(string(content), brainExtractMarker) {
		return
	}
	// Extension sniff — only scan files we're reasonably sure are textual.
	ext := strings.ToLower(filepath.Ext(resolvedPath))
	if ext != "" && !textualExtensions[ext] {
		return
	}
	if ext == "" && !looksTextual(content) {
		return
	}

	entries := parseBrainExtractBlocks(string(content))
	if len(entries) == 0 {
		return
	}

	source := "file:" + userPath
	bulk := make([]types.BrainBulkEntry, len(entries))
	for i, e := range entries {
		bulk[i] = types.BrainBulkEntry{
			Key:    e.Key,
			Value:  e.Value,
			Tier:   types.BrainTierWorking,
			Source: source,
		}
	}
	if err := t.registry.services.Brain.StoreBulk(ctx, bulk); err != nil {
		slog.Warn("Read: brain-extract StoreBulk failed",
			"path", userPath, "resolved_path", resolvedPath,
			"entries", len(bulk), "err", err)
		return
	}
	slog.Debug("Read: auto-stored brain-extract entries",
		"path", userPath, "entries", len(bulk))
}

// extractedEntry is the internal shape returned by parseBrainExtractBlocks.
type extractedEntry struct {
	Key   string
	Value string
}

// parseBrainExtractBlocks duplicates brain.ExtractBulkEntries so the tools
// layer can avoid importing internal/brain directly.
func parseBrainExtractBlocks(content string) []extractedEntry {
	matches := extractBlockRegex.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}
	var out []extractedEntry
	for _, m := range matches {
		body := m[1]
		for _, rawLine := range strings.Split(body, "\n") {
			line := strings.TrimSpace(rawLine)
			if line == "" {
				continue
			}
			kv := extractLineRegex.FindStringSubmatch(line)
			if kv == nil {
				continue
			}
			out = append(out, extractedEntry{Key: kv[1], Value: kv[2]})
		}
	}
	return out
}

// looksTextual is a lightweight UTF-8/ASCII sniff used when no extension is
// present. It rejects buffers containing NUL bytes or a high density of
// non-printable bytes; it is not meant to be perfect, just safe.
func looksTextual(b []byte) bool {
	const sample = 512
	n := len(b)
	if n > sample {
		n = sample
	}
	if n == 0 {
		return true
	}
	nonPrintable := 0
	for i := 0; i < n; i++ {
		c := b[i]
		if c == 0 {
			return false
		}
		if c < 0x09 || (c > 0x0D && c < 0x20) {
			nonPrintable++
		}
	}
	return nonPrintable*10 < n // <10% non-printable → treat as text
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

	if !t.registry.isPathAllowed(resolvedPath) {
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

// ListFilesTool implements directory listing functionality
type ListFilesTool struct {
	registry *Registry
}

func (t *ListFilesTool) Name() string {
	return "Glob"
}

func (t *ListFilesTool) Description() string {
	return "List files and directories in a path"
}

func (t *ListFilesTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Path to list (defaults to workspace)",
			},
		},
	}
}

func (t *ListFilesTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		path = t.registry.sandboxCfg.WorkspaceDir
	}

	if !t.registry.isPathAllowed(path) {
		return &types.ToolResult{
			Success: false,
			Error:   "path is not allowed in sandbox",
		}, nil
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to read directory: %v", err),
		}, nil
	}

	var files []map[string]interface{}
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
	}

	filesJSON, _ := json.MarshalIndent(files, "", "  ")

	return &types.ToolResult{
		Success: true,
		Content: string(filesJSON),
		Data: map[string]interface{}{
			"files": files,
			"count": len(files),
		},
	}, nil
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
