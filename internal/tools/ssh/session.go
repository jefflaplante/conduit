// Package ssh implements the SSH remote execution tool with security controls.
package ssh

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"conduit/internal/config"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

// SessionOutput represents the output of a command executed in a persistent session
type SessionOutput struct {
	Stdout   string        `json:"stdout"`
	Stderr   string        `json:"stderr"`
	ExitCode int           `json:"exit_code"`
	Duration time.Duration `json:"duration"`
}

// SessionInfo provides information about an active session
type SessionInfo struct {
	ID         string    `json:"id"`
	Host       string    `json:"host"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at"`
	CommandCount int     `json:"command_count"`
}

// PersistentSession wraps an SSH client with stdin/stdout/stderr pipes
// to keep a shell session alive between commands
type PersistentSession struct {
	mu           sync.Mutex
	id           string
	host         string
	client       *SSHClient
	session      *ssh.Session
	stdin        io.WriteCloser
	stdout       io.Reader
	stderr       io.Reader
	stdoutBuf    *bytes.Buffer
	stderrBuf    *bytes.Buffer
	createdAt    time.Time
	lastUsedAt   time.Time
	commandCount int
	closed       bool
	marker       string
	shell        string
}

// SessionManager manages persistent SSH sessions with lifecycle control
type SessionManager struct {
	mu           sync.RWMutex
	sessions     map[string]*PersistentSession
	maxSessions  int
	idleTimeout  time.Duration
	marker       string
	shell        string
	defaults     config.SSHHostDefaults
	poolConfig   config.SSHPoolConfig
	hosts        map[string]config.SSHHostConfig
	cleanupDone  chan struct{}
	cleanupOnce  sync.Once
}

// NewSessionManager creates a new session manager
func NewSessionManager(cfg config.SSHSessionConfig, hosts []config.SSHHostConfig, defaults config.SSHHostDefaults, poolConfig config.SSHPoolConfig) *SessionManager {
	// Apply defaults
	maxSessions := cfg.MaxConcurrentSessions
	if maxSessions <= 0 {
		maxSessions = 5
	}

	idleTimeout := cfg.SessionIdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = 10 * time.Minute
	}

	marker := cfg.OutputBoundaryMarker
	if marker == "" {
		marker = "___CONDUIT_OUTPUT_BOUNDARY___"
	}

	shell := cfg.DefaultShell
	if shell == "" {
		shell = "/bin/sh"
	}

	// Build host lookup map
	hostMap := make(map[string]config.SSHHostConfig)
	for _, host := range hosts {
		hostMap[host.Name] = host
	}

	sm := &SessionManager{
		sessions:    make(map[string]*PersistentSession),
		maxSessions: maxSessions,
		idleTimeout: idleTimeout,
		marker:      marker,
		shell:       shell,
		defaults:    defaults,
		poolConfig:  poolConfig,
		hosts:       hostMap,
		cleanupDone: make(chan struct{}),
	}

	// Start cleanup goroutine
	go sm.cleanupLoop()

	return sm
}

// StartSession starts a new persistent session on the specified host
func (sm *SessionManager) StartSession(hostName string) (string, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Check session limit
	if len(sm.sessions) >= sm.maxSessions {
		return "", fmt.Errorf("maximum concurrent sessions reached (%d)", sm.maxSessions)
	}

	// Look up host configuration
	hostConfig, ok := sm.hosts[hostName]
	if !ok {
		return "", fmt.Errorf("unknown host: %s", hostName)
	}

	// Check if host is enabled
	if !hostConfig.IsHostEnabled() {
		return "", fmt.Errorf("host %s is disabled", hostName)
	}

	// Connect to the host
	client, err := Connect(hostConfig, sm.defaults, sm.poolConfig)
	if err != nil {
		return "", fmt.Errorf("failed to connect to %s: %w", hostName, err)
	}

	// Create persistent session
	ps, err := sm.createPersistentSession(hostName, client)
	if err != nil {
		client.Close()
		return "", fmt.Errorf("failed to create persistent session: %w", err)
	}

	sm.sessions[ps.id] = ps
	return ps.id, nil
}

// createPersistentSession creates a new persistent session with the given client
func (sm *SessionManager) createPersistentSession(hostName string, client *SSHClient) (*PersistentSession, error) {
	// Create a new SSH session
	session, err := client.conn.NewSession()
	if err != nil {
		return nil, fmt.Errorf("failed to create SSH session: %w", err)
	}

	// Set up pipes
	stdin, err := session.StdinPipe()
	if err != nil {
		session.Close()
		return nil, fmt.Errorf("failed to create stdin pipe: %w", err)
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		session.Close()
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	stderr, err := session.StderrPipe()
	if err != nil {
		session.Close()
		return nil, fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	// Request a PTY for better shell compatibility
	modes := ssh.TerminalModes{
		ssh.ECHO:          0,     // disable echoing
		ssh.TTY_OP_ISPEED: 14400, // input speed = 14.4kbaud
		ssh.TTY_OP_OSPEED: 14400, // output speed = 14.4kbaud
	}
	if err := session.RequestPty("xterm", 80, 40, modes); err != nil {
		// PTY not required, continue without it
		_ = err
	}

	// Start the shell
	if err := session.Shell(); err != nil {
		session.Close()
		return nil, fmt.Errorf("failed to start shell: %w", err)
	}

	sessionID := uuid.New().String()[:8]
	now := time.Now()

	ps := &PersistentSession{
		id:         sessionID,
		host:       hostName,
		client:     client,
		session:    session,
		stdin:      stdin,
		stdout:     stdout,
		stderr:     stderr,
		stdoutBuf:  &bytes.Buffer{},
		stderrBuf:  &bytes.Buffer{},
		createdAt:  now,
		lastUsedAt: now,
		marker:     sm.marker,
		shell:      sm.shell,
	}

	// Start background readers
	go ps.readOutput()

	return ps, nil
}

// readOutput continuously reads from stdout and stderr into buffers
func (ps *PersistentSession) readOutput() {
	var wg sync.WaitGroup
	wg.Add(2)

	// Read stdout
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(ps.stdout)
		for scanner.Scan() {
			ps.mu.Lock()
			if ps.closed {
				ps.mu.Unlock()
				return
			}
			ps.stdoutBuf.WriteString(scanner.Text())
			ps.stdoutBuf.WriteString("\n")
			ps.mu.Unlock()
		}
	}()

	// Read stderr
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(ps.stderr)
		for scanner.Scan() {
			ps.mu.Lock()
			if ps.closed {
				ps.mu.Unlock()
				return
			}
			ps.stderrBuf.WriteString(scanner.Text())
			ps.stderrBuf.WriteString("\n")
			ps.mu.Unlock()
		}
	}()

	wg.Wait()
}

// SendCommand sends a command to an existing session and returns the output
func (sm *SessionManager) SendCommand(sessionID, command string, timeout time.Duration) (*SessionOutput, error) {
	sm.mu.RLock()
	ps, ok := sm.sessions[sessionID]
	sm.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}

	return ps.execute(command, timeout)
}

// execute runs a command in the persistent session using boundary markers
func (ps *PersistentSession) execute(command string, timeout time.Duration) (*SessionOutput, error) {
	ps.mu.Lock()
	if ps.closed {
		ps.mu.Unlock()
		return nil, fmt.Errorf("session is closed")
	}

	ps.lastUsedAt = time.Now()
	ps.commandCount++

	// Clear buffers
	ps.stdoutBuf.Reset()
	ps.stderrBuf.Reset()
	ps.mu.Unlock()

	// Generate unique boundary markers for this command
	cmdUUID := uuid.New().String()[:8]
	startMarker := fmt.Sprintf("---START-%s-%s---", ps.marker, cmdUUID)
	endMarkerPrefix := fmt.Sprintf("---END-%s-%s---", ps.marker, cmdUUID)

	// Build the command with boundary markers
	// The format captures exit code in the end marker
	wrappedCmd := fmt.Sprintf(
		"echo '%s'; %s; __exit_code=$?; echo '%s'\"$__exit_code\"\n",
		startMarker, command, endMarkerPrefix,
	)

	startTime := time.Now()

	// Send the command
	ps.mu.Lock()
	_, err := ps.stdin.Write([]byte(wrappedCmd))
	ps.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("failed to send command: %w", err)
	}

	// Wait for output with timeout
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	output, err := ps.waitForOutput(ctx, startMarker, endMarkerPrefix)
	if err != nil {
		return nil, err
	}

	output.Duration = time.Since(startTime)
	return output, nil
}

// waitForOutput waits for the command output between boundary markers
func (ps *PersistentSession) waitForOutput(ctx context.Context, startMarker, endMarkerPrefix string) (*SessionOutput, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	var foundStart bool
	var outputLines []string

	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("command timed out")
		case <-ticker.C:
			ps.mu.Lock()
			stdout := ps.stdoutBuf.String()
			stderr := ps.stderrBuf.String()
			ps.mu.Unlock()

			// Parse the output
			lines := strings.Split(stdout, "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)

				// Look for start marker
				if !foundStart {
					if strings.Contains(line, startMarker) {
						foundStart = true
					}
					continue
				}

				// Look for end marker with exit code
				if strings.HasPrefix(line, endMarkerPrefix) {
					// Extract exit code
					exitCodeStr := strings.TrimPrefix(line, endMarkerPrefix)
					exitCode := 0
					if exitCodeStr != "" {
						fmt.Sscanf(exitCodeStr, "%d", &exitCode)
					}

					return &SessionOutput{
						Stdout:   strings.Join(outputLines, "\n"),
						Stderr:   strings.TrimSpace(stderr),
						ExitCode: exitCode,
					}, nil
				}

				// Accumulate output lines
				outputLines = append(outputLines, line)
			}
		}
	}
}

// CloseSession closes and removes a specific session
func (sm *SessionManager) CloseSession(sessionID string) error {
	sm.mu.Lock()
	ps, ok := sm.sessions[sessionID]
	if !ok {
		sm.mu.Unlock()
		return fmt.Errorf("session not found: %s", sessionID)
	}
	delete(sm.sessions, sessionID)
	sm.mu.Unlock()

	return ps.close()
}

// close closes the persistent session and its underlying connections
func (ps *PersistentSession) close() error {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if ps.closed {
		return nil
	}

	ps.closed = true

	// Close stdin to signal end of input
	if ps.stdin != nil {
		ps.stdin.Close()
	}

	// Close the SSH session
	if ps.session != nil {
		ps.session.Close()
	}

	// Close the SSH client
	if ps.client != nil {
		ps.client.Close()
	}

	return nil
}

// ListSessions returns information about all active sessions
func (sm *SessionManager) ListSessions() []*SessionInfo {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	sessions := make([]*SessionInfo, 0, len(sm.sessions))
	for _, ps := range sm.sessions {
		ps.mu.Lock()
		info := &SessionInfo{
			ID:           ps.id,
			Host:         ps.host,
			CreatedAt:    ps.createdAt,
			LastUsedAt:   ps.lastUsedAt,
			CommandCount: ps.commandCount,
		}
		ps.mu.Unlock()
		sessions = append(sessions, info)
	}

	return sessions
}

// GetSession returns information about a specific session
func (sm *SessionManager) GetSession(sessionID string) (*SessionInfo, error) {
	sm.mu.RLock()
	ps, ok := sm.sessions[sessionID]
	sm.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}

	ps.mu.Lock()
	info := &SessionInfo{
		ID:           ps.id,
		Host:         ps.host,
		CreatedAt:    ps.createdAt,
		LastUsedAt:   ps.lastUsedAt,
		CommandCount: ps.commandCount,
	}
	ps.mu.Unlock()

	return info, nil
}

// SessionCount returns the number of active sessions
func (sm *SessionManager) SessionCount() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return len(sm.sessions)
}

// cleanupLoop periodically cleans up idle sessions
func (sm *SessionManager) cleanupLoop() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-sm.cleanupDone:
			return
		case <-ticker.C:
			sm.cleanupIdleSessions()
		}
	}
}

// cleanupIdleSessions removes sessions that have been idle too long
func (sm *SessionManager) cleanupIdleSessions() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	now := time.Now()
	var toRemove []string

	for id, ps := range sm.sessions {
		ps.mu.Lock()
		idle := now.Sub(ps.lastUsedAt) > sm.idleTimeout
		ps.mu.Unlock()

		if idle {
			toRemove = append(toRemove, id)
		}
	}

	for _, id := range toRemove {
		if ps, ok := sm.sessions[id]; ok {
			ps.close()
			delete(sm.sessions, id)
		}
	}
}

// Close shuts down the session manager and all sessions
func (sm *SessionManager) Close() {
	// Signal cleanup goroutine to stop
	sm.cleanupOnce.Do(func() {
		close(sm.cleanupDone)
	})

	// Close all sessions
	sm.mu.Lock()
	defer sm.mu.Unlock()

	for id, ps := range sm.sessions {
		ps.close()
		delete(sm.sessions, id)
	}
}

// AddHost dynamically adds a host configuration
func (sm *SessionManager) AddHost(host config.SSHHostConfig) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if _, exists := sm.hosts[host.Name]; exists {
		return fmt.Errorf("host %s already exists", host.Name)
	}

	sm.hosts[host.Name] = host
	return nil
}

// GetHostConfig returns the configuration for a host
func (sm *SessionManager) GetHostConfig(hostName string) (config.SSHHostConfig, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	host, ok := sm.hosts[hostName]
	return host, ok
}

// HasSession checks if a session exists
func (sm *SessionManager) HasSession(sessionID string) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	_, ok := sm.sessions[sessionID]
	return ok
}
