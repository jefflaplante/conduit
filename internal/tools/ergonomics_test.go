package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"conduit/internal/ai"
	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// conduit-31jg.39 tests: Read offset/limit, Glob pattern, Data opt-in,
// rune-safe truncation.

func writeNumberedFile(t *testing.T, path string, n int) {
	t.Helper()
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d of the big file\n", i)
	}
	require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o644))
}

func TestReadFile_OffsetLimitMiddleOfLargeFile(t *testing.T) {
	registry, tempDir := setupTestRegistry(t, "workspace", "sandbox")
	path := filepath.Join(tempDir, "workspace", "big.txt")
	writeNumberedFile(t, path, 5000)

	tool := &ReadFileTool{registry: registry}
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"path": "big.txt", "offset": float64(2500), "limit": float64(3),
	})
	require.NoError(t, err)
	require.True(t, res.Success, res.Error)

	lines := strings.Split(strings.TrimRight(res.Content, "\n"), "\n")
	require.GreaterOrEqual(t, len(lines), 3)
	assert.Equal(t, "  2500\tline 2500 of the big file", lines[0])
	assert.Equal(t, "  2502\tline 2502 of the big file", lines[2])
	assert.NotContains(t, res.Content, "line 2503 of")
	assert.Contains(t, res.Content, "offset=2503", "must tell the model how to continue")
	assert.Contains(t, res.Content, "5000")
}

func TestReadFile_DefaultReadFitsBudgetWithContinuationMarker(t *testing.T) {
	registry, tempDir := setupTestRegistry(t, "workspace", "sandbox")
	path := filepath.Join(tempDir, "workspace", "big.txt")
	writeNumberedFile(t, path, 5000)

	tool := &ReadFileTool{registry: registry}
	res, err := tool.Execute(context.Background(), map[string]interface{}{"path": "big.txt"})
	require.NoError(t, err)
	require.True(t, res.Success, res.Error)
	assert.LessOrEqual(t, len(res.Content), DefaultMaxToolResultChars,
		"Read must fit the engine budget so smartTruncate never cuts the middle")
	assert.True(t, strings.HasPrefix(res.Content, "     1\tline 1 of"))
	assert.Contains(t, res.Content, "truncated")
	assert.Regexp(t, `offset=\d+`, res.Content)
}

func TestReadFile_SmallFileNumberedNoMarker(t *testing.T) {
	registry, tempDir := setupTestRegistry(t, "workspace", "sandbox")
	path := filepath.Join(tempDir, "workspace", "small.txt")
	require.NoError(t, os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644))

	tool := &ReadFileTool{registry: registry}
	res, err := tool.Execute(context.Background(), map[string]interface{}{"file_path": path})
	require.NoError(t, err)
	require.True(t, res.Success, res.Error)
	assert.Equal(t, "     1\talpha\n     2\tbeta\n", res.Content)
}

func TestReadFile_OffsetPastEnd(t *testing.T) {
	registry, tempDir := setupTestRegistry(t, "workspace", "sandbox")
	path := filepath.Join(tempDir, "workspace", "small.txt")
	require.NoError(t, os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644))

	tool := &ReadFileTool{registry: registry}
	res, err := tool.Execute(context.Background(), map[string]interface{}{"path": "small.txt", "offset": float64(10)})
	require.NoError(t, err)
	assert.True(t, res.Success)
	assert.Contains(t, res.Content, "only 2 lines")
}

func TestGlob_DoubleStarRelativeToWorkspace(t *testing.T) {
	registry, tempDir := setupTestRegistry(t, "workspace", "sandbox")
	ws := filepath.Join(tempDir, "workspace")
	for _, p := range []string{"a.go", "pkg/b.go", "pkg/deep/c.go", "pkg/deep/notes.md", ".git/x.go"} {
		full := filepath.Join(ws, p)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte("x"), 0o644))
	}
	// Process cwd must not matter.
	t.Chdir(t.TempDir())

	tool := &ListFilesTool{registry: registry}
	res, err := tool.Execute(context.Background(), map[string]interface{}{"pattern": "**/*.go"})
	require.NoError(t, err)
	require.True(t, res.Success, res.Error)
	for _, want := range []string{"a.go", "pkg/b.go", "pkg/deep/c.go"} {
		assert.Contains(t, res.Content, filepath.Join(ws, want))
	}
	assert.NotContains(t, res.Content, "notes.md")
	assert.NotContains(t, res.Content, ".git")

	res, err = tool.Execute(context.Background(), map[string]interface{}{"pattern": "*.go", "path": "pkg"})
	require.NoError(t, err)
	require.True(t, res.Success, res.Error)
	assert.Contains(t, res.Content, filepath.Join(ws, "pkg/b.go"))
	assert.NotContains(t, res.Content, "c.go")

	res, err = tool.Execute(context.Background(), map[string]interface{}{"pattern": "pkg/**/*.{go,md}"})
	require.NoError(t, err)
	assert.Contains(t, res.Content, "notes.md")
	assert.Contains(t, res.Content, "c.go")
	assert.NotContains(t, res.Content, filepath.Join(ws, "a.go"))
}

func TestGlob_ListingRelativeToWorkspaceAndSentOnce(t *testing.T) {
	registry, tempDir := setupTestRegistry(t, "workspace", "sandbox")
	ws := filepath.Join(tempDir, "workspace")
	require.NoError(t, os.MkdirAll(filepath.Join(ws, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(ws, "sub", "file_marker.txt"), []byte("x"), 0o644))
	t.Chdir(t.TempDir())

	tool := &ListFilesTool{registry: registry}
	res, err := tool.Execute(context.Background(), map[string]interface{}{"path": "sub"})
	require.NoError(t, err)
	require.True(t, res.Success, res.Error)
	assert.Contains(t, res.Content, "file_marker.txt")

	engine := NewExecutionEngine(registry, 1, 0, 1)
	out := engine.formatToolResultForAI(&ExecutionResult{ToolCall: &ai.ToolCall{Name: "Glob"}, Result: res})
	assert.Equal(t, 1, strings.Count(out, "file_marker.txt"), "file list must appear once: %s", out)
}

func TestFormatToolResultForAI_DataOptIn(t *testing.T) {
	registry, _ := setupTestRegistry(t, "", "")
	registry.mu.Lock()
	registry.tools["OptIn"] = &dataOptInTool{}
	registry.mu.Unlock()
	engine := NewExecutionEngine(registry, 1, 0, 1)

	res := &ToolResult{Success: true, Content: "summary", Data: map[string]interface{}{"ids": []int{7}}}

	out := engine.formatToolResultForAI(&ExecutionResult{ToolCall: &ai.ToolCall{Name: "Read"}, Result: res})
	assert.Equal(t, "summary", out, "Data must not be appended unless the tool opts in")

	out = engine.formatToolResultForAI(&ExecutionResult{ToolCall: &ai.ToolCall{Name: "OptIn"}, Result: res})
	assert.Contains(t, out, `"ids":[7]`)

	empty := &ToolResult{Success: true, Data: map[string]interface{}{"only": "here"}}
	out = engine.formatToolResultForAI(&ExecutionResult{ToolCall: &ai.ToolCall{Name: "Read"}, Result: empty})
	assert.Contains(t, out, `"only":"here"`, "empty Content falls back to Data")
}

type dataOptInTool struct{}

func (d *dataOptInTool) Name() string        { return "OptIn" }
func (d *dataOptInTool) Description() string { return "x" }
func (d *dataOptInTool) Parameters() map[string]interface{} {
	return map[string]interface{}{"type": "object"}
}
func (d *dataOptInTool) Execute(context.Context, map[string]interface{}) (*types.ToolResult, error) {
	return &types.ToolResult{Success: true}, nil
}
func (d *dataOptInTool) IncludeDataInModelOutput() bool { return true }

func TestSmartTruncate_RuneSafe(t *testing.T) {
	engine := NewExecutionEngine(nil, 1, 0, 1)
	content := strings.Repeat("日本語テキスト", 500) // single line, 3-byte runes
	for _, max := range []int{100, 101, 102, 1000, 1001} {
		out := engine.smartTruncate(content, max)
		assert.True(t, utf8.ValidString(out), "max=%d produced invalid UTF-8", max)
	}
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString(strings.Repeat("é", 40) + "\n")
	}
	out := engine.smartTruncate(b.String(), 501)
	assert.True(t, utf8.ValidString(out))
}

// catN renders s the way Read does for a small file (conduit-31jg.39).
func catN(s string) string {
	var b strings.Builder
	for i, line := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
		fmt.Fprintf(&b, "%6d\t%s\n", i+1, line)
	}
	return b.String()
}
