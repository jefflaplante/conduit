//go:build unix

package tui

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// conduit-31jg.69: 50MB of output (no newlines, so the old line reader
// would have buffered it all in one ReadString) is capped.
func TestRunShellCommand_OutputCapped(t *testing.T) {
	msg := runShellCommand("s", "head -c 52428800 /dev/zero | tr '\\0' 'a'", t.TempDir(), nil, 30*time.Second)
	require.NoError(t, msg.Err)
	assert.LessOrEqual(t, len(msg.Output), MaxOutputBytes+200, "output must be bounded")
	assert.Contains(t, msg.Output, "output truncated")
}

// conduit-31jg.69: after a normal exit, a background child holding the pipe
// must not block the result until it exits.
func TestRunShellCommand_NormalExitWithBackgroundChild(t *testing.T) {
	start := time.Now()
	msg := runShellCommand("s", "echo done; sleep 30 &", t.TempDir(), nil, time.Minute)
	elapsed := time.Since(start)
	require.NoError(t, msg.Err, "ErrWaitDelay alone is a success")
	assert.Contains(t, msg.Output, "done")
	assert.Less(t, elapsed, 10*time.Second, "must return shortly after WaitDelay, not after sleep 30")
}

func TestRunShellCommand_TimeoutAndEnv(t *testing.T) {
	msg := runShellCommand("s", "echo $CONDUIT_T; sleep 30", t.TempDir(),
		append(os.Environ(), "CONDUIT_T=hello"), 500*time.Millisecond)
	require.Error(t, msg.Err)
	assert.Contains(t, msg.Output, "hello")
	assert.Contains(t, msg.Output, "timed out")
}

func TestRunShellCommand_StderrAndExitCode(t *testing.T) {
	msg := runShellCommand("s", "echo out; echo err >&2; exit 2", t.TempDir(), nil, 10*time.Second)
	require.Error(t, msg.Err)
	assert.Contains(t, msg.Output, "out")
	assert.Contains(t, msg.Output, "err")
}

// conduit-31jg.69: a background job with a lingering grandchild completes
// promptly and its output is bounded.
func TestExecuteBackgroundCmd_BoundedAndPrompt(t *testing.T) {
	jobs := NewJobManager()
	cmd := executeBackgroundCmd("s", "head -c 5000000 /dev/zero | tr '\\0' 'b'; sleep 30 &", t.TempDir(), jobs)
	started, ok := cmd().(BackgroundJobStartedMsg)
	require.True(t, ok)

	select {
	case id := <-jobs.jobsDone:
		assert.Equal(t, started.JobID, id)
	case <-time.After(10 * time.Second):
		t.Fatal("background job blocked by lingering child")
	}
	job := jobs.GetJob(started.JobID)
	require.NotNil(t, job)
	assert.Equal(t, JobCompleted, job.Status)
	out := job.Output.String()
	assert.LessOrEqual(t, len(out), MaxOutputBytes+200)
	assert.True(t, strings.HasPrefix(out, "bbb"))
}
