//go:build with_k8s

package k8s

import (
	"context"
	"fmt"
	"testing"
	"time"

	"conduit/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/fake"
)

func TestNewPortForwarder(t *testing.T) {
	pf := NewPortForwarder(10)
	assert.NotNil(t, pf)
	assert.Equal(t, 10, pf.maxForwards)
	assert.Empty(t, pf.forwards)
}

func TestNewPortForwarder_DefaultMax(t *testing.T) {
	pf := NewPortForwarder(0)
	assert.Equal(t, 10, pf.maxForwards)

	pf2 := NewPortForwarder(-5)
	assert.Equal(t, 10, pf2.maxForwards)
}

func TestPortForwarder_Validation(t *testing.T) {
	tests := []struct {
		name       string
		localPort  int
		remotePort int
		wantErr    string
	}{
		{
			name:       "privileged local port",
			localPort:  80,
			remotePort: 8080,
			wantErr:    "local port must be >= 1024 or 0",
		},
		{
			name:       "local port 1 is privileged",
			localPort:  1,
			remotePort: 80,
			wantErr:    "local port must be >= 1024 or 0",
		},
		{
			name:       "local port 1023 is privileged",
			localPort:  1023,
			remotePort: 80,
			wantErr:    "local port must be >= 1024 or 0",
		},
		{
			name:       "local port too high",
			localPort:  70000,
			remotePort: 80,
			wantErr:    "local port must be <= 65535",
		},
		{
			name:       "remote port zero",
			localPort:  8080,
			remotePort: 0,
			wantErr:    "remote port must be 1-65535",
		},
		{
			name:       "remote port negative",
			localPort:  8080,
			remotePort: -1,
			wantErr:    "remote port must be 1-65535",
		},
		{
			name:       "remote port too high",
			localPort:  8080,
			remotePort: 70000,
			wantErr:    "remote port must be 1-65535",
		},
		{
			name:       "valid auto-assign local",
			localPort:  0,
			remotePort: 80,
			wantErr:    "",
		},
		{
			name:       "valid explicit ports",
			localPort:  8080,
			remotePort: 80,
			wantErr:    "",
		},
		{
			name:       "valid local 1024",
			localPort:  1024,
			remotePort: 443,
			wantErr:    "",
		},
		{
			name:       "valid local 65535",
			localPort:  65535,
			remotePort: 1,
			wantErr:    "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePorts(tc.localPort, tc.remotePort)
			if tc.wantErr == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
			}
		})
	}
}

func TestPortForwarder_MaxForwards(t *testing.T) {
	pf := NewPortForwarder(3)

	// Manually populate forwards to simulate active sessions.
	pf.mu.Lock()
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("pf-test-%d", i)
		pf.forwards[id] = &activeForward{
			ID:        id,
			Cluster:   "test",
			Pod:       fmt.Sprintf("pod-%d", i),
			Namespace: "default",
			stopChan:  make(chan struct{}),
			CreatedAt: time.Now(),
		}
	}
	pf.mu.Unlock()

	// Creating a new forward should fail at the limit.
	_, err := pf.Create(&ClusterClient{
		name:      "test",
		clientset: fake.NewSimpleClientset(),
	}, "pod-new", "default", 8080, 80, "test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "maximum number of port forwards reached")
}

func TestPortForwarder_Close_NotFound(t *testing.T) {
	pf := NewPortForwarder(10)
	err := pf.Close("nonexistent-id")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "port forward not found")
}

func TestPortForwarder_Close(t *testing.T) {
	pf := NewPortForwarder(10)

	// Insert a mock forward.
	stopChan := make(chan struct{})
	pf.mu.Lock()
	pf.forwards["test-id"] = &activeForward{
		ID:        "test-id",
		Cluster:   "test",
		Pod:       "my-pod",
		Namespace: "default",
		stopChan:  stopChan,
		CreatedAt: time.Now(),
	}
	pf.mu.Unlock()

	err := pf.Close("test-id")
	require.NoError(t, err)

	// Verify stopChan was closed.
	select {
	case <-stopChan:
		// Good, it was closed.
	default:
		t.Fatal("stopChan should have been closed")
	}

	// Verify removed from map.
	_, ok := pf.Get("test-id")
	assert.False(t, ok)
}

func TestPortForwarder_List_Empty(t *testing.T) {
	pf := NewPortForwarder(10)
	list := pf.List()
	assert.NotNil(t, list)
	assert.Empty(t, list)
}

func TestPortForwarder_List(t *testing.T) {
	pf := NewPortForwarder(10)

	pf.mu.Lock()
	pf.forwards["id-1"] = &activeForward{
		ID:         "id-1",
		Cluster:    "prod",
		Pod:        "web-abc",
		Namespace:  "default",
		LocalPort:  8080,
		RemotePort: 80,
		stopChan:   make(chan struct{}),
		CreatedAt:  time.Now(),
	}
	pf.forwards["id-2"] = &activeForward{
		ID:         "id-2",
		Cluster:    "prod",
		Pod:        "api-def",
		Namespace:  "backend",
		LocalPort:  9090,
		RemotePort: 8080,
		stopChan:   make(chan struct{}),
		CreatedAt:  time.Now(),
	}
	pf.mu.Unlock()

	list := pf.List()
	assert.Len(t, list, 2)
}

func TestPortForwarder_Get(t *testing.T) {
	pf := NewPortForwarder(10)

	pf.mu.Lock()
	pf.forwards["id-1"] = &activeForward{
		ID:      "id-1",
		Pod:     "web-abc",
		Cluster: "prod",
	}
	pf.mu.Unlock()

	fwd, ok := pf.Get("id-1")
	assert.True(t, ok)
	assert.Equal(t, "web-abc", fwd.Pod)

	_, ok = pf.Get("nonexistent")
	assert.False(t, ok)
}

func TestPortForwarder_CloseAll(t *testing.T) {
	pf := NewPortForwarder(10)

	stopChans := make([]chan struct{}, 3)
	pf.mu.Lock()
	for i := 0; i < 3; i++ {
		stopChans[i] = make(chan struct{})
		id := fmt.Sprintf("pf-%d", i)
		pf.forwards[id] = &activeForward{
			ID:       id,
			stopChan: stopChans[i],
		}
	}
	pf.mu.Unlock()

	pf.CloseAll()

	// Verify all stop channels were closed.
	for i, ch := range stopChans {
		select {
		case <-ch:
			// Good.
		default:
			t.Fatalf("stopChan[%d] should have been closed", i)
		}
	}

	// Verify map is empty.
	assert.Empty(t, pf.List())
}

func TestK8sTool_Execute_PortForwardList_Empty(t *testing.T) {
	tool := setupTestTool(t)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "portforward_list",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "0 active")
}

func TestK8sTool_Execute_PortForwardClose_Missing(t *testing.T) {
	tool := setupTestTool(t)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":     "portforward_close",
		"forward_id": "nonexistent",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "not found")
}

func TestK8sTool_Execute_PortForwardClose_MissingID(t *testing.T) {
	tool := setupTestTool(t)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "portforward_close",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "forward_id parameter is required")
}

func TestK8sTool_Execute_PortForwardCreate_ValidationError(t *testing.T) {
	tool := setupTestTool(t)

	// Privileged local port should be rejected.
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":      "portforward_create",
		"name":        "web-abc123",
		"local_port":  float64(80),
		"remote_port": float64(8080),
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "local port must be >= 1024")
}

func TestK8sTool_Execute_PortForwardCreate_MissingParams(t *testing.T) {
	tool := setupTestTool(t)

	// Missing pod name.
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":      "portforward_create",
		"remote_port": float64(80),
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "name parameter is required")

	// Missing remote_port.
	result, err = tool.Execute(context.Background(), map[string]interface{}{
		"action": "portforward_create",
		"name":   "web-abc123",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "remote_port parameter is required")
}

// setupTestToolWithPortForwarder is a helper that returns the tool with an
// accessible portForwarder for tests that need to check forward state.
func setupTestToolWithPortForwarder(t *testing.T) *K8sTool {
	t.Helper()
	tool := setupTestTool(t)
	return tool
}

func TestK8sTool_PortForwarder_Initialized(t *testing.T) {
	cfg := &config.KubernetesConfig{
		Enabled: true,
		Clusters: []config.KubernetesCluster{
			{Name: "prod", KubeconfigPath: "/path"},
		},
	}
	tool, err := NewK8sTool(nil, cfg)
	require.NoError(t, err)
	assert.NotNil(t, tool.portForwarder)
	assert.Equal(t, 10, tool.portForwarder.maxForwards)
}
