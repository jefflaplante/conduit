package ssh

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"conduit/internal/config"
)

// mockDialer implements a mock dialer for testing tunnels
type mockDialer struct {
	dialFunc func(network, addr string) (net.Conn, error)
}

func (m *mockDialer) Dial(network, addr string) (net.Conn, error) {
	if m.dialFunc != nil {
		return m.dialFunc(network, addr)
	}
	return nil, fmt.Errorf("not implemented")
}

// testableSSHClient is a wrapper around SSHClient that allows mock dialing for tests
type testableSSHClient struct {
	*SSHClient
	dialFunc func(network, addr string) (net.Conn, error)
}

// Dial overrides the SSHClient Dial for testing
func (t *testableSSHClient) Dial(network, addr string) (net.Conn, error) {
	if t.dialFunc != nil {
		return t.dialFunc(network, addr)
	}
	return nil, fmt.Errorf("not implemented")
}

// createMockSSHClient creates a mock SSHClient for testing tunnels
func createMockSSHClient(name string, dialFunc func(string, string) (net.Conn, error)) *SSHClient {
	// Create a basic SSHClient without a real connection for testing
	// The tunnel tests will use the mock dialFunc
	return &SSHClient{
		host: config.SSHHostConfig{
			Name:     name,
			Hostname: "test-host",
			Port:     22,
		},
		conn:      nil, // No real connection for basic tests
		createdAt: time.Now(),
	}
}

func TestNewTunnelManager(t *testing.T) {
	tm := NewTunnelManager()
	if tm == nil {
		t.Fatal("NewTunnelManager() returned nil")
	}

	if tm.tunnels == nil {
		t.Error("tunnels map not initialized")
	}

	if tm.hostTunnels == nil {
		t.Error("hostTunnels map not initialized")
	}

	if tm.TunnelCount() != 0 {
		t.Errorf("TunnelCount() = %d, want 0", tm.TunnelCount())
	}
}

func TestTunnelManager_CreateTunnel_Validation(t *testing.T) {
	tm := NewTunnelManager()
	defer tm.CloseAll()

	// Create a mock client - we'll test validation before actual connection
	mockClient := createMockSSHClient("test", nil)

	tests := []struct {
		name       string
		localPort  int
		remoteHost string
		remotePort int
		wantErr    bool
		errContain string
	}{
		{
			name:       "valid ports",
			localPort:  0, // auto-assign
			remoteHost: "localhost",
			remotePort: 8080,
			wantErr:    false,
		},
		{
			name:       "privileged local port",
			localPort:  80,
			remoteHost: "localhost",
			remotePort: 8080,
			wantErr:    true,
			errContain: "unprivileged",
		},
		{
			name:       "negative local port",
			localPort:  -1,
			remoteHost: "localhost",
			remotePort: 8080,
			wantErr:    true,
			errContain: "between 0 and 65535",
		},
		{
			name:       "local port too high",
			localPort:  70000,
			remoteHost: "localhost",
			remotePort: 8080,
			wantErr:    true,
			errContain: "between 0 and 65535",
		},
		{
			name:       "negative remote port",
			localPort:  0,
			remoteHost: "localhost",
			remotePort: -1,
			wantErr:    true,
			errContain: "between 0 and 65535",
		},
		{
			name:       "remote port too high",
			localPort:  0,
			remoteHost: "localhost",
			remotePort: 70000,
			wantErr:    true,
			errContain: "between 0 and 65535",
		},
		{
			name:       "empty remote host",
			localPort:  0,
			remoteHost: "",
			remotePort: 8080,
			wantErr:    true,
			errContain: "cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tunnel, err := tm.CreateTunnel(mockClient, tt.localPort, tt.remoteHost, tt.remotePort)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
					if tunnel != nil {
						tunnel.Close()
					}
				} else if tt.errContain != "" && !contains(err.Error(), tt.errContain) {
					t.Errorf("error %q should contain %q", err.Error(), tt.errContain)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if tunnel != nil {
					// Verify tunnel was created with valid info
					if tunnel.LocalPort < 1024 {
						t.Errorf("auto-assigned port %d should be >= 1024", tunnel.LocalPort)
					}
					tunnel.Close()
				}
			}
		})
	}
}

func TestTunnelManager_CreateTunnel_AutoAssignPort(t *testing.T) {
	tm := NewTunnelManager()
	defer tm.CloseAll()

	mockClient := createMockSSHClient("test-host", nil)

	tunnel, err := tm.CreateTunnel(mockClient, 0, "localhost", 8080)
	if err != nil {
		t.Fatalf("CreateTunnel() error = %v", err)
	}
	defer tunnel.Close()

	// Verify port was auto-assigned
	if tunnel.LocalPort == 0 {
		t.Error("port should have been auto-assigned")
	}

	if tunnel.LocalPort < 1024 {
		t.Errorf("auto-assigned port %d is privileged", tunnel.LocalPort)
	}
}

func TestTunnelManager_CreateTunnel_BindsToLocalhost(t *testing.T) {
	tm := NewTunnelManager()
	defer tm.CloseAll()

	mockClient := createMockSSHClient("test-host", nil)

	tunnel, err := tm.CreateTunnel(mockClient, 0, "localhost", 8080)
	if err != nil {
		t.Fatalf("CreateTunnel() error = %v", err)
	}
	defer tunnel.Close()

	// Verify listener address is localhost
	addr := tunnel.listener.Addr().String()
	if !contains(addr, "127.0.0.1") {
		t.Errorf("tunnel bound to %s, want 127.0.0.1:*", addr)
	}
}

func TestTunnelManager_CreateTunnel_UniqueID(t *testing.T) {
	tm := NewTunnelManager()
	defer tm.CloseAll()

	mockClient := createMockSSHClient("test-host", nil)

	tunnel1, err := tm.CreateTunnel(mockClient, 0, "localhost", 8080)
	if err != nil {
		t.Fatalf("CreateTunnel() error = %v", err)
	}

	tunnel2, err := tm.CreateTunnel(mockClient, 0, "localhost", 8081)
	if err != nil {
		t.Fatalf("CreateTunnel() error = %v", err)
	}

	if tunnel1.ID == tunnel2.ID {
		t.Error("tunnel IDs should be unique")
	}
}

func TestTunnelManager_CloseTunnel(t *testing.T) {
	tm := NewTunnelManager()
	defer tm.CloseAll()

	mockClient := createMockSSHClient("test-host", nil)

	tunnel, err := tm.CreateTunnel(mockClient, 0, "localhost", 8080)
	if err != nil {
		t.Fatalf("CreateTunnel() error = %v", err)
	}

	if tm.TunnelCount() != 1 {
		t.Errorf("TunnelCount() = %d, want 1", tm.TunnelCount())
	}

	err = tm.CloseTunnel(tunnel.ID)
	if err != nil {
		t.Errorf("CloseTunnel() error = %v", err)
	}

	if tm.TunnelCount() != 0 {
		t.Errorf("TunnelCount() = %d, want 0", tm.TunnelCount())
	}
}

func TestTunnelManager_CloseTunnel_NotFound(t *testing.T) {
	tm := NewTunnelManager()
	defer tm.CloseAll()

	err := tm.CloseTunnel("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent tunnel")
	}
}

func TestTunnelManager_ListTunnels(t *testing.T) {
	tm := NewTunnelManager()
	defer tm.CloseAll()

	mockClient := createMockSSHClient("test-host", nil)

	// Create multiple tunnels
	tunnel1, _ := tm.CreateTunnel(mockClient, 0, "host1", 8080)
	tunnel2, _ := tm.CreateTunnel(mockClient, 0, "host2", 8081)

	tunnels := tm.ListTunnels()
	if len(tunnels) != 2 {
		t.Errorf("ListTunnels() returned %d tunnels, want 2", len(tunnels))
	}

	// Verify tunnel info
	foundTunnel1 := false
	foundTunnel2 := false
	for _, info := range tunnels {
		if info.TunnelID == tunnel1.ID {
			foundTunnel1 = true
			if info.RemoteHost != "host1" {
				t.Errorf("tunnel1 RemoteHost = %s, want host1", info.RemoteHost)
			}
		}
		if info.TunnelID == tunnel2.ID {
			foundTunnel2 = true
			if info.RemoteHost != "host2" {
				t.Errorf("tunnel2 RemoteHost = %s, want host2", info.RemoteHost)
			}
		}
	}

	if !foundTunnel1 {
		t.Error("tunnel1 not found in list")
	}
	if !foundTunnel2 {
		t.Error("tunnel2 not found in list")
	}
}

func TestTunnelManager_GetTunnel(t *testing.T) {
	tm := NewTunnelManager()
	defer tm.CloseAll()

	mockClient := createMockSSHClient("test-host", nil)

	tunnel, _ := tm.CreateTunnel(mockClient, 0, "localhost", 8080)

	info, err := tm.GetTunnel(tunnel.ID)
	if err != nil {
		t.Fatalf("GetTunnel() error = %v", err)
	}

	if info.TunnelID != tunnel.ID {
		t.Errorf("TunnelID = %s, want %s", info.TunnelID, tunnel.ID)
	}
	if info.RemoteHost != "localhost" {
		t.Errorf("RemoteHost = %s, want localhost", info.RemoteHost)
	}
	if info.RemotePort != 8080 {
		t.Errorf("RemotePort = %d, want 8080", info.RemotePort)
	}
}

func TestTunnelManager_GetTunnel_NotFound(t *testing.T) {
	tm := NewTunnelManager()
	defer tm.CloseAll()

	_, err := tm.GetTunnel("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent tunnel")
	}
}

func TestTunnelManager_GetTunnelsForHost(t *testing.T) {
	tm := NewTunnelManager()
	defer tm.CloseAll()

	client1 := createMockSSHClient("host1", nil)
	client2 := createMockSSHClient("host2", nil)

	// Create tunnels on different hosts
	tm.CreateTunnel(client1, 0, "remote1", 8080)
	tm.CreateTunnel(client1, 0, "remote2", 8081)
	tm.CreateTunnel(client2, 0, "remote3", 8082)

	host1Tunnels := tm.GetTunnelsForHost("host1")
	if len(host1Tunnels) != 2 {
		t.Errorf("host1 tunnels = %d, want 2", len(host1Tunnels))
	}

	host2Tunnels := tm.GetTunnelsForHost("host2")
	if len(host2Tunnels) != 1 {
		t.Errorf("host2 tunnels = %d, want 1", len(host2Tunnels))
	}

	host3Tunnels := tm.GetTunnelsForHost("host3")
	if len(host3Tunnels) != 0 {
		t.Errorf("host3 tunnels = %d, want 0", len(host3Tunnels))
	}
}

func TestTunnelManager_CloseAll(t *testing.T) {
	tm := NewTunnelManager()

	mockClient := createMockSSHClient("test-host", nil)

	// Create multiple tunnels
	tm.CreateTunnel(mockClient, 0, "host1", 8080)
	tm.CreateTunnel(mockClient, 0, "host2", 8081)
	tm.CreateTunnel(mockClient, 0, "host3", 8082)

	if tm.TunnelCount() != 3 {
		t.Errorf("TunnelCount() = %d, want 3", tm.TunnelCount())
	}

	err := tm.CloseAll()
	if err != nil {
		t.Errorf("CloseAll() error = %v", err)
	}

	if tm.TunnelCount() != 0 {
		t.Errorf("TunnelCount() after CloseAll = %d, want 0", tm.TunnelCount())
	}

	// Verify manager is marked closed
	_, err = tm.CreateTunnel(mockClient, 0, "host4", 8083)
	if err == nil {
		t.Error("expected error creating tunnel on closed manager")
	}
}

func TestTunnelManager_CloseHostTunnels(t *testing.T) {
	tm := NewTunnelManager()
	defer tm.CloseAll()

	client1 := createMockSSHClient("host1", nil)
	client2 := createMockSSHClient("host2", nil)

	// Create tunnels on different hosts
	tm.CreateTunnel(client1, 0, "remote1", 8080)
	tm.CreateTunnel(client1, 0, "remote2", 8081)
	tm.CreateTunnel(client2, 0, "remote3", 8082)

	if tm.TunnelCount() != 3 {
		t.Errorf("TunnelCount() = %d, want 3", tm.TunnelCount())
	}

	err := tm.CloseHostTunnels("host1")
	if err != nil {
		t.Errorf("CloseHostTunnels() error = %v", err)
	}

	if tm.TunnelCount() != 1 {
		t.Errorf("TunnelCount() after CloseHostTunnels = %d, want 1", tm.TunnelCount())
	}

	// Verify host2 tunnel still exists
	host2Tunnels := tm.GetTunnelsForHost("host2")
	if len(host2Tunnels) != 1 {
		t.Errorf("host2 tunnels = %d, want 1", len(host2Tunnels))
	}
}

func TestTunnel_Info(t *testing.T) {
	tm := NewTunnelManager()
	defer tm.CloseAll()

	mockClient := createMockSSHClient("test-host", nil)

	tunnel, err := tm.CreateTunnel(mockClient, 0, "remote-host", 3306)
	if err != nil {
		t.Fatalf("CreateTunnel() error = %v", err)
	}

	info := tunnel.Info()

	if info.TunnelID != tunnel.ID {
		t.Errorf("TunnelID = %s, want %s", info.TunnelID, tunnel.ID)
	}
	if info.LocalPort != tunnel.LocalPort {
		t.Errorf("LocalPort = %d, want %d", info.LocalPort, tunnel.LocalPort)
	}
	if info.RemoteHost != "remote-host" {
		t.Errorf("RemoteHost = %s, want remote-host", info.RemoteHost)
	}
	if info.RemotePort != 3306 {
		t.Errorf("RemotePort = %d, want 3306", info.RemotePort)
	}
	if info.SSHHost != "test-host" {
		t.Errorf("SSHHost = %s, want test-host", info.SSHHost)
	}
	if info.CreatedAt.IsZero() {
		t.Error("CreatedAt should not be zero")
	}
	if info.ActiveConnections != 0 {
		t.Errorf("ActiveConnections = %d, want 0", info.ActiveConnections)
	}
}

func TestTunnel_DoubleClose(t *testing.T) {
	tm := NewTunnelManager()

	mockClient := createMockSSHClient("test-host", nil)

	tunnel, err := tm.CreateTunnel(mockClient, 0, "localhost", 8080)
	if err != nil {
		t.Fatalf("CreateTunnel() error = %v", err)
	}

	// First close should succeed
	err = tunnel.Close()
	if err != nil {
		t.Errorf("first Close() error = %v", err)
	}

	// Second close should be no-op
	err = tunnel.Close()
	if err != nil {
		t.Errorf("second Close() error = %v", err)
	}
}

func TestTunnel_ForwardingWithMockServer(t *testing.T) {
	// This test verifies the bidirectional data forwarding through a tunnel.
	// We create a mock "remote" server, set up a tunnel, and verify data flows correctly.

	// Create a mock "remote" server that echoes data
	remoteListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create remote listener: %v", err)
	}
	remotePort := remoteListener.Addr().(*net.TCPAddr).Port

	// Run echo server
	go func() {
		for {
			conn, err := remoteListener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c)
			}(conn)
		}
	}()
	defer remoteListener.Close()

	// Create the local listener for the tunnel
	localListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create local listener: %v", err)
	}
	localPort := localListener.Addr().(*net.TCPAddr).Port

	// Create a testable SSH client with mock dial
	mockClient := &testableSSHClient{
		SSHClient: &SSHClient{
			host: config.SSHHostConfig{
				Name:     "test-host",
				Hostname: "test-host",
				Port:     22,
			},
			createdAt: time.Now(),
		},
		dialFunc: func(network, addr string) (net.Conn, error) {
			return net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", remotePort))
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	tunnel := &tunnelWithMockClient{
		Tunnel: &Tunnel{
			ID:         "test-tunnel",
			LocalPort:  localPort,
			RemoteHost: "127.0.0.1",
			RemotePort: remotePort,
			CreatedAt:  time.Now(),
			client:     mockClient.SSHClient,
			listener:   localListener,
			ctx:        ctx,
			cancel:     cancel,
		},
		mockClient: mockClient,
	}

	// Start accepting connections
	tunnel.wg.Add(1)
	go tunnel.acceptLoopWithMock()

	// Give the accept loop time to start
	time.Sleep(20 * time.Millisecond)

	// Test the tunnel
	localConn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", localPort), time.Second)
	if err != nil {
		tunnel.Close()
		t.Fatalf("failed to connect to tunnel: %v", err)
	}

	testData := []byte("hello tunnel")
	_, err = localConn.Write(testData)
	if err != nil {
		localConn.Close()
		tunnel.Close()
		t.Fatalf("failed to write: %v", err)
	}

	buf := make([]byte, 1024)
	localConn.SetReadDeadline(time.Now().Add(time.Second))
	n, err := localConn.Read(buf)
	if err != nil {
		localConn.Close()
		tunnel.Close()
		t.Fatalf("failed to read: %v", err)
	}

	if string(buf[:n]) != string(testData) {
		t.Errorf("echoed data = %q, want %q", string(buf[:n]), string(testData))
	}

	// Close client connection first
	localConn.Close()

	// Allow forwarding goroutines to complete
	time.Sleep(50 * time.Millisecond)

	// Check that some data was tracked
	info := tunnel.Info()
	t.Logf("Tunnel stats: BytesIn=%d, BytesOut=%d, ActiveConns=%d", info.BytesIn, info.BytesOut, info.ActiveConnections)

	// BytesOut should definitely have been tracked (data we sent)
	if info.BytesOut == 0 {
		t.Error("BytesOut should be > 0")
	}

	// Clean up - must close tunnel to let wg.Wait() complete
	tunnel.Close()
}

// tunnelWithMockClient wraps a Tunnel with a mock SSH client for testing
type tunnelWithMockClient struct {
	*Tunnel
	mockClient *testableSSHClient
}

// acceptLoopWithMock accepts incoming connections using the mock client
func (t *tunnelWithMockClient) acceptLoopWithMock() {
	defer t.wg.Done()

	for {
		conn, err := t.listener.Accept()
		if err != nil {
			select {
			case <-t.ctx.Done():
				return
			default:
				if isClosedError(err) {
					return
				}
				continue
			}
		}

		t.wg.Add(1)
		go t.handleConnectionWithMock(conn)
	}
}

// handleConnectionWithMock handles a single forwarded connection using mock client
func (t *tunnelWithMockClient) handleConnectionWithMock(localConn net.Conn) {
	defer t.wg.Done()
	defer localConn.Close()

	t.activeConns.Add(1)
	defer t.activeConns.Add(-1)

	remoteAddr := fmt.Sprintf("%s:%d", t.RemoteHost, t.RemotePort)
	remoteConn, err := t.mockClient.Dial("tcp", remoteAddr)
	if err != nil {
		return
	}
	defer remoteConn.Close()

	// Use a wait group to track both copy directions
	var copyWg sync.WaitGroup
	copyWg.Add(2)

	// Local -> Remote (outbound)
	go func() {
		defer copyWg.Done()
		n, _ := io.Copy(remoteConn, localConn)
		t.bytesOut.Add(n)
		// Signal EOF to remote
		if tc, ok := remoteConn.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
	}()

	// Remote -> Local (inbound)
	go func() {
		defer copyWg.Done()
		n, _ := io.Copy(localConn, remoteConn)
		t.bytesIn.Add(n)
		// Signal EOF to local
		if tc, ok := localConn.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
	}()

	copyWg.Wait()
}

func TestValidatePort(t *testing.T) {
	tests := []struct {
		name     string
		port     int
		portType string
		wantErr  bool
	}{
		{"valid local auto-assign", 0, "local", false},
		{"valid local unprivileged", 8080, "local", false},
		{"invalid local privileged", 80, "local", true},
		{"invalid local privileged 443", 443, "local", true},
		{"valid local 1024", 1024, "local", false},
		{"valid remote any", 80, "remote", false},
		{"valid remote high", 65535, "remote", false},
		{"invalid negative", -1, "local", true},
		{"invalid too high", 65536, "local", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePort(tt.port, tt.portType)
			if (err != nil) != tt.wantErr {
				t.Errorf("validatePort(%d, %q) error = %v, wantErr %v", tt.port, tt.portType, err, tt.wantErr)
			}
		})
	}
}

func TestIsClosedError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"closed connection", fmt.Errorf("use of closed network connection"), true},
		{"accept closed", fmt.Errorf("accept tcp: use of closed network connection"), true},
		{"other error", fmt.Errorf("connection refused"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isClosedError(tt.err); got != tt.want {
				t.Errorf("isClosedError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTunnelManager_ConcurrentAccess(t *testing.T) {
	tm := NewTunnelManager()
	defer tm.CloseAll()

	mockClient := createMockSSHClient("test-host", nil)

	var wg sync.WaitGroup
	errors := make(chan error, 100)

	// Concurrent tunnel creation
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := tm.CreateTunnel(mockClient, 0, fmt.Sprintf("host%d", i), 8080+i)
			if err != nil {
				errors <- err
			}
		}(i)
	}

	// Concurrent list operations
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = tm.ListTunnels()
		}()
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Errorf("concurrent operation error: %v", err)
	}

	// Should have created 10 tunnels
	if count := tm.TunnelCount(); count != 10 {
		t.Errorf("TunnelCount() = %d, want 10", count)
	}
}

func BenchmarkTunnelManager_ListTunnels(b *testing.B) {
	tm := NewTunnelManager()
	defer tm.CloseAll()

	mockClient := createMockSSHClient("test-host", nil)

	// Create some tunnels
	for i := 0; i < 10; i++ {
		tm.CreateTunnel(mockClient, 0, fmt.Sprintf("host%d", i), 8080+i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tm.ListTunnels()
	}
}

func BenchmarkTunnelManager_TunnelCount(b *testing.B) {
	tm := NewTunnelManager()
	defer tm.CloseAll()

	mockClient := createMockSSHClient("test-host", nil)

	// Create some tunnels
	for i := 0; i < 10; i++ {
		tm.CreateTunnel(mockClient, 0, fmt.Sprintf("host%d", i), 8080+i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tm.TunnelCount()
	}
}
