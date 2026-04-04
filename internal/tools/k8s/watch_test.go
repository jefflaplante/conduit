package k8s

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func makeTestClient(objects ...runtime.Object) *ClusterClient {
	cs := fake.NewSimpleClientset(objects...)
	return &ClusterClient{
		name:      "test",
		clientset: cs,
		namespace: "default",
	}
}

func TestWatchResources_Pods(t *testing.T) {
	client := makeTestClient()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the watch in a goroutine and collect results.
	resultCh := make(chan *WatchResult, 1)
	errCh := make(chan error, 1)
	go func() {
		r, err := WatchResources(ctx, client, "pods", "default", "", 5*time.Second)
		resultCh <- r
		errCh <- err
	}()

	// Give the watch a moment to start, then create a pod.
	time.Sleep(100 * time.Millisecond)
	_, err := client.clientset.CoreV1().Pods("default").Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	// Cancel to end the watch early.
	time.Sleep(100 * time.Millisecond)
	cancel()

	result := <-resultCh
	require.NoError(t, <-errCh)
	require.NotNil(t, result)
	assert.GreaterOrEqual(t, len(result.Events), 1)

	// Find an event for our pod.
	found := false
	for _, e := range result.Events {
		if e.Name == "test-pod" {
			found = true
			assert.Equal(t, "pods", e.Resource)
			assert.Equal(t, "default", e.Namespace)
			assert.Contains(t, e.Summary, "test-pod")
			break
		}
	}
	assert.True(t, found, "expected to find event for test-pod")
}

func TestWatchResources_Timeout(t *testing.T) {
	client := makeTestClient()

	result, err := WatchResources(context.Background(), client, "pods", "default", "", 1*time.Second)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Completed, "watch should complete via timeout")
	assert.Empty(t, result.Events)
	assert.NotEmpty(t, result.Duration)
}

func TestWatchResources_MaxEvents(t *testing.T) {
	client := makeTestClient()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resultCh := make(chan *WatchResult, 1)
	errCh := make(chan error, 1)
	go func() {
		r, err := WatchResources(ctx, client, "pods", "default", "", 30*time.Second)
		resultCh <- r
		errCh <- err
	}()

	// Give the watch time to start.
	time.Sleep(100 * time.Millisecond)

	// Create 110 pods — should be capped at 100 events.
	// Pace writes to avoid overflowing the fake watcher's internal channel buffer
	// (RaceFreeFakeWatcher panics on Add if channel is full).
	for i := 0; i < 110; i++ {
		_, err := client.clientset.CoreV1().Pods("default").Create(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("pod-%03d", i),
				Namespace: "default",
			},
		}, metav1.CreateOptions{})
		if err != nil {
			break // context may be cancelled after 100
		}
		time.Sleep(time.Millisecond)
	}

	result := <-resultCh
	require.NoError(t, <-errCh)
	require.NotNil(t, result)
	assert.Len(t, result.Events, maxWatchEvents)
	assert.False(t, result.Completed, "should stop due to max events, not timeout")
}

func TestWatchResources_UnsupportedKind(t *testing.T) {
	client := makeTestClient()
	_, err := WatchResources(context.Background(), client, "customresource", "default", "", 1*time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "watch not supported")
}

func TestBuildEventSummary_Pod(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	summary := buildEventSummary("ADDED", pod)
	assert.Contains(t, summary, "nginx")
	assert.Contains(t, summary, "ADDED")
	assert.Contains(t, summary, "Running")
}

func TestBuildEventSummary_Deployment(t *testing.T) {
	replicas := int32(3)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web"},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 2},
	}
	summary := buildEventSummary("MODIFIED", dep)
	assert.Contains(t, summary, "web")
	assert.Contains(t, summary, "MODIFIED")
	assert.Contains(t, summary, "2/3")
}

func TestBuildEventSummary_Service(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "api-svc"},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, ClusterIP: "10.0.0.1"},
	}
	summary := buildEventSummary("ADDED", svc)
	assert.Contains(t, summary, "api-svc")
	assert.Contains(t, summary, "ClusterIP")
	assert.Contains(t, summary, "10.0.0.1")
}

func TestBuildEventSummary_ConfigMap(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "my-config"},
		Data:       map[string]string{"key1": "val1", "key2": "val2"},
	}
	summary := buildEventSummary("MODIFIED", cm)
	assert.Contains(t, summary, "my-config")
	assert.Contains(t, summary, "2 keys")
}

func TestBuildEventSummary_Node(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			},
		},
	}
	summary := buildEventSummary("MODIFIED", node)
	assert.Contains(t, summary, "node-1")
	assert.Contains(t, summary, "Ready")
}

func TestBuildEventSummary_Nil(t *testing.T) {
	summary := buildEventSummary("DELETED", nil)
	assert.Equal(t, "DELETED", summary)
}

func TestBuildEventSummary_Event(t *testing.T) {
	ev := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "ev-1"},
		InvolvedObject: corev1.ObjectReference{Name: "web-pod"},
		Reason:         "Pulled",
		Message:        "Successfully pulled image",
	}
	summary := buildEventSummary("ADDED", ev)
	assert.Contains(t, summary, "web-pod")
	assert.Contains(t, summary, "Pulled")
}

func TestWatchResources_Deployments(t *testing.T) {
	client := makeTestClient()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resultCh := make(chan *WatchResult, 1)
	errCh := make(chan error, 1)
	go func() {
		r, err := WatchResources(ctx, client, "deployments", "default", "", 5*time.Second)
		resultCh <- r
		errCh <- err
	}()

	time.Sleep(100 * time.Millisecond)
	replicas := int32(1)
	_, err := client.clientset.AppsV1().Deployments("default").Create(ctx, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "test-deploy", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "test"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "test"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "img"}}},
			},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)
	cancel()

	result := <-resultCh
	require.NoError(t, <-errCh)
	require.NotNil(t, result)
	assert.GreaterOrEqual(t, len(result.Events), 1)
}

func TestK8sTool_Execute_Watch(t *testing.T) {
	tool := setupTestTool(t)

	// Watch for a short time with no events expected.
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":   "watch",
		"resource": "pods",
		"timeout":  float64(1),
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "Watch completed")
	assert.Contains(t, result.Content, "test-cluster")
	assert.NotNil(t, result.Data)
}

func TestK8sTool_Execute_Watch_MissingResource(t *testing.T) {
	tool := setupTestTool(t)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "watch",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "resource parameter is required")
}
