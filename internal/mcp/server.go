package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"conduit/internal/tools/types"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Server wraps the MCP protocol server and exposes Conduit's tools
// to external MCP clients such as Claude Code.
type Server struct {
	registry  types.ToolRegistry
	port      int
	mcpServer *sdkmcp.Server

	// conduit-31jg.78: runCtx is the parent of every HTTP request context
	// (http.Server.BaseContext) and of every tool call. Stop cancels it so
	// long-lived streams (the standalone SSE GET, hanging POSTs) and in-flight
	// tool calls end instead of holding http.Server.Shutdown open.
	runCtx    context.Context
	runCancel context.CancelFunc

	mu         sync.Mutex
	httpServer *http.Server
}

// stopGrace bounds how long Stop waits for sessions and connections to
// drain before force-closing them (conduit-31jg.78).
const stopGrace = 500 * time.Millisecond

// NewServer creates a new MCP server that will expose tools from the registry.
// The server binds to localhost only on the given port.
func NewServer(registry types.ToolRegistry, port int) *Server {
	runCtx, runCancel := context.WithCancel(context.Background())
	s := &Server{
		registry:  registry,
		port:      port,
		runCtx:    runCtx,
		runCancel: runCancel,
	}

	// Create the MCP protocol server.
	s.mcpServer = sdkmcp.NewServer(
		&sdkmcp.Implementation{
			Name:    "conduit",
			Version: "1.0.0",
		},
		nil,
	)

	// Note: tools are registered lazily via RegisterTools() after the tool
	// registry is fully populated (SetServices). The MCP server is created
	// during gateway init but tools may not be available yet.
	return s
}

// RegisterTools adds all MCP-eligible tools from the registry to the MCP server.
// Can be called multiple times to pick up tools registered after construction.
func (s *Server) RegisterTools() {
	filtered := FilterToolsForMCP(s.registry)
	for name, tool := range filtered {
		mcpTool := AdaptToolToMCP(tool)
		toolName := name // capture for closure
		s.mcpServer.AddTool(mcpTool, s.makeToolHandler(toolName))
	}
}

// makeToolHandler creates a ToolHandler that delegates to the Conduit tool registry.
func (s *Server) makeToolHandler(toolName string) sdkmcp.ToolHandler {
	return func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		log.Printf("[mcp] tool call: %s", toolName)

		// conduit-31jg.78: tie the call to the server lifetime so Stop
		// cancels it even if the SDK's handler ctx outlives the request.
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		defer context.AfterFunc(s.runCtx, cancel)()

		// Parse arguments from the raw JSON.
		args := make(map[string]interface{})
		if req.Params.Arguments != nil {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				result := &sdkmcp.CallToolResult{
					IsError: true,
					Content: []sdkmcp.Content{&sdkmcp.TextContent{
						Text: fmt.Sprintf("failed to parse arguments: %v", err),
					}},
				}
				return result, nil
			}
		}

		// Execute the tool via the registry.
		toolResult, err := s.registry.ExecuteTool(ctx, toolName, args)
		if err != nil && toolResult != nil && !toolResult.Success {
			// conduit-31jg.47: the registry now keeps output returned with an
			// error; AdaptToolResult renders error + output.
			return AdaptToolResult(toolResult), nil
		}
		if err != nil {
			result := &sdkmcp.CallToolResult{
				IsError: true,
				Content: []sdkmcp.Content{&sdkmcp.TextContent{
					Text: fmt.Sprintf("tool execution error: %v", err),
				}},
			}
			return result, nil
		}

		return AdaptToolResult(toolResult), nil
	}
}

// Start begins serving MCP requests on 127.0.0.1:<port>.
// It returns immediately; the server runs in a background goroutine.
func (s *Server) Start(ctx context.Context) error {
	addr := fmt.Sprintf("127.0.0.1:%d", s.port)

	// Create the streamable HTTP handler from the SDK.
	handler := sdkmcp.NewStreamableHTTPHandler(
		func(_ *http.Request) *sdkmcp.Server { return s.mcpServer },
		&sdkmcp.StreamableHTTPOptions{
			// Stateful sessions required by Claude Code and most MCP clients.
		},
	)

	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)

	runCtx := s.runCtx
	hs := &http.Server{
		Addr:    addr,
		Handler: mux,
		// conduit-31jg.78: request contexts derive from runCtx so Stop can
		// release hanging SSE/long-poll handlers by cancelling it.
		BaseContext: func(net.Listener) context.Context { return runCtx },
	}

	// Verify we can listen on the port before returning.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("mcp server: failed to listen on %s: %w", addr, err)
	}

	s.mu.Lock()
	s.httpServer = hs
	s.mu.Unlock()

	log.Printf("[mcp] server starting on %s", addr)

	go func() {
		if err := hs.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("[mcp] server error: %v", err)
		}
	}()

	return nil
}

// Stop shuts down the MCP server. conduit-31jg.78: connected clients hold a
// standalone SSE stream (and possibly tool calls) open indefinitely, so a
// plain http.Server.Shutdown always ran into its 5s timeout. Stop now cancels
// the server context (ending hanging handlers and in-flight tool calls),
// closes every MCP session, gives connections stopGrace to drain and then
// force-closes whatever is left. A forced close with live clients is
// expected and is not reported as an error.
func (s *Server) Stop(ctx context.Context) error {
	s.mu.Lock()
	hs := s.httpServer
	s.httpServer = nil
	s.mu.Unlock()
	if hs == nil {
		return nil
	}

	log.Printf("[mcp] server stopping")
	start := time.Now()

	s.runCancel()
	sessions := s.closeSessions(ctx)

	graceCtx, cancel := context.WithTimeout(ctx, stopGrace)
	defer cancel()
	if err := hs.Shutdown(graceCtx); err != nil {
		if cerr := hs.Close(); cerr != nil {
			log.Printf("[mcp] server close error: %v", cerr)
			return cerr
		}
		log.Printf("[mcp] server stopped in %s (%d session(s) closed; lingering connections force-closed)",
			time.Since(start).Round(time.Millisecond), sessions)
		return nil
	}
	log.Printf("[mcp] server stopped in %s (%d session(s) closed)", time.Since(start).Round(time.Millisecond), sessions)
	return nil
}

// closeSessions closes all live MCP sessions concurrently. ServerSession.Close
// waits for in-flight requests (already cancelled via runCtx); the wait is
// bounded by stopGrace so a tool that ignores cancellation cannot stall
// shutdown (conduit-31jg.78).
func (s *Server) closeSessions(ctx context.Context) int {
	var wg sync.WaitGroup
	n := 0
	for ss := range s.mcpServer.Sessions() {
		n++
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = ss.Close()
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	t := time.NewTimer(stopGrace)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
		log.Printf("[mcp] session close still pending after %s; continuing shutdown", stopGrace)
	case <-ctx.Done():
	}
	return n
}
