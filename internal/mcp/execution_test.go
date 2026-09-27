package mcp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"conduit/internal/approval"
	"conduit/internal/skills"
	"conduit/internal/tools"
	"conduit/internal/tools/types"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// conduit-31jg.8: MCP tool calls go through the ExecutionEngine's single-call
// pipeline (per-call timeout, panic recovery, truncation, reflection hook,
// Data opt-in) and are non-interactive for the approval gate.

// funcTool is a tool whose behavior is a closure.
type funcTool struct {
	name string
	fn   func(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error)
}

func (f *funcTool) Name() string        { return f.name }
func (f *funcTool) Description() string { return f.name }
func (f *funcTool) Parameters() map[string]interface{} {
	return map[string]interface{}{"type": "object"}
}
func (f *funcTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	return f.fn(ctx, args)
}

// dataOptInRegistry adds the Registry.IncludeDataInModelOutput opt-in.
type dataOptInRegistry struct {
	*mockRegistry
	optIn map[string]bool
}

func (r *dataOptInRegistry) IncludeDataInModelOutput(name string) bool { return r.optIn[name] }

// callViaMCP registers the registry's tools on srv, connects an in-memory
// client and calls name.
func callViaMCP(t *testing.T, srv *Server, name string, args map[string]any) (*sdkmcp.CallToolResult, string) {
	t.Helper()
	srv.RegisterTools()
	ctx := context.Background()
	t1, t2 := sdkmcp.NewInMemoryTransports()
	_, err := srv.mcpServer.Connect(ctx, t1, nil)
	require.NoError(t, err)
	cs, err := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, t2, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })

	res, err := cs.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	require.Len(t, res.Content, 1)
	tc, ok := res.Content[0].(*sdkmcp.TextContent)
	require.True(t, ok)
	return res, tc.Text
}

func newEngine(reg tools.ToolRegistry, timeout time.Duration) *tools.ExecutionEngine {
	return tools.NewExecutionEngine(reg, 1, timeout, 25)
}

func TestMCPToolCall_PerCallTimeout(t *testing.T) {
	reg := newMockRegistry(&funcTool{name: "Slow", fn: func(ctx context.Context, _ map[string]interface{}) (*types.ToolResult, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Second):
			return &types.ToolResult{Success: true, Content: "late"}, nil
		}
	}})
	srv := NewServer(reg, 0, WithExecutor(newEngine(reg, 150*time.Millisecond)))

	start := time.Now()
	res, text := callViaMCP(t, srv, "Slow", nil)
	assert.Less(t, time.Since(start), 3*time.Second, "engine per-call timeout must bound the MCP call")
	assert.True(t, res.IsError)
	assert.Contains(t, text, "deadline exceeded")
}

func TestMCPToolCall_Truncated(t *testing.T) {
	big := strings.Repeat("line of tool output\n", 5000) // ~100KB
	reg := newMockRegistry(&funcTool{name: "Big", fn: func(context.Context, map[string]interface{}) (*types.ToolResult, error) {
		return &types.ToolResult{Success: true, Content: big}, nil
	}})
	eng := newEngine(reg, time.Second)
	eng.SetMaxResultChars(2000)
	srv := NewServer(reg, 0, WithExecutor(eng))

	res, text := callViaMCP(t, srv, "Big", nil)
	assert.False(t, res.IsError)
	assert.Less(t, len(text), 4000, "result must be smart-truncated to ~maxResultChars")
	assert.Contains(t, strings.ToLower(text), "truncated")
}

func TestMCPToolCall_PanicRecovered(t *testing.T) {
	reg := newMockRegistry(&funcTool{name: "Boom", fn: func(context.Context, map[string]interface{}) (*types.ToolResult, error) {
		panic("kaboom")
	}})
	srv := NewServer(reg, 0, WithExecutor(newEngine(reg, time.Second)))

	res, text := callViaMCP(t, srv, "Boom", nil)
	assert.True(t, res.IsError)
	assert.Contains(t, text, "panic")
	assert.Contains(t, text, "kaboom")
}

func TestMCPToolCall_ReflectionHookFires(t *testing.T) {
	reg := newMockRegistry(&mockTool{name: "Echo", params: map[string]interface{}{"type": "object"}})
	eng := newEngine(reg, time.Second)
	var mu sync.Mutex
	var seen []string
	eng.SetAfterExecutionHook(func(_ context.Context, name string, _ *tools.ExecutionResult) {
		mu.Lock()
		seen = append(seen, name)
		mu.Unlock()
	})
	srv := NewServer(reg, 0, WithExecutor(eng))

	_, text := callViaMCP(t, srv, "Echo", nil)
	assert.Equal(t, "executed Echo", text)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"Echo"}, seen)
}

func TestMCPToolCall_DataOptInRendered(t *testing.T) {
	result := func(context.Context, map[string]interface{}) (*types.ToolResult, error) {
		return &types.ToolResult{Success: true, Content: "summary", Data: map[string]interface{}{"items": []string{"a", "b"}}}, nil
	}
	newReg := func() *dataOptInRegistry {
		return &dataOptInRegistry{
			mockRegistry: newMockRegistry(&funcTool{name: "WithData", fn: result}, &funcTool{name: "NoOptIn", fn: result}),
			optIn:        map[string]bool{"WithData": true},
		}
	}

	t.Run("engine", func(t *testing.T) {
		reg := newReg()
		srv := NewServer(reg, 0, WithExecutor(newEngine(reg, time.Second)))
		_, text := callViaMCP(t, srv, "WithData", nil)
		assert.Contains(t, text, `Structured data: {"items":["a","b"]}`)
	})
	t.Run("engine_no_opt_in", func(t *testing.T) {
		reg := newReg()
		srv := NewServer(reg, 0, WithExecutor(newEngine(reg, time.Second)))
		_, text := callViaMCP(t, srv, "NoOptIn", nil)
		assert.Equal(t, "summary", text)
	})
	t.Run("registry_fallback", func(t *testing.T) {
		srv := NewServer(newReg(), 0)
		_, text := callViaMCP(t, srv, "WithData", nil)
		assert.Contains(t, text, `Structured data: {"items":["a","b"]}`)
	})
	t.Run("adapt", func(t *testing.T) {
		r, _ := result(context.Background(), nil)
		assert.Equal(t, "summary", AdaptToolResult(r, false).Content[0].(*sdkmcp.TextContent).Text)
		assert.Contains(t, AdaptToolResult(r, true).Content[0].(*sdkmcp.TextContent).Text, "Structured data:")
		empty := &types.ToolResult{Success: true, Data: map[string]interface{}{"k": 1}}
		assert.Contains(t, AdaptToolResult(empty, false).Content[0].(*sdkmcp.TextContent).Text, `{"k":1}`)
	})
}

// An owner-account email send over MCP must fail closed: an MCP caller is not
// a live human on a promptable channel, so no approval prompt may be sent.
func TestMCPToolCall_OwnerEmailSendFailsClosed(t *testing.T) {
	mgr := approval.NewManager(approval.Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	t.Cleanup(mgr.Close)
	exec := skills.NewExecutor(skills.ExecutionConfig{TimeoutSeconds: 5})
	exec.SetApprover(mgr)
	skill := skills.Skill{Name: "email", Location: t.TempDir()}

	var gotOrigin approval.Origin
	emailTool := &funcTool{name: "skill_email", fn: func(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
		gotOrigin, _ = approval.OriginFrom(ctx)
		action, _ := args["action"].(string)
		res, err := exec.ExecuteSkill(ctx, skill, action, args)
		if err != nil {
			return nil, err
		}
		if !res.Success {
			return &types.ToolResult{Success: false, Error: res.Error}, nil
		}
		return nil, errors.New("owner send was not gated")
	}}
	reg := newMockRegistry(emailTool)

	for name, srv := range map[string]*Server{
		"engine":   NewServer(reg, 0, WithExecutor(newEngine(reg, 5*time.Second))),
		"registry": NewServer(reg, 0),
	} {
		t.Run(name, func(t *testing.T) {
			res, text := callViaMCP(t, srv, "skill_email", map[string]any{
				"action": "send", "account": "jeff", "to": "bob@example.com", "subject": "hi", "body": "b",
			})
			assert.True(t, res.IsError)
			assert.Contains(t, text, "NOT SENT")
			assert.Contains(t, text, "origin: mcp")
			assert.False(t, gotOrigin.Interactive)
			assert.Equal(t, "mcp", gotOrigin.Source)
		})
	}
}
