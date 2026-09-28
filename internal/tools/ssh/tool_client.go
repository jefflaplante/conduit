//go:build with_ssh

package ssh

import (
	"context"
	"fmt"
	"time"
)

// ExecutionResult represents the result of an SSH command execution
type ExecutionResult struct {
	Host     string        `json:"host"`
	Command  string        `json:"command"`
	ExitCode int           `json:"exit_code"`
	Stdout   string        `json:"stdout"`
	Stderr   string        `json:"stderr"`
	Duration time.Duration `json:"duration"`
	Error    string        `json:"error,omitempty"`
	TimedOut bool          `json:"timed_out,omitempty"`
}

// PoolStatus represents the status of the SSH connection pool
type PoolStatus struct {
	TotalConnections  int            `json:"total_connections"`
	ActiveConnections int            `json:"active_connections"`
	IdleConnections   int            `json:"idle_connections"`
	HostStats         map[string]int `json:"host_stats"`
}

// Client defines the interface for SSH client operations.
// This will be implemented by the actual SSH client/pool.
type Client interface {
	// Execute runs a command on the specified host
	Execute(ctx context.Context, host, command string, timeout time.Duration) (*ExecutionResult, error)

	// GetPoolStatus returns the current connection pool status
	GetPoolStatus() *PoolStatus

	// Close closes all connections in the pool
	Close() error
}

// getSSHClientForHost gets or creates an SSHClient for a host
// This is a helper that bridges between the Client interface and the actual SSHClient needed for tunnels
func (t *SSHTool) getSSHClientForHost(hostName string) (*SSHClient, error) {
	// If the client is a PoolClient, we can get the underlying SSHClient
	if poolClient, ok := t.client.(*PoolClient); ok {
		return poolClient.GetClient(hostName)
	}

	// For other client types, we need to check if they can provide an SSHClient
	if clientProvider, ok := t.client.(SSHClientProvider); ok {
		return clientProvider.GetSSHClient(hostName)
	}

	return nil, fmt.Errorf("client does not support tunneling - requires SSHClient access")
}

// SSHClientProvider is an interface for clients that can provide underlying SSHClient instances
type SSHClientProvider interface {
	GetSSHClient(hostName string) (*SSHClient, error)
}

// PoolClient wraps a Pool to implement the Client interface
type PoolClient struct {
	pool *Pool
}

// NewPoolClient creates a new PoolClient wrapping a Pool
func NewPoolClient(pool *Pool) *PoolClient {
	return &PoolClient{pool: pool}
}

// Execute runs a command on the specified host
func (p *PoolClient) Execute(ctx context.Context, host, command string, timeout time.Duration) (*ExecutionResult, error) {
	result, err := p.pool.ExecWithTimeout(host, command, timeout)
	if err != nil {
		return nil, err
	}

	return &ExecutionResult{
		Host:     host,
		Command:  command,
		ExitCode: result.ExitCode,
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
		Duration: timeout, // Approximate - actual duration tracked in ExecResult
	}, nil
}

// GetPoolStatus returns the current connection pool status
func (p *PoolClient) GetPoolStatus() *PoolStatus {
	stats := p.pool.Stats()

	hostStats := make(map[string]int)
	for host, hs := range stats.HostStats {
		hostStats[host] = hs.Total
	}

	active := 0
	idle := 0
	for _, hs := range stats.HostStats {
		active += hs.InUse
		idle += hs.Available
	}

	return &PoolStatus{
		TotalConnections:  stats.TotalConnections,
		ActiveConnections: active,
		IdleConnections:   idle,
		HostStats:         hostStats,
	}
}

// Close closes all connections in the pool
func (p *PoolClient) Close() error {
	p.pool.Close()
	return nil
}

// GetClient gets an SSHClient from the pool for the specified host
func (p *PoolClient) GetClient(hostName string) (*SSHClient, error) {
	return p.pool.Get(hostName)
}
