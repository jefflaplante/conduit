//go:build with_k8s

package k8s

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"
)

// fakeExecutor is a remotecommand.Executor that returns canned output.
type fakeExecutor struct {
	stdout string
	delay  time.Duration
	err    error
}

func (f *fakeExecutor) Stream(opts remotecommand.StreamOptions) error {
	return f.StreamWithContext(context.Background(), opts)
}

func (f *fakeExecutor) StreamWithContext(ctx context.Context, opts remotecommand.StreamOptions) error {
	if f.stdout != "" && opts.Stdout != nil {
		_, _ = opts.Stdout.Write([]byte(f.stdout))
	}
	select {
	case <-time.After(f.delay):
	case <-ctx.Done():
		return ctx.Err()
	}
	return f.err
}

func fakeClusterClient(t *testing.T) *ClusterClient {
	t.Helper()
	tool := setupTestTool(t)
	client, err := tool.clients.GetClient("test-cluster")
	require.NoError(t, err)
	client.restConfig = &rest.Config{Host: "https://example.invalid"}
	return client
}

// conduit-31jg.69: the command sent to the pod must be wrapped in the
// watchdog script, with the user command and timeout as positional args.
func TestPodExecutor_Execute_SendsKillWrapper(t *testing.T) {
	client := fakeClusterClient(t)
	pe := NewPodExecutor()

	var captured *corev1.PodExecOptions
	pe.newExecutor = func(_ *ClusterClient, pod, ns string, opts *corev1.PodExecOptions) (remotecommand.Executor, error) {
		captured = opts
		assert.Equal(t, "web-abc123", pod)
		assert.Equal(t, "default", ns)
		return &fakeExecutor{stdout: "hi\n"}, nil
	}

	res, err := pe.Execute(context.Background(), client, "web-abc123", "default", "web", "echo hi | cat", 2500*time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, "hi\n", res.Stdout)
	assert.False(t, res.TimedOut)

	require.NotNil(t, captured)
	require.Len(t, captured.Command, 6)
	assert.Equal(t, []string{"sh", "-c"}, captured.Command[:2])
	assert.Equal(t, remoteExecWrapper, captured.Command[2])
	assert.Contains(t, captured.Command[2], "kill -9")
	assert.Contains(t, captured.Command[2], "sleep")
	assert.Equal(t, "echo hi | cat", captured.Command[4], "user command passed verbatim as $1")
	assert.Equal(t, "3", captured.Command[5], "timeout rounded up to whole seconds as $2")
}

// conduit-31jg.69: a watchdog kill (exit 137 at/after the deadline) is
// reported as a timeout with its partial output.
func TestPodExecutor_Execute_RemoteKillReportedAsTimeout(t *testing.T) {
	client := fakeClusterClient(t)
	pe := NewPodExecutor()
	pe.newExecutor = func(*ClusterClient, string, string, *corev1.PodExecOptions) (remotecommand.Executor, error) {
		return &fakeExecutor{
			stdout: "partial",
			delay:  1100 * time.Millisecond,
			err:    utilexec.CodeExitError{Err: errors.New("killed"), Code: 137},
		}, nil
	}

	res, err := pe.Execute(context.Background(), client, "web-abc123", "default", "web", "sleep 100", time.Second)
	require.NoError(t, err)
	assert.True(t, res.TimedOut)
	assert.Equal(t, 137, res.ExitCode)
	assert.Equal(t, "partial", res.Stdout)
}

// A fast non-zero exit is not a timeout.
func TestPodExecutor_Execute_ExitCodeNotTimeout(t *testing.T) {
	client := fakeClusterClient(t)
	pe := NewPodExecutor()
	pe.newExecutor = func(*ClusterClient, string, string, *corev1.PodExecOptions) (remotecommand.Executor, error) {
		return &fakeExecutor{err: utilexec.CodeExitError{Err: errors.New("x"), Code: 137}}, nil
	}
	res, err := pe.Execute(context.Background(), client, "web-abc123", "default", "web", "kill -9 $$", 10*time.Second)
	require.NoError(t, err)
	assert.False(t, res.TimedOut)
	assert.Equal(t, 137, res.ExitCode)
}

func TestBuildRemoteCommand_MinimumOneSecond(t *testing.T) {
	cmd := buildRemoteCommand("ls", 10*time.Millisecond)
	assert.Equal(t, "1", cmd[5])
}

// conduit-31jg.69: run the real wrapper under the local sh to prove it
// kills a long-running command (and its pipeline) at the deadline and that a
// normal command's exit status passes through.
func TestRemoteExecWrapper_LocalShell(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}

	t.Run("kills at deadline", func(t *testing.T) {
		argv := buildRemoteCommand("echo started; sleep 30 | cat", time.Second)
		start := time.Now()
		out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
		elapsed := time.Since(start)

		var exitErr *exec.ExitError
		require.ErrorAs(t, err, &exitErr)
		assert.Equal(t, remoteKilledExitCode, exitErr.ExitCode())
		assert.Less(t, elapsed, 10*time.Second, "wrapper must not wait for the full sleep")
		assert.Contains(t, string(out), "started")
		assert.Contains(t, string(out), "exceeded 1s")
	})

	t.Run("passes exit status and quoting", func(t *testing.T) {
		argv := buildRemoteCommand(`printf '%s\n' "a b" "$HOME" >/dev/null; echo 'it''s'; exit 3`, 5*time.Second)
		start := time.Now()
		out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
		var exitErr *exec.ExitError
		require.ErrorAs(t, err, &exitErr)
		assert.Equal(t, 3, exitErr.ExitCode())
		assert.Equal(t, "its", strings.TrimSpace(string(out)))
		assert.Less(t, time.Since(start), 3*time.Second, "watchdog must be cancelled on normal exit")
	})
}
