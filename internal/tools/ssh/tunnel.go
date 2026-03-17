// Package ssh implements the SSH remote execution tool with security controls.
package ssh

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// Tunnel represents an active SSH port forwarding tunnel
type Tunnel struct {
	ID           string
	LocalPort    int
	RemoteHost   string
	RemotePort   int
	CreatedAt    time.Time
	client       *SSHClient
	listener     net.Listener
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	bytesIn      atomic.Int64
	bytesOut     atomic.Int64
	activeConns  atomic.Int32
	closed       atomic.Bool
}

// TunnelInfo contains information about a tunnel for external reporting
type TunnelInfo struct {
	TunnelID          string    `json:"tunnel_id"`
	LocalPort         int       `json:"local_port"`
	RemoteHost        string    `json:"remote_host"`
	RemotePort        int       `json:"remote_port"`
	CreatedAt         time.Time `json:"created_at"`
	BytesIn           int64     `json:"bytes_in"`
	BytesOut          int64     `json:"bytes_out"`
	ActiveConnections int32     `json:"active_connections"`
	SSHHost           string    `json:"ssh_host"`
}

// TunnelManager manages SSH tunnels with lifecycle control
type TunnelManager struct {
	mu      sync.RWMutex
	tunnels map[string]*Tunnel
	// Track tunnels per SSH host for resource management
	hostTunnels map[string][]string // ssh host name -> tunnel IDs
	closed      bool
}

// NewTunnelManager creates a new TunnelManager
func NewTunnelManager() *TunnelManager {
	return &TunnelManager{
		tunnels:     make(map[string]*Tunnel),
		hostTunnels: make(map[string][]string),
	}
}

// CreateTunnel creates a new local port forwarding tunnel
// The tunnel binds to 127.0.0.1:localPort and forwards to remoteHost:remotePort via the SSH connection
func (m *TunnelManager) CreateTunnel(client *SSHClient, localPort int, remoteHost string, remotePort int) (*Tunnel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return nil, fmt.Errorf("tunnel manager is closed")
	}

	// Validate port numbers
	if err := validatePort(localPort, "local"); err != nil {
		return nil, err
	}
	if err := validatePort(remotePort, "remote"); err != nil {
		return nil, err
	}

	// Validate remote host
	if remoteHost == "" {
		return nil, fmt.Errorf("remote host cannot be empty")
	}

	// Create listener bound to localhost only (security requirement)
	localAddr := fmt.Sprintf("127.0.0.1:%d", localPort)
	listener, err := net.Listen("tcp", localAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to bind to %s: %w", localAddr, err)
	}

	// Get the actual port if 0 was specified (auto-assign)
	actualPort := listener.Addr().(*net.TCPAddr).Port

	ctx, cancel := context.WithCancel(context.Background())

	tunnel := &Tunnel{
		ID:         uuid.New().String(),
		LocalPort:  actualPort,
		RemoteHost: remoteHost,
		RemotePort: remotePort,
		CreatedAt:  time.Now(),
		client:     client,
		listener:   listener,
		ctx:        ctx,
		cancel:     cancel,
	}

	// Start accepting connections
	tunnel.wg.Add(1)
	go tunnel.acceptLoop()

	// Track the tunnel
	m.tunnels[tunnel.ID] = tunnel
	hostName := client.Host().Name
	m.hostTunnels[hostName] = append(m.hostTunnels[hostName], tunnel.ID)

	return tunnel, nil
}

// CloseTunnel closes a tunnel by ID
func (m *TunnelManager) CloseTunnel(tunnelID string) error {
	m.mu.Lock()
	tunnel, ok := m.tunnels[tunnelID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("tunnel %s not found", tunnelID)
	}

	// Remove from tracking
	delete(m.tunnels, tunnelID)
	hostName := tunnel.client.Host().Name
	m.removeHostTunnel(hostName, tunnelID)
	m.mu.Unlock()

	// Close the tunnel (outside lock to avoid deadlock)
	return tunnel.Close()
}

// removeHostTunnel removes a tunnel ID from the host's tunnel list (must be called with lock held)
func (m *TunnelManager) removeHostTunnel(hostName, tunnelID string) {
	tunnels := m.hostTunnels[hostName]
	for i, id := range tunnels {
		if id == tunnelID {
			m.hostTunnels[hostName] = append(tunnels[:i], tunnels[i+1:]...)
			break
		}
	}
	if len(m.hostTunnels[hostName]) == 0 {
		delete(m.hostTunnels, hostName)
	}
}

// ListTunnels returns information about all active tunnels
func (m *TunnelManager) ListTunnels() []*TunnelInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	infos := make([]*TunnelInfo, 0, len(m.tunnels))
	for _, tunnel := range m.tunnels {
		infos = append(infos, tunnel.Info())
	}
	return infos
}

// GetTunnel returns information about a specific tunnel
func (m *TunnelManager) GetTunnel(tunnelID string) (*TunnelInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	tunnel, ok := m.tunnels[tunnelID]
	if !ok {
		return nil, fmt.Errorf("tunnel %s not found", tunnelID)
	}
	return tunnel.Info(), nil
}

// GetTunnelsForHost returns all tunnels for a specific SSH host
func (m *TunnelManager) GetTunnelsForHost(hostName string) []*TunnelInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	tunnelIDs := m.hostTunnels[hostName]
	infos := make([]*TunnelInfo, 0, len(tunnelIDs))
	for _, id := range tunnelIDs {
		if tunnel, ok := m.tunnels[id]; ok {
			infos = append(infos, tunnel.Info())
		}
	}
	return infos
}

// CloseAll closes all tunnels
func (m *TunnelManager) CloseAll() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true

	// Collect all tunnels
	tunnels := make([]*Tunnel, 0, len(m.tunnels))
	for _, tunnel := range m.tunnels {
		tunnels = append(tunnels, tunnel)
	}
	m.tunnels = make(map[string]*Tunnel)
	m.hostTunnels = make(map[string][]string)
	m.mu.Unlock()

	// Close all tunnels outside the lock
	var lastErr error
	for _, tunnel := range tunnels {
		if err := tunnel.Close(); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// CloseHostTunnels closes all tunnels for a specific SSH host
func (m *TunnelManager) CloseHostTunnels(hostName string) error {
	m.mu.Lock()
	tunnelIDs := m.hostTunnels[hostName]
	tunnels := make([]*Tunnel, 0, len(tunnelIDs))
	for _, id := range tunnelIDs {
		if tunnel, ok := m.tunnels[id]; ok {
			tunnels = append(tunnels, tunnel)
			delete(m.tunnels, id)
		}
	}
	delete(m.hostTunnels, hostName)
	m.mu.Unlock()

	// Close tunnels outside the lock
	var lastErr error
	for _, tunnel := range tunnels {
		if err := tunnel.Close(); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// TunnelCount returns the number of active tunnels
func (m *TunnelManager) TunnelCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.tunnels)
}

// Info returns tunnel information for external reporting
func (t *Tunnel) Info() *TunnelInfo {
	return &TunnelInfo{
		TunnelID:          t.ID,
		LocalPort:         t.LocalPort,
		RemoteHost:        t.RemoteHost,
		RemotePort:        t.RemotePort,
		CreatedAt:         t.CreatedAt,
		BytesIn:           t.bytesIn.Load(),
		BytesOut:          t.bytesOut.Load(),
		ActiveConnections: t.activeConns.Load(),
		SSHHost:           t.client.Host().Name,
	}
}

// Close shuts down the tunnel and waits for all connections to close
func (t *Tunnel) Close() error {
	if t.closed.Swap(true) {
		return nil // Already closed
	}

	// Signal shutdown
	t.cancel()

	// Close the listener to stop accepting new connections
	if err := t.listener.Close(); err != nil {
		// Ignore "use of closed network connection" errors
		if !isClosedError(err) {
			return fmt.Errorf("failed to close listener: %w", err)
		}
	}

	// Wait for all goroutines to complete
	t.wg.Wait()

	return nil
}

// acceptLoop accepts incoming connections and starts forwarding goroutines
func (t *Tunnel) acceptLoop() {
	defer t.wg.Done()

	for {
		conn, err := t.listener.Accept()
		if err != nil {
			// Check if we're shutting down
			select {
			case <-t.ctx.Done():
				return
			default:
				// Log error but continue if not closed
				if isClosedError(err) {
					return
				}
				// Transient error, continue accepting
				continue
			}
		}

		// Start a new goroutine to handle this connection
		t.wg.Add(1)
		go t.handleConnection(conn)
	}
}

// handleConnection handles a single forwarded connection
func (t *Tunnel) handleConnection(localConn net.Conn) {
	defer t.wg.Done()
	defer localConn.Close()

	t.activeConns.Add(1)
	defer t.activeConns.Add(-1)

	// Connect to the remote host via SSH tunnel
	remoteAddr := fmt.Sprintf("%s:%d", t.RemoteHost, t.RemotePort)
	remoteConn, err := t.client.Dial("tcp", remoteAddr)
	if err != nil {
		// Connection failed, close the local side
		return
	}
	defer remoteConn.Close()

	// Create a context for this connection that respects the tunnel's context
	ctx, cancel := context.WithCancel(t.ctx)
	defer cancel()

	// Start bidirectional copy
	var wg sync.WaitGroup
	wg.Add(2)

	// Local -> Remote
	go func() {
		defer wg.Done()
		defer cancel() // Signal the other direction to stop
		n, _ := t.copyWithContext(ctx, remoteConn, localConn)
		t.bytesOut.Add(n)
	}()

	// Remote -> Local
	go func() {
		defer wg.Done()
		defer cancel() // Signal the other direction to stop
		n, _ := t.copyWithContext(ctx, localConn, remoteConn)
		t.bytesIn.Add(n)
	}()

	wg.Wait()
}

// copyWithContext copies data from src to dst, respecting context cancellation
func (t *Tunnel) copyWithContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	// We need to handle context cancellation for cleanup
	// Use a goroutine to detect context cancellation
	done := make(chan struct{})
	defer close(done)

	var n int64
	var err error

	copyDone := make(chan struct{})
	go func() {
		n, err = io.Copy(dst, src)
		close(copyDone)
	}()

	select {
	case <-ctx.Done():
		// Context cancelled, the copy will be interrupted when connections close
		<-copyDone
		return n, ctx.Err()
	case <-copyDone:
		return n, err
	}
}

// validatePort validates a port number
func validatePort(port int, portType string) error {
	if port < 0 || port > 65535 {
		return fmt.Errorf("%s port must be between 0 and 65535", portType)
	}
	// For local ports, restrict to unprivileged ports unless 0 (auto-assign)
	if portType == "local" && port != 0 && port < 1024 {
		return fmt.Errorf("local port must be >= 1024 (unprivileged) or 0 for auto-assign, got %d", port)
	}
	return nil
}

// isClosedError checks if an error is a "use of closed network connection" error
func isClosedError(err error) bool {
	if err == nil {
		return false
	}
	// Check for common closed connection errors
	errStr := err.Error()
	return errStr == "use of closed network connection" ||
		errStr == "accept tcp: use of closed network connection"
}
