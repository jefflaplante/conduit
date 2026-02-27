package chain

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockExecutor implements ToolExecutor for testing.
type mockExecutor struct {
	tools   map[string]bool
	results map[string]string
	errors  map[string]error
	calls   []string
}

func newMockExecutor() *mockExecutor {
	return &mockExecutor{
		tools:   make(map[string]bool),
		results: make(map[string]string),
		errors:  make(map[string]error),
	}
}

func (m *mockExecutor) ToolExists(name string) bool {
	return m.tools[name]
}

func (m *mockExecutor) ExecuteTool(name string, params map[string]interface{}) (string, error) {
	m.calls = append(m.calls, name)
	if err, ok := m.errors[name]; ok {
		return "", err
	}
	if result, ok := m.results[name]; ok {
		return result, nil
	}
	return fmt.Sprintf("%s executed", name), nil
}

func TestLoadChain(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-chain.json")

	chainJSON := `{
		"name": "test-chain",
		"description": "A test chain",
		"steps": [
			{"id": "s1", "tool_name": "Read", "params": {"path": "/tmp/file.txt"}},
			{"id": "s2", "tool_name": "Write", "params": {"path": "/tmp/out.txt", "content": "hello"}, "depends_on": ["s1"]}
		],
		"variables": [
			{"name": "input_path", "description": "Path to read", "required": true}
		]
	}`

	require.NoError(t, os.WriteFile(path, []byte(chainJSON), 0o644))

	c, err := LoadChain(path)
	require.NoError(t, err)

	assert.Equal(t, "test-chain", c.Name)
	assert.Equal(t, "A test chain", c.Description)
	assert.Len(t, c.Steps, 2)
	assert.Len(t, c.Variables, 1)
	assert.Equal(t, "Read", c.Steps[0].ToolName)
	assert.Equal(t, []string{"s1"}, c.Steps[1].DependsOn)
}

func TestLoadChain_InvalidPath(t *testing.T) {
	_, err := LoadChain("/nonexistent/chain.json")
	assert.Error(t, err)
}

func TestLoadChain_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	require.NoError(t, os.WriteFile(path, []byte("not json"), 0o644))

	_, err := LoadChain(path)
	assert.Error(t, err)
}

func TestSaveChain(t *testing.T) {
	dir := t.TempDir()

	c := &Chain{
		Name:        "my-workflow",
		Description: "Test workflow",
		Steps: []ChainStep{
			{ID: "s1", ToolName: "Bash", Params: map[string]interface{}{"command": "ls"}},
		},
	}

	err := SaveChain(c, dir)
	require.NoError(t, err)

	// Verify file exists.
	path := filepath.Join(dir, "my-workflow.json")
	_, err = os.Stat(path)
	require.NoError(t, err)

	// Load it back.
	loaded, err := LoadChain(path)
	require.NoError(t, err)
	assert.Equal(t, "my-workflow", loaded.Name)
}

func TestValidateChain_Valid(t *testing.T) {
	exec := newMockExecutor()
	exec.tools["Read"] = true
	exec.tools["Write"] = true

	c := &Chain{
		Name: "valid-chain",
		Steps: []ChainStep{
			{ID: "s1", ToolName: "Read", Params: map[string]interface{}{"path": "{{input}}"}},
			{ID: "s2", ToolName: "Write", Params: map[string]interface{}{"content": "done"}, DependsOn: []string{"s1"}},
		},
		Variables: []Variable{
			{Name: "input", Required: true},
		},
	}

	errs := ValidateChain(c, exec)
	assert.Empty(t, errs)
}

func TestValidateChain_NoName(t *testing.T) {
	c := &Chain{
		Steps: []ChainStep{
			{ID: "s1", ToolName: "Read"},
		},
	}

	errs := ValidateChain(c, nil)
	assert.NotEmpty(t, errs)

	found := false
	for _, e := range errs {
		if e.Error() == "chain name is required" {
			found = true
		}
	}
	assert.True(t, found)
}

func TestValidateChain_NoSteps(t *testing.T) {
	c := &Chain{Name: "empty"}
	errs := ValidateChain(c, nil)

	found := false
	for _, e := range errs {
		if e.Error() == "chain must have at least one step" {
			found = true
		}
	}
	assert.True(t, found)
}

func TestValidateChain_DuplicateID(t *testing.T) {
	c := &Chain{
		Name: "dup",
		Steps: []ChainStep{
			{ID: "s1", ToolName: "Read"},
			{ID: "s1", ToolName: "Write"},
		},
	}

	errs := ValidateChain(c, nil)
	found := false
	for _, e := range errs {
		if containsStr(e.Error(), "duplicate ID") {
			found = true
		}
	}
	assert.True(t, found)
}

func TestValidateChain_UnknownTool(t *testing.T) {
	exec := newMockExecutor()
	exec.tools["Read"] = true

	c := &Chain{
		Name: "unknown-tool",
		Steps: []ChainStep{
			{ID: "s1", ToolName: "NonexistentTool"},
		},
	}

	errs := ValidateChain(c, exec)
	found := false
	for _, e := range errs {
		if containsStr(e.Error(), "unknown tool") {
			found = true
		}
	}
	assert.True(t, found)
}

func TestValidateChain_UnknownDependency(t *testing.T) {
	c := &Chain{
		Name: "bad-dep",
		Steps: []ChainStep{
			{ID: "s1", ToolName: "Read", DependsOn: []string{"nonexistent"}},
		},
	}

	errs := ValidateChain(c, nil)
	found := false
	for _, e := range errs {
		if containsStr(e.Error(), "depends on unknown step") {
			found = true
		}
	}
	assert.True(t, found)
}

func TestValidateChain_InvalidOnError(t *testing.T) {
	c := &Chain{
		Name: "bad-onerror",
		Steps: []ChainStep{
			{ID: "s1", ToolName: "Read", OnError: "panic"},
		},
	}

	errs := ValidateChain(c, nil)
	found := false
	for _, e := range errs {
		if containsStr(e.Error(), "invalid on_error") {
			found = true
		}
	}
	assert.True(t, found)
}

func TestValidateChain_UndefinedVariable(t *testing.T) {
	c := &Chain{
		Name: "undef-var",
		Steps: []ChainStep{
			{ID: "s1", ToolName: "Read", Params: map[string]interface{}{"path": "{{undefined_var}}"}},
		},
	}

	errs := ValidateChain(c, nil)
	found := false
	for _, e := range errs {
		if containsStr(e.Error(), "undefined variable") {
			found = true
		}
	}
	assert.True(t, found)
}

func TestValidateChain_CyclicDependency(t *testing.T) {
	c := &Chain{
		Name: "cycle",
		Steps: []ChainStep{
			{ID: "s1", ToolName: "Read", DependsOn: []string{"s2"}},
			{ID: "s2", ToolName: "Write", DependsOn: []string{"s1"}},
		},
	}

	errs := ValidateChain(c, nil)
	found := false
	for _, e := range errs {
		if containsStr(e.Error(), "cycle") {
			found = true
		}
	}
	assert.True(t, found)
}

func TestExecute_Basic(t *testing.T) {
	exec := newMockExecutor()
	exec.tools["Read"] = true
	exec.tools["Write"] = true
	exec.results["Read"] = "file contents"
	exec.results["Write"] = "written"

	runner := NewRunner(exec)

	c := &Chain{
		Name: "basic",
		Steps: []ChainStep{
			{ID: "s1", ToolName: "Read", Params: map[string]interface{}{"path": "/tmp/in.txt"}},
			{ID: "s2", ToolName: "Write", Params: map[string]interface{}{"path": "/tmp/out.txt"}, DependsOn: []string{"s1"}},
		},
	}

	result, err := runner.Execute(c, nil)
	require.NoError(t, err)

	assert.True(t, result.Success)
	assert.Len(t, result.StepResults, 2)
	assert.Equal(t, "file contents", result.StepResults[0].Output)
	assert.Equal(t, "written", result.StepResults[1].Output)
	assert.Equal(t, []string{"Read", "Write"}, exec.calls)
}

func TestExecute_WithVariables(t *testing.T) {
	exec := newMockExecutor()
	exec.tools["Read"] = true

	runner := NewRunner(exec)

	c := &Chain{
		Name: "vars",
		Steps: []ChainStep{
			{ID: "s1", ToolName: "Read", Params: map[string]interface{}{"path": "{{input_path}}"}},
		},
		Variables: []Variable{
			{Name: "input_path", Required: true},
		},
	}

	result, err := runner.Execute(c, map[string]string{"input_path": "/data/file.txt"})
	require.NoError(t, err)
	assert.True(t, result.Success)
}

func TestExecute_MissingRequiredVariable(t *testing.T) {
	exec := newMockExecutor()
	runner := NewRunner(exec)

	c := &Chain{
		Name: "missing-var",
		Steps: []ChainStep{
			{ID: "s1", ToolName: "Read", Params: map[string]interface{}{"path": "{{input}}"}},
		},
		Variables: []Variable{
			{Name: "input", Required: true},
		},
	}

	_, err := runner.Execute(c, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "required variable")
}

func TestExecute_DefaultVariable(t *testing.T) {
	exec := newMockExecutor()
	exec.tools["Read"] = true

	runner := NewRunner(exec)

	c := &Chain{
		Name: "defaults",
		Steps: []ChainStep{
			{ID: "s1", ToolName: "Read", Params: map[string]interface{}{"path": "{{input}}"}},
		},
		Variables: []Variable{
			{Name: "input", Default: "/tmp/default.txt", Required: true},
		},
	}

	result, err := runner.Execute(c, nil)
	require.NoError(t, err)
	assert.True(t, result.Success)
}

func TestExecute_StopOnError(t *testing.T) {
	exec := newMockExecutor()
	exec.tools["Read"] = true
	exec.tools["Write"] = true
	exec.errors["Read"] = fmt.Errorf("permission denied")

	runner := NewRunner(exec)

	c := &Chain{
		Name: "stop",
		Steps: []ChainStep{
			{ID: "s1", ToolName: "Read", OnError: "stop"},
			{ID: "s2", ToolName: "Write", DependsOn: []string{"s1"}},
		},
	}

	result, err := runner.Execute(c, nil)
	require.NoError(t, err)

	assert.False(t, result.Success)
	assert.Len(t, result.StepResults, 1)
	assert.Contains(t, result.Error, "permission denied")
}

func TestExecute_SkipOnError(t *testing.T) {
	exec := newMockExecutor()
	exec.tools["Read"] = true
	exec.tools["Write"] = true
	exec.errors["Read"] = fmt.Errorf("not found")
	exec.results["Write"] = "written"

	runner := NewRunner(exec)

	c := &Chain{
		Name: "skip",
		Steps: []ChainStep{
			{ID: "s1", ToolName: "Read", OnError: "skip"},
			{ID: "s2", ToolName: "Write"},
		},
	}

	result, err := runner.Execute(c, nil)
	require.NoError(t, err)

	assert.True(t, result.Success)
	assert.Len(t, result.StepResults, 2)
	assert.True(t, result.StepResults[0].Skipped)
	assert.True(t, result.StepResults[1].Success)
}

func TestExecute_RetryOnError(t *testing.T) {
	exec := newMockExecutor()
	exec.tools["Read"] = true
	// First call fails, but retry succeeds (mockExecutor returns error for
	// all calls to the same tool, so we adjust for the test).
	callCount := 0
	origExec := exec.ExecuteTool
	_ = origExec
	runner := NewRunner(&retryMockExecutor{failFirst: true})

	c := &Chain{
		Name: "retry",
		Steps: []ChainStep{
			{ID: "s1", ToolName: "Flaky", OnError: "retry"},
		},
	}

	result, err := runner.Execute(c, nil)
	require.NoError(t, err)
	_ = callCount

	assert.True(t, result.Success)
	assert.Len(t, result.StepResults, 1)
	assert.True(t, result.StepResults[0].Success)
}

type retryMockExecutor struct {
	callCount int
	failFirst bool
}

func (r *retryMockExecutor) ToolExists(name string) bool { return true }

func (r *retryMockExecutor) ExecuteTool(name string, params map[string]interface{}) (string, error) {
	r.callCount++
	if r.failFirst && r.callCount == 1 {
		return "", fmt.Errorf("transient error")
	}
	return "success", nil
}

func TestExecute_NoExecutor(t *testing.T) {
	runner := NewRunner(nil)
	c := &Chain{
		Name:  "no-exec",
		Steps: []ChainStep{{ID: "s1", ToolName: "Read"}},
	}

	_, err := runner.Execute(c, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no tool executor")
}

func TestListChains(t *testing.T) {
	dir := t.TempDir()

	c1 := &Chain{Name: "alpha", Description: "First chain", Steps: []ChainStep{{ID: "s1", ToolName: "Read"}}}
	c2 := &Chain{Name: "beta", Description: "Second chain", Steps: []ChainStep{{ID: "s1", ToolName: "Write"}, {ID: "s2", ToolName: "Read"}}}

	require.NoError(t, SaveChain(c1, dir))
	require.NoError(t, SaveChain(c2, dir))

	summaries, err := ListChains(dir)
	require.NoError(t, err)
	assert.Len(t, summaries, 2)

	// Sorted alphabetically.
	assert.Equal(t, "alpha", summaries[0].Name)
	assert.Equal(t, 1, summaries[0].StepCount)
	assert.Equal(t, "beta", summaries[1].Name)
	assert.Equal(t, 2, summaries[1].StepCount)
}

func TestListChains_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	summaries, err := ListChains(dir)
	require.NoError(t, err)
	assert.Empty(t, summaries)
}

func TestListChains_NonexistentDir(t *testing.T) {
	summaries, err := ListChains("/nonexistent/path/for/testing")
	require.NoError(t, err)
	assert.Nil(t, summaries)
}

func TestFindChain(t *testing.T) {
	dir := t.TempDir()
	c := &Chain{Name: "my-chain", Steps: []ChainStep{{ID: "s1", ToolName: "Read"}}}
	require.NoError(t, SaveChain(c, dir))

	found, err := FindChain(dir, "my-chain")
	require.NoError(t, err)
	assert.Equal(t, "my-chain", found.Name)
}

func TestFindChain_NotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := FindChain(dir, "nonexistent")
	assert.Error(t, err)
}

func TestDeleteChain(t *testing.T) {
	dir := t.TempDir()
	c := &Chain{Name: "delete-me", Steps: []ChainStep{{ID: "s1", ToolName: "Read"}}}
	require.NoError(t, SaveChain(c, dir))

	err := DeleteChain(dir, "delete-me")
	require.NoError(t, err)

	// Verify it's gone.
	_, err = FindChain(dir, "delete-me")
	assert.Error(t, err)
}

func TestDeleteChain_NotFound(t *testing.T) {
	dir := t.TempDir()
	err := DeleteChain(dir, "nonexistent")
	assert.Error(t, err)
}

func TestSanitizeFilename(t *testing.T) {
	assert.Equal(t, "hello-world", sanitizeFilename("Hello World"))
	assert.Equal(t, "my_chain-1", sanitizeFilename("my_chain-1"))
	assert.Equal(t, "unnamed", sanitizeFilename("!!!"))
	assert.Equal(t, "test", sanitizeFilename("test"))
}

func TestFindTemplateVars(t *testing.T) {
	assert.Equal(t, []string{"foo"}, findTemplateVars("{{foo}}"))
	assert.Equal(t, []string{"foo", "bar"}, findTemplateVars("{{foo}} and {{bar}}"))
	assert.Empty(t, findTemplateVars("no vars here"))
	assert.Equal(t, []string{"spaced"}, findTemplateVars("{{ spaced }}"))
}

func TestSubstituteVars(t *testing.T) {
	params := map[string]interface{}{
		"path":    "{{dir}}/{{file}}",
		"content": "hello",
		"nested": map[string]interface{}{
			"key": "{{nested_var}}",
		},
	}

	vars := map[string]string{
		"dir":        "/tmp",
		"file":       "output.txt",
		"nested_var": "value",
	}

	result := substituteVars(params, vars)
	assert.Equal(t, "/tmp/output.txt", result["path"])
	assert.Equal(t, "hello", result["content"])

	nested := result["nested"].(map[string]interface{})
	assert.Equal(t, "value", nested["key"])
}

func TestTopologicalSort_Simple(t *testing.T) {
	steps := []ChainStep{
		{ID: "c", ToolName: "C", DependsOn: []string{"b"}},
		{ID: "a", ToolName: "A"},
		{ID: "b", ToolName: "B", DependsOn: []string{"a"}},
	}

	sorted, err := topologicalSort(steps)
	require.NoError(t, err)
	assert.Len(t, sorted, 3)

	// a must come before b, and b before c.
	idxA, idxB, idxC := -1, -1, -1
	for i, s := range sorted {
		switch s.ID {
		case "a":
			idxA = i
		case "b":
			idxB = i
		case "c":
			idxC = i
		}
	}

	assert.True(t, idxA < idxB, "a should come before b")
	assert.True(t, idxB < idxC, "b should come before c")
}

func TestTopologicalSort_Cycle(t *testing.T) {
	steps := []ChainStep{
		{ID: "a", ToolName: "A", DependsOn: []string{"b"}},
		{ID: "b", ToolName: "B", DependsOn: []string{"a"}},
	}

	_, err := topologicalSort(steps)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cycle")
}

func TestTopologicalSort_NoDeps(t *testing.T) {
	steps := []ChainStep{
		{ID: "a", ToolName: "A"},
		{ID: "b", ToolName: "B"},
	}

	sorted, err := topologicalSort(steps)
	require.NoError(t, err)
	assert.Len(t, sorted, 2)
}

// helper
func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
