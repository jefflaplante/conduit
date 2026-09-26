package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"conduit/internal/chain"
	"conduit/internal/config"
	"conduit/internal/tools/types"
)

// selfExecutor routes "Chain" back into the ChainTool under test, as the
// real Registry does, so a chain that runs itself recurses.
type selfExecutor struct {
	chainTool *ChainTool
	calls     atomic.Int32
}

func (s *selfExecutor) ExecuteTool(ctx context.Context, name string, args map[string]interface{}) (*types.ToolResult, error) {
	if s.calls.Add(1) > 50 {
		panic("unbounded Chain recursion")
	}
	if name == "Chain" {
		return s.chainTool.Execute(ctx, args)
	}
	return &types.ToolResult{Success: true, Content: "ok"}, nil
}

func (s *selfExecutor) GetAvailableTools() map[string]types.Tool {
	return map[string]types.Tool{"Chain": s.chainTool}
}

// conduit-31jg.39: a chain that runs itself must stop at the depth guard
// instead of recursing until the stack overflows.
func TestChainTool_RecursionDepthGuard(t *testing.T) {
	dir := t.TempDir()
	chainsDir := filepath.Join(dir, "chains")
	if err := os.MkdirAll(chainsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	exec := &selfExecutor{}
	exec.chainTool = NewChainTool(&types.ToolServices{}, config.SandboxConfig{WorkspaceDir: dir}, exec)
	writeChain(t, chainsDir, &chain.Chain{
		Name: "loop",
		Steps: []chain.ChainStep{{
			ID: "again", ToolName: "Chain",
			Params: map[string]interface{}{"action": "run", "name": "loop"},
		}},
	})

	res, err := exec.chainTool.Execute(context.Background(), map[string]interface{}{"action": "run", "name": "loop"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Success {
		t.Fatal("recursive chain should fail")
	}
	if !strings.Contains(res.Error+res.Content, "nesting") {
		t.Fatalf("expected nesting-limit error, got error=%q content=%q", res.Error, res.Content)
	}
	if n := exec.calls.Load(); n > int32(3)+1 {
		t.Fatalf("recursed %d times; guard is 3", n)
	}
}

func TestChainTool_StepOutputTrimIsRuneSafe(t *testing.T) {
	executor := &mockChainExecutor{
		tools:   map[string]types.Tool{"Echo": &stubTool{name: "Echo"}},
		results: map[string]*types.ToolResult{"Echo": {Success: true, Content: strings.Repeat("日本", 400)}},
	}
	tool, chainsDir := newTestChainTool(t, executor)
	writeChain(t, chainsDir, &chain.Chain{Name: "u", Steps: []chain.ChainStep{{ID: "s", ToolName: "Echo", Params: map[string]interface{}{}}}})
	res, err := tool.Execute(context.Background(), map[string]interface{}{"action": "run", "name": "u"})
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(res.Content) {
		t.Fatalf("step output trim split a rune: %q", res.Content)
	}
}
