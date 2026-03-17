package ssh

import (
	"fmt"
	"sync"
	"time"

	"conduit/internal/config"
)

// Pool manages a pool of SSH connections for reuse
type Pool struct {
	mu          sync.Mutex
	connections map[string][]*pooledClient // host name -> connections
	defaults    config.SSHHostDefaults
	poolConfig  config.SSHPoolConfig
	hosts       map[string]config.SSHHostConfig // host name -> config
	totalConns  int
	closed      bool
	cleanupDone chan struct{}
	cleanupOnce sync.Once
}

// pooledClient wraps an SSHClient with pool metadata
type pooledClient struct {
	client     *SSHClient
	inUse      bool
	returnedAt time.Time
}

// NewPool creates a new connection pool
func NewPool(hosts []config.SSHHostConfig, defaults config.SSHHostDefaults, poolConfig config.SSHPoolConfig) *Pool {
	// Apply defaults
	if poolConfig.MaxConnectionsPerHost == 0 {
		poolConfig.MaxConnectionsPerHost = 5
	}
	if poolConfig.MaxTotalConnections == 0 {
		poolConfig.MaxTotalConnections = 50
	}
	if poolConfig.IdleTimeout == 0 {
		poolConfig.IdleTimeout = 5 * time.Minute
	}
	if poolConfig.HealthCheckInterval == 0 {
		poolConfig.HealthCheckInterval = 1 * time.Minute
	}

	// Build host lookup map
	hostMap := make(map[string]config.SSHHostConfig)
	for _, host := range hosts {
		hostMap[host.Name] = host
	}

	p := &Pool{
		connections: make(map[string][]*pooledClient),
		defaults:    defaults,
		poolConfig:  poolConfig,
		hosts:       hostMap,
		cleanupDone: make(chan struct{}),
	}

	// Start cleanup goroutine
	go p.cleanupLoop()

	return p
}

// Get retrieves or creates a connection to the specified host
func (p *Pool) Get(hostName string) (*SSHClient, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil, fmt.Errorf("pool is closed")
	}

	// Look up host configuration
	hostConfig, ok := p.hosts[hostName]
	if !ok {
		return nil, fmt.Errorf("unknown host: %s", hostName)
	}

	// Check if host is enabled
	if !hostConfig.IsHostEnabled() {
		return nil, fmt.Errorf("host %s is disabled", hostName)
	}

	// Look for an available connection in the pool
	if conns, ok := p.connections[hostName]; ok {
		for _, pc := range conns {
			if !pc.inUse && !pc.client.IsClosed() {
				// Found an available connection, check health
				if pc.client.IsHealthy() {
					pc.inUse = true
					return pc.client, nil
				}
				// Unhealthy connection, close it
				pc.client.Close()
				p.removeConnection(hostName, pc)
			}
		}
	}

	// No available connection, create a new one
	return p.createConnection(hostName, hostConfig)
}

// createConnection creates a new connection (must be called with lock held)
func (p *Pool) createConnection(hostName string, hostConfig config.SSHHostConfig) (*SSHClient, error) {
	// Check if we're at capacity for this host
	if len(p.connections[hostName]) >= p.poolConfig.MaxConnectionsPerHost {
		return nil, fmt.Errorf("max connections per host reached for %s", hostName)
	}

	// Check total capacity
	if p.totalConns >= p.poolConfig.MaxTotalConnections {
		return nil, fmt.Errorf("max total connections reached")
	}

	// Release lock while connecting (can be slow)
	p.mu.Unlock()
	client, err := Connect(hostConfig, p.defaults, p.poolConfig)
	p.mu.Lock()

	if err != nil {
		return nil, err
	}

	// Double-check we're not closed while we were connecting
	if p.closed {
		client.Close()
		return nil, fmt.Errorf("pool closed during connection")
	}

	// Add to pool
	pc := &pooledClient{
		client: client,
		inUse:  true,
	}
	p.connections[hostName] = append(p.connections[hostName], pc)
	p.totalConns++

	return client, nil
}

// Put returns a connection to the pool
func (p *Pool) Put(hostName string, client *SSHClient) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		client.Close()
		return
	}

	// Find the pooled client
	conns := p.connections[hostName]
	for _, pc := range conns {
		if pc.client == client {
			if client.IsClosed() {
				// Connection is closed, remove it
				p.removeConnection(hostName, pc)
			} else {
				// Return to pool
				pc.inUse = false
				pc.returnedAt = time.Now()
			}
			return
		}
	}

	// Client wasn't from our pool, just close it
	client.Close()
}

// removeConnection removes a connection from the pool (must be called with lock held)
func (p *Pool) removeConnection(hostName string, pc *pooledClient) {
	conns := p.connections[hostName]
	for i, c := range conns {
		if c == pc {
			p.connections[hostName] = append(conns[:i], conns[i+1:]...)
			p.totalConns--
			break
		}
	}
}

// Close shuts down the pool and all connections
func (p *Pool) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.mu.Unlock()

	// Signal cleanup goroutine to stop
	p.cleanupOnce.Do(func() {
		close(p.cleanupDone)
	})

	// Close all connections
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, conns := range p.connections {
		for _, pc := range conns {
			pc.client.Close()
		}
	}
	p.connections = make(map[string][]*pooledClient)
	p.totalConns = 0
}

// cleanupLoop periodically cleans up idle and unhealthy connections
func (p *Pool) cleanupLoop() {
	ticker := time.NewTicker(p.poolConfig.HealthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-p.cleanupDone:
			return
		case <-ticker.C:
			p.cleanup()
		}
	}
}

// cleanup removes idle and unhealthy connections
func (p *Pool) cleanup() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return
	}

	now := time.Now()

	for hostName, conns := range p.connections {
		var toRemove []*pooledClient

		for _, pc := range conns {
			// Skip connections in use
			if pc.inUse {
				continue
			}

			// Check idle timeout
			if now.Sub(pc.returnedAt) > p.poolConfig.IdleTimeout {
				toRemove = append(toRemove, pc)
				continue
			}

			// Check health
			if !pc.client.IsHealthy() {
				toRemove = append(toRemove, pc)
				continue
			}
		}

		// Remove stale connections
		for _, pc := range toRemove {
			pc.client.Close()
			p.removeConnection(hostName, pc)
		}
	}
}

// Stats returns current pool statistics
func (p *Pool) Stats() PoolStats {
	p.mu.Lock()
	defer p.mu.Unlock()

	stats := PoolStats{
		TotalConnections: p.totalConns,
		HostStats:        make(map[string]HostPoolStats),
	}

	for hostName, conns := range p.connections {
		hostStats := HostPoolStats{
			Total: len(conns),
		}
		for _, pc := range conns {
			if pc.inUse {
				hostStats.InUse++
			} else {
				hostStats.Available++
			}
		}
		stats.HostStats[hostName] = hostStats
	}

	return stats
}

// PoolStats contains pool statistics
type PoolStats struct {
	TotalConnections int
	HostStats        map[string]HostPoolStats
}

// HostPoolStats contains per-host pool statistics
type HostPoolStats struct {
	Total     int
	InUse     int
	Available int
}

// AddHost dynamically adds a host configuration to the pool
func (p *Pool) AddHost(host config.SSHHostConfig) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return fmt.Errorf("pool is closed")
	}

	if _, exists := p.hosts[host.Name]; exists {
		return fmt.Errorf("host %s already exists", host.Name)
	}

	p.hosts[host.Name] = host
	return nil
}

// RemoveHost removes a host and closes all its connections
func (p *Pool) RemoveHost(hostName string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return fmt.Errorf("pool is closed")
	}

	if _, exists := p.hosts[hostName]; !exists {
		return fmt.Errorf("host %s not found", hostName)
	}

	// Close all connections for this host
	if conns, ok := p.connections[hostName]; ok {
		for _, pc := range conns {
			pc.client.Close()
		}
		p.totalConns -= len(conns)
		delete(p.connections, hostName)
	}

	delete(p.hosts, hostName)
	return nil
}

// GetHostConfig returns the configuration for a host
func (p *Pool) GetHostConfig(hostName string) (config.SSHHostConfig, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	host, ok := p.hosts[hostName]
	return host, ok
}

// ListHosts returns the names of all configured hosts
func (p *Pool) ListHosts() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	hosts := make([]string, 0, len(p.hosts))
	for name := range p.hosts {
		hosts = append(hosts, name)
	}
	return hosts
}

// Exec is a convenience method that gets a connection, executes a command, and returns it
func (p *Pool) Exec(hostName string, cmd string) (*ExecResult, error) {
	client, err := p.Get(hostName)
	if err != nil {
		return nil, fmt.Errorf("failed to get connection: %w", err)
	}
	defer p.Put(hostName, client)

	return client.Exec(cmd)
}

// ExecWithTimeout is a convenience method that executes a command with a timeout
func (p *Pool) ExecWithTimeout(hostName string, cmd string, timeout time.Duration) (*ExecResult, error) {
	client, err := p.Get(hostName)
	if err != nil {
		return nil, fmt.Errorf("failed to get connection: %w", err)
	}
	defer p.Put(hostName, client)

	return client.ExecWithTimeout(cmd, timeout)
}
