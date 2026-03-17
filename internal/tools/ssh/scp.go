// Package ssh implements the SSH remote execution tool with security controls.
package ssh

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// SCPClient provides SCP file transfer operations over SSH
type SCPClient struct {
	sshClient *SSHClient
}

// NewSCPClient creates a new SCP client wrapping an SSH connection
func NewSCPClient(client *SSHClient) *SCPClient {
	return &SCPClient{
		sshClient: client,
	}
}

// Upload uploads a local file to the remote host
func (s *SCPClient) Upload(localPath, remotePath string, mode os.FileMode) error {
	// Read the local file
	data, err := os.ReadFile(localPath)
	if err != nil {
		return fmt.Errorf("failed to read local file: %w", err)
	}

	// Get file info for the filename
	info, err := os.Stat(localPath)
	if err != nil {
		return fmt.Errorf("failed to stat local file: %w", err)
	}

	// Use the actual mode from the file if mode is 0
	if mode == 0 {
		mode = info.Mode()
	}

	return s.UploadBytes(data, remotePath, mode)
}

// UploadBytes uploads data as a file to the remote host
func (s *SCPClient) UploadBytes(data []byte, remotePath string, mode os.FileMode) error {
	// Start SCP in sink mode (receive files)
	session, err := s.sshClient.conn.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	// Set up pipes for SCP protocol communication
	stdin, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdin pipe: %w", err)
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdout pipe: %w", err)
	}

	// Extract filename from remote path
	filename := filepath.Base(remotePath)

	// Get directory portion for the scp command
	remoteDir := filepath.Dir(remotePath)
	if remoteDir == "." {
		remoteDir = filename
	} else {
		remoteDir = remotePath
	}

	// Start scp in sink mode (-t = to remote)
	// The remote path is the target directory or file
	if err := session.Start(fmt.Sprintf("scp -t %s", remoteDir)); err != nil {
		return fmt.Errorf("failed to start scp: %w", err)
	}

	// Read initial acknowledgment
	if err := checkSCPResponse(stdout); err != nil {
		return fmt.Errorf("initial scp handshake failed: %w", err)
	}

	// Send file header: C<mode> <size> <filename>\n
	// Mode is octal format (e.g., 0644)
	modeOctal := fmt.Sprintf("%04o", mode&0777)
	header := fmt.Sprintf("C%s %d %s\n", modeOctal, len(data), filename)
	if _, err := stdin.Write([]byte(header)); err != nil {
		return fmt.Errorf("failed to send file header: %w", err)
	}

	// Read acknowledgment
	if err := checkSCPResponse(stdout); err != nil {
		return fmt.Errorf("header acknowledgment failed: %w", err)
	}

	// Send file data
	if _, err := stdin.Write(data); err != nil {
		return fmt.Errorf("failed to send file data: %w", err)
	}

	// Send end-of-data marker (null byte)
	if _, err := stdin.Write([]byte{0}); err != nil {
		return fmt.Errorf("failed to send end marker: %w", err)
	}

	// Read final acknowledgment
	if err := checkSCPResponse(stdout); err != nil {
		return fmt.Errorf("final acknowledgment failed: %w", err)
	}

	// Close stdin to signal end of transmission
	stdin.Close()

	// Wait for session to complete
	if err := session.Wait(); err != nil {
		return fmt.Errorf("scp session failed: %w", err)
	}

	return nil
}

// Download downloads a file from the remote host to a local path
func (s *SCPClient) Download(remotePath, localPath string) error {
	// Validate local path to prevent writing outside workspace
	if err := s.validateLocalPath(localPath); err != nil {
		return fmt.Errorf("invalid local path: %w", err)
	}

	// Start SCP in source mode (send files)
	session, err := s.sshClient.conn.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	// Set up pipes
	stdin, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdin pipe: %w", err)
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdout pipe: %w", err)
	}

	// Start scp in source mode (-f = from remote)
	if err := session.Start(fmt.Sprintf("scp -f %s", remotePath)); err != nil {
		return fmt.Errorf("failed to start scp: %w", err)
	}

	// Send initial acknowledgment (null byte)
	if _, err := stdin.Write([]byte{0}); err != nil {
		return fmt.Errorf("failed to send initial ack: %w", err)
	}

	// Read file header: C<mode> <size> <filename>\n
	header, err := readSCPLine(stdout)
	if err != nil {
		return fmt.Errorf("failed to read file header: %w", err)
	}

	// Parse header
	mode, size, filename, err := parseSCPHeader(header)
	if err != nil {
		return fmt.Errorf("failed to parse header: %w", err)
	}

	// Send acknowledgment
	if _, err := stdin.Write([]byte{0}); err != nil {
		return fmt.Errorf("failed to send header ack: %w", err)
	}

	// Read file data
	data := make([]byte, size)
	if _, err := io.ReadFull(stdout, data); err != nil {
		return fmt.Errorf("failed to read file data: %w", err)
	}

	// Read end-of-data marker (null byte)
	endMarker := make([]byte, 1)
	if _, err := stdout.Read(endMarker); err != nil {
		return fmt.Errorf("failed to read end marker: %w", err)
	}
	if endMarker[0] != 0 {
		return fmt.Errorf("unexpected end marker: %d", endMarker[0])
	}

	// Send final acknowledgment
	if _, err := stdin.Write([]byte{0}); err != nil {
		return fmt.Errorf("failed to send final ack: %w", err)
	}

	// Ensure the local directory exists
	localDir := filepath.Dir(localPath)
	if err := os.MkdirAll(localDir, 0755); err != nil {
		return fmt.Errorf("failed to create local directory: %w", err)
	}

	// Write file to local path
	if err := os.WriteFile(localPath, data, mode); err != nil {
		return fmt.Errorf("failed to write local file: %w", err)
	}

	// Close stdin to signal end
	stdin.Close()

	// Wait for session to complete
	if err := session.Wait(); err != nil {
		return fmt.Errorf("scp session failed: %w", err)
	}

	// Log the downloaded filename for reference
	_ = filename

	return nil
}

// validateLocalPath ensures the local path is safe for writing
// This prevents path traversal attacks and writing outside the workspace
func (s *SCPClient) validateLocalPath(localPath string) error {
	// Convert to absolute path
	absPath, err := filepath.Abs(localPath)
	if err != nil {
		return fmt.Errorf("failed to get absolute path: %w", err)
	}

	// Clean the path to remove .. and . components
	absPath = filepath.Clean(absPath)

	// Check for suspicious patterns
	if strings.Contains(absPath, "..") {
		return fmt.Errorf("path contains '..' after cleaning: %s", absPath)
	}

	// Ensure the path doesn't start with /etc, /bin, /sbin, etc.
	// (system directories that should never be written to via SCP download)
	systemDirs := []string{
		"/etc",
		"/bin",
		"/sbin",
		"/usr/bin",
		"/usr/sbin",
		"/boot",
		"/sys",
		"/proc",
		"/dev",
	}

	for _, dir := range systemDirs {
		if strings.HasPrefix(absPath, dir+"/") || absPath == dir {
			return fmt.Errorf("writing to system directory is not allowed: %s", dir)
		}
	}

	return nil
}

// checkSCPResponse reads and validates an SCP protocol response
func checkSCPResponse(r io.Reader) error {
	buf := make([]byte, 1)
	_, err := r.Read(buf)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	switch buf[0] {
	case 0:
		// Success
		return nil
	case 1:
		// Warning - read the message
		msg, _ := readSCPLine(r)
		return fmt.Errorf("scp warning: %s", msg)
	case 2:
		// Error - read the message
		msg, _ := readSCPLine(r)
		return fmt.Errorf("scp error: %s", msg)
	default:
		return fmt.Errorf("unexpected scp response code: %d", buf[0])
	}
}

// readSCPLine reads a line from the SCP stream (terminated by \n)
func readSCPLine(r io.Reader) (string, error) {
	var buf bytes.Buffer
	oneByte := make([]byte, 1)

	for {
		n, err := r.Read(oneByte)
		if err != nil {
			return "", err
		}
		if n == 0 {
			break
		}
		if oneByte[0] == '\n' {
			break
		}
		buf.WriteByte(oneByte[0])
	}

	return buf.String(), nil
}

// parseSCPHeader parses an SCP file header line
// Format: C<mode> <size> <filename>\n
func parseSCPHeader(header string) (os.FileMode, int64, string, error) {
	if !strings.HasPrefix(header, "C") {
		return 0, 0, "", fmt.Errorf("invalid header format: %s", header)
	}

	parts := strings.SplitN(header[1:], " ", 3)
	if len(parts) != 3 {
		return 0, 0, "", fmt.Errorf("invalid header parts: %s", header)
	}

	// Parse mode (octal)
	modeInt, err := strconv.ParseInt(parts[0], 8, 32)
	if err != nil {
		return 0, 0, "", fmt.Errorf("invalid mode: %w", err)
	}
	mode := os.FileMode(modeInt)

	// Parse size
	size, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, 0, "", fmt.Errorf("invalid size: %w", err)
	}

	// Filename is the third part
	filename := parts[2]

	return mode, size, filename, nil
}
