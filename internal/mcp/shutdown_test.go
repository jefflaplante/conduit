package mcp

import (
	"context"
	"fmt"
	"testing"
	"time"

	"conduit/internal/tools/types"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// blockingTool blocks until its context is cancelled, simulating a
// long-running in-flight tool call (e.g. a slow Bash command).
type blockingTool struct{ started chan struct{} }

func (b *blockingTool) Name() string        { return "Block" }
func (b *blockingTool) Description() string { return "blocks until cancelled" }
func (b *blockingTool) Parameters() map[string]interface{} {
	return map[string]interface{}{"type": "object"}
}
func (b *blockingTool) Execute(ctx context.Context, _ map[string]interface{}) (*types.ToolResult, error) {
	close(b.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

// conduit-31jg.78: a connected streamable-HTTP client holds a standalone SSE
// GET open (and possibly an in-flight tool call), which used to keep
// http.Server.Shutdown waiting for the full 5s timeout on every restart.
func TestStopWithConnectedStreamingClient(t *testing.T) {
	bt := &blockingTool{started: make(chan struct{})}
	registry := newMockRegistry(&mockTool{name: "Echo", params: map[string]interface{}{"type": "object"}}, bt)

	port := freePort(t)
	srv := NewServer(registry, port)
	srv.RegisterTools()
	require.NoError(t, srv.Start(context.Background()))

	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	transport := &sdkmcp.StreamableClientTransport{
		Endpoint:   fmt.Sprintf("http://127.0.0.1:%d/mcp", port),
		MaxRetries: -1,
	}
	cs, err := client.Connect(context.Background(), transport, nil)
	require.NoError(t, err)
	// Don't block the test on client teardown if the server misbehaves.
	defer func() { go cs.Close() }()

	// A completed call proves the session (and its standalone SSE stream) is up.
	_, err = cs.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "Echo"})
	require.NoError(t, err)

	// Leave one tool call in flight.
	go func() { _, _ = cs.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "Block"}) }()
	select {
	case <-bt.started:
	case <-time.After(5 * time.Second):
		t.Fatal("blocking tool never started")
	}

	start := time.Now()
	err = srv.Stop(context.Background())
	elapsed := time.Since(start)
	require.NoError(t, err, "Stop must not report a deadline error")
	require.Less(t, elapsed, time.Second, "Stop took %s with a connected client", elapsed)
}
