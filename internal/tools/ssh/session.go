//go:build with_ssh

// Package ssh implements the SSH remote execution tool with security controls.
package ssh

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
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
	ID           string    `json:"id"`
	Host         string    `json:"host"`
	CreatedAt    time.Time `json:"created_at"`
	LastUsedAt   time.Time `json:"last_used_at"`
	CommandCount int       `json:"command_count"`
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
	mu          sync.RWMutex
	sessions    map[string]*PersistentSession
	maxSessions int
	// maxPerHost caps open sessions on any one host (conduit-1kxf).
	maxPerHost int
	// pending counts slots reserved by StartSession calls that are still
	// connecting, so concurrent starts cannot overshoot either cap while
	// the (slow) connect runs without the lock held.
	pending      map[string]int
	pendingTotal int
	closed       bool
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

	maxPerHost := cfg.MaxSessionsPerHost
	if maxPerHost <= 0 {
		maxPerHost = defaultMaxSessionsPerHost
	}

	idleTimeout := cfg.SessionIdleTimeout.Duration()
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
		maxPerHost:  maxPerHost,
		pending:     make(map[string]int),
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

// defaultMaxSessionsPerHost applies when sessions.max_sessions_per_host is
// unset (conduit-1kxf).
const defaultMaxSessionsPerHost = 2

// SessionLimitError reports that a session cap is reached. Scope is
// "global" (sessions.max_concurrent_sessions) or "host"
// (sessions.max_sessions_per_host); Open lists the IDs of the sessions
// counting against that cap so the caller can reuse or close one.
type SessionLimitError struct {
	Scope string
	Host  string
	Limit int
	Open  []string
}

func (e *SessionLimitError) Error() string {
	if e.Scope == "host" {
		return fmt.Sprintf("maximum persistent sessions on host %s reached (%d, sessions.max_sessions_per_host)", e.Host, e.Limit)
	}
	return fmt.Sprintf("maximum concurrent sessions reached (%d, sessions.max_concurrent_sessions)", e.Limit)
}

// StartSession starts a new persistent session on the specified host. Both
// caps are checked and a slot reserved before connecting; the connection
// itself is made without holding the manager lock.
func (sm *SessionManager) StartSession(hostName string) (string, error) {
	hostConfig, err := sm.reserve(hostName)
	if err != nil {
		return "", err
	}

	ps, err := sm.open(hostName, hostConfig)

	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.release(hostName)
	if err != nil {
		return "", err
	}
	if sm.closed {
		_ = ps.close()
		return "", fmt.Errorf("session manager closed while connecting to %s", hostName)
	}
	sm.sessions[ps.id] = ps
	return ps.id, nil
}

// reserve validates hostName and claims a slot under both caps.
func (sm *SessionManager) reserve(hostName string) (config.SSHHostConfig, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.closed {
		return config.SSHHostConfig{}, fmt.Errorf("session manager is closed")
	}

	// Look up host configuration
	hostConfig, ok := sm.hosts[hostName]
	if !ok {
		return config.SSHHostConfig{}, fmt.Errorf("unknown host: %s", hostName)
	}

	// Check if host is enabled
	if !hostConfig.IsHostEnabled() {
		return config.SSHHostConfig{}, fmt.Errorf("host %s is disabled", hostName)
	}

	if len(sm.sessions)+sm.pendingTotal >= sm.maxSessions {
		return config.SSHHostConfig{}, &SessionLimitError{Scope: "global", Limit: sm.maxSessions, Open: sm.sessionIDsLocked("")}
	}
	onHost := sm.sessionIDsLocked(hostName)
	if len(onHost)+sm.pending[hostName] >= sm.maxPerHost {
		return config.SSHHostConfig{}, &SessionLimitError{Scope: "host", Host: hostName, Limit: sm.maxPerHost, Open: onHost}
	}

	sm.pending[hostName]++
	sm.pendingTotal++
	return hostConfig, nil
}

// release returns a slot claimed by reserve. Callers hold sm.mu.
func (sm *SessionManager) release(hostName string) {
	sm.pending[hostName]--
	if sm.pending[hostName] <= 0 {
		delete(sm.pending, hostName)
	}
	sm.pendingTotal--
}

// open connects to the host and starts the persistent shell.
func (sm *SessionManager) open(hostName string, hostConfig config.SSHHostConfig) (*PersistentSession, error) {
	client, err := Connect(hostConfig, sm.defaults, sm.poolConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to %s: %w", hostName, err)
	}
	ps, err := sm.createPersistentSession(hostName, client)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("failed to create persistent session: %w", err)
	}
	return ps, nil
}

// sessionIDsLocked returns the sorted IDs of open sessions, on hostName
// only when it is non-empty. Callers hold sm.mu.
func (sm *SessionManager) sessionIDsLocked(hostName string) []string {
	ids := make([]string, 0, len(sm.sessions))
	for id, ps := range sm.sessions {
		if hostName == "" || ps.host == hostName {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// MaxSessions returns the global cap on open sessions.
func (sm *SessionManager) MaxSessions() int { return sm.maxSessions }

// MaxSessionsPerHost returns the per-host cap on open sessions.
func (sm *SessionManager) MaxSessionsPerHost() int { return sm.maxPerHost }

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

	// Close all sessions; a StartSession still connecting closes its
	// session instead of registering it.
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.closed = true

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
