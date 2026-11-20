package tools

import (
	"context"
	gojson "encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"go.starlark.net/lib/json"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"conduit/internal/ai"
	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
)

// Code mode (prototype). The Code tool runs a short Starlark script that
// calls other Conduit tools and returns only the value the model needs, so
// a multi-call task costs one round trip and the intermediate results never
// enter the context window.
//
// Starlark has no file, network or process access of its own: every side
// effect is a tool call. Each inner call goes through the same
// ExecutionEngine.executeSingle path as a model tool call (per-call
// deadline, middleware, policy observation, approval origin, reflection
// hook, tool events, per-turn failure tracking), under the tool's own name.

const (
	codeToolName          = "Code"
	codeDefaultTimeout    = 120 * time.Second
	codeMaxTimeout        = 600 * time.Second
	codeMaxCalls          = 50
	codeMaxExecutionSteps = 50_000_000
	codeMaxStdout         = 64 * 1024
)

// codeForbiddenCalls are tools a script may not call (normalized names):
// no nested scripts, no saved chains, no sub-agents.
var codeForbiddenCalls = map[string]bool{
	"code":          true,
	"chain":         true,
	"sessionsspawn": true,
}

type codeEngineKey struct{}

// withCodeEngine records the engine running a call so the Code tool can
// send its inner calls through the same pipeline.
func withCodeEngine(ctx context.Context, e *ExecutionEngine) context.Context {
	return context.WithValue(ctx, codeEngineKey{}, e)
}

func codeEngineFrom(ctx context.Context) *ExecutionEngine {
	e, _ := ctx.Value(codeEngineKey{}).(*ExecutionEngine)
	return e
}

// CodeTool runs Starlark scripts that call other tools.
type CodeTool struct {
	registry *Registry
}

func (t *CodeTool) Name() string { return codeToolName }

func (t *CodeTool) Description() string {
	return `Run a short Starlark (Python-like) script that calls other tools and returns only what you need. Use it when a task needs 3+ tool calls whose intermediate results you don't need to read: batch reads, search-then-filter, the same change across many files, aggregating data.

Inside the script:
- tools.<Name>(param=value, ...) calls any enabled tool with its normal parameters and returns {"ok": bool, "content": str, "error": str, "data": dict}. A failed call returns ok=False; it does not stop the script.
- parallel([call("Read", file_path="a"), call("Read", file_path="b")]) runs calls concurrently and returns a list of results in order.
- json.encode / json.decode / json.encode_indent work on values.
- print(...) writes to stdout, which is returned.
- Assign the value to return to the global "result".

Example:
    hits = tools.MemorySearch(query="retry policy")
    paths = [l.split(":")[0] for l in hits["content"].splitlines() if l.endswith(".md")][:5]
    reads = parallel([call("Read", file_path=p) for p in paths])
    result = {p: r["content"][:300] for p, r in zip(paths, reads) if r["ok"]}

Limits: ` + fmt.Sprint(codeMaxCalls) + ` tool calls per script; Code, Chain and SessionsSpawn cannot be called from a script; no imports, files or network except through tools. Tool calls already made are reported even if the script fails, so check before re-running side-effecting work.`
}

func (t *CodeTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"code": map[string]interface{}{
				"type":        "string",
				"description": "Starlark source. Set the global `result` to the value to return.",
			},
			"timeout": map[string]interface{}{
				"type":        "integer",
				"description": fmt.Sprintf("Seconds before the script is stopped (default %d, max %d)", int(codeDefaultTimeout.Seconds()), int(codeMaxTimeout.Seconds())),
			},
		},
		"required": []string{"code"},
	}
}

// CallTimeout sizes the engine deadline for the whole script (inner calls
// get their own deadlines inside it).
func (t *CodeTool) CallTimeout(args map[string]interface{}) (time.Duration, bool) {
	return codeTimeout(args), true
}

func codeTimeout(args map[string]interface{}) time.Duration {
	secs := toolargs.GetInt(args, "timeout", 0)
	if secs <= 0 {
		return codeDefaultTimeout
	}
	d := time.Duration(secs) * time.Second
	if d > codeMaxTimeout {
		return codeMaxTimeout
	}
	return d
}

func (t *CodeTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	src := toolargs.GetString(args, "code", "")
	if strings.TrimSpace(src) == "" {
		return types.NewErrorResult("missing_parameter", "code is required"), nil
	}
	engine := codeEngineFrom(ctx)
	if engine == nil {
		return types.NewErrorResult("code_engine_unavailable",
			"Code must run through the tool execution engine (it cannot be called from Chain or directly)"), nil
	}

	runCtx, cancel := context.WithTimeout(ctx, codeTimeout(args))
	defer cancel()

	run := &codeRun{ctx: runCtx, engine: engine, registry: t.registry, used: map[string]int{}}
	return run.execute(src), nil
}

func (t *CodeTool) SelfTest(ctx context.Context, opts *types.SelfTestOptions) *types.SelfTestResult {
	start := time.Now()
	thread := &starlark.Thread{Name: "selftest"}
	_, err := starlark.ExecFileOptions(codeFileOptions(), thread, "selftest.star", "result = sum([1, 2, 3])", nil)
	res := &types.SelfTestResult{
		Status:       types.SelfTestStatusOK,
		Message:      "Code tool is functional",
		Capabilities: []string{"starlark", "tool_calls", "parallel", "json"},
		TestedAt:     time.Now(),
	}
	if err != nil {
		res.Status = types.SelfTestStatusFailed
		res.Message = "Starlark interpreter failed: " + err.Error()
	}
	res.TestDuration = time.Since(start)
	return res
}

func codeFileOptions() *syntax.FileOptions {
	return &syntax.FileOptions{Set: true, While: true, TopLevelControl: true, GlobalReassign: true}
}

// codeCallRecord is one inner call, reported back to the model.
type codeCallRecord struct {
	Tool string `json:"tool"`
	OK   bool   `json:"ok"`
}

// codeRun is the state of one script execution. Starlark is single-threaded
// and parallel() waits for its batch, so the fields need no lock.
type codeRun struct {
	ctx       context.Context
	engine    *ExecutionEngine
	registry  *Registry
	nCalls    int
	calls     []codeCallRecord
	used      map[string]int
	untrusted []string
	stdout    strings.Builder
	truncated bool
	seq       atomic.Int64
}

func (r *codeRun) execute(src string) *types.ToolResult {
	thread := &starlark.Thread{
		Name: "code",
		Print: func(_ *starlark.Thread, msg string) {
			if r.stdout.Len()+len(msg)+1 > codeMaxStdout {
				r.truncated = true
				return
			}
			r.stdout.WriteString(msg)
			r.stdout.WriteByte('\n')
		},
	}
	thread.SetMaxExecutionSteps(codeMaxExecutionSteps)

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-r.ctx.Done():
			thread.Cancel(r.ctx.Err().Error())
		case <-done:
		}
	}()

	predeclared := starlark.StringDict{
		"tools":    &codeTools{run: r},
		"call":     starlark.NewBuiltin("call", r.builtinCall),
		"parallel": starlark.NewBuiltin("parallel", r.builtinParallel),
		"json":     json.Module,
	}

	start := time.Now()
	globals, err := starlark.ExecFileOptions(codeFileOptions(), thread, "code.star", src, predeclared)

	var body strings.Builder
	data := map[string]interface{}{
		"tool_calls": r.calls,
		"tools_used": r.used,
		"duration":   time.Since(start).Round(time.Millisecond).String(),
	}

	if err == nil {
		if v, ok := globals["result"]; ok {
			rendered := renderStarlark(thread, v)
			body.WriteString("result:\n")
			body.WriteString(rendered)
			body.WriteByte('\n')
		} else {
			body.WriteString("result: (none — assign the global `result` to return a value)\n")
		}
	}
	if r.stdout.Len() > 0 {
		body.WriteString("stdout:\n")
		body.WriteString(r.stdout.String())
		if r.truncated {
			body.WriteString("[stdout truncated]\n")
		}
	}
	var errMsg string
	if err != nil {
		errMsg = codeErrorText(err)
		body.WriteString("error:\n")
		body.WriteString(errMsg)
		body.WriteByte('\n')
	}
	body.WriteString(r.usedSummary())

	content := r.engine.truncateForModel(strings.TrimRight(body.String(), "\n"))
	if len(r.untrusted) > 0 {
		sort.Strings(r.untrusted)
		content = envelopeUntrusted(codeToolName+" via "+strings.Join(r.untrusted, ", "), content)
	}

	if err != nil {
		data["error"] = errMsg
		return &types.ToolResult{Success: false, Error: "script failed: " + firstLine(errMsg), Content: content, Data: data}
	}
	return &types.ToolResult{Success: true, Content: content, Data: data}
}

func (r *codeRun) usedSummary() string {
	if len(r.calls) == 0 {
		return "tool calls: none"
	}
	names := make([]string, 0, len(r.used))
	for n := range r.used {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	failed := 0
	for _, c := range r.calls {
		if !c.OK {
			failed++
		}
	}
	for i, n := range names {
		parts[i] = fmt.Sprintf("%s×%d", n, r.used[n])
	}
	return fmt.Sprintf("tool calls: %d (%s), %d failed", len(r.calls), strings.Join(parts, ", "), failed)
}

func codeErrorText(err error) string {
	var evalErr *starlark.EvalError
	if errors.As(err, &evalErr) {
		return evalErr.Backtrace()
	}
	return err.Error()
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// renderStarlark returns strings as-is and everything else as indented
// JSON, falling back to Starlark's own repr for values JSON cannot encode.
func renderStarlark(thread *starlark.Thread, v starlark.Value) string {
	if s, ok := v.(starlark.String); ok {
		return string(s)
	}
	out, err := starlark.Call(thread, json.Module.Members["encode_indent"], starlark.Tuple{v}, nil)
	if err != nil {
		return v.String()
	}
	if s, ok := out.(starlark.String); ok {
		return string(s)
	}
	return v.String()
}

// --- argument and result conversion (via JSON, so tools see exactly the
// shapes a model tool call would produce: numbers are float64) ---

func toGoArgs(thread *starlark.Thread, v starlark.Value) (map[string]interface{}, error) {
	out, err := starlark.Call(thread, json.Module.Members["encode"], starlark.Tuple{v}, nil)
	if err != nil {
		return nil, fmt.Errorf("tool arguments must be JSON-encodable: %w", err)
	}
	args := map[string]interface{}{}
	if err := jsonUnmarshalString(string(out.(starlark.String)), &args); err != nil {
		return nil, err
	}
	return args, nil
}

func toStarlark(thread *starlark.Thread, v interface{}) (starlark.Value, error) {
	b, err := jsonMarshal(v)
	if err != nil {
		return nil, err
	}
	return starlark.Call(thread, json.Module.Members["decode"], starlark.Tuple{starlark.String(b)}, nil)
}

// kwargsDict builds the argument dict from kwargs plus an optional single
// positional dict (tools.Read({"file_path": "x"}) or tools.Read(file_path="x")).
func kwargsDict(fn string, args starlark.Tuple, kwargs []starlark.Tuple) (*starlark.Dict, error) {
	d := starlark.NewDict(len(kwargs))
	switch len(args) {
	case 0:
	case 1:
		src, ok := args[0].(*starlark.Dict)
		if !ok {
			return nil, fmt.Errorf("%s: positional argument must be a dict of parameters, got %s", fn, args[0].Type())
		}
		for _, item := range src.Items() {
			if err := d.SetKey(item[0], item[1]); err != nil {
				return nil, err
			}
		}
	default:
		return nil, fmt.Errorf("%s: pass parameters by name, e.g. %s(file_path=\"x\")", fn, fn)
	}
	for _, kv := range kwargs {
		if err := d.SetKey(kv[0], kv[1]); err != nil {
			return nil, err
		}
	}
	return d, nil
}

// --- tool calls ---

func (r *codeRun) checkCall(name string, n int) error {
	if codeForbiddenCalls[normalizeToolName(name)] {
		return fmt.Errorf("%s cannot be called from a Code script", name)
	}
	if r.nCalls+n > codeMaxCalls {
		return fmt.Errorf("tool call limit reached (%d per script)", codeMaxCalls)
	}
	if err := r.ctx.Err(); err != nil {
		return fmt.Errorf("script stopped: %w", err)
	}
	return nil
}

func (r *codeRun) toolCall(name string, args map[string]interface{}) ai.ToolCall {
	return ai.ToolCall{ID: fmt.Sprintf("code_%d", r.seq.Add(1)), Name: name, Args: args}
}

// record notes an inner call's outcome and converts it for the script.
func (r *codeRun) record(thread *starlark.Thread, res *ExecutionResult) (starlark.Value, error) {
	name := res.ToolCall.Name
	ok := res.Error == nil && res.Result != nil && res.Result.Success
	r.calls = append(r.calls, codeCallRecord{Tool: name, OK: ok})
	r.used[name]++
	if untrustedOutputTools[name] && !containsString(r.untrusted, name) {
		r.untrusted = append(r.untrusted, name)
	}

	out := map[string]interface{}{"ok": ok, "content": "", "error": "", "data": map[string]interface{}{}}
	if res.Result != nil {
		out["content"] = res.Result.Content
		out["error"] = res.Result.Error
		if res.Result.Data != nil {
			out["data"] = res.Result.Data
		}
	}
	if res.Error != nil {
		out["error"] = res.Error.Error()
	}
	v, err := toStarlark(thread, out)
	if err != nil {
		// Data that does not survive JSON: drop it, keep the text.
		out["data"] = map[string]interface{}{}
		return toStarlark(thread, out)
	}
	return v, nil
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (r *codeRun) invoke(thread *starlark.Thread, name string, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	fn := "tools." + name
	d, err := kwargsDict(fn, args, kwargs)
	if err != nil {
		return nil, err
	}
	if err := r.checkCall(name, 1); err != nil {
		return nil, err
	}
	goArgs, err := toGoArgs(thread, d)
	if err != nil {
		return nil, err
	}
	r.nCalls++
	res := r.engine.executeSingle(r.ctx, r.toolCall(name, goArgs))
	return r.record(thread, res)
}

// builtinCall builds a pending call for parallel(): call("Read", file_path="x").
func (r *codeRun) builtinCall(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("call: first argument must be the tool name")
	}
	name, ok := starlark.AsString(args[0])
	if !ok {
		return nil, fmt.Errorf("call: tool name must be a string, got %s", args[0].Type())
	}
	d, err := kwargsDict("call", args[1:], kwargs)
	if err != nil {
		return nil, err
	}
	goArgs, err := toGoArgs(thread, d)
	if err != nil {
		return nil, err
	}
	return &codePending{name: name, args: goArgs}, nil
}

func (r *codeRun) builtinParallel(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var list *starlark.List
	if err := starlark.UnpackPositionalArgs("parallel", args, kwargs, 1, &list); err != nil {
		return nil, err
	}
	calls := make([]ai.ToolCall, 0, list.Len())
	for i := 0; i < list.Len(); i++ {
		p, ok := list.Index(i).(*codePending)
		if !ok {
			return nil, fmt.Errorf("parallel: item %d is %s, want call(...)", i, list.Index(i).Type())
		}
		if err := r.checkCall(p.name, 0); err != nil {
			return nil, err
		}
		calls = append(calls, r.toolCall(p.name, p.args))
	}
	if err := r.checkCall("", len(calls)); err != nil {
		return nil, err
	}
	r.nCalls += len(calls)
	results, _ := r.engine.ExecuteToolCalls(r.ctx, calls)
	out := make([]starlark.Value, len(results))
	for i, res := range results {
		v, err := r.record(thread, res)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return starlark.NewList(out), nil
}

// codeTools is the `tools` object: tools.<Name>(...) calls an enabled tool.
type codeTools struct{ run *codeRun }

func (t *codeTools) String() string        { return "<tools>" }
func (t *codeTools) Type() string          { return "tools" }
func (t *codeTools) Freeze()               {}
func (t *codeTools) Truth() starlark.Bool  { return starlark.True }
func (t *codeTools) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable: tools") }

func (t *codeTools) Attr(name string) (starlark.Value, error) {
	reg := t.run.registry
	reg.mu.RLock()
	_, exists := reg.tools[name]
	enabled := reg.isToolEnabledLocked(name)
	reg.mu.RUnlock()
	if !exists || !enabled {
		return nil, starlark.NoSuchAttrError(fmt.Sprintf("no enabled tool %q (see dir(tools))", name))
	}
	return starlark.NewBuiltin("tools."+name, func(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		return t.run.invoke(thread, name, args, kwargs)
	}), nil
}

func (t *codeTools) AttrNames() []string {
	names := t.run.registry.getEnabledToolNames()
	out := names[:0]
	for _, n := range names {
		if !codeForbiddenCalls[normalizeToolName(n)] {
			out = append(out, n)
		}
	}
	return out
}

// codePending is a call(...) value waiting for parallel().
type codePending struct {
	name string
	args map[string]interface{}
}

func (p *codePending) String() string        { return fmt.Sprintf("call(%q)", p.name) }
func (p *codePending) Type() string          { return "call" }
func (p *codePending) Freeze()               {}
func (p *codePending) Truth() starlark.Bool  { return starlark.True }
func (p *codePending) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable: call") }

func jsonMarshal(v interface{}) (string, error) {
	b, err := gojson.Marshal(v)
	return string(b), err
}

func jsonUnmarshalString(s string, v interface{}) error {
	return gojson.Unmarshal([]byte(s), v)
}
