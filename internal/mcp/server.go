package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"conduit/internal/tools/types"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Server wraps the MCP protocol server and exposes Conduit's tools
// to external MCP clients such as Claude Code.
type Server struct {
	registry   types.ToolRegistry
	port       int
	httpServer *http.Server
	mcpServer  *sdkmcp.Server
}

// NewServer creates a new MCP server that will expose tools from the registry.
// The server binds to localhost only on the given port.
func NewServer(registry types.ToolRegistry, port int) *Server {
	s := &Server{
		registry: registry,
		port:     port,
	}

	// Create the MCP protocol server.
	s.mcpServer = sdkmcp.NewServer(
		&sdkmcp.Implementation{
			Name:    "conduit",
			Version: "1.0.0",
		},
		nil,
	)

	// Register tools from the registry.
	s.registerTools()

	return s
}

// registerTools adds all MCP-eligible tools from the registry to the MCP server.
func (s *Server) registerTools() {
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
			Stateless: true, // Each request is independent, no session tracking needed.
		},
	)

	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)

	s.httpServer = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	// Verify we can listen on the port before returning.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("mcp server: failed to listen on %s: %w", addr, err)
	}

	log.Printf("[mcp] server starting on %s", addr)

	go func() {
		if err := s.httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("[mcp] server error: %v", err)
		}
	}()

	return nil
}

// Stop gracefully shuts down the MCP server with a 5-second timeout.
func (s *Server) Stop(ctx context.Context) error {
	if s.httpServer == nil {
		return nil
	}

	log.Printf("[mcp] server stopping")

	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	err := s.httpServer.Shutdown(shutdownCtx)
	if err != nil {
		log.Printf("[mcp] server shutdown error: %v", err)
	} else {
		log.Printf("[mcp] server stopped")
	}
	return err
}
