package tools

import (
	"context"

	"conduit/internal/ai"
)

// ExecuteForModel runs one tool call through the same pipeline the agent loop
// uses for a single call — per-call deadline (callTimeout, incl. Bash's own
// timeout), pipeline panic recovery, middleware, debug buffer and the
// reflection hook — and returns the model-facing rendering of the result
// (Data opt-in, smart truncation to maxResultChars). isError reports a Go
// error or an unsuccessful result.
//
// conduit-31jg.8: external callers (the MCP server) used to call
// Registry.ExecuteTool directly and so skipped all of the above. Callers are
// responsible for the approval origin on ctx (MCP marks it non-interactive).
func (e *ExecutionEngine) ExecuteForModel(ctx context.Context, name string, args map[string]interface{}) (content string, isError bool) {
	res := e.executeSingle(ctx, ai.ToolCall{Name: name, Args: args})
	isError = res.Error != nil || res.Result == nil || !res.Result.Success
	return e.formatToolResultForAI(res), isError
}
