//go:build with_k8s

package k8s

import (
	"bytes"
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

const (
	defaultMaxOutputBytes = 32 * 1024 // 32KB
	defaultExecTimeout    = 30 * time.Second
)

// PodExecutor handles command execution inside pod containers.
type PodExecutor struct {
	maxOutputBytes int
	defaultTimeout time.Duration
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

	if timeout == 0 {
		timeout = pe.defaultTimeout
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	execOpts := &corev1.PodExecOptions{
		Container: resolvedContainer,
		Command:   []string{"sh", "-c", command},
		Stdout:    true,
		Stderr:    true,
	}

	execURL := client.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pod).
		Namespace(ns).
		SubResource("exec").
		VersionedParams(execOpts, scheme.ParameterCodec).
		URL()

	executor, err := remotecommand.NewSPDYExecutor(client.restConfig, "POST", execURL)
	if err != nil {
		return nil, fmt.Errorf("creating SPDY executor: %w", err)
	}

	var stdout, stderr bytes.Buffer
	limitedStdout := &limitedWriter{buf: &stdout, max: pe.maxOutputBytes}
	limitedStderr := &limitedWriter{buf: &stderr, max: pe.maxOutputBytes}

	streamErr := executor.StreamWithContext(execCtx, remotecommand.StreamOptions{
		Stdout: limitedStdout,
		Stderr: limitedStderr,
	})

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
