//go:build with_ssh

package ssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"conduit/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Regression tests for H9: accept-new used to fall back to accepting any
// key when known_hosts existed but failed to parse, and dropped errors
// from appending a newly accepted key.

func acceptNewPool(kh string) config.SSHPoolConfig {
	return config.SSHPoolConfig{KnownHostsFile: kh, StrictHostKeyChecking: "accept-new"}
}

func otherHostKey(t *testing.T) gossh.PublicKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := gossh.NewSignerFromKey(priv)
	require.NoError(t, err)
	return signer.PublicKey()
}

func TestAcceptNew_MalformedKnownHostsFailsClosed(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	srv := newLiveSSHServer(t)

	// known_hosts pins srv's address to a DIFFERENT key, plus one bad line.
	kh := filepath.Join(t.TempDir(), "known_hosts")
	line := knownhosts.Line([]string{srv.addr}, otherHostKey(t))
	require.NoError(t, os.WriteFile(kh, []byte(line+"\nthis-is-not-a-valid-line\n"), 0o600))

	c, err := Connect(hygieneHost(srv), config.SSHHostDefaults{}, acceptNewPool(kh))
	if err == nil {
		c.Close()
		t.Fatal("accept-new connected despite known_hosts pinning a different key")
	}
	assert.Contains(t, err.Error(), "known_hosts")

	_, err = buildHostKeyCallback(acceptNewPool(kh))
	assert.Error(t, err, "an unparseable known_hosts must not yield an accept-anything callback")

	// The bad file was left as it was: nothing got appended.
	data, rerr := os.ReadFile(kh)
	require.NoError(t, rerr)
	assert.Equal(t, line+"\nthis-is-not-a-valid-line\n", string(data))
}

func TestAcceptNew_UnreadableKnownHostsFailsClosed(t *testing.T) {
	// A known_hosts path that exists but cannot be read as a file.
	dir := filepath.Join(t.TempDir(), "known_hosts")
	require.NoError(t, os.Mkdir(dir, 0o700))
	_, err := buildHostKeyCallback(acceptNewPool(dir))
	assert.Error(t, err)
}

func TestAcceptNew_PinnedMismatchRefused(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	srv := newLiveSSHServer(t)
	kh := filepath.Join(t.TempDir(), "known_hosts")
	require.NoError(t, os.WriteFile(kh, []byte(knownhosts.Line([]string{srv.addr}, otherHostKey(t))+"\n"), 0o600))

	c, err := Connect(hygieneHost(srv), config.SSHHostDefaults{}, acceptNewPool(kh))
	if err == nil {
		c.Close()
		t.Fatal("accept-new accepted a key that contradicts the pinned one")
	}
}

func TestAcceptNew_NewHostAcceptedAndPersisted(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	srv := newLiveSSHServer(t)

	for name, existing := range map[string]string{
		"no file":         "",
		"file with other": knownhosts.Line([]string{"[192.0.2.10]:22"}, otherHostKey(t)) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			kh := filepath.Join(t.TempDir(), "known_hosts")
			if existing != "" {
				require.NoError(t, os.WriteFile(kh, []byte(existing), 0o600))
			}
			c, err := Connect(hygieneHost(srv), config.SSHHostDefaults{}, acceptNewPool(kh))
			require.NoError(t, err)
			c.Close()

			data, err := os.ReadFile(kh)
			require.NoError(t, err)
			assert.True(t, strings.HasPrefix(string(data), existing))
			assert.Contains(t, string(data), knownhosts.Line([]string{srv.addr}, srv.hostKey.PublicKey()))

			// The persisted key now pins the host, even in strict mode.
			c, err = Connect(hygieneHost(srv), config.SSHHostDefaults{}, config.SSHPoolConfig{KnownHostsFile: kh, StrictHostKeyChecking: "yes"})
			require.NoError(t, err)
			c.Close()
		})
	}
}

func TestAcceptNew_AppendFailureFailsConnection(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	srv := newLiveSSHServer(t)

	// The known_hosts directory cannot be created: its parent is a file.
	parent := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(parent, nil, 0o600))
	kh := filepath.Join(parent, "known_hosts")
	c, err := Connect(hygieneHost(srv), config.SSHHostDefaults{}, acceptNewPool(kh))
	if err == nil {
		c.Close()
		t.Fatal("accept-new connected although the new host key could not be saved")
	}
	assert.Contains(t, err.Error(), "known_hosts")

	t.Run("read-only file", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores file permission bits")
		}
		kh := filepath.Join(t.TempDir(), "known_hosts")
		require.NoError(t, os.WriteFile(kh, []byte(knownhosts.Line([]string{"[192.0.2.10]:22"}, otherHostKey(t))+"\n"), 0o400))
		c, err := Connect(hygieneHost(srv), config.SSHHostDefaults{}, acceptNewPool(kh))
		if err == nil {
			c.Close()
			t.Fatal("accept-new connected although known_hosts is read-only")
		}
	})
}

func TestAcceptNew_WriteErrorIsReturnedAndNotRemembered(t *testing.T) {
	if _, err := os.Stat("/dev/full"); err != nil {
		t.Skip("/dev/full not available")
	}
	// Writes to /dev/full fail with ENOSPC, even for root.
	cb := &acceptNewCallback{knownHostsFile: "/dev/full", known: map[string]gossh.PublicKey{}}
	key := otherHostKey(t)
	addr := &net.TCPAddr{IP: net.IPv4(192, 0, 2, 20), Port: 22}

	err := cb.callback("192.0.2.20:22", addr, key)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "known_hosts")
	assert.Empty(t, cb.known, "a key that could not be saved must not be trusted for the session")
	require.Error(t, cb.callback("192.0.2.20:22", addr, key), "retry must fail again, not hit the session cache")

	// Same through the existing-file path.
	kh := filepath.Join(t.TempDir(), "known_hosts")
	require.NoError(t, os.WriteFile(kh, []byte(knownhosts.Line([]string{"[192.0.2.10]:22"}, otherHostKey(t))+"\n"), 0o600))
	strict, err := knownhosts.New(kh)
	require.NoError(t, err)
	err = cb.withExistingKnownHosts(strict)("192.0.2.20:22", addr, key)
	require.Error(t, err)
}
