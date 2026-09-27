package vision

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"conduit/internal/config"
	"conduit/internal/httpsafe"
	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// conduit-31jg.62 test helpers.

// sandboxedImageTool returns an Image tool whose only sandbox root is dir.
func sandboxedImageTool(dir string) *ImageTool {
	return NewImageToolWithSandbox(nil, config.SandboxConfig{WorkspaceDir: dir})
}

// loopbackImageTool returns an Image tool whose SSRF policy allowlists
// loopback (tools.web.allowed_hosts), so httptest servers are reachable.
func loopbackImageTool() *ImageTool {
	cfg := &config.Config{}
	cfg.Tools.Web.AllowedHosts = []string{"localhost:*"}
	return NewImageTool(&types.ToolServices{ConfigMgr: cfg})
}

func writeJPEG(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}, 0o644))
}

func TestImageTool_SSRF_LoopbackBlockedByDefault(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte{0xFF, 0xD8, 0xFF, 0xE0})
	}))
	defer server.Close()

	tool := NewImageTool(nil) // default policy, no allowlist
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"image": server.URL + "/x.jpg", // http://127.0.0.1:port
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, httpsafe.ErrBlockedAddress.Error())
	assert.Zero(t, hits.Load(), "blocked request must never reach the server")
}

func TestImageTool_SSRF_MetadataBlockedEvenWithAllowlist(t *testing.T) {
	cfg := &config.Config{}
	// Link-local can't be re-opened by the allowlist.
	cfg.Tools.Web.AllowedHosts = []string{"169.254.169.254:*", "localhost:*"}
	for _, tool := range []*ImageTool{NewImageTool(nil), NewImageTool(&types.ToolServices{ConfigMgr: cfg})} {
		result, err := tool.Execute(context.Background(), map[string]interface{}{
			"image": "http://169.254.169.254/latest/meta-data/iam.jpg",
		})
		require.NoError(t, err)
		assert.False(t, result.Success)
		assert.Contains(t, result.Error, httpsafe.ErrBlockedAddress.Error())
	}
}

func TestImageTool_SSRF_RFC1918AllowedByDefault(t *testing.T) {
	// 10.255.255.1 is not routable in CI; the point is that the SSRF policy
	// lets the dial proceed (it then times out) instead of rejecting it.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	result, err := NewImageTool(nil).Execute(ctx, map[string]interface{}{
		"image": "http://10.255.255.1/cam.jpg",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.NotContains(t, result.Error, httpsafe.ErrBlockedAddress.Error())

	// With tools.web.block_private_networks the same URL is rejected.
	cfg := &config.Config{}
	cfg.Tools.Web.BlockPrivateNetworks = true
	result, err = NewImageTool(&types.ToolServices{ConfigMgr: cfg}).Execute(context.Background(),
		map[string]interface{}{"image": "http://10.255.255.1/cam.jpg"})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, httpsafe.ErrBlockedAddress.Error())
}

func TestImageTool_SSRF_AllowlistedLoopbackWorks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte{0xFF, 0xD8, 0xFF, 0xE0})
	}))
	defer server.Close()

	result, err := loopbackImageTool().Execute(context.Background(), map[string]interface{}{
		"image": server.URL + "/x.jpg",
	})
	require.NoError(t, err)
	assert.True(t, result.Success, result.Error)
}

func TestImageTool_EffectiveMaxBytes_CappedAtMediaLimit(t *testing.T) {
	assert.Equal(t, httpsafe.MediaBodyLimit, effectiveMaxBytes(1000)) // 1000 MB requested
	assert.Equal(t, httpsafe.MediaBodyLimit, effectiveMaxBytes(1e12))
	assert.Equal(t, int64(5<<20), effectiveMaxBytes(0))
	assert.Equal(t, int64(5<<20), effectiveMaxBytes(-3))
	assert.Equal(t, int64(1<<20), effectiveMaxBytes(1))
}

func TestImageTool_URLDownloadCapped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		chunk := bytes.Repeat([]byte{0xFF}, 64<<10)
		for i := 0; i < 64; i++ { // 4 MiB
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	result, err := loopbackImageTool().Execute(context.Background(), map[string]interface{}{
		"image":      server.URL + "/big.jpg",
		"maxBytesMb": 1.0,
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "exceeds limit")
}

func TestImageTool_Sandbox_FileOutsideRootsDenied(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.jpg")
	writeJPEG(t, outside)

	result, err := sandboxedImageTool(root).Execute(context.Background(), map[string]interface{}{
		"image": outside,
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "not allowed in sandbox")

	// Relative traversal out of the workspace is denied too.
	rel, err := filepath.Rel(root, outside)
	require.NoError(t, err)
	result, err = sandboxedImageTool(root).Execute(context.Background(), map[string]interface{}{
		"image": rel,
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "not allowed in sandbox")
}

func TestImageTool_Sandbox_NoRootsDeniesFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jpg")
	writeJPEG(t, path)
	result, err := NewImageTool(nil).Execute(context.Background(), map[string]interface{}{"image": path})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "not allowed in sandbox")
}

func TestImageTool_Sandbox_SymlinkEscapeDenied(t *testing.T) {
	root := t.TempDir()
	outsideDir := t.TempDir()
	secret := filepath.Join(outsideDir, "secret.jpg")
	writeJPEG(t, secret)

	fileLink := filepath.Join(root, "link.jpg")
	require.NoError(t, os.Symlink(secret, fileLink))
	dirLink := filepath.Join(root, "nas")
	require.NoError(t, os.Symlink(outsideDir, dirLink))

	tool := sandboxedImageTool(root)
	for _, p := range []string{fileLink, filepath.Join(dirLink, "secret.jpg"), "link.jpg"} {
		result, err := tool.Execute(context.Background(), map[string]interface{}{"image": p})
		require.NoError(t, err)
		assert.False(t, result.Success, p)
		assert.Contains(t, result.Error, "not allowed in sandbox", p)
	}
}

func TestImageTool_Sandbox_FileUnderAllowedRootWorks(t *testing.T) {
	workspace := t.TempDir()
	nas := t.TempDir() // stands in for an allowed_paths entry like /mnt/nas/share
	img := filepath.Join(nas, "photos", "cat.jpg")
	writeJPEG(t, img)

	// Through ConfigMgr (NewImageTool) and explicit config (registry path).
	cfg := &config.Config{}
	cfg.Tools.Sandbox = config.SandboxConfig{WorkspaceDir: workspace, AllowedPaths: []string{nas}}
	tools := []*ImageTool{
		NewImageTool(&types.ToolServices{ConfigMgr: cfg}),
		NewImageToolWithSandbox(nil, cfg.Tools.Sandbox),
	}
	wantPath, err := filepath.EvalSymlinks(img)
	require.NoError(t, err)
	for _, tool := range tools {
		result, err := tool.Execute(context.Background(), map[string]interface{}{"image": img})
		require.NoError(t, err)
		require.True(t, result.Success, result.Error)
		ar := result.Data["result"].(*ImageAnalysisResult)
		assert.Equal(t, wantPath, ar.Metadata["path"], "I/O must use the canonical path")
	}

	// A symlink inside the workspace pointing at another allowed root is fine.
	link := filepath.Join(workspace, "nas-link.jpg")
	require.NoError(t, os.Symlink(img, link))
	result, err := tools[1].Execute(context.Background(), map[string]interface{}{"image": "nas-link.jpg"})
	require.NoError(t, err)
	assert.True(t, result.Success, result.Error)
}
