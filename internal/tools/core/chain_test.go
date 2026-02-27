package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"conduit/internal/chain"
	"conduit/internal/config"
	"conduit/internal/tools/types"
)

// mockChainExecutor implements ChainToolExecutor for tests.
type mockChainExecutor struct {
	tools   map[string]types.Tool
	results map[string]*types.ToolResult
}

func (m *mockChainExecutor) ExecuteTool(_ context.Context, name string, _ map[string]interface{}) (*types.ToolResult, error) {
	if r, ok := m.results[name]; ok {
		return r, nil
	}
	return &types.ToolResult{Success: true, Content: "ok from " + name}, nil
}

func (m *mockChainExecutor) GetAvailableTools() map[string]types.Tool {
	return m.tools
}

// stubTool satisfies types.Tool for mock registration.
type stubTool struct{ name string }

func (s *stubTool) Name() string                       { return s.name }
func (s *stubTool) Description() string                { return s.name + " stub" }
func (s *stubTool) Parameters() map[string]interface{} { return map[string]interface{}{} }
func (s *stubTool) Execute(_ context.Context, _ map[string]interface{}) (*types.ToolResult, error) {
	return &types.ToolResult{Success: true, Content: "stub"}, nil
}

func newTestChainTool(t *testing.T, executor *mockChainExecutor) (*ChainTool, string) {
	t.Helper()
	dir := t.TempDir()
	chainsDir := filepath.Join(dir, "chains")
	if err := os.MkdirAll(chainsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sandboxCfg := config.SandboxConfig{WorkspaceDir: dir}
	services := &types.ToolServices{}

	return NewChainTool(services, sandboxCfg, executor), chainsDir
}

func writeChain(t *testing.T, chainsDir string, c *chain.Chain) {
	t.Helper()
	if err := chain.SaveChain(c, chainsDir); err != nil {
		t.Fatal(err)
	}
}

func sampleChain() *chain.Chain {
	return &chain.Chain{
		Name:        "test-chain",
		Description: "A test chain",
		Steps: []chain.ChainStep{
			{ID: "s1", ToolName: "Bash", Params: map[string]interface{}{"command": "echo {{msg}}"}},
		},
		Variables: []chain.Variable{
			{Name: "msg", Description: "message", Default: "hello"},
		},
	}
}

func TestChainTool_ListEmpty(t *testing.T) {
	tool, _ := newTestChainTool(t, &mockChainExecutor{tools: map[string]types.Tool{}})
	result, err := tool.Execute(context.Background(), map[string]interface{}{"action": "list"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if result.Data["count"] != 0 {
		t.Fatalf("expected 0 chains, got %v", result.Data["count"])
	}
}

func TestChainTool_ListWithChains(t *testing.T) {
	tool, chainsDir := newTestChainTool(t, &mockChainExecutor{tools: map[string]types.Tool{}})
	writeChain(t, chainsDir, sampleChain())

	result, err := tool.Execute(context.Background(), map[string]interface{}{"action": "list"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if result.Data["count"] != 1 {
		t.Fatalf("expected 1 chain, got %v", result.Data["count"])
	}
}

func TestChainTool_Show(t *testing.T) {
	tool, chainsDir := newTestChainTool(t, &mockChainExecutor{tools: map[string]types.Tool{}})
	writeChain(t, chainsDir, sampleChain())

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "show",
		"name":   "test-chain",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}

	// Content should be valid JSON.
	var c chain.Chain
	if err := json.Unmarshal([]byte(result.Content), &c); err != nil {
		t.Fatalf("expected JSON content, got parse error: %v", err)
	}
	if c.Name != "test-chain" {
		t.Fatalf("expected chain name test-chain, got %s", c.Name)
	}
}

func TestChainTool_ShowMissing(t *testing.T) {
	tool, _ := newTestChainTool(t, &mockChainExecutor{tools: map[string]types.Tool{}})
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "show",
		"name":   "nonexistent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success {
		t.Fatal("expected failure for missing chain")
	}
}

func TestChainTool_ShowMissingName(t *testing.T) {
	tool, _ := newTestChainTool(t, &mockChainExecutor{tools: map[string]types.Tool{}})
	result, err := tool.Execute(context.Background(), map[string]interface{}{"action": "show"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success {
		t.Fatal("expected failure when name is missing")
	}
}

func TestChainTool_ValidateValid(t *testing.T) {
	executor := &mockChainExecutor{
		tools: map[string]types.Tool{
			"Bash": &stubTool{name: "Bash"},
		},
	}
	tool, chainsDir := newTestChainTool(t, executor)
	writeChain(t, chainsDir, sampleChain())

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "validate",
		"name":   "test-chain",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("expected valid chain, got error: %s", result.Error)
	}
}

func TestChainTool_ValidateUnknownTool(t *testing.T) {
	// No tools registered — Bash won't be found.
	executor := &mockChainExecutor{tools: map[string]types.Tool{}}
	tool, chainsDir := newTestChainTool(t, executor)
	writeChain(t, chainsDir, sampleChain())

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "validate",
		"name":   "test-chain",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success {
		t.Fatal("expected validation failure for unknown tool")
	}
}

func TestChainTool_Run(t *testing.T) {
	executor := &mockChainExecutor{
		tools: map[string]types.Tool{
			"Bash": &stubTool{name: "Bash"},
		},
		results: map[string]*types.ToolResult{
			"Bash": {Success: true, Content: "hello world"},
		},
	}
	tool, chainsDir := newTestChainTool(t, executor)
	writeChain(t, chainsDir, sampleChain())

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":    "run",
		"name":      "test-chain",
		"variables": map[string]interface{}{"msg": "hi"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if result.Data["success"] != true {
		t.Fatal("expected chain result success=true")
	}
}

func TestChainTool_RunFailingStep(t *testing.T) {
	executor := &mockChainExecutor{
		tools: map[string]types.Tool{
			"Bash": &stubTool{name: "Bash"},
		},
		results: map[string]*types.ToolResult{
			"Bash": {Success: false, Error: "command not found"},
		},
	}
	tool, chainsDir := newTestChainTool(t, executor)
	writeChain(t, chainsDir, sampleChain())

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "run",
		"name":   "test-chain",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Chain should report failure from the step.
	if result.Success {
		t.Fatal("expected chain failure when step fails")
	}
}

func TestChainTool_UnknownAction(t *testing.T) {
	tool, _ := newTestChainTool(t, &mockChainExecutor{tools: map[string]types.Tool{}})
	result, err := tool.Execute(context.Background(), map[string]interface{}{"action": "delete"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success {
		t.Fatal("expected failure for unknown action")
	}
}
