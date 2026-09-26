package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"conduit/internal/ai"
	"conduit/internal/config"
	"conduit/internal/skills"
	"conduit/internal/tools/types"
)

// newPanicTestRegistry builds a real Registry (so the recover in
// Registry.ExecuteTool is exercised) with the given extra tools enabled.
func newPanicTestRegistry(t *testing.T, extra ...types.Tool) *Registry {
	t.Helper()
	dir := t.TempDir()
	r := NewRegistry(config.ToolsConfig{
		Sandbox: config.SandboxConfig{WorkspaceDir: dir, AllowedPaths: []string{dir}},
	})
	r.SetServices(&types.ToolServices{})
	r.mu.Lock()
	for _, tool := range extra {
		r.tools[tool.Name()] = tool
		r.enabledTools[normalizeToolName(tool.Name())] = true
	}
	r.mu.Unlock()
	return r
}

func panicTool(name string) *MockTool {
	return &MockTool{
		name: name,
		executeFunc: func(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
			var m map[string]int
			m["boom"] = 1 // nil map write -> panic
			return nil, nil
		},
	}
}

func okTool(name string, delay time.Duration) *MockTool {
	return &MockTool{
		name: name,
		executeFunc: func(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
			time.Sleep(delay)
			return &ToolResult{Success: true, Content: "ok:" + name}, nil
		},
	}
}

// conduit-31jg.19: a panicking tool must yield a failed result, not crash.
func TestRegistry_ExecuteTool_RecoversPanic(t *testing.T) {
	r := newPanicTestRegistry(t, panicTool("Boom"))

	res, err := r.ExecuteTool(context.Background(), "Boom", nil)
	if err == nil {
		t.Fatal("expected error from panicking tool")
	}
	if !strings.Contains(err.Error(), `"Boom"`) || !strings.Contains(err.Error(), "panicked") {
		t.Errorf("error should name the tool and say it panicked: %v", err)
	}
	if res == nil || res.Success {
		t.Fatalf("expected failed result, got %+v", res)
	}
	if res.ErrorDetails == nil || res.ErrorDetails.Type != "tool_panic" {
		t.Errorf("expected tool_panic error type, got %+v", res.ErrorDetails)
	}
}

func TestExecutionEngine_SingleCallPanicRecovered(t *testing.T) {
	r := newPanicTestRegistry(t, panicTool("Boom"))
	engine := NewExecutionEngine(r, 3, 5*time.Second, 10)

	results, err := engine.ExecuteToolCalls(context.Background(), []ai.ToolCall{{ID: "1", Name: "Boom"}})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Error == nil {
		t.Fatal("expected execution error for panicking tool")
	}
	if msg := engine.formatToolResultForAI(results[0]); !strings.Contains(msg, "panicked") {
		t.Errorf("model-facing text should mention panic: %q", msg)
	}
}

func TestExecutionEngine_ParallelPanicKeepsOrder(t *testing.T) {
	r := newPanicTestRegistry(t,
		okTool("A", 30*time.Millisecond),
		panicTool("Boom"),
		okTool("C", 1*time.Millisecond),
		okTool("D", 10*time.Millisecond),
	)
	engine := NewExecutionEngine(r, 4, 5*time.Second, 10)

	calls := []ai.ToolCall{{ID: "a", Name: "A"}, {ID: "b", Name: "Boom"}, {ID: "c", Name: "C"}, {ID: "d", Name: "D"}}
	results, err := engine.ExecuteToolCalls(context.Background(), calls)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(calls) {
		t.Fatalf("got %d results, want %d", len(results), len(calls))
	}
	for i, res := range results {
		if res.ToolCall.ID != calls[i].ID {
			t.Errorf("result %d out of order: got %s want %s", i, res.ToolCall.ID, calls[i].ID)
		}
	}
	if results[1].Error == nil || results[1].Result == nil || results[1].Result.Success {
		t.Errorf("panicking tool should fail: %+v", results[1])
	}
	for _, i := range []int{0, 2, 3} {
		want := "ok:" + calls[i].Name
		if results[i].Error != nil || results[i].Result == nil || results[i].Result.Content != want {
			t.Errorf("result %d: want %q, got %+v", i, want, results[i].Result)
		}
	}
}

// writeSkill creates a minimal SKILL.md with a script so it yields a skill tool.
func writeSkill(t *testing.T, dir, name string) {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Join(skillDir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	md := fmt.Sprintf("---\nname: %s\ndescription: test skill %s\n---\n\n# %s\n\nTest skill.\n", name, name, name)
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "scripts", "run.sh"), []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// conduit-31jg.19: RefreshSkillTools concurrently with ExecuteTool /
// GetAvailableTools must not race ("concurrent map read and map write").
// Run with -race.
func TestRegistry_RefreshSkillToolsConcurrentWithExecute(t *testing.T) {
	skillsDir := t.TempDir()
	writeSkill(t, skillsDir, "alpha")
	writeSkill(t, skillsDir, "beta")

	mgr := skills.NewManager(skills.SkillsConfig{Enabled: true, SearchPaths: []string{skillsDir}})
	if err := mgr.Initialize(context.Background()); err != nil {
		t.Fatalf("skills init: %v", err)
	}

	dir := t.TempDir()
	r := NewRegistry(config.ToolsConfig{
		Sandbox: config.SandboxConfig{WorkspaceDir: dir, AllowedPaths: []string{dir}},
	})
	r.SetServices(&types.ToolServices{SkillsManager: mgr})
	r.mu.Lock()
	r.tools["Fast"] = okTool("Fast", 0)
	r.enabledTools[normalizeToolName("Fast")] = true
	r.mu.Unlock()

	if n := r.RefreshSkillTools(); n == 0 {
		t.Fatal("expected skill tools to be registered; test would not exercise the bridge path")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ctx.Err() == nil {
			r.RefreshSkillTools()
		}
	}()
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				if _, err := r.ExecuteTool(context.Background(), "Fast", nil); err != nil {
					t.Errorf("ExecuteTool: %v", err)
					return
				}
				// Unknown tool exercises the suggestion helpers that iterate the maps.
				_, _ = r.ExecuteTool(context.Background(), "NoSuchTool", nil)
				_ = r.GetAvailableTools()
				_ = r.HasTool("Fast")
				_ = r.GetToolHelp("Fast")
			}
		}()
	}
	wg.Wait()
}

// A running tool that triggers RefreshSkillTools (as the Gateway reload action
// does) must not deadlock: ExecuteTool must not hold the lock while executing.
func TestRegistry_ToolCanRefreshRegistryWithoutDeadlock(t *testing.T) {
	var r *Registry
	reloader := &MockTool{
		name: "Reloader",
		executeFunc: func(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
			n := r.RefreshSkillTools()
			return &ToolResult{Success: true, Content: fmt.Sprint(n)}, nil
		},
	}
	r = newPanicTestRegistry(t, reloader)

	done := make(chan struct{})
	go func() {
		_, _ = r.ExecuteTool(context.Background(), "Reloader", nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock: tool calling RefreshSkillTools from inside ExecuteTool hung")
	}
}
