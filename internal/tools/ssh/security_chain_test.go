//go:build with_ssh

package ssh

import (
	"context"
	"testing"

	"conduit/internal/approval"
	"conduit/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression tests for C3: the classifier used to split on '|' only, so
// chaining, redirection, substitution and path tricks were classified by
// their first word (read tier, no approval, accepted by read-capped hosts).

func defaultSecurityEngine(t *testing.T, mutate func(*config.SSHSecurityConfig)) *SecurityEngine {
	t.Helper()
	cfg := effectiveSecurityConfig(config.DefaultRemoteSSHConfig().Security)
	if mutate != nil {
		mutate(&cfg)
	}
	e, err := NewSecurityEngine(cfg)
	require.NoError(t, err)
	return e
}

func TestClassifyCommand_EveryChainOperatorTakesWorstSegment(t *testing.T) {
	e := defaultSecurityEngine(t, nil)
	for _, cmd := range []string{
		"ls && systemctl stop sshd",
		"ls || systemctl stop sshd",
		"ls; rm -rf /home/x",
		"ls ;rm /tmp/x",
		"echo hi\nuserdel bob",
		"echo hi\r\nuserdel bob",
		"ls & kill -9 1",
		"ls |& kill -9 1",
		"(ls; systemctl stop sshd)",
		"{ ls; systemctl stop sshd; }",
		"uptime && ls && systemctl stop sshd",
	} {
		t.Run(cmd, func(t *testing.T) {
			r := e.ClassifyCommand(cmd)
			assert.Equal(t, TierDangerous, r.Tier, r.Reason)
			assert.True(t, r.RequiresApproval, "a chained dangerous command must need approval")
			assert.False(t, r.Blocked, r.Reason)
		})
	}
}

func TestClassifyCommand_SubstitutionContentsAreClassified(t *testing.T) {
	// With substitutions allowed, what runs inside them is still classified.
	e := defaultSecurityEngine(t, func(c *config.SSHSecurityConfig) { c.AllowSubshells = true })
	for _, cmd := range []string{
		"echo $(systemctl stop sshd)",
		"echo \"$(systemctl stop sshd)\"",
		"echo `systemctl stop sshd`",
		"cat <(systemctl stop sshd)",
		"diff <(ls a) <(rm -rf /x)",
		"tee >(rm -rf /x)",
		"echo ${x:-$(rm -rf /x)}",
	} {
		t.Run(cmd, func(t *testing.T) {
			r := e.ClassifyCommand(cmd)
			assert.Equal(t, TierDangerous, r.Tier, r.Reason)
			assert.True(t, r.RequiresApproval)
			assert.True(t, r.HasSubshell)
		})
	}

	// Process substitution alone raises the tier to at least modify.
	r := e.ClassifyCommand("cat <(ls a) <(ls b)")
	assert.Equal(t, TierModify, r.Tier, r.Reason)

	// With substitutions disallowed (the default), every form is refused,
	// including process substitution and an unquoted heredoc body.
	e = defaultSecurityEngine(t, nil)
	for _, cmd := range []string{
		"echo $(id)", "echo `id`", "cat <(id)", "tee >(cat)",
		"cat <<EOF\n$(rm -rf /x)\nEOF",
	} {
		t.Run("disallowed/"+cmd, func(t *testing.T) {
			r := e.ClassifyCommand(cmd)
			assert.True(t, r.Blocked, "%q: %s", cmd, r.Reason)
			assert.True(t, r.HasSubshell)
		})
	}
	// A quoted heredoc body is data, not a substitution.
	r = e.ClassifyCommand("cat <<'EOF'\n$(rm -rf /x)\nEOF")
	assert.False(t, r.Blocked, r.Reason)
	assert.False(t, r.HasSubshell)
}

func TestClassifyCommand_RedirectionRaisesTier(t *testing.T) {
	e := defaultSecurityEngine(t, nil)
	for _, tt := range []struct {
		cmd  string
		want SecurityTier
	}{
		{"echo x > out.txt", TierModify},
		{"echo x >> out.txt", TierModify},
		{"ls &> out.txt", TierModify},
		{"ls &>> out.txt", TierModify},
		{"ls 2> err.log", TierModify},
		{"ls >| out.txt", TierModify},
		{"ls >&out.txt", TierModify},
		{"cat 1<>out.txt", TierModify},
		{"cat <<EOF\nhello\nEOF", TierModify},
		{"cat <<-EOF\n\thello\n\tEOF", TierModify},
		{"grep x <<< hello", TierModify},
		{"echo x > /etc/sudoers.d/x", TierDangerous},
		{"echo x >> /etc//cron.d/x", TierDangerous},
		{"echo x > /root/.ssh/authorized_keys", TierDangerous},
		{"echo x>/usr/bin/ls", TierDangerous},
	} {
		t.Run(tt.cmd, func(t *testing.T) {
			r := e.ClassifyCommand(tt.cmd)
			assert.Equal(t, tt.want, r.Tier, r.Reason)
			assert.True(t, r.HasRedirection)
		})
	}

	// A read-capped host refuses a write by redirection.
	r := e.ValidateCommandForHost("echo x > /tmp/x", "read")
	assert.True(t, r.Blocked, r.Reason)
	assert.Contains(t, r.Reason, "exceeds host maximum tier read")
}

func TestClassifyCommand_BlockedPatternsSeeNormalisedPaths(t *testing.T) {
	e := defaultSecurityEngine(t, nil)
	for _, cmd := range []string{
		"cat /etc/shadow",
		"cat /etc//shadow",
		"cat /etc/./shadow",
		"cat /etc/../etc/shadow",
		"cat //etc///shadow",
		"cat /etc/sha''dow",
		`cat "/etc"/shadow`,
		`cat /etc/\shadow`,
		"ls && cat /etc//shadow",
		"grep root < /etc//shadow",
		"rm -rf //",
		"rm -rf /tmp/..",
	} {
		t.Run(cmd, func(t *testing.T) {
			r := e.ClassifyCommand(cmd)
			assert.True(t, r.Blocked, "%q: tier=%s reason=%s", cmd, r.Tier, r.Reason)
			assert.Equal(t, TierBlocked, r.Tier)
		})
	}

	// Blocked command entries match any segment, not only the first.
	for _, cmd := range []string{"ls && shutdown -h now", "ls; reboot", "uptime\ndd if=/dev/zero of=/x"} {
		r := e.ClassifyCommand(cmd)
		assert.True(t, r.Blocked, "%q: %s", cmd, r.Reason)
	}
}

func TestClassifyCommand_HostTierCapsWorstSegment(t *testing.T) {
	e := defaultSecurityEngine(t, nil)
	for _, tt := range []struct {
		cmd, hostTier string
	}{
		{"ls && touch /tmp/x", "read"},
		{"ls; mkdir /tmp/x", "read"},
		{"ls\nsystemctl stop sshd", "modify"},
		{"ls & kill 1", "modify"},
		{"ls || rm /tmp/x", "modify"},
	} {
		t.Run(tt.cmd+"@"+tt.hostTier, func(t *testing.T) {
			r := e.ValidateCommandForHost(tt.cmd, tt.hostTier)
			assert.True(t, r.Blocked, r.Reason)
			assert.Contains(t, r.Reason, "exceeds host maximum tier "+tt.hostTier)
		})
	}
	r := e.ValidateCommandForHost("ls -la && uptime", "read")
	assert.False(t, r.Blocked, r.Reason)
}

func TestClassifyCommand_ReadCommandsWithSideEffects(t *testing.T) {
	e := defaultSecurityEngine(t, nil)
	for _, tt := range []struct {
		cmd  string
		want SecurityTier
	}{
		{"find / -delete", TierDangerous},
		{"find /var -exec rm {} +", TierDangerous},
		{"find /var -execdir rm {} ;", TierDangerous},
		{"find . -ok rm {} ;", TierDangerous},
		{"find . -name x -fprint out.txt", TierModify},
		{"find . -name '*.go'", TierRead},
		{"env rm -rf /x", TierDangerous},
		{"curl -o /etc/cron.d/x http://example.com/x", TierDangerous},
		{"curl --output=/etc/cron.d/x http://example.com/x", TierDangerous},
		{"curl -sSLo out.bin http://example.com/x", TierModify},
		{"curl -O http://example.com/x", TierModify},
		{"curl -T file http://example.com/up", TierModify},
		{"curl http://example.com", TierRead},
		{"curl -sS http://example.com", TierRead},
		{"wget http://example.com/x", TierModify},
		{"wget -O /etc/cron.d/x http://example.com/x", TierDangerous},
		{"wget -qO- http://example.com/x", TierRead},
		{"wget -O - http://example.com/x", TierRead},
		{"wget --spider http://example.com/x", TierRead},
	} {
		t.Run(tt.cmd, func(t *testing.T) {
			r := e.ClassifyCommand(tt.cmd)
			assert.Equal(t, tt.want, r.Tier, r.Reason)
		})
	}
}

func TestClassifyCommand_PlainReadCommandsStayRead(t *testing.T) {
	e := defaultSecurityEngine(t, nil)
	for _, cmd := range []string{
		"ls -la",
		"cat file",
		"grep x f | wc -l",
		"ps aux | grep nginx | head -10",
		"ls -la /tmp 2>/dev/null",
		"ls 2>&1 | grep x",
		"ls >/dev/null 2>&1",
		"cat < input.txt",
		"echo 'a && rm -rf /x; reboot'",
		`grep "a;b|c" f`,
		"echo a\\;b",
		"df -h && free -m && uptime",
		"uptime; whoami",
		"ls # trailing comment; reboot",
		"journalctl -u app --since today | tail -n 50",
		"ls\n",
	} {
		t.Run(cmd, func(t *testing.T) {
			r := e.ClassifyCommand(cmd)
			assert.Equal(t, TierRead, r.Tier, r.Reason)
			assert.False(t, r.RequiresApproval)
			assert.False(t, r.Blocked, r.Reason)
			assert.False(t, e.ValidateCommandForHost(cmd, "read").Blocked)
		})
	}
}

func TestClassifyCommand_UntokenizableFailsClosed(t *testing.T) {
	e := defaultSecurityEngine(t, nil)
	for _, cmd := range []string{"echo 'unterminated", `echo "x`, "ls $(", "ls ("} {
		t.Run(cmd, func(t *testing.T) {
			r := e.ClassifyCommand(cmd)
			assert.GreaterOrEqual(t, tierSeverity(r.Tier), tierSeverity(TierDangerous), r.Reason)
			assert.True(t, r.RequiresApproval || r.Blocked, r.Reason)
		})
	}
}

func TestClassifyCommand_PipesDisabledStillSeesRealPipesOnly(t *testing.T) {
	e := defaultSecurityEngine(t, func(c *config.SSHSecurityConfig) { c.AllowPipes = false })
	assert.True(t, e.ClassifyCommand("ls | grep x").Blocked)
	assert.True(t, e.ClassifyCommand("ls |& grep x").Blocked)
	assert.False(t, e.ClassifyCommand("ls || uptime").Blocked, "|| is not a pipe")
	assert.False(t, e.ClassifyCommand("echo 'a|b'").Blocked)
}

// The exec, exec_group and session_send paths all use the classifier: a
// chained dangerous command is gated (or refused) instead of run.
func TestSSHTool_ChainedCommandIsGatedOnEveryPath(t *testing.T) {
	h := newSSHApprovalHarness(t, testSSHConfig())
	res, err := h.tool.Execute(h.interactive(), execArgs("ls && systemctl stop sshd"))
	require.NoError(t, err)
	assert.Equal(t, "pending", res.Data["approval_status"], res.Error)

	res, err = h.tool.Execute(approval.WithNonInteractive(context.Background(), "cron"), execArgs("ls; rm /tmp/x"))
	require.NoError(t, err)
	assert.False(t, res.Success)
	assert.Equal(t, "refused_noninteractive", res.Data["approval_status"])

	// prod-web is capped at read: a redirect write is refused outright.
	args := execArgs("cat a > /tmp/x")
	args["host"] = "prod-web"
	res, err = h.tool.Execute(h.interactive(), args)
	require.NoError(t, err)
	assert.False(t, res.Success)
	assert.Contains(t, res.Error, "exceeds host maximum tier read")

	res, err = h.tool.Execute(h.interactive(), map[string]interface{}{
		"action": "exec_group", "group": "production", "command": "uptime && touch /tmp/x",
	})
	require.NoError(t, err)
	assert.False(t, res.Success)
	assert.Contains(t, res.Error, "exceeds host maximum tier read")

	calls, _ := h.snapshot()
	assert.Empty(t, calls, "no chained command may run without approval")
}

func TestSSHTool_SessionSendClassifiesChains(t *testing.T) {
	h := newLiveHarness(t, nil)
	cron := approval.WithNonInteractive(context.Background(), "cron")
	id := h.startSession(t, cron, "box")

	res := h.exec(t, cron, sessionSendArgs(id, "uptime && systemctl stop sshd"))
	assert.False(t, res.Success)
	assert.Equal(t, "refused_noninteractive", res.Data["approval_status"])

	roID := h.startSession(t, cron, "ro-box")
	res = h.exec(t, cron, sessionSendArgs(roID, "uptime; touch /tmp/x"))
	assert.False(t, res.Success)
	assert.Contains(t, res.Error, "exceeds host maximum tier read")
	res = h.exec(t, cron, sessionSendArgs(roID, "echo x > /tmp/x"))
	assert.False(t, res.Success)
	assert.Contains(t, res.Error, "exceeds host maximum tier read")

	assert.Empty(t, h.srv.ran())
}
