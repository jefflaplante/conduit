//go:build with_ssh

package ssh

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"conduit/internal/sandbox"
	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newSandboxedSSHTool(t *testing.T, root string) *SSHTool {
	t.Helper()
	tool, err := NewSSHTool(&types.ToolServices{}, testSSHConfig())
	require.NoError(t, err)
	if root != "" {
		tool.SetSandbox(sandbox.New(root, nil))
	}
	return tool
}

// conduit-31jg.69: scp_upload must not read local files outside the sandbox.
func TestSCPUpload_OutsideSandboxDenied(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "id_ed25519")
	require.NoError(t, os.WriteFile(outside, []byte("secret"), 0600))

	tool := newSandboxedSSHTool(t, root)
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "scp_upload", "host": "test-host",
		"local_path": outside, "remote_path": "/tmp/x",
	})
	require.NoError(t, err)
	assert.False(t, res.Success)
	assert.Contains(t, res.Error, "not allowed")
}

// A symlink inside the sandbox pointing outside is denied too.
func TestSCPUpload_SymlinkEscapeDenied(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	require.NoError(t, os.WriteFile(outside, []byte("secret"), 0600))
	link := filepath.Join(root, "link")
	require.NoError(t, os.Symlink(outside, link))

	tool := newSandboxedSSHTool(t, root)
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "scp_upload", "host": "test-host",
		"local_path": link, "remote_path": "/tmp/x",
	})
	require.NoError(t, err)
	assert.False(t, res.Success)
	assert.Contains(t, res.Error, "not allowed")
}

// No sandbox configured fails closed.
func TestSCPUpload_NoSandboxDenied(t *testing.T) {
	f := filepath.Join(t.TempDir(), "f")
	require.NoError(t, os.WriteFile(f, []byte("x"), 0600))
	tool := newSandboxedSSHTool(t, "")
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "scp_upload", "host": "test-host",
		"local_path": f, "remote_path": "/tmp/x",
	})
	require.NoError(t, err)
	assert.False(t, res.Success)
	assert.Contains(t, res.Error, "not allowed")
}

// Inside the sandbox the path check passes (the request then proceeds to
// the connection step, which fails in tests with no real host).
func TestSCPUpload_InsideSandboxPassesPathCheck(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "data.json")
	require.NoError(t, os.WriteFile(f, []byte("{}"), 0600))
	tool := newSandboxedSSHTool(t, root)
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "scp_upload", "host": "test-host",
		"local_path": f, "remote_path": "/tmp/x",
	})
	require.NoError(t, err)
	assert.NotContains(t, res.Error, "not allowed")
}

// conduit-31jg.69: scp_download destinations are confined as well.
func TestSCPDownload_OutsideSandboxDenied(t *testing.T) {
	root := t.TempDir()
	tool := newSandboxedSSHTool(t, root)
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "scp_download", "host": "test-host",
		"remote_path": "/etc/passwd", "local_path": filepath.Join(t.TempDir(), "passwd"),
	})
	require.NoError(t, err)
	assert.False(t, res.Success)
	assert.Contains(t, res.Error, "not allowed")
}
