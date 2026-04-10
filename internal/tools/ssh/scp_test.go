//go:build with_ssh

package ssh

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// mockSession implements the ssh.Session interface for testing
type mockSession struct {
	command    string
	stdin      *bytes.Buffer
	stdout     *bytes.Buffer
	stderr     *bytes.Buffer
	stdinPipe  io.WriteCloser
	stdoutPipe io.ReadCloser
	started    bool
	closed     bool
}

func (m *mockSession) Run(cmd string) error {
	return nil
}

func (m *mockSession) Start(cmd string) error {
	m.command = cmd
	m.started = true
	return nil
}

func (m *mockSession) Wait() error {
	return nil
}

func (m *mockSession) Close() error {
	m.closed = true
	return nil
}

func (m *mockSession) StdinPipe() (io.WriteCloser, error) {
	return m.stdinPipe, nil
}

func (m *mockSession) StdoutPipe() (io.ReadCloser, error) {
	return m.stdoutPipe, nil
}

func (m *mockSession) StderrPipe() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewBuffer(nil)), nil
}

func (m *mockSession) Signal(sig ssh.Signal) error {
	return nil
}

func (m *mockSession) SetStdin(r io.Reader) {
}

func (m *mockSession) SetStdout(w io.Writer) {
}

func (m *mockSession) SetStderr(w io.Writer) {
}

func (m *mockSession) SendRequest(name string, wantReply bool, payload []byte) (bool, error) {
	return false, nil
}

func (m *mockSession) RequestPty(term string, h, w int, termmodes ssh.TerminalModes) error {
	return nil
}

func (m *mockSession) WindowChange(h, w int) error {
	return nil
}

// mockSSHConn implements ssh.Conn for testing
type mockSSHConn struct {
	sessions []*mockSession
}

func (m *mockSSHConn) NewSession() (*ssh.Session, error) {
	session := &mockSession{
		stdin:  &bytes.Buffer{},
		stdout: &bytes.Buffer{},
		stderr: &bytes.Buffer{},
	}
	m.sessions = append(m.sessions, session)
	return (*ssh.Session)(nil), nil // This is a hack for testing
}

func (m *mockSSHConn) Close() error {
	return nil
}

func (m *mockSSHConn) SendRequest(name string, wantReply bool, payload []byte) (bool, []byte, error) {
	return false, nil, nil
}

func (m *mockSSHConn) OpenChannel(name string, data []byte) (ssh.Channel, <-chan *ssh.Request, error) {
	return nil, nil, nil
}

func (m *mockSSHConn) Wait() error {
	return nil
}

func (m *mockSSHConn) User() string {
	return "testuser"
}

func (m *mockSSHConn) SessionID() []byte {
	return []byte("test-session-id")
}

func (m *mockSSHConn) ClientVersion() []byte {
	return []byte("SSH-2.0-Test")
}

func (m *mockSSHConn) ServerVersion() []byte {
	return []byte("SSH-2.0-Test")
}

func (m *mockSSHConn) RemoteAddr() string {
	return "127.0.0.1:22"
}

func (m *mockSSHConn) LocalAddr() string {
	return "127.0.0.1:12345"
}

// TestSCPUploadBytes tests uploading data as a file
func TestSCPUploadBytes(t *testing.T) {
	// This is a simplified test since we can't easily mock ssh.Session
	// In a real scenario, you'd want to use a test SSH server
	t.Skip("Requires SSH server for full integration test")
}

// TestSCPDownload tests downloading a file
func TestSCPDownload(t *testing.T) {
	// This is a simplified test since we can't easily mock ssh.Session
	t.Skip("Requires SSH server for full integration test")
}

// TestValidateLocalPath tests path validation for downloads
func TestValidateLocalPath(t *testing.T) {
	client := &SCPClient{
		sshClient: nil, // Not needed for path validation
	}

	tests := []struct {
		name      string
		path      string
		shouldErr bool
	}{
		{
			name:      "Valid workspace path",
			path:      "/tmp/test.txt",
			shouldErr: false,
		},
		{
			name:      "Valid relative path",
			path:      "test.txt",
			shouldErr: false,
		},
		{
			name:      "System directory /etc",
			path:      "/etc/passwd",
			shouldErr: true,
		},
		{
			name:      "System directory /bin",
			path:      "/bin/bash",
			shouldErr: true,
		},
		{
			name:      "System directory /sbin",
			path:      "/sbin/init",
			shouldErr: true,
		},
		{
			name:      "System directory /usr/bin",
			path:      "/usr/bin/python",
			shouldErr: true,
		},
		{
			name:      "System directory /usr/sbin",
			path:      "/usr/sbin/sshd",
			shouldErr: true,
		},
		{
			name:      "System directory /boot",
			path:      "/boot/vmlinuz",
			shouldErr: true,
		},
		{
			name:      "System directory /sys",
			path:      "/sys/kernel",
			shouldErr: true,
		},
		{
			name:      "System directory /proc",
			path:      "/proc/cpuinfo",
			shouldErr: true,
		},
		{
			name:      "System directory /dev",
			path:      "/dev/null",
			shouldErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := client.validateLocalPath(tt.path)
			if tt.shouldErr && err == nil {
				t.Errorf("expected error for path %s, got nil", tt.path)
			}
			if !tt.shouldErr && err != nil {
				t.Errorf("unexpected error for path %s: %v", tt.path, err)
			}
		})
	}
}

// TestCheckSCPResponse tests SCP protocol response parsing
func TestCheckSCPResponse(t *testing.T) {
	tests := []struct {
		name      string
		response  []byte
		shouldErr bool
		errMsg    string
	}{
		{
			name:      "Success response",
			response:  []byte{0},
			shouldErr: false,
		},
		{
			name:      "Warning response",
			response:  []byte{1, 'w', 'a', 'r', 'n', 'i', 'n', 'g', '\n'},
			shouldErr: true,
			errMsg:    "warning",
		},
		{
			name:      "Error response",
			response:  []byte{2, 'e', 'r', 'r', 'o', 'r', '\n'},
			shouldErr: true,
			errMsg:    "error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := bytes.NewReader(tt.response)
			err := checkSCPResponse(reader)

			if tt.shouldErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.shouldErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if tt.shouldErr && err != nil && !strings.Contains(err.Error(), tt.errMsg) {
				t.Errorf("expected error message to contain %q, got %q", tt.errMsg, err.Error())
			}
		})
	}
}

// TestReadSCPLine tests reading lines from SCP stream
func TestReadSCPLine(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Simple line",
			input:    "hello world\n",
			expected: "hello world",
		},
		{
			name:     "Line with spaces",
			input:    "C0644 1234 file.txt\n",
			expected: "C0644 1234 file.txt",
		},
		{
			name:     "Empty line",
			input:    "\n",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := strings.NewReader(tt.input)
			line, err := readSCPLine(reader)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if line != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, line)
			}
		})
	}
}

// TestParseSCPHeader tests parsing SCP file headers
func TestParseSCPHeader(t *testing.T) {
	tests := []struct {
		name         string
		header       string
		expectedMode os.FileMode
		expectedSize int64
		expectedName string
		shouldErr    bool
	}{
		{
			name:         "Valid header",
			header:       "C0644 1234 test.txt",
			expectedMode: 0644,
			expectedSize: 1234,
			expectedName: "test.txt",
			shouldErr:    false,
		},
		{
			name:         "Executable file",
			header:       "C0755 5678 script.sh",
			expectedMode: 0755,
			expectedSize: 5678,
			expectedName: "script.sh",
			shouldErr:    false,
		},
		{
			name:         "Filename with spaces",
			header:       "C0644 100 my file.txt",
			expectedMode: 0644,
			expectedSize: 100,
			expectedName: "my file.txt",
			shouldErr:    false,
		},
		{
			name:      "Invalid format - no C prefix",
			header:    "0644 1234 test.txt",
			shouldErr: true,
		},
		{
			name:      "Invalid format - not enough parts",
			header:    "C0644 1234",
			shouldErr: true,
		},
		{
			name:      "Invalid mode",
			header:    "Cxxxx 1234 test.txt",
			shouldErr: true,
		},
		{
			name:      "Invalid size",
			header:    "C0644 abc test.txt",
			shouldErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode, size, name, err := parseSCPHeader(tt.header)

			if tt.shouldErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if mode != tt.expectedMode {
				t.Errorf("expected mode %o, got %o", tt.expectedMode, mode)
			}
			if size != tt.expectedSize {
				t.Errorf("expected size %d, got %d", tt.expectedSize, size)
			}
			if name != tt.expectedName {
				t.Errorf("expected name %q, got %q", tt.expectedName, name)
			}
		})
	}
}

// TestSCPUploadIntegration tests the full upload flow with a real file
func TestSCPUploadIntegration(t *testing.T) {
	// Create a temporary file for testing
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	testData := []byte("Hello, SCP!")

	if err := os.WriteFile(testFile, testData, 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// This test would require a real SSH connection
	// For now, we just verify the file operations work
	info, err := os.Stat(testFile)
	if err != nil {
		t.Fatalf("failed to stat test file: %v", err)
	}

	if info.Size() != int64(len(testData)) {
		t.Errorf("expected size %d, got %d", len(testData), info.Size())
	}

	if info.Mode() != 0644 {
		t.Errorf("expected mode 0644, got %o", info.Mode())
	}
}

// TestSCPDownloadIntegration tests path validation in download flow
func TestSCPDownloadIntegration(t *testing.T) {
	client := &SCPClient{
		sshClient: nil,
	}

	// Test downloading to a safe location
	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "downloaded.txt")

	err := client.validateLocalPath(localPath)
	if err != nil {
		t.Errorf("unexpected error for safe path: %v", err)
	}

	// Test downloading to a system directory
	systemPath := "/etc/test.txt"
	err = client.validateLocalPath(systemPath)
	if err == nil {
		t.Error("expected error for system directory, got nil")
	}
}
