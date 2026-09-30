//go:build with_ssh

package ssh

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"conduit/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// Connection hygiene against the in-process server (conduit-uanm).

func hygieneHost(srv *liveSSHServer) config.SSHHostConfig {
	return config.SSHHostConfig{
		Name: "host-a", Hostname: "127.0.0.1", Port: srv.port, User: "tester", IdentityFile: srv.keyPath,
	}
}

// runWithin fails the test if fn has not returned after d, so a hang shows
// up as a failure rather than a stuck test binary.
func runWithin(t *testing.T, d time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("did not return within %v", d)
	}
}

func TestConnect_JumpHostClosedWithTarget(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	srv := newLiveSSHServer(t)
	host := hygieneHost(srv)
	host.JumpHost = "tester@127.0.0.1:" + strconv.Itoa(srv.port) // the server forwards to itself
	// pool.connect_timeout 0 must not mean "no timeout" for the jump dial.
	pool := config.SSHPoolConfig{KnownHostsFile: srv.knownHosts}

	c, err := Connect(host, config.SSHHostDefaults{}, pool)
	require.NoError(t, err)
	res, err := c.Exec("echo via-jump")
	require.NoError(t, err)
	assert.Equal(t, "ran: echo via-jump\n", res.Stdout)
	assert.Equal(t, 2, srv.openConns(), "jump + target connections")

	require.NoError(t, c.Close())
	assert.Eventually(t, func() bool { return srv.openConns() == 0 }, 5*time.Second, 10*time.Millisecond,
		"closing the target client must also close the jump-host client")
}

func TestConnect_JumpHostDialHonoursConnectTimeout(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	srv := newLiveSSHServer(t)

	// A jump host that accepts TCP but never speaks SSH.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var held []net.Conn
	var mu sync.Mutex
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		for _, c := range held {
			_ = c.Close()
		}
		mu.Unlock()
	})

	host := hygieneHost(srv)
	host.JumpHost = "tester@" + ln.Addr().String()
	host.ConnectTimeout = config.Duration(300 * time.Millisecond)
	pool := config.SSHPoolConfig{KnownHostsFile: srv.knownHosts} // connect_timeout 0

	runWithin(t, 5*time.Second, func() {
		_, err = Connect(host, config.SSHHostDefaults{}, pool)
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "jump host")
}

// fakeAgent serves an ssh-agent on a unix socket and counts open client
// connections.
type fakeAgent struct {
	mu   sync.Mutex
	open int
}

func (a *fakeAgent) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.open
}

func startFakeAgent(t *testing.T, keyring agent.Agent) *fakeAgent {
	t.Helper()
	dir, err := os.MkdirTemp("", "agt") // short path: unix sockets cap at ~108 bytes
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "agent.sock"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	t.Setenv("SSH_AUTH_SOCK", ln.Addr().String())

	a := &fakeAgent{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			a.mu.Lock()
			a.open++
			a.mu.Unlock()
			go func() {
				_ = agent.ServeAgent(keyring, c) // returns when the client closes
				_ = c.Close()
				a.mu.Lock()
				a.open--
				a.mu.Unlock()
			}()
		}
	}()
	return a
}

func TestConnect_ClosesAgentSocketAfterHandshake(t *testing.T) {
	srv := newLiveSSHServer(t)

	// Authenticate through the agent only: load the accepted key into it
	// and point identity_file at a missing file.
	raw, err := os.ReadFile(srv.keyPath)
	require.NoError(t, err)
	priv, err := ssh.ParseRawPrivateKey(raw)
	require.NoError(t, err)
	keyring := agent.NewKeyring()
	require.NoError(t, keyring.Add(agent.AddedKey{PrivateKey: priv}))
	fa := startFakeAgent(t, keyring)

	host := hygieneHost(srv)
	host.IdentityFile = filepath.Join(t.TempDir(), "missing")
	c, err := Connect(host, config.SSHHostDefaults{}, config.SSHPoolConfig{KnownHostsFile: srv.knownHosts})
	require.NoError(t, err, "agent auth must still work")
	defer c.Close()

	assert.Eventually(t, func() bool { return fa.count() == 0 }, 5*time.Second, 10*time.Millisecond,
		"the ssh-agent socket must be closed once the handshake is done, not leaked per connection")
	res, err := c.Exec("echo still-open")
	require.NoError(t, err)
	assert.Equal(t, "ran: echo still-open\n", res.Stdout)
}

func newHygienePoolClient(t *testing.T, srv *liveSSHServer) *PoolClient {
	t.Helper()
	pool := NewPool([]config.SSHHostConfig{hygieneHost(srv)}, config.SSHHostDefaults{},
		config.SSHPoolConfig{KnownHostsFile: srv.knownHosts})
	t.Cleanup(pool.Close)
	return NewPoolClient(pool)
}

func TestPoolClient_ExecuteCancelledByContext(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	srv := newLiveSSHServer(t)
	pc := newHygienePoolClient(t, srv)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	var err error
	runWithin(t, 5*time.Second, func() {
		_, err = pc.Execute(ctx, "host-a", "hang", time.Minute)
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Eventually(t, func() bool { return len(srv.gotSignals()) == 1 }, 5*time.Second, 10*time.Millisecond,
		"the remote command must be signalled")
	assert.Equal(t, []string{"KILL"}, srv.gotSignals())

	// The pooled connection is still usable afterwards.
	res, err := pc.Execute(context.Background(), "host-a", "echo after", 5*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "ran: echo after\n", res.Stdout)
}

func TestPoolClient_ExecuteDeadlineAndTimeout(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	srv := newLiveSSHServer(t)
	pc := newHygienePoolClient(t, srv)

	// The turn's deadline applies even when the tool timeout is longer.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	var err error
	runWithin(t, 5*time.Second, func() {
		_, err = pc.Execute(ctx, "host-a", "hang", time.Minute)
	})
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	// The tool timeout still applies on its own.
	runWithin(t, 5*time.Second, func() {
		_, err = pc.Execute(context.Background(), "host-a", "hang", 200*time.Millisecond)
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
}

func TestPoolClient_ExecuteAlreadyCancelledRunsNothing(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	srv := newLiveSSHServer(t)
	pc := newHygienePoolClient(t, srv)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := pc.Execute(ctx, "host-a", "echo never", 5*time.Second)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, srv.ran())
	assert.Equal(t, 0, srv.openConns(), "no connection is dialled for a cancelled turn")
}

func TestFanout_CancelledByContext(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	srv := newLiveSSHServer(t)
	pool := NewPool([]config.SSHHostConfig{hygieneHost(srv)}, config.SSHHostDefaults{},
		config.SSHPoolConfig{KnownHostsFile: srv.knownHosts})
	t.Cleanup(pool.Close)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	var res *FanoutResult
	runWithin(t, 5*time.Second, func() {
		res = NewFanoutExecutor(pool, 2).Execute(ctx, []string{"host-a"}, "hang", time.Minute)
	})
	require.NotNil(t, res.Results["host-a"])
	assert.NotEmpty(t, res.Results["host-a"].Error)
}
