package k8s

import (
	"bytes"
	"fmt"
	"net/http"
	"sync"
	"time"

	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// PortForwarder manages active port-forward sessions to Kubernetes pods.
type PortForwarder struct {
	forwards    map[string]*activeForward
	mu          sync.RWMutex
	maxForwards int
}

// activeForward tracks a single port-forward session.
type activeForward struct {
	ID         string    `json:"id"`
	Cluster    string    `json:"cluster"`
	Pod        string    `json:"pod"`
	Namespace  string    `json:"namespace"`
	LocalPort  int       `json:"local_port"`
	RemotePort int       `json:"remote_port"`
	stopChan   chan struct{}
	readyChan  chan struct{}
	errChan    chan error
	CreatedAt  time.Time `json:"created_at"`
}

// NewPortForwarder creates a new PortForwarder. If maxForwards is <= 0, defaults to 10.
func NewPortForwarder(maxForwards int) *PortForwarder {
	if maxForwards <= 0 {
		maxForwards = 10
	}
	return &PortForwarder{
		forwards:    make(map[string]*activeForward),
		maxForwards: maxForwards,
	}
}

// validatePorts checks that local and remote ports are within acceptable ranges.
func validatePorts(localPort, remotePort int) error {
	if localPort != 0 && localPort < 1024 {
		return fmt.Errorf("local port must be >= 1024 or 0 for auto-assign, got %d", localPort)
	}
	if localPort > 65535 {
		return fmt.Errorf("local port must be <= 65535, got %d", localPort)
	}
	if remotePort < 1 || remotePort > 65535 {
		return fmt.Errorf("remote port must be 1-65535, got %d", remotePort)
	}
	return nil
}

// Create establishes a new port-forward session to a pod.
func (pf *PortForwarder) Create(client *ClusterClient, pod, namespace string, localPort, remotePort int, cluster string) (*activeForward, error) {
	if err := validatePorts(localPort, remotePort); err != nil {
		return nil, err
	}

	pf.mu.Lock()
	if len(pf.forwards) >= pf.maxForwards {
		pf.mu.Unlock()
		return nil, fmt.Errorf("maximum number of port forwards reached (%d)", pf.maxForwards)
	}
	pf.mu.Unlock()

	ns := namespace
	if ns == "" {
		ns = "default"
	}

	// Build the SPDY URL for portforward subresource.
	url := client.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(ns).
		Name(pod).
		SubResource("portforward").
		URL()

	transport, upgrader, err := spdy.RoundTripperFor(client.restConfig)
	if err != nil {
		return nil, fmt.Errorf("creating SPDY round tripper: %w", err)
	}

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, "POST", url)

	stopChan := make(chan struct{}, 1)
	readyChan := make(chan struct{})
	errChan := make(chan error, 1)

	portSpec := fmt.Sprintf("%d:%d", localPort, remotePort)
	ports := []string{portSpec}

	var out, errOut bytes.Buffer

	fw, err := portforward.New(dialer, ports, stopChan, readyChan, &out, &errOut)
	if err != nil {
		return nil, fmt.Errorf("creating port forwarder: %w", err)
	}

	// Run forwarding in background.
	go func() {
		if fwErr := fw.ForwardPorts(); fwErr != nil {
			errChan <- fwErr
		}
	}()

	// Wait for ready or error.
	select {
	case <-readyChan:
		// Success — get the actual assigned ports.
	case fwErr := <-errChan:
		return nil, fmt.Errorf("port forward failed: %w", fwErr)
	}

	// Retrieve actual local port (matters when localPort was 0).
	assignedLocal := localPort
	forwardedPorts, err := fw.GetPorts()
	if err == nil && len(forwardedPorts) > 0 {
		assignedLocal = int(forwardedPorts[0].Local)
	}

	id := fmt.Sprintf("pf-%s-%s-%d-%d", cluster, pod, assignedLocal, time.Now().UnixMilli())

	fwd := &activeForward{
		ID:         id,
		Cluster:    cluster,
		Pod:        pod,
		Namespace:  ns,
		LocalPort:  assignedLocal,
		RemotePort: remotePort,
		stopChan:   stopChan,
		readyChan:  readyChan,
		errChan:    errChan,
		CreatedAt:  time.Now(),
	}

	pf.mu.Lock()
	pf.forwards[id] = fwd
	pf.mu.Unlock()

	return fwd, nil
}

// Close terminates a port-forward session by ID.
func (pf *PortForwarder) Close(id string) error {
	pf.mu.Lock()
	defer pf.mu.Unlock()

	fwd, ok := pf.forwards[id]
	if !ok {
		return fmt.Errorf("port forward not found: %s", id)
	}

	close(fwd.stopChan)
	delete(pf.forwards, id)
	return nil
}

// List returns a copy of all active port-forward sessions.
func (pf *PortForwarder) List() []activeForward {
	pf.mu.RLock()
	defer pf.mu.RUnlock()

	result := make([]activeForward, 0, len(pf.forwards))
	for _, fwd := range pf.forwards {
		result = append(result, *fwd)
	}
	return result
}

// Get returns a port-forward session by ID.
func (pf *PortForwarder) Get(id string) (*activeForward, bool) {
	pf.mu.RLock()
	defer pf.mu.RUnlock()

	fwd, ok := pf.forwards[id]
	if !ok {
		return nil, false
	}
	return fwd, true
}

// CloseAll terminates all active port-forward sessions.
func (pf *PortForwarder) CloseAll() {
	pf.mu.Lock()
	defer pf.mu.Unlock()

	for id, fwd := range pf.forwards {
		close(fwd.stopChan)
		delete(pf.forwards, id)
	}
}
