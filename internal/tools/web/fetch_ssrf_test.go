package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"conduit/internal/config"
	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// conduit-31jg.7: WebFetch must refuse loopback / link-local targets.
func TestWebFetch_SSRF_BlocksLoopback(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "gateway internals")
	}))
	defer srv.Close()

	tool := NewWebFetchTool(nil) // default policy, no allowlist
	res, err := tool.Execute(context.Background(), map[string]interface{}{"url": srv.URL})
	require.NoError(t, err)
	assert.False(t, res.Success)
	assert.Contains(t, res.Error, "blocked")
	assert.False(t, hit, "request must not reach the loopback server")
}

func TestWebFetch_SSRF_BlocksMetadata(t *testing.T) {
	tool := NewWebFetchTool(nil)
	res, err := tool.Execute(context.Background(), map[string]interface{}{"url": "http://169.254.169.254/latest/meta-data/"})
	require.NoError(t, err)
	assert.False(t, res.Success)
	assert.Contains(t, res.Error, "blocked")
}

func TestWebFetch_SSRF_BlocksRedirectToLoopback(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "secret")
	}))
	defer target.Close()
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer front.Close()

	// Allowlist only the front server via config; the redirect hop is a
	// different loopback port and must be re-checked.
	cfg := &config.Config{Tools: config.ToolsConfig{Web: config.WebToolsConfig{
		AllowedHosts: []string{strings.TrimPrefix(front.URL, "http://")},
	}}}
	tool := NewWebFetchTool(&types.ToolServices{ConfigMgr: cfg})
	res, err := tool.Execute(context.Background(), map[string]interface{}{"url": front.URL})
	require.NoError(t, err)
	assert.False(t, res.Success)
	assert.Contains(t, res.Error, "blocked")
	assert.NotContains(t, res.Content, "secret")
}

func TestWebFetch_SSRF_ConfigAllowlistPermitsLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "ok from allowlisted host")
	}))
	defer srv.Close()
	cfg := &config.Config{Tools: config.ToolsConfig{Web: config.WebToolsConfig{
		AllowedHosts: []string{strings.TrimPrefix(srv.URL, "http://")},
	}}}
	tool := NewWebFetchTool(&types.ToolServices{ConfigMgr: cfg})
	res, err := tool.Execute(context.Background(), map[string]interface{}{"url": srv.URL})
	require.NoError(t, err)
	require.True(t, res.Success, res.Error)
	assert.Contains(t, res.Content, "allowlisted")
}

// conduit-31jg.7: the body read is bounded, not io.ReadAll-then-truncate.
func TestWebFetch_OversizedBodyIsLimited(t *testing.T) {
	const limit = 4096
	var written int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		chunk := []byte(strings.Repeat("A", 1024))
		for i := 0; i < 1024; i++ { // 1 MiB
			n, err := w.Write(chunk)
			written += int64(n)
			if err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	tool := newLoopbackFetchTool()
	tool.maxBodyBytes = limit
	res, err := tool.Execute(context.Background(), map[string]interface{}{"url": srv.URL, "maxChars": 1 << 30})
	require.NoError(t, err)
	require.True(t, res.Success, res.Error)
	assert.LessOrEqual(t, strings.Count(res.Content, "A"), limit)
	assert.Equal(t, true, res.Data["truncated"])
}

// newLoopbackFetchTool returns a WebFetch tool whose SSRF policy allowlists
// all loopback ports, for tests that talk to httptest servers.
func newLoopbackFetchTool() *WebFetchTool {
	cfg := &config.Config{Tools: config.ToolsConfig{Web: config.WebToolsConfig{
		AllowedHosts: []string{"127.0.0.1:*", "[::1]:*"},
	}}}
	return NewWebFetchTool(&types.ToolServices{ConfigMgr: cfg})
}
