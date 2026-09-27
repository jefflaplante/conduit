package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"conduit/internal/ai"
	"conduit/internal/config"
	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// conduit-31jg.58: calls in one turn that touch the same file (at least one
// of them writing) run sequentially in model order; everything else stays
// parallel.

// slowAppendTool is a read-modify-write tool with a window between the read
// and the write, so concurrent calls on one file reliably lose updates.
func slowAppendTool(name string) *MockTool {
	return &MockTool{name: name, executeFunc: func(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
		p, _ := args["path"].(string)
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		time.Sleep(10 * time.Millisecond)
		line, _ := args["line"].(string)
		if err := os.WriteFile(p, append(b, []byte(line+"\n")...), 0o644); err != nil {
			return nil, err
		}
		return &ToolResult{Success: true, Content: "appended " + line}, nil
	}}
}

func TestExecuteParallel_SamePathWritesAllApply(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "f.txt")
	require.NoError(t, os.WriteFile(f, nil, 0o644))

	reg := NewMockRegistry()
	reg.AddTool(slowAppendTool("Edit"))
	reg.AddTool(slowAppendTool("Write"))
	e := NewExecutionEngine(reg, 8, 0, 5)

	var calls []ai.ToolCall
	for i := 0; i < 6; i++ {
		name := "Edit"
		if i%2 == 1 {
			name = "Write"
		}
		calls = append(calls, ai.ToolCall{ID: fmt.Sprint(i), Name: name,
			Args: map[string]interface{}{"path": f, "line": fmt.Sprint(i)}})
	}
	results, err := e.ExecuteToolCalls(context.Background(), calls)
	require.NoError(t, err)
	for i, r := range results {
		require.NotNil(t, r)
		require.NoError(t, r.Error)
		assert.Equal(t, fmt.Sprint(i), r.ToolCall.ID)
	}
	b, err := os.ReadFile(f)
	require.NoError(t, err)
	// All applied, in model order.
	assert.Equal(t, "0\n1\n2\n3\n4\n5\n", string(b))
}

func TestExecuteParallel_AliasedPathsCollide(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "sub"), 0o755))
	f := filepath.Join(dir, "f.txt")
	require.NoError(t, os.WriteFile(f, nil, 0o644))
	link := filepath.Join(dir, "link.txt")
	require.NoError(t, os.Symlink(f, link))
	linkDir := filepath.Join(dir, "linkdir")
	require.NoError(t, os.Symlink(dir, linkDir))

	reg := NewMockRegistry()
	reg.AddTool(slowAppendTool("Edit"))
	e := NewExecutionEngine(reg, 8, 0, 5)

	aliases := []string{
		f,
		dir + "/sub/../f.txt",
		link,
		filepath.Join(linkDir, "f.txt"),
	}
	var calls []ai.ToolCall
	for i, p := range aliases {
		calls = append(calls, ai.ToolCall{ID: fmt.Sprint(i), Name: "Edit",
			Args: map[string]interface{}{"path": p, "line": fmt.Sprint(i)}})
	}
	results, err := e.ExecuteToolCalls(context.Background(), calls)
	require.NoError(t, err)
	for _, r := range results {
		require.NoError(t, r.Error)
	}
	b, err := os.ReadFile(f)
	require.NoError(t, err)
	assert.Equal(t, "0\n1\n2\n3\n", string(b))
}

// barrierTool blocks until `want` calls are inside it at once (or fails
// after a timeout). Parallel calls pass quickly; serialized calls fail
// deterministically, so the test cannot flake on a slow machine.
type barrierTool struct {
	name    string
	want    int32
	arrived atomic.Int32
}

func (b *barrierTool) tool() *MockTool {
	return &MockTool{name: b.name, executeFunc: func(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
		b.arrived.Add(1)
		deadline := time.Now().Add(3 * time.Second)
		for b.arrived.Load() < b.want {
			if time.Now().After(deadline) {
				return &ToolResult{Success: false, Content: "barrier timeout"}, nil
			}
			time.Sleep(time.Millisecond)
		}
		return &ToolResult{Success: true, Content: fmt.Sprint(args["path"])}, nil
	}}
}

func TestExecuteParallel_DifferentPathsStayParallel(t *testing.T) {
	dir := t.TempDir()
	bw := &barrierTool{name: "Write", want: 3}
	reg := NewMockRegistry()
	reg.AddTool(bw.tool())
	e := NewExecutionEngine(reg, 4, 0, 5)

	calls := []ai.ToolCall{
		{ID: "a", Name: "Write", Args: map[string]interface{}{"path": filepath.Join(dir, "a.txt")}},
		{ID: "b", Name: "Write", Args: map[string]interface{}{"path": filepath.Join(dir, "b.txt")}},
		{ID: "c", Name: "Write", Args: map[string]interface{}{"path": filepath.Join(dir, "c.txt")}},
	}
	results, err := e.ExecuteToolCalls(context.Background(), calls)
	require.NoError(t, err)
	for i, r := range results {
		assert.True(t, r.Result.Success, "call %d: %s", i, r.Result.Content)
		assert.Equal(t, calls[i].ID, r.ToolCall.ID)
		assert.Equal(t, calls[i].Args["path"], r.Result.Content)
	}
}

func TestExecuteParallel_ReadOnlySamePathStaysParallel(t *testing.T) {
	f := filepath.Join(t.TempDir(), "r.txt")
	br := &barrierTool{name: "Read", want: 2}
	reg := NewMockRegistry()
	reg.AddTool(br.tool())
	e := NewExecutionEngine(reg, 4, 0, 5)

	calls := []ai.ToolCall{
		{ID: "1", Name: "Read", Args: map[string]interface{}{"path": f}},
		{ID: "2", Name: "Read", Args: map[string]interface{}{"path": f}},
	}
	results, err := e.ExecuteToolCalls(context.Background(), calls)
	require.NoError(t, err)
	for _, r := range results {
		assert.True(t, r.Result.Success, r.Result.Content)
	}
}

func TestExecuteParallel_ReadAfterWriteOrdered(t *testing.T) {
	f := filepath.Join(t.TempDir(), "raw.txt")
	require.NoError(t, os.WriteFile(f, []byte("old"), 0o644))

	reg := NewMockRegistry()
	reg.AddTool(&MockTool{name: "Write", executeFunc: func(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
		time.Sleep(20 * time.Millisecond)
		return &ToolResult{Success: true}, os.WriteFile(args["path"].(string), []byte("new"), 0o644)
	}})
	reg.AddTool(&MockTool{name: "Read", executeFunc: func(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
		b, err := os.ReadFile(args["path"].(string))
		return &ToolResult{Success: true, Content: string(b)}, err
	}})
	e := NewExecutionEngine(reg, 4, 0, 5)

	results, err := e.ExecuteToolCalls(context.Background(), []ai.ToolCall{
		{ID: "w", Name: "Write", Args: map[string]interface{}{"path": f}},
		{ID: "r", Name: "Read", Args: map[string]interface{}{"path": f}},
	})
	require.NoError(t, err)
	assert.Equal(t, "w", results[0].ToolCall.ID)
	assert.Equal(t, "new", results[1].Result.Content)
}

// A panic in one call of a serialized group must not skip the rest.
func TestExecuteParallel_SamePathGroupSurvivesPanic(t *testing.T) {
	f := filepath.Join(t.TempDir(), "p.txt")
	var ran atomic.Int32
	reg := NewMockRegistry()
	reg.AddTool(&MockTool{name: "Write", executeFunc: func(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
		ran.Add(1)
		return &ToolResult{Success: true}, nil
	}})
	e := NewExecutionEngine(reg, 4, 0, 5)
	e.AddMiddleware(&panicOnceMiddleware{})

	results, err := e.ExecuteToolCalls(context.Background(), []ai.ToolCall{
		{ID: "1", Name: "Write", Args: map[string]interface{}{"path": f}},
		{ID: "2", Name: "Write", Args: map[string]interface{}{"path": f}},
	})
	require.NoError(t, err)
	require.Error(t, results[0].Error)
	require.NoError(t, results[1].Error)
	assert.EqualValues(t, 1, ran.Load())
}

type panicOnceMiddleware struct{ once sync.Once }

func (p *panicOnceMiddleware) BeforeExecution(context.Context, *ai.ToolCall) error {
	fired := false
	p.once.Do(func() { fired = true })
	if fired {
		panic("first call boom")
	}
	return nil
}

func (p *panicOnceMiddleware) AfterExecution(context.Context, *ai.ToolCall, *ExecutionResult) error {
	return nil
}

// End to end with the real Registry and Edit tool; relative and absolute
// spellings of one file must collide (relative paths resolve from the
// workspace, not the process cwd).
func TestExecuteParallel_RealEditsSameFileAllApply(t *testing.T) {
	ws := t.TempDir()
	f := filepath.Join(ws, "doc.txt")
	const n = 8
	var lines []string
	for i := 0; i < n; i++ {
		lines = append(lines, fmt.Sprintf("line%d", i))
	}
	require.NoError(t, os.WriteFile(f, []byte(strings.Join(lines, "\n")+"\n"), 0o644))

	cfg := &config.Config{Tools: config.ToolsConfig{
		EnabledTools: []string{"Edit"},
		Sandbox:      config.SandboxConfig{WorkspaceDir: ws},
	}}
	reg := NewRegistry(cfg.Tools)
	reg.SetServices(&types.ToolServices{ConfigMgr: cfg})
	e := NewExecutionEngine(reg, 8, 0, 5)

	var calls []ai.ToolCall
	for i := 0; i < n; i++ {
		p := f
		if i%2 == 1 {
			p = "doc.txt"
		}
		calls = append(calls, ai.ToolCall{ID: fmt.Sprint(i), Name: "Edit", Args: map[string]interface{}{
			"path": p, "old_string": fmt.Sprintf("line%d\n", i), "new_string": fmt.Sprintf("LINE%d\n", i),
		}})
	}
	results, err := e.ExecuteToolCalls(context.Background(), calls)
	require.NoError(t, err)
	for i, r := range results {
		require.NoError(t, r.Error)
		require.True(t, r.Result.Success, "edit %d: %s %s", i, r.Result.Error, r.Result.Content)
	}
	b, err := os.ReadFile(f)
	require.NoError(t, err)
	for i := 0; i < n; i++ {
		assert.Contains(t, string(b), fmt.Sprintf("LINE%d\n", i), "edit %d lost", i)
	}
}
