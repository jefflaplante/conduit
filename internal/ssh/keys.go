package ssh

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"conduit/internal/datadir"

	charmssh "github.com/charmbracelet/ssh"
	gossh "golang.org/x/crypto/ssh"
)

// DataDirConfig holds the optional config value for the data directory.
// Set this before calling SSH key functions so the config-level
// data_dir override is respected.
var DataDirConfig string

// KeyEntry represents an authorized public key with metadata
type KeyEntry struct {
	PublicKey   charmssh.PublicKey
	Comment     string
	Fingerprint string
}

// sshConfigDir returns the conduit data directory for SSH keys.
func sshConfigDir() (string, error) {
	return datadir.Resolve(DataDirConfig)
}

// defaultAuthorizedKeysPath returns the default path for authorized_keys
func defaultAuthorizedKeysPath() string {
	dir, err := sshConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "authorized_keys")
}

// LoadAuthorizedKeys loads SSH public keys from an authorized_keys file
func LoadAuthorizedKeys(path string) ([]charmssh.PublicKey, error) {
	if path == "" {
		path = defaultAuthorizedKeysPath()
	}
	if path == "" {
		return nil, fmt.Errorf("no authorized keys path available")
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open authorized keys: %w", err)
	}
	defer f.Close()

	var keys []charmssh.PublicKey
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		pubKey, _, _, _, err := gossh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			continue // skip invalid lines
		}
		keys = append(keys, pubKey)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading authorized keys: %w", err)
	}

	return keys, nil
}

// AddAuthorizedKey appends a public key to the authorized_keys file
func AddAuthorizedKey(path string, keyData string) error {
	if path == "" {
		path = defaultAuthorizedKeysPath()
	}
	if path == "" {
		return fmt.Errorf("no authorized keys path available")
	}

	// Validate the key
	_, _, _, _, err := gossh.ParseAuthorizedKey([]byte(strings.TrimSpace(keyData)))
	if err != nil {
		return fmt.Errorf("invalid public key: %w", err)
	}

	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("failed to open authorized keys: %w", err)
	}
	defer f.Close()

	line := strings.TrimSpace(keyData) + "\n"
	if _, err := f.WriteString(line); err != nil {
		return fmt.Errorf("failed to write key: %w", err)
	}

	return nil
}

// ListAuthorizedKeys returns all authorized keys with fingerprints
func ListAuthorizedKeys(path string) ([]KeyEntry, error) {
	if path == "" {
		path = defaultAuthorizedKeysPath()
	}
	if path == "" {
		return nil, fmt.Errorf("no authorized keys path available")
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open authorized keys: %w", err)
	}
	defer f.Close()

	var entries []KeyEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		pubKey, comment, _, _, err := gossh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			continue
		}

		entries = append(entries, KeyEntry{
			PublicKey:   pubKey,
			Comment:     comment,
			Fingerprint: gossh.FingerprintSHA256(pubKey),
		})
	}

	return entries, scanner.Err()
}

// RemoveAuthorizedKey removes a key by fingerprint from the authorized_keys file
func RemoveAuthorizedKey(path string, fingerprint string) error {
	if path == "" {
		path = defaultAuthorizedKeysPath()
	}
	if path == "" {
		return fmt.Errorf("no authorized keys path available")
	}

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("failed to open authorized keys: %w", err)
	}

	var lines []string
	found := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			lines = append(lines, line)
			continue
		}

		pubKey, _, _, _, err := gossh.ParseAuthorizedKey([]byte(trimmed))
		if err != nil {
			lines = append(lines, line)
			continue
		}

		if gossh.FingerprintSHA256(pubKey) == fingerprint {
			found = true
			continue // skip this line (remove it)
		}

		lines = append(lines, line)
	}

	f.Close()

	if !found {
		return fmt.Errorf("key with fingerprint %s not found", fingerprint)
	}

	// Write back
	content := strings.Join(lines, "\n")
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return os.WriteFile(path, []byte(content), 0600)
}

// InitSSHKeys creates the host key and empty authorized_keys file
func InitSSHKeys(hostKeyPath, authorizedKeysPath string) error {
	if hostKeyPath == "" || authorizedKeysPath == "" {
		dir, err := sshConfigDir()
		if err != nil {
			return err
		}
		if hostKeyPath == "" {
			hostKeyPath = filepath.Join(dir, "ssh_host_key")
		}
		if authorizedKeysPath == "" {
			authorizedKeysPath = filepath.Join(dir, "authorized_keys")
		}
	}

	// Create authorized_keys file if it doesn't exist
	if _, err := os.Stat(authorizedKeysPath); os.IsNotExist(err) {
		dir := filepath.Dir(authorizedKeysPath)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("failed to create directory: %w", err)
		}
		if err := os.WriteFile(authorizedKeysPath, []byte("# Conduit authorized SSH keys\n"), 0600); err != nil {
			return fmt.Errorf("failed to create authorized_keys: %w", err)
		}
		fmt.Printf("Created: %s\n", authorizedKeysPath)
	} else {
		fmt.Printf("Exists: %s\n", authorizedKeysPath)
	}

	// Host key is auto-generated by Wish when the server starts
	fmt.Printf("Host key path: %s (auto-generated on first SSH server start)\n", hostKeyPath)

	return nil
}
