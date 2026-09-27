//go:build with_k8s

package k8s

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

const (
	defaultMaxOutputBytes = 32 * 1024 // 32KB
	defaultExecTimeout    = 30 * time.Second

	// execLocalGrace is how long past the remote deadline the local stream
	// stays open so the in-pod watchdog's kill (with its exit status and the
	// partial output) can arrive before we give up locally. conduit-31jg.69
	execLocalGrace = 5 * time.Second

	// remoteKilledExitCode is what sh reports for a child killed by SIGKILL.
	remoteKilledExitCode = 137
)

// remoteExecWrapper is the sh script every pod exec runs under. Closing the
// SPDY stream does NOT kill the remote process (conduit-31jg.69), so the
// deadline is enforced inside the pod:
//
//   - $1 is the user command (passed as an argument, never interpolated into
//     the script, so it needs no extra quoting), $2 the timeout in seconds.
//   - The command runs in the background, under `setsid` when available so
//     it leads its own process group and the watchdog can kill the whole
//     group (pipelines, grandchildren); otherwise only its `sh -c` is killed.
//   - A watchdog subshell sleeps $2 seconds, then SIGKILLs the group/pid and
//     prints a marker on stderr. It is a separate process, so it still fires
//     if the wrapper itself dies when the local side drops the stream. Its
//     `sleep` holds no stream fds, so after a normal exit the orphaned sleep
//     cannot keep the exec stream open until the deadline.
//
// Limits: needs `sh` (already required) and `sleep` in the image. Without
// `sleep` the command runs unguarded (plain exec, the pre-conduit-31jg.69
// behaviour); without `setsid` descendants of the command may survive the
// kill. Images without `sh` (distroless) cannot exec at all.
const remoteExecWrapper = `if ! command -v sleep >/dev/null 2>&1; then exec sh -c "$1"; fi
if command -v setsid >/dev/null 2>&1; then setsid sh -c "$1" & else sh -c "$1" & fi
pid=$!
( sleep "$2" </dev/null >/dev/null 2>&1; echo "conduit: command exceeded ${2}s, killing" >&2; kill -9 -"$pid" 2>/dev/null || kill -9 "$pid" 2>/dev/null ) >/dev/null &
wd=$!
wait "$pid"; rc=$?
kill "$wd" 2>/dev/null
exit $rc`

// buildRemoteCommand returns the argv sent to the pod: the wrapper script
// with the user command and the timeout (whole seconds, rounded up, >= 1) as
// positional parameters. conduit-31jg.69
func buildRemoteCommand(command string, timeout time.Duration) []string {
	secs := int(math.Ceil(timeout.Seconds()))
	if secs < 1 {
		secs = 1
	}
	return []string{"sh", "-c", remoteExecWrapper, "conduit-exec", command, strconv.Itoa(secs)}
}

// executorFactory builds the remotecommand.Executor for an exec request. It
// is a seam so tests can capture the PodExecOptions without an API server.
type executorFactory func(client *ClusterClient, pod, namespace string, opts *corev1.PodExecOptions) (remotecommand.Executor, error)

// spdyExecutorFactory is the production executorFactory.
func spdyExecutorFactory(client *ClusterClient, pod, namespace string, opts *corev1.PodExecOptions) (remotecommand.Executor, error) {
	execURL := client.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pod).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(opts, scheme.ParameterCodec).
		URL()
	return remotecommand.NewSPDYExecutor(client.restConfig, "POST", execURL)
}

// PodExecutor handles command execution inside pod containers.
type PodExecutor struct {
	maxOutputBytes int
	defaultTimeout time.Duration
	newExecutor    executorFactory
}

// ExecResult holds the output of a pod exec invocation.
type ExecResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	TimedOut bool   `json:"timed_out,omitempty"`
}

// NewPodExecutor creates a PodExecutor with sensible defaults.
func NewPodExecutor() *PodExecutor {
	return &PodExecutor{
		maxOutputBytes: defaultMaxOutputBytes,
		defaultTimeout: defaultExecTimeout,
		newExecutor:    spdyExecutorFactory,
	}
}

// Execute runs a command in the specified pod container and captures output.
func (pe *PodExecutor) Execute(ctx context.Context, client *ClusterClient, pod, namespace, container, command string, timeout time.Duration) (*ExecResult, error) {
	if client.restConfig == nil {
		return nil, fmt.Errorf("cluster client has no REST config (exec requires a real cluster connection)")
	}

	ns := client.resolveNamespace(namespace)

	// Resolve container name if not provided.
	resolvedContainer, err := pe.resolveContainer(ctx, client, pod, ns, container)
	if err != nil {
		return nil, fmt.Errorf("resolving container: %w", err)
	}

	if timeout <= 0 {
		timeout = pe.defaultTimeout
	}
	// The pod-side watchdog enforces `timeout`; the local deadline is a
	// backstop slightly later so the kill's exit status and partial output
	// can still arrive. conduit-31jg.69
	execCtx, cancel := context.WithTimeout(ctx, timeout+execLocalGrace)
	defer cancel()

	execOpts := &corev1.PodExecOptions{
		Container: resolvedContainer,
		Command:   buildRemoteCommand(command, timeout),
		Stdout:    true,
		Stderr:    true,
	}

	newExecutor := pe.newExecutor
	if newExecutor == nil {
		newExecutor = spdyExecutorFactory
	}
	executor, err := newExecutor(client, pod, ns, execOpts)
	if err != nil {
		return nil, fmt.Errorf("creating SPDY executor: %w", err)
	}

	var stdout, stderr bytes.Buffer
	limitedStdout := &limitedWriter{buf: &stdout, max: pe.maxOutputBytes}
	limitedStderr := &limitedWriter{buf: &stderr, max: pe.maxOutputBytes}

	start := time.Now()
	streamErr := executor.StreamWithContext(execCtx, remotecommand.StreamOptions{
		Stdout: limitedStdout,
		Stderr: limitedStderr,
	})
	elapsed := time.Since(start)

	result := &ExecResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}

	if execCtx.Err() == context.DeadlineExceeded {
		result.TimedOut = true
		result.ExitCode = -1
		return result, nil
	}

	if streamErr != nil {
		if exitErr, ok := streamErr.(interface{ ExitStatus() int }); ok {
			result.ExitCode = exitErr.ExitStatus()
			// Killed by the pod-side watchdog at the deadline. conduit-31jg.69
			if result.ExitCode == remoteKilledExitCode && elapsed >= timeout {
				result.TimedOut = true
			}
			return result, nil
		}
		return result, fmt.Errorf("exec stream: %w", streamErr)
	}

	return result, nil
}

// resolveContainer returns the container name to use. If container is non-empty
// it is returned as-is. Otherwise the first container in the pod spec is used.
func (pe *PodExecutor) resolveContainer(ctx context.Context, client *ClusterClient, pod, namespace, container string) (string, error) {
	if container != "" {
		return container, nil
	}

	p, err := client.clientset.CoreV1().Pods(namespace).Get(ctx, pod, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("getting pod %s/%s to resolve container: %w", namespace, pod, err)
	}
	if len(p.Spec.Containers) == 0 {
		return "", fmt.Errorf("pod %s/%s has no containers", namespace, pod)
	}
	return p.Spec.Containers[0].Name, nil
}

// limitedWriter wraps a bytes.Buffer and stops writing once max bytes are reached.
type limitedWriter struct {
	buf *bytes.Buffer
	max int
}

func (lw *limitedWriter) Write(p []byte) (int, error) {
	remaining := lw.max - lw.buf.Len()
	if remaining <= 0 {
		return len(p), nil // discard silently
	}
	if len(p) > remaining {
		p = p[:remaining]
	}
	return lw.buf.Write(p)
}
