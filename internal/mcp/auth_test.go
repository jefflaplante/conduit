package mcp

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// conduit-31jg.8: bearer-token auth on the MCP endpoint.

const testToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

const initializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`

func startAuthServer(t *testing.T, mode AuthMode, token string) string {
	t.Helper()
	reg := newMockRegistry(&mockTool{name: "Echo", params: map[string]interface{}{"type": "object"}})
	port := freePort(t)
	srv := NewServer(reg, port, WithAuth(mode, token))
	srv.RegisterTools()
	require.NoError(t, srv.Start(context.Background()))
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })
	return fmt.Sprintf("http://127.0.0.1:%d/mcp", port)
}

// postInitialize sends a raw initialize request with the given Authorization
// header ("" = none) and optional Host override; returns the status code.
func postInitialize(t *testing.T, url, authz, host string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(initializeBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	if host != "" {
		req.Host = host
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	return resp.StatusCode
}

func TestAuthEnforce(t *testing.T) {
	url := startAuthServer(t, AuthEnforce, testToken)

	assert.Equal(t, http.StatusUnauthorized, postInitialize(t, url, "", ""), "no token")
	assert.Equal(t, http.StatusUnauthorized, postInitialize(t, url, "Bearer ", ""), "empty bearer")
	assert.Equal(t, http.StatusUnauthorized, postInitialize(t, url, "Bearer wrong", ""), "wrong token")
	assert.Equal(t, http.StatusUnauthorized, postInitialize(t, url, "Bearer "+testToken[:len(testToken)-1], ""), "prefix of token")
	assert.Equal(t, http.StatusUnauthorized, postInitialize(t, url, "Basic "+testToken, ""), "wrong scheme")
	assert.Equal(t, http.StatusOK, postInitialize(t, url, "Bearer "+testToken, ""), "valid token")
	assert.Equal(t, http.StatusOK, postInitialize(t, url, "bearer "+testToken, ""), "scheme is case-insensitive")
}

func TestAuthEnforce_WWWAuthenticate(t *testing.T) {
	url := startAuthServer(t, AuthEnforce, testToken)
	resp, err := http.Post(url, "application/json", strings.NewReader(initializeBody))
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("WWW-Authenticate"), `Bearer realm="conduit-mcp"`)
}

func TestAuthWarnMode(t *testing.T) {
	url := startAuthServer(t, AuthWarn, testToken)

	assert.Equal(t, http.StatusOK, postInitialize(t, url, "", ""), "warn mode serves unauthenticated requests")
	assert.Equal(t, http.StatusOK, postInitialize(t, url, "Bearer ", ""), "unset ${VAR:-} => treated as no token")
	assert.Equal(t, http.StatusOK, postInitialize(t, url, "Bearer "+testToken, ""), "valid token")
	assert.Equal(t, http.StatusUnauthorized, postInitialize(t, url, "Bearer wrong", ""), "wrong token rejected even in warn mode")
}

func TestAuthDisabled(t *testing.T) {
	url := startAuthServer(t, AuthDisabled, "")
	assert.Equal(t, http.StatusOK, postInitialize(t, url, "", ""))
	assert.Equal(t, http.StatusOK, postInitialize(t, url, "Bearer whatever", ""))
}

// The SDK's DNS-rebinding protection must stay on behind the auth wrapper.
func TestAuthKeepsDNSRebindingProtection(t *testing.T) {
	url := startAuthServer(t, AuthEnforce, testToken)
	assert.Equal(t, http.StatusForbidden, postInitialize(t, url, "Bearer "+testToken, "evil.example.com"))
}

func TestBearerValid_ConstantTimeCompare(t *testing.T) {
	a := newBearerAuth(AuthEnforce, testToken, nil)
	assert.True(t, a.valid(testToken))
	assert.False(t, a.valid(""))
	assert.False(t, a.valid(testToken+"x"))
	assert.False(t, a.valid(strings.ToUpper(testToken)))
	// No configured token: nothing validates (not even the empty string).
	assert.False(t, newBearerAuth(AuthEnforce, "", nil).valid(""))
}

func TestResolveAuthMode(t *testing.T) {
	yes, no := true, false
	assert.Equal(t, AuthWarn, ResolveAuthMode(nil))
	assert.Equal(t, AuthEnforce, ResolveAuthMode(&yes))
	assert.Equal(t, AuthDisabled, ResolveAuthMode(&no))
}

// headerTransport injects an Authorization header, like Claude Code does from
// .mcp.json "headers".
type headerTransport struct{ token string }

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+h.token)
	return http.DefaultTransport.RoundTrip(r)
}

// End-to-end with the SDK's streamable client: a session with the right
// token works (initialize, tools/list, tools/call, standalone SSE GET); a
// client without one cannot connect.
func TestAuthEnforce_SDKClient(t *testing.T) {
	url := startAuthServer(t, AuthEnforce, testToken)
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "c", Version: "1"}, nil)

	_, err := client.Connect(context.Background(), &sdkmcp.StreamableClientTransport{Endpoint: url, MaxRetries: -1}, nil)
	require.Error(t, err, "unauthenticated client must not connect")

	cs, err := client.Connect(context.Background(), &sdkmcp.StreamableClientTransport{
		Endpoint: url, MaxRetries: -1, HTTPClient: &http.Client{Transport: headerTransport{token: testToken}},
	}, nil)
	require.NoError(t, err)
	defer cs.Close()
	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "Echo"})
	require.NoError(t, err)
	assert.Equal(t, "executed Echo", res.Content[0].(*sdkmcp.TextContent).Text)
}
