//go:build with_ssh

package ssh

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"conduit/internal/approval"
	"conduit/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// conduit-1kxf: session_start is classified (read tier, capped by the host's
// security_tier), refused for unknown/disabled hosts before any connection,
// bounded by per-host and global session caps, never runs a command itself
// and is audit-logged. Commands sent into the session are still classified
// one by one by session_send.

func sessionStartArgs(host string) map[string]interface{} {
	return map[string]interface{}{"action": "session_start", "host": host}
}

func sessionSendArgs(id, command string) map[string]interface{} {
	return map[string]interface{}{"action": "session_send", "session_id": id, "command": command, "timeout": 5}
}

func (h *liveHarness) startSession(t *testing.T, ctx context.Context, host string) string {
	t.Helper()
	res := h.exec(t, ctx, sessionStartArgs(host))
	require.True(t, res.Success, res.Error)
	id, _ := res.Data["session_id"].(string)
	require.NotEmpty(t, id)
	return id
}

func TestSessionStart_HostNotAllowedRefusedBeforeConnect(t *testing.T) {
	h := newLiveHarness(t, func(cfg *config.Config) {
		off := false
		cfg.RemoteSSH.Hosts = append(cfg.RemoteSSH.Hosts, config.SSHHostConfig{
			Name: "off-box", Hostname: "127.0.0.1", Port: cfg.RemoteSSH.Hosts[0].Port,
			User: "tester", IdentityFile: cfg.RemoteSSH.Hosts[0].IdentityFile, Enabled: &off,
		})
	})

	res := h.exec(t, context.Background(), sessionStartArgs("host-a.example"))
	assert.False(t, res.Success)
	assert.Contains(t, res.Error, "not found in configuration")

	res = h.exec(t, context.Background(), sessionStartArgs("off-box"))
	assert.False(t, res.Success)
	assert.Contains(t, res.Error, "disabled")

	assert.Zero(t, h.srv.acceptedConns(), "a refused session_start must not connect")
	assert.Zero(t, h.tool.sessionManager.SessionCount())
}

func TestSessionStart_OpensAndCommandsAreClassified(t *testing.T) {
	h := newLiveHarness(t, nil)

	// Opening needs no approval, even from a non-interactive (cron) turn.
	cron := approval.WithNonInteractive(context.Background(), "cron")
	res := h.exec(t, cron, sessionStartArgs("box"))
	require.True(t, res.Success, res.Error)
	assert.Equal(t, "read", res.Data["tier"])
	id := res.Data["session_id"].(string)
	assert.Equal(t, 1, h.srv.shellsOpened())
	assert.Empty(t, h.srv.ran(), "opening a session must not run a command")

	// Read tier runs.
	res = h.exec(t, cron, sessionSendArgs(id, "uptime"))
	require.True(t, res.Success, res.Error)
	assert.Contains(t, res.Content, "ran: uptime")
	assert.Equal(t, "read", res.Data["tier"])

	// Dangerous tier is gated per command; nothing runs before approval.
	res = h.exec(t, h.interactive(), sessionSendArgs(id, "systemctl restart app"))
	assert.Equal(t, "pending", res.Data["approval_status"])
	// Blocked never runs.
	res = h.exec(t, h.interactive(), sessionSendArgs(id, "mkfs.ext4 /dev/sdb"))
	assert.False(t, res.Success)
	assert.Contains(t, res.Error, "blocked")
	assert.Equal(t, []string{"uptime"}, h.srv.ran())

	h.approveLatest(t)
	assert.Equal(t, []string{"uptime", "systemctl restart app"}, h.srv.ran())

	// A read-capped host still opens, but modify commands in it are refused.
	roID := h.startSession(t, cron, "ro-box")
	res = h.exec(t, cron, sessionSendArgs(roID, "touch /tmp/x"))
	assert.False(t, res.Success)
	assert.Contains(t, res.Error, "exceeds host maximum tier read")
	assert.NotContains(t, h.srv.ran(), "touch /tmp/x")
}

func TestSessionStart_InitialCommandRefused(t *testing.T) {
	h := newLiveHarness(t, nil)

	args := sessionStartArgs("box")
	args["command"] = "rm -rf /srv/app"
	res := h.exec(t, h.interactive(), args)
	assert.False(t, res.Success)
	assert.Contains(t, res.Error, "session_send")
	assert.Zero(t, h.srv.acceptedConns())
	assert.Empty(t, h.srv.ran())

	v := h.tool.ValidateParameters(context.Background(), args)
	assert.False(t, v.Valid, "validation must flag a command on session_start")
}

func TestSessionStart_ApprovalForReadDoesNotGateOpen(t *testing.T) {
	h := newLiveHarness(t, func(cfg *config.Config) {
		cfg.RemoteSSH.Security.RequireApproval = []string{"read", "modify", "dangerous", "blocked"}
	})
	cron := approval.WithNonInteractive(context.Background(), "cron")

	id := h.startSession(t, cron, "box")

	// The command inside still needs approval and fails closed here.
	res := h.exec(t, cron, sessionSendArgs(id, "uptime"))
	assert.False(t, res.Success)
	assert.Empty(t, h.srv.ran())
}

func TestSessionStart_PerHostCap(t *testing.T) {
	h := newLiveHarness(t, nil)
	ctx := context.Background()
	require.Equal(t, 2, h.tool.sessionManager.MaxSessionsPerHost(), "default per-host cap")

	first := h.startSession(t, ctx, "box")
	second := h.startSession(t, ctx, "box")
	accepted := h.srv.acceptedConns()

	res := h.exec(t, ctx, sessionStartArgs("box"))
	assert.False(t, res.Success)
	assert.Contains(t, res.Error, "max_sessions_per_host")
	assert.Equal(t, "host", res.Data["limit_scope"])
	assert.Equal(t, "remote_ssh.sessions.max_sessions_per_host", res.Data["config_key"])
	assert.ElementsMatch(t, []string{first, second}, res.Data["open_sessions"])
	suggestions := strings.Join(res.Data["suggestions"].([]string), "\n")
	assert.Contains(t, suggestions, "session_close")
	assert.Contains(t, suggestions, first)
	assert.Equal(t, accepted, h.srv.acceptedConns(), "a capped session_start must not connect")

	// Other hosts are unaffected; closing one frees a slot.
	h.startSession(t, ctx, "ro-box")
	res = h.exec(t, ctx, map[string]interface{}{"action": "session_close", "session_id": first})
	require.True(t, res.Success, res.Error)
	h.startSession(t, ctx, "box")
}

func TestSessionStart_GlobalCap(t *testing.T) {
	h := newLiveHarness(t, func(cfg *config.Config) {
		cfg.RemoteSSH.Sessions.MaxConcurrentSessions = 2
	})
	ctx := context.Background()

	h.startSession(t, ctx, "box")
	h.startSession(t, ctx, "ro-box")

	res := h.exec(t, ctx, sessionStartArgs("mod-box"))
	assert.False(t, res.Success)
	assert.Contains(t, res.Error, "max_concurrent_sessions")
	assert.Equal(t, "global", res.Data["limit_scope"])
	assert.Len(t, res.Data["open_sessions"], 2)
}

// Concurrent starts cannot overshoot the cap while connections are in
// flight (slots are reserved before connecting).
func TestSessionStart_ConcurrentStartsRespectCap(t *testing.T) {
	h := newLiveHarness(t, nil)

	var (
		wg sync.WaitGroup
		mu sync.Mutex
		ok int
	)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := h.tool.Execute(context.Background(), sessionStartArgs("box"))
			if err == nil && res.Success {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, 2, ok)
	assert.Equal(t, 2, h.tool.sessionManager.SessionCount())
}

func TestSessionStart_ViaJumpHost(t *testing.T) {
	h := newLiveHarness(t, func(cfg *config.Config) {
		base := cfg.RemoteSSH.Hosts[0]
		cfg.RemoteSSH.Hosts = append(cfg.RemoteSSH.Hosts, config.SSHHostConfig{
			Name: "inner-box", Hostname: "127.0.0.1", Port: base.Port, User: "tester",
			IdentityFile: base.IdentityFile,
			JumpHost:     "tester@127.0.0.1:" + strconv.Itoa(base.Port), // the server forwards to itself
		})
	})

	id := h.startSession(t, context.Background(), "inner-box")
	res := h.exec(t, context.Background(), sessionSendArgs(id, "hostname"))
	require.True(t, res.Success, res.Error)
	assert.Equal(t, 2, h.srv.acceptedConns(), "jump hop plus tunnelled target")
}

func TestSessionStart_AuditLogged(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "ssh_audit.jsonl")
	h := newLiveHarness(t, func(cfg *config.Config) {
		cfg.RemoteSSH.Audit = config.SSHAuditConfig{Enabled: true, LogPath: logPath, LogCommands: true}
	})
	ctx := context.Background()

	id := h.startSession(t, ctx, "box")
	res := h.exec(t, ctx, sessionSendArgs(id, "uptime"))
	require.True(t, res.Success, res.Error)
	res = h.exec(t, ctx, map[string]interface{}{"action": "session_close", "session_id": id})
	require.True(t, res.Success, res.Error)
	h.tool.Close()

	data, err := os.ReadFile(logPath)
	require.NoError(t, err)
	var entries []AuditEntry
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var e AuditEntry
		require.NoError(t, json.Unmarshal([]byte(line), &e))
		entries = append(entries, e)
	}
	require.Len(t, entries, 3)

	assert.Equal(t, "session_start", entries[0].Action)
	assert.Equal(t, id, entries[0].SessionID)
	assert.Equal(t, "box", entries[0].Host)
	assert.Equal(t, "read", entries[0].SecurityTier)
	assert.True(t, entries[0].Approved)

	assert.Empty(t, entries[1].Action)
	assert.Equal(t, "uptime", entries[1].Command)
	assert.Equal(t, id, entries[1].SessionID)

	assert.Equal(t, "session_close", entries[2].Action)
	assert.Equal(t, id, entries[2].SessionID)
}
