// Package ssh implements the SSH remote execution tool with security controls.
package ssh

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"conduit/internal/config"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// SSHClient wraps an SSH connection with session management
type SSHClient struct {
	mu         sync.Mutex
	conn       *ssh.Client
	host       config.SSHHostConfig
	defaults   config.SSHHostDefaults
	poolConfig config.SSHPoolConfig
	createdAt  time.Time
	lastUsedAt time.Time
	closed     bool
}

// ExecResult contains the result of executing a command
type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Connect establishes an SSH connection to the specified host
func Connect(host config.SSHHostConfig, defaults config.SSHHostDefaults, poolConfig config.SSHPoolConfig) (*SSHClient, error) {
	// Get effective connection parameters
	port := host.GetPort(defaults)
	username := host.GetUser(defaults)
	if username == "" {
		// Fall back to current user
		currentUser, err := user.Current()
		if err != nil {
			return nil, fmt.Errorf("failed to get current user: %w", err)
		}
		username = currentUser.Username
	}

	timeout := host.GetConnectTimeout(defaults)
	if poolConfig.ConnectTimeout > 0 {
		timeout = poolConfig.ConnectTimeout
	}

	// Build auth methods
	authMethods, err := buildAuthMethods(host, defaults)
	if err != nil {
		return nil, fmt.Errorf("failed to build auth methods: %w", err)
	}

	if len(authMethods) == 0 {
		return nil, fmt.Errorf("no authentication methods available")
	}

	// Build host key callback
	hostKeyCallback, err := buildHostKeyCallback(poolConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to build host key callback: %w", err)
	}

	// Create SSH client config
	sshConfig := &ssh.ClientConfig{
		User:            username,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
		Timeout:         timeout,
	}

	// Connect through jump host if specified
	var conn *ssh.Client
	if host.JumpHost != "" {
		conn, err = connectViaJumpHost(host, defaults, poolConfig, sshConfig)
	} else {
		addr := fmt.Sprintf("%s:%d", host.Hostname, port)
		conn, err = ssh.Dial("tcp", addr, sshConfig)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to connect to %s: %w", host.Name, err)
	}

	return &SSHClient{
		conn:       conn,
		host:       host,
		defaults:   defaults,
		poolConfig: poolConfig,
		createdAt:  time.Now(),
		lastUsedAt: time.Now(),
	}, nil
}

// connectViaJumpHost establishes a connection through a bastion/jump host
func connectViaJumpHost(host config.SSHHostConfig, defaults config.SSHHostDefaults, poolConfig config.SSHPoolConfig, targetConfig *ssh.ClientConfig) (*ssh.Client, error) {
	// For simplicity, we support a single jump host specification
	// The jump host is expected to be a "user@host:port" string or just "host"
	jumpSpec := host.JumpHost

	jumpUser := ""
	jumpHost := jumpSpec
	jumpPort := 22

	// Parse user@host:port format
	if atIdx := strings.Index(jumpSpec, "@"); atIdx != -1 {
		jumpUser = jumpSpec[:atIdx]
		jumpHost = jumpSpec[atIdx+1:]
	}

	if colonIdx := strings.LastIndex(jumpHost, ":"); colonIdx != -1 {
		portStr := jumpHost[colonIdx+1:]
		jumpHost = jumpHost[:colonIdx]
		if _, err := fmt.Sscanf(portStr, "%d", &jumpPort); err != nil {
			return nil, fmt.Errorf("invalid jump host port: %s", portStr)
		}
	}

	if jumpUser == "" {
		currentUser, err := user.Current()
		if err != nil {
			return nil, fmt.Errorf("failed to get current user for jump host: %w", err)
		}
		jumpUser = currentUser.Username
	}

	// Build auth methods for jump host (use same methods as target for now)
	jumpAuthMethods, err := buildAuthMethods(host, defaults)
	if err != nil {
		return nil, fmt.Errorf("failed to build jump host auth methods: %w", err)
	}

	hostKeyCallback, err := buildHostKeyCallback(poolConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to build jump host key callback: %w", err)
	}

	jumpConfig := &ssh.ClientConfig{
		User:            jumpUser,
		Auth:            jumpAuthMethods,
		HostKeyCallback: hostKeyCallback,
		Timeout:         poolConfig.ConnectTimeout,
	}

	// Connect to jump host
	jumpAddr := fmt.Sprintf("%s:%d", jumpHost, jumpPort)
	jumpConn, err := ssh.Dial("tcp", jumpAddr, jumpConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to jump host %s: %w", jumpAddr, err)
	}

	// Connect to target through jump host
	targetPort := host.GetPort(defaults)
	targetAddr := fmt.Sprintf("%s:%d", host.Hostname, targetPort)

	// Open a connection to the target through the jump host
	netConn, err := jumpConn.Dial("tcp", targetAddr)
	if err != nil {
		jumpConn.Close()
		return nil, fmt.Errorf("failed to dial target through jump host: %w", err)
	}

	// Create SSH connection over the proxied connection
	ncc, chans, reqs, err := ssh.NewClientConn(netConn, targetAddr, targetConfig)
	if err != nil {
		netConn.Close()
		jumpConn.Close()
		return nil, fmt.Errorf("failed to create SSH connection through jump host: %w", err)
	}

	return ssh.NewClient(ncc, chans, reqs), nil
}

// buildAuthMethods creates SSH authentication methods
func buildAuthMethods(host config.SSHHostConfig, defaults config.SSHHostDefaults) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	// Try SSH agent first
	if agentAuth := getAgentAuth(); agentAuth != nil {
		methods = append(methods, agentAuth)
	}

	// Try identity file
	identityFile := host.GetIdentityFile(defaults)
	if identityFile != "" {
		keyAuth, err := getKeyFileAuth(identityFile)
		if err == nil && keyAuth != nil {
			methods = append(methods, keyAuth)
		}
	}

	// Try default identity files if no specific one configured
	if identityFile == "" {
		defaultKeys := []string{
			"~/.ssh/id_ed25519",
			"~/.ssh/id_rsa",
			"~/.ssh/id_ecdsa",
		}
		for _, keyPath := range defaultKeys {
			keyAuth, err := getKeyFileAuth(keyPath)
			if err == nil && keyAuth != nil {
				methods = append(methods, keyAuth)
				break // Use first available key
			}
		}
	}

	return methods, nil
}

// getAgentAuth returns an SSH agent authentication method if available
func getAgentAuth() ssh.AuthMethod {
	socket := os.Getenv("SSH_AUTH_SOCK")
	if socket == "" {
		return nil
	}

	conn, err := net.Dial("unix", socket)
	if err != nil {
		return nil
	}

	agentClient := agent.NewClient(conn)
	return ssh.PublicKeysCallback(agentClient.Signers)
}

// getKeyFileAuth returns an SSH key file authentication method
func getKeyFileAuth(keyPath string) (ssh.AuthMethod, error) {
	// Expand ~ to home directory
	if strings.HasPrefix(keyPath, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		keyPath = filepath.Join(home, keyPath[1:])
	}

	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}

	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		// Key might be encrypted, but we don't support passphrase prompting
		return nil, err
	}

	return ssh.PublicKeys(signer), nil
}

// buildHostKeyCallback creates a host key verification callback
func buildHostKeyCallback(poolConfig config.SSHPoolConfig) (ssh.HostKeyCallback, error) {
	mode := poolConfig.StrictHostKeyChecking
	if mode == "" {
		mode = "yes"
	}

	switch mode {
	case "no":
		// WARNING: Insecure - accepts any host key
		return ssh.InsecureIgnoreHostKey(), nil

	case "accept-new":
		// Accept new keys and add them to known_hosts
		return newAcceptNewCallback(poolConfig.KnownHostsFile)

	case "yes":
		// Strict verification against known_hosts
		return newStrictCallback(poolConfig.KnownHostsFile)

	default:
		return nil, fmt.Errorf("invalid strict_host_key_checking value: %s", mode)
	}
}

// newStrictCallback creates a strict known_hosts verification callback
func newStrictCallback(knownHostsFile string) (ssh.HostKeyCallback, error) {
	if knownHostsFile == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		knownHostsFile = filepath.Join(home, ".ssh", "known_hosts")
	}

	// Expand ~ if present
	if strings.HasPrefix(knownHostsFile, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		knownHostsFile = filepath.Join(home, knownHostsFile[1:])
	}

	// Check if file exists
	if _, err := os.Stat(knownHostsFile); os.IsNotExist(err) {
		return nil, fmt.Errorf("known_hosts file not found: %s", knownHostsFile)
	}

	callback, err := knownhosts.New(knownHostsFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load known_hosts: %w", err)
	}

	return callback, nil
}

// acceptNewCallback is a host key callback that accepts new keys
type acceptNewCallback struct {
	knownHostsFile string
	mu             sync.Mutex
	known          map[string]ssh.PublicKey
}

// newAcceptNewCallback creates an accept-new host key callback
func newAcceptNewCallback(knownHostsFile string) (ssh.HostKeyCallback, error) {
	if knownHostsFile == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		knownHostsFile = filepath.Join(home, ".ssh", "known_hosts")
	}

	// Expand ~ if present
	if strings.HasPrefix(knownHostsFile, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		knownHostsFile = filepath.Join(home, knownHostsFile[1:])
	}

	cb := &acceptNewCallback{
		knownHostsFile: knownHostsFile,
		known:          make(map[string]ssh.PublicKey),
	}

	// Try to load existing known hosts
	if _, err := os.Stat(knownHostsFile); err == nil {
		// File exists, try to use strict callback for existing keys
		if strictCb, err := knownhosts.New(knownHostsFile); err == nil {
			// Wrap the strict callback
			return cb.withExistingKnownHosts(strictCb), nil
		}
	}

	return cb.callback, nil
}

// withExistingKnownHosts wraps an existing known_hosts callback
func (c *acceptNewCallback) withExistingKnownHosts(existing ssh.HostKeyCallback) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		// First try the existing known_hosts
		err := existing(hostname, remote, key)
		if err == nil {
			return nil
		}

		// If key is unknown (not a mismatch), accept and save it
		if keyErr, ok := err.(*knownhosts.KeyError); ok {
			if len(keyErr.Want) == 0 {
				// Host not in known_hosts, accept and add it
				return c.acceptAndSave(hostname, remote, key)
			}
			// Key mismatch - reject
			return err
		}

		return err
	}
}

// callback is the host key callback for when there's no existing known_hosts
func (c *acceptNewCallback) callback(hostname string, remote net.Addr, key ssh.PublicKey) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Check if we've seen this host before in this session
	hostKey := hostname
	if existingKey, ok := c.known[hostKey]; ok {
		if !bytes.Equal(existingKey.Marshal(), key.Marshal()) {
			return fmt.Errorf("host key mismatch for %s", hostname)
		}
		return nil
	}

	// Accept and save the new key
	return c.acceptAndSave(hostname, remote, key)
}

// acceptAndSave accepts a new host key and saves it to known_hosts
func (c *acceptNewCallback) acceptAndSave(hostname string, remote net.Addr, key ssh.PublicKey) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Remember this key for this session
	c.known[hostname] = key

	// Try to save to known_hosts file
	if err := c.appendToKnownHosts(hostname, key); err != nil {
		// Log but don't fail - we still accept the key
		// In production, you'd want proper logging here
		_ = err
	}

	return nil
}

// appendToKnownHosts adds a host key to the known_hosts file
func (c *acceptNewCallback) appendToKnownHosts(hostname string, key ssh.PublicKey) error {
	// Ensure directory exists
	dir := filepath.Dir(c.knownHostsFile)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create .ssh directory: %w", err)
	}

	// Format the known_hosts line
	line := knownhosts.Line([]string{hostname}, key)

	// Append to file
	f, err := os.OpenFile(c.knownHostsFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("failed to open known_hosts: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(line + "\n"); err != nil {
		return fmt.Errorf("failed to write to known_hosts: %w", err)
	}

	return nil
}

// Exec executes a command on the remote host
func (c *SSHClient) Exec(cmd string) (*ExecResult, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("client is closed")
	}
	c.lastUsedAt = time.Now()
	c.mu.Unlock()

	session, err := c.conn.NewSession()
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	err = session.Run(cmd)

	result := &ExecResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: 0,
	}

	if err != nil {
		if exitErr, ok := err.(*ssh.ExitError); ok {
			result.ExitCode = exitErr.ExitStatus()
		} else {
			return result, fmt.Errorf("command execution failed: %w", err)
		}
	}

	return result, nil
}

// ExecWithTimeout executes a command with a timeout
func (c *SSHClient) ExecWithTimeout(cmd string, timeout time.Duration) (*ExecResult, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("client is closed")
	}
	c.lastUsedAt = time.Now()
	c.mu.Unlock()

	session, err := c.conn.NewSession()
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	// Start the command
	if err := session.Start(cmd); err != nil {
		return nil, fmt.Errorf("failed to start command: %w", err)
	}

	// Wait with timeout
	done := make(chan error, 1)
	go func() {
		done <- session.Wait()
	}()

	var waitErr error
	select {
	case waitErr = <-done:
		// Command completed
	case <-time.After(timeout):
		// Timeout - try to close the session
		session.Signal(ssh.SIGKILL)
		return nil, fmt.Errorf("command timed out after %v", timeout)
	}

	result := &ExecResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: 0,
	}

	if waitErr != nil {
		if exitErr, ok := waitErr.(*ssh.ExitError); ok {
			result.ExitCode = exitErr.ExitStatus()
		} else {
			return result, fmt.Errorf("command execution failed: %w", waitErr)
		}
	}

	return result, nil
}

// ExecStreaming executes a command and streams output to writers
func (c *SSHClient) ExecStreaming(cmd string, stdout, stderr io.Writer) (int, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return -1, fmt.Errorf("client is closed")
	}
	c.lastUsedAt = time.Now()
	c.mu.Unlock()

	session, err := c.conn.NewSession()
	if err != nil {
		return -1, fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	session.Stdout = stdout
	session.Stderr = stderr

	err = session.Run(cmd)

	if err != nil {
		if exitErr, ok := err.(*ssh.ExitError); ok {
			return exitErr.ExitStatus(), nil
		}
		return -1, fmt.Errorf("command execution failed: %w", err)
	}

	return 0, nil
}

// Close closes the SSH connection
func (c *SSHClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}

	c.closed = true
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// IsHealthy checks if the connection is still usable
func (c *SSHClient) IsHealthy() bool {
	c.mu.Lock()
	if c.closed || c.conn == nil {
		c.mu.Unlock()
		return false
	}
	c.mu.Unlock()

	// Try to create a session as a health check
	session, err := c.conn.NewSession()
	if err != nil {
		return false
	}
	session.Close()
	return true
}

// Host returns the host configuration
func (c *SSHClient) Host() config.SSHHostConfig {
	return c.host
}

// CreatedAt returns when the client was created
func (c *SSHClient) CreatedAt() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.createdAt
}

// LastUsedAt returns when the client was last used
func (c *SSHClient) LastUsedAt() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastUsedAt
}

// IsClosed returns whether the client is closed
func (c *SSHClient) IsClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// Dial opens a connection to the given address via the SSH connection
// This is used for port forwarding/tunneling
func (c *SSHClient) Dial(network, addr string) (net.Conn, error) {
	c.mu.Lock()
	if c.closed || c.conn == nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("client is closed")
	}
	c.lastUsedAt = time.Now()
	c.mu.Unlock()

	return c.conn.Dial(network, addr)
}
