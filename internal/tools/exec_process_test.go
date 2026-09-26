//go:build unix

package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"conduit/internal/procutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// waitGroupGone polls until process group pgid has no members (ESRCH) or
// the deadline passes. Orphaned grandchildren are reaped by init, so a
// short poll is needed even after a successful SIGKILL.
func waitGroupGone(pgid int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(-pgid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// conduit-31jg.20: a timed-out command with a background grandchild must
// return promptly and leave no member of its process group alive.
func TestExecTool_TimeoutKillsProcessGroup(t *testing.T) {
	tool := newExecToolForTest(t, nil)
	pidFile := filepath.Join(tool.registry.sandboxCfg.WorkspaceDir, "pgid")

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	res, err := tool.Execute(ctx, map[string]interface{}{
		"command": "echo $$ > " + pidFile + "; sleep 30 & sleep 30",
	})
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.False(t, res.Success)
	assert.Less(t, elapsed, 500*time.Millisecond+procutil.DefaultWaitDelay+time.Second,
		"Execute must return within timeout + WaitDelay, took %s", elapsed)
	require.NotNil(t, res.ErrorDetails)
	assert.Equal(t, "timeout_error", res.ErrorDetails.Type)

	raw, err := os.ReadFile(pidFile)
	require.NoError(t, err)
	pgid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	require.NoError(t, err)
	assert.True(t, waitGroupGone(pgid, 3*time.Second), "process group %d still has live members", pgid)
}

// conduit-31jg.20: per-call timeout (milliseconds, Claude Code contract).
func TestExecTool_PerCallTimeoutHonoured(t *testing.T) {
	tool := newExecToolForTest(t, nil)
	start := time.Now()
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"command": "sleep 10",
		"timeout": float64(300),
	})
	require.NoError(t, err)
	assert.False(t, res.Success)
	assert.Less(t, time.Since(start), 5*time.Second)
	require.NotNil(t, res.ErrorDetails)
	assert.Equal(t, "timeout_error", res.ErrorDetails.Type)
	assert.Contains(t, res.Error, "timed out")
}

func TestExecTool_CallTimeoutBounds(t *testing.T) {
	tool := newExecToolForTest(t, nil)
	d, ok := tool.CallTimeout(map[string]interface{}{"timeout": float64(120000)})
	assert.True(t, ok)
	assert.Equal(t, 120*time.Second, d)

	d, ok = tool.CallTimeout(map[string]interface{}{"timeout": float64(99999999)})
	assert.True(t, ok)
	assert.Equal(t, MaxBashTimeout, d)

	_, ok = tool.CallTimeout(map[string]interface{}{})
	assert.False(t, ok)
	_, ok = tool.CallTimeout(map[string]interface{}{"timeout": float64(0)})
	assert.False(t, ok)
}

// conduit-31jg.20: output is capped, not buffered without limit.
func TestExecTool_OutputCapped(t *testing.T) {
	tool := newExecToolForTest(t, nil)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"command": "yes | head -c 50000000",
	})
	runtime.ReadMemStats(&after)
	require.NoError(t, err)
	require.True(t, res.Success, "error: %s", res.Error)

	assert.LessOrEqual(t, len(res.Content), MaxBashOutputBytes+200)
	assert.Contains(t, res.Content, "bytes omitted")
	assert.True(t, strings.HasPrefix(res.Content, "y\ny\n"))
	assert.True(t, strings.HasSuffix(res.Content, "y\n"))
	// The old CombinedOutput path grew a bytes.Buffer past 50MB.
	assert.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(25<<20),
		"allocated %d bytes for a 50MB stream", after.TotalAlloc-before.TotalAlloc)
}

// A background child that keeps the pipe open after a successful exit must
// not turn a success into a hang or an error (WaitDelay path).
func TestExecTool_BackgroundChildDoesNotHang(t *testing.T) {
	tool := newExecToolForTest(t, nil)
	start := time.Now()
	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"command": "echo started; sleep 4 &",
	})
	require.NoError(t, err)
	assert.True(t, res.Success, "error: %s", res.Error)
	assert.Contains(t, res.Content, "started")
	assert.Less(t, time.Since(start), 10*time.Second)
}
