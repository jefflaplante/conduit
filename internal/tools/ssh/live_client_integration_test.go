//go:build with_ssh

package ssh

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"conduit/internal/approval"
	"conduit/internal/config"
	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// End-to-end tests of the SSH tool as the gateway registers it
// (newRegisteredTool: real PoolClient, sandbox) against an in-process SSH
// server on loopback, with a real approval.Manager (conduit-enf0).

type liveHarness struct {
	srv     *liveSSHServer
	tool    *SSHTool
	mgr     *approval.Manager
	origin  approval.Origin
	sandbox string

	mu      sync.Mutex
	notices []approval.Notice
}

// newLiveHarness builds the registered tool against a fresh server. Hosts:
// "box" (no tier cap, group "fleet"), "ro-box" (read cap), "mod-box"
// (modify cap), all the same server. mutate may adjust the config first.
func newLiveHarness(t *testing.T, mutate func(*config.Config)) *liveHarness {
	t.Helper()
	t.Setenv("SSH_AUTH_SOCK", "") // hermetic: only the test key authenticates

	h := &liveHarness{srv: newLiveSSHServer(t), sandbox: t.TempDir()}
	host := func(name, tier string, groups ...string) config.SSHHostConfig {
		return config.SSHHostConfig{
			Name: name, Hostname: "127.0.0.1", Port: h.srv.port, User: "tester",
			IdentityFile: h.srv.keyPath, SecurityTier: tier, Groups: groups,
		}
	}
	def := config.DefaultRemoteSSHConfig()
	cfg := &config.Config{}
	cfg.RemoteSSH = config.RemoteSSHConfig{
		Enabled:  true,
		Hosts:    []config.SSHHostConfig{host("box", "", "fleet"), host("ro-box", "read"), host("mod-box", "modify")},
		Security: def.Security,
		Pool:     def.Pool,
		Sessions: def.Sessions,
		Defaults: def.Defaults,
	}
	cfg.RemoteSSH.Pool.KnownHostsFile = h.srv.knownHosts
	cfg.RemoteSSH.Pool.ConnectTimeout = config.Duration(5 * time.Second)
	cfg.Tools.Sandbox = config.SandboxConfig{WorkspaceDir: h.sandbox}
	if mutate != nil {
		mutate(cfg)
	}

	h.mgr = approval.NewManager(approval.Config{})
	t.Cleanup(h.mgr.Close)
	tool, err := newRegisteredTool(&types.ToolServices{Approvals: h.mgr}, cfg)
	require.NoError(t, err)
	t.Cleanup(tool.Close)
	_, isPool := tool.client.(*PoolClient)
	require.True(t, isPool, "registered tool must use the pool-backed client")
	h.tool = tool

	h.origin = approval.Origin{
		Source: "telegram", ChannelID: "telegram", UserID: "owner", SessionKey: "s1",
		Notify: func(_ context.Context, n approval.Notice) error {
			h.mu.Lock()
			h.notices = append(h.notices, n)
			h.mu.Unlock()
			return nil
		},
	}
	return h
}

func (h *liveHarness) interactive() context.Context {
	return approval.WithInteractiveOrigin(context.Background(), h.origin)
}

func (h *liveHarness) exec(t *testing.T, ctx context.Context, args map[string]interface{}) *types.ToolResult {
	t.Helper()
	res, err := h.tool.Execute(ctx, args)
	require.NoError(t, err)
	return res
}

// approveLatest replies "YES <code>" to the most recent prompt and waits
// for the approved action to finish.
func (h *liveHarness) approveLatest(t *testing.T) {
	t.Helper()
	code := h.latestCode(t)
	h.mgr.HandleReply(context.Background(), approval.Inbound{
		ChannelID: h.origin.ChannelID, UserID: h.origin.UserID, SessionKey: h.origin.SessionKey,
		Text: "YES " + code, Notify: h.origin.Notify,
	})
	h.mgr.Wait()
}

func (h *liveHarness) latestCode(t *testing.T) string {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := len(h.notices) - 1; i >= 0; i-- {
		if m := sshCodeRe.FindStringSubmatch(h.notices[i].Text); m != nil {
			return m[1]
		}
	}
	t.Fatal("no approval prompt was delivered")
	return ""
}

func (h *liveHarness) lastNotice() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.notices) == 0 {
		return ""
	}
	return h.notices[len(h.notices)-1].Text
}

func liveExec(host, command string) map[string]interface{} {
	return map[string]interface{}{"action": "exec", "host": host, "command": command}
}

func TestLiveSSH_ReadTierRunsOnRealHost(t *testing.T) {
	h := newLiveHarness(t, nil)

	// Read tier needs no approval, so it runs even from a cron turn.
	res := h.exec(t, approval.WithNonInteractive(context.Background(), "cron"), liveExec("box", "uptime"))
	require.True(t, res.Success, res.Error)
	assert.Contains(t, res.Content, "ran: uptime")
	assert.Equal(t, 0, res.Data["exit_code"])
	assert.Equal(t, []string{"uptime"}, h.srv.ran())

	// The connection went back to the pool and is reused.
	res = h.exec(t, context.Background(), liveExec("box", "df -h"))
	require.True(t, res.Success, res.Error)
	stats := h.tool.pool.Stats()
	assert.Equal(t, 1, stats.TotalConnections)
	assert.Equal(t, 0, stats.HostStats["box"].InUse)

	// exec_group fans out through the same pool.
	res = h.exec(t, context.Background(), map[string]interface{}{"action": "exec_group", "group": "fleet", "command": "hostname"})
	require.True(t, res.Success, res.Error)
	assert.Contains(t, h.srv.ran(), "hostname")
}

func TestLiveSSH_DangerousRunsOnlyAfterApproval(t *testing.T) {
	h := newLiveHarness(t, nil)

	args := liveExec("box", "systemctl restart app")
	res := h.exec(t, h.interactive(), args)
	require.True(t, res.Success)
	assert.Equal(t, "pending", res.Data["approval_status"])
	assert.Contains(t, res.Content, "NOT RUN YET")
	assert.Empty(t, h.srv.ran(), "dangerous command reached the host before approval")

	// Changing the model's args after the request cannot change what runs.
	args["command"] = "systemctl stop app"
	h.approveLatest(t)

	assert.Equal(t, []string{"systemctl restart app"}, h.srv.ran())
	assert.Contains(t, h.lastNotice(), "ran: systemctl restart app")
}

func TestLiveSSH_BlockedNeverRuns(t *testing.T) {
	h := newLiveHarness(t, nil)

	for name, args := range map[string]map[string]interface{}{
		"blocked pattern":        liveExec("box", "mkfs.ext4 /dev/sdb1"),
		"blocked command":        liveExec("box", "shutdown -h now"),
		"subshell":               liveExec("box", "echo $(id)"),
		"host read cap vs touch": liveExec("ro-box", "touch /tmp/x"),
		"group blocked command":  {"action": "exec_group", "group": "fleet", "command": "reboot"},
	} {
		t.Run(name, func(t *testing.T) {
			res := h.exec(t, h.interactive(), args)
			assert.False(t, res.Success)
			assert.Contains(t, res.Error, "blocked")
		})
	}
	assert.Empty(t, h.srv.ran())
	h.mu.Lock()
	assert.Empty(t, h.notices, "blocked operations must not prompt for approval")
	h.mu.Unlock()
}

func TestLiveSSH_NonInteractiveFailsClosed(t *testing.T) {
	h := newLiveHarness(t, nil)
	upload := filepath.Join(h.sandbox, "payload.txt")
	require.NoError(t, os.WriteFile(upload, []byte("data"), 0o600))

	gated := map[string]map[string]interface{}{
		"exec":       liveExec("box", "systemctl restart app"),
		"exec_group": {"action": "exec_group", "group": "fleet", "command": "systemctl restart app"},
		"scp_upload": {"action": "scp_upload", "host": "box", "local_path": upload, "remote_path": "/tmp/payload.txt"},
		"tunnel":     {"action": "tunnel_create", "host": "box", "remote_host": "localhost", "remote_port": float64(5432)},
	}
	origins := map[string]context.Context{
		"cron":      approval.WithNonInteractive(context.Background(), "cron"),
		"heartbeat": approval.WithNonInteractive(context.Background(), "heartbeat"),
		"mcp":       approval.WithNonInteractive(context.Background(), "mcp"),
		"unknown":   context.Background(),
	}
	for oname, ctx := range origins {
		for aname, args := range gated {
			t.Run(oname+"/"+aname, func(t *testing.T) {
				res := h.exec(t, ctx, args)
				assert.False(t, res.Success)
				assert.Equal(t, "refused_noninteractive", res.Data["approval_status"], res.Error)
			})
		}
	}
	assert.Empty(t, h.srv.ran(), "nothing may reach the host from a non-interactive turn")
	_, uploaded := h.srv.file("/tmp/payload.txt")
	assert.False(t, uploaded)
	assert.Zero(t, h.tool.tunnelManager.TunnelCount())
	assert.Empty(t, h.mgr.Pending("s1"))
}

func TestLiveSSH_HostKeyVerification(t *testing.T) {
	t.Run("changed key is refused", func(t *testing.T) {
		h := newLiveHarness(t, func(cfg *config.Config) {
			other := newLiveSSHServer(t) // a different host key for the same address
			cfg.RemoteSSH.Pool.KnownHostsFile = other.writeKnownHostsFor(t, cfg.RemoteSSH.Hosts[0].Port)
		})
		res := h.exec(t, context.Background(), liveExec("box", "uptime"))
		assert.False(t, res.Success)
		assert.Contains(t, res.Error, "HOST KEY MISMATCH")
		assert.Empty(t, h.srv.ran())
	})

	t.Run("unknown host is refused", func(t *testing.T) {
		h := newLiveHarness(t, func(cfg *config.Config) {
			empty := filepath.Join(t.TempDir(), "known_hosts")
			require.NoError(t, os.WriteFile(empty, nil, 0o600))
			cfg.RemoteSSH.Pool.KnownHostsFile = empty
		})
		res := h.exec(t, context.Background(), liveExec("box", "uptime"))
		assert.False(t, res.Success)
		assert.Contains(t, res.Error, "is not in")
		assert.Contains(t, res.Error, "ssh-keyscan")
		assert.Empty(t, h.srv.ran())
	})

	t.Run("strict_host_key_checking=no is refused", func(t *testing.T) {
		h := newLiveHarness(t, func(cfg *config.Config) {
			cfg.RemoteSSH.Pool.StrictHostKeyChecking = "no"
		})
		res := h.exec(t, context.Background(), liveExec("box", "uptime"))
		assert.False(t, res.Success)
		assert.Contains(t, res.Error, "not supported")
		assert.Empty(t, h.srv.ran())

		st := h.tool.SelfTest(context.Background(), &types.SelfTestOptions{})
		assert.Equal(t, types.SelfTestStatusFailed, st.Status, st.Message)
	})

	t.Run("accept-new trusts and records a first-seen key", func(t *testing.T) {
		kh := filepath.Join(t.TempDir(), "known_hosts")
		h := newLiveHarness(t, func(cfg *config.Config) {
			cfg.RemoteSSH.Pool.StrictHostKeyChecking = "accept-new"
			cfg.RemoteSSH.Pool.KnownHostsFile = kh
		})
		res := h.exec(t, context.Background(), liveExec("box", "uptime"))
		require.True(t, res.Success, res.Error)
		saved, err := os.ReadFile(kh)
		require.NoError(t, err)
		assert.Contains(t, string(saved), "ssh-ed25519")
	})
}

// writeKnownHostsFor writes a known_hosts entry binding 127.0.0.1:port to
// this server's host key (used to simulate a changed key).
func (s *liveSSHServer) writeKnownHostsFor(t *testing.T, port int) string {
	t.Helper()
	saved := s.addr
	s.addr = "127.0.0.1:" + strconv.Itoa(port)
	defer func() { s.addr = saved }()
	return s.writeKnownHosts(t, s.hostKey.PublicKey())
}

func TestLiveSSH_ApprovalTimeoutHonoured(t *testing.T) {
	h := newLiveHarness(t, func(cfg *config.Config) {
		cfg.RemoteSSH.Security.ApprovalTimeout = config.Duration(200 * time.Millisecond)
	})

	before := time.Now()
	res := h.exec(t, h.interactive(), liveExec("box", "systemctl restart app"))
	require.Equal(t, "pending", res.Data["approval_status"])
	expires, err := time.Parse(time.RFC3339, res.Data["expires_at"].(string))
	require.NoError(t, err)
	assert.WithinDuration(t, before, expires, 2*time.Second, "approval must use security.approval_timeout, not the 5m default")

	code := h.latestCode(t)
	require.Eventually(t, func() bool { return len(h.mgr.Pending("s1")) == 0 }, 3*time.Second, 20*time.Millisecond)
	h.mgr.HandleReply(context.Background(), approval.Inbound{
		ChannelID: h.origin.ChannelID, UserID: h.origin.UserID, SessionKey: h.origin.SessionKey,
		Text: "YES " + code, Notify: h.origin.Notify,
	})
	h.mgr.Wait()
	assert.Empty(t, h.srv.ran(), "an expired approval must not run")
}

func TestLiveSSH_SCPTierFromOperationAndHost(t *testing.T) {
	h := newLiveHarness(t, nil)
	local := filepath.Join(h.sandbox, "app.conf")
	require.NoError(t, os.WriteFile(local, []byte("listen 8080\n"), 0o600))
	upload := func(host, remote string) *types.ToolResult {
		return h.exec(t, h.interactive(), map[string]interface{}{
			"action": "scp_upload", "host": host, "local_path": local, "remote_path": remote,
		})
	}

	// Uncapped host: dangerous tier, approval-gated, then lands verbatim.
	// The hostile-looking path is one quoted word for the remote shell.
	remote := "/srv/app; touch pwned"
	res := upload("box", remote)
	require.True(t, res.Success, res.Error)
	assert.Equal(t, "pending", res.Data["approval_status"])
	assert.Equal(t, string(TierDangerous), res.Data["tier"])
	assert.Empty(t, h.srv.ran())
	h.approveLatest(t)
	got, ok := h.srv.file(remote)
	require.True(t, ok, "approved upload did not arrive: %s", h.lastNotice())
	assert.Equal(t, "listen 8080\n", string(got))
	assert.Equal(t, []string{"scp -t '/srv/app; touch pwned'"}, h.srv.ran())

	// Hosts capped below dangerous refuse uploads outright (no prompt).
	for _, host := range []string{"ro-box", "mod-box"} {
		res = upload(host, "/srv/app.conf")
		assert.False(t, res.Success)
		assert.Contains(t, res.Error, "exceeds host maximum tier")
	}

	// Downloads are read tier: allowed on a read-capped host without approval.
	h.srv.putFile("/var/log/app.log", []byte("ok\n"))
	res = h.exec(t, approval.WithNonInteractive(context.Background(), "cron"), map[string]interface{}{
		"action": "scp_download", "host": "ro-box", "remote_path": "/var/log/app.log",
		"local_path": filepath.Join(h.sandbox, "app.log"),
	})
	require.True(t, res.Success, res.Error)
	data, err := os.ReadFile(filepath.Join(h.sandbox, "app.log"))
	require.NoError(t, err)
	assert.Equal(t, "ok\n", string(data))

	// ... but blocked_patterns apply to the remote path.
	res = h.exec(t, context.Background(), map[string]interface{}{
		"action": "scp_download", "host": "box", "remote_path": "/etc/shadow",
		"local_path": filepath.Join(h.sandbox, "shadow"),
	})
	assert.False(t, res.Success)
	assert.Contains(t, res.Error, "blocked pattern")

	// Connections used by SCP go back to the pool.
	for host, hs := range h.tool.pool.Stats().HostStats {
		assert.Zero(t, hs.InUse, "connection to %s leaked by SCP", host)
	}
}

func TestLiveSSH_DownloadGatedWhenReadRequiresApproval(t *testing.T) {
	h := newLiveHarness(t, func(cfg *config.Config) {
		cfg.RemoteSSH.Security.RequireApproval = []string{"read", "modify", "dangerous"}
	})
	h.srv.putFile("/var/log/app.log", []byte("ok\n"))
	res := h.exec(t, h.interactive(), map[string]interface{}{
		"action": "scp_download", "host": "box", "remote_path": "/var/log/app.log",
		"local_path": filepath.Join(h.sandbox, "app.log"),
	})
	require.True(t, res.Success, res.Error)
	assert.Equal(t, "pending", res.Data["approval_status"])
	assert.Empty(t, h.srv.ran())
	h.approveLatest(t)
	assert.Equal(t, []string{"scp -f '/var/log/app.log'"}, h.srv.ran())
}

func TestLiveSSH_TunnelGatedAndReleasesConnection(t *testing.T) {
	h := newLiveHarness(t, nil)

	res := h.exec(t, h.interactive(), map[string]interface{}{
		"action": "tunnel_create", "host": "ro-box", "remote_host": "localhost", "remote_port": float64(5432),
	})
	assert.False(t, res.Success)
	assert.Contains(t, res.Error, "exceeds host maximum tier")

	res = h.exec(t, h.interactive(), map[string]interface{}{
		"action": "tunnel_create", "host": "box", "remote_host": "localhost", "remote_port": float64(5432),
	})
	require.True(t, res.Success, res.Error)
	assert.Equal(t, "pending", res.Data["approval_status"])
	assert.Zero(t, h.tool.tunnelManager.TunnelCount())

	h.approveLatest(t)
	require.Equal(t, 1, h.tool.tunnelManager.TunnelCount(), h.lastNotice())
	assert.Equal(t, 1, h.tool.pool.Stats().HostStats["box"].InUse)

	require.NoError(t, h.tool.tunnelManager.CloseAll())
	assert.Equal(t, 0, h.tool.pool.Stats().HostStats["box"].InUse, "closing the tunnel must return its connection")
}

func TestLiveSSH_SelfTest(t *testing.T) {
	h := newLiveHarness(t, nil)

	st := h.tool.SelfTest(context.Background(), &types.SelfTestOptions{})
	assert.Equal(t, types.SelfTestStatusOK, st.Status, st.Message)
	assert.Zero(t, h.tool.pool.Stats().TotalConnections, "SelfTest must not dial without CheckDependencies")

	st = h.tool.SelfTest(context.Background(), &types.SelfTestOptions{CheckDependencies: true})
	assert.Equal(t, types.SelfTestStatusOK, st.Status, st.Message)
	reachable := 0
	for _, d := range st.Dependencies {
		if d.Status == "reachable" {
			reachable++
		}
	}
	assert.Equal(t, 3, reachable)
	assert.Empty(t, h.srv.ran(), "probes must not run commands")
}
