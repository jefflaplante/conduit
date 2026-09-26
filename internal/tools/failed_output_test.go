package tools

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"conduit/internal/ai"
	"conduit/internal/config"
	"conduit/internal/tools/types"
)

// conduit-31jg.10: a failed tool's Content (e.g. compiler/test output from a
// non-zero Bash exit) must reach the model, not just "exit status 1".
func TestFormatToolResultForAI_FailedResultIncludesOutput(t *testing.T) {
	engine := NewExecutionEngine(NewMockRegistry(), 3, 30*time.Second, 10)

	output := "# conduit/internal/foo\n./foo.go:12:2: undefined: bar\nFAIL\tconduit/internal/foo [build failed]"
	res := types.NewErrorResult("execution_error", "Command execution failed: exit status 1").
		WithContext(map[string]interface{}{"output": output}).
		WithSuggestions([]string{"Check command output for error details"})
	res.Content = output

	formatted := engine.formatToolResultForAI(&ExecutionResult{
		ToolCall: &ai.ToolCall{Name: "Bash"},
		Result:   res,
	})

	if !strings.Contains(formatted, "Command execution failed: exit status 1") {
		t.Errorf("missing error line:\n%s", formatted)
	}
	if !strings.Contains(formatted, "undefined: bar") {
		t.Errorf("failed tool output not shown to model:\n%s", formatted)
	}
	if n := strings.Count(formatted, "undefined: bar"); n != 1 {
		t.Errorf("output rendered %d times, want exactly once:\n%s", n, formatted)
	}
	// Output comes after the error/suggestion lines.
	if strings.Index(formatted, "Suggestions:") > strings.Index(formatted, "undefined: bar") {
		t.Errorf("output should follow error details:\n%s", formatted)
	}
}

func TestFormatToolResultForAI_FailedOutputTruncated(t *testing.T) {
	engine := NewExecutionEngine(NewMockRegistry(), 3, 30*time.Second, 10)
	engine.SetMaxResultChars(2000)

	var b strings.Builder
	for i := 0; i < 2000; i++ {
		b.WriteString("=== RUN TestSomething line of noisy output\n")
	}
	res := &ToolResult{Success: false, Error: "Command execution failed: exit status 1", Content: b.String()}

	formatted := engine.formatToolResultForAI(&ExecutionResult{
		ToolCall: &ai.ToolCall{Name: "Bash"},
		Result:   res,
	})
	// error line + a bit of header, plus truncated output bounded by maxChars.
	if len(formatted) > 2000+500 {
		t.Errorf("failed output not truncated: %d chars", len(formatted))
	}
	if !strings.Contains(formatted, "=== RUN TestSomething") {
		t.Errorf("expected (truncated) output to be present")
	}
}

func TestFormatToolResultForAI_FailedContentEqualsErrorNotDuplicated(t *testing.T) {
	engine := NewExecutionEngine(NewMockRegistry(), 3, 30*time.Second, 10)
	res := &ToolResult{Success: false, Error: "boom", Content: "boom"}
	formatted := engine.formatToolResultForAI(&ExecutionResult{
		ToolCall: &ai.ToolCall{Name: "X"},
		Result:   res,
	})
	if formatted != "Tool 'X' failed: boom" {
		t.Errorf("unexpected: %q", formatted)
	}
}

// End-to-end through the real ExecTool: a failing command's stderr reaches the model.
func TestExecToolFailure_OutputReachesModel(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	tmpDir := t.TempDir()
	registry := NewRegistry(config.ToolsConfig{
		EnabledTools: []string{"Bash"},
		Sandbox:      config.SandboxConfig{WorkspaceDir: tmpDir, AllowedPaths: []string{tmpDir}},
	})
	registry.SetServices(&types.ToolServices{})
	engine := NewExecutionEngine(registry, 3, 30*time.Second, 10)

	results, err := engine.ExecuteToolCalls(context.Background(), []ai.ToolCall{{
		ID:   "1",
		Name: "Bash",
		Args: map[string]interface{}{"command": "echo compile-error-marker >&2; exit 3"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	formatted := engine.formatToolResultForAI(results[0])
	if !strings.Contains(formatted, "compile-error-marker") {
		t.Fatalf("stderr of failed command missing from model-facing text:\n%s", formatted)
	}
}
