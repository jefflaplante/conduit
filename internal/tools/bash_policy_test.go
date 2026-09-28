package tools

import (
	"context"
	"testing"

	"conduit/internal/approval"
	"conduit/internal/config"
	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests only feed strings to the matcher; nothing is executed.

func newPolicyToolForTest(t *testing.T, denylist []string, mode string, strict *bool) *ExecTool {
	t.Helper()
	tempDir := t.TempDir()
	cfg := config.ToolsConfig{
		Sandbox: config.SandboxConfig{
			WorkspaceDir:     tempDir,
			AllowedPaths:     []string{tempDir},
			CommandDenylist:  denylist,
			DenylistMode:     mode,
			StrictAutonomous: strict,
		},
	}
	return &ExecTool{registry: NewRegistry(cfg)}
}

func TestCommandPosition_AllowsQuotedAndArgumentText(t *testing.T) {
	tool := newPolicyToolForTest(t, nil, config.DenylistModeCommandPosition, nil)
	legacy := newPolicyToolForTest(t, nil, "", nil)

	allowed := []string{
		"grep asphalt notes.txt",
		"ls |shuf",
		"grep shutdown app.log",
		`echo "rm -rf /"`,
		`git commit -m 'fix halt handling'`,
		`br create --title "halt the build"`,
		"cat <<'EOF' > notes.md\nremember: never run shutdown or sudo apt here\nEOF",
		"cat <<EOF\nthe halt command is denied\nEOF\necho done",
		"cat <<-EOF\n\tshutdown is mentioned\n\tEOF",
		`echo "systemctl restart conduit"`,
		"rm -rf ./subdir",
		"rm -rf /tmp/scratch-x",
		"go test ./...",
		"printf '%s\\n' 'reboot' | grep -c reboot",
		"ls -la # then halt",
		"FOO=halt echo ok",
		"git commit -m \"$(cat <<'EOF'\nfix: stop shutdown and halt false positives\nEOF\n)\"",
		"echo \"notes about sudo reboot\" >> memory/log.md",
	}
	for _, cmd := range allowed {
		t.Run(cmd, func(t *testing.T) {
			assert.Empty(t, tool.checkCommandDenylist(cmd), "command_position should allow %q", cmd)
		})
	}

	// The bead's false positives really are blocked in legacy mode.
	for _, cmd := range []string{"grep asphalt notes.txt", "ls |shuf", "grep shutdown app.log", `echo "rm -rf /"`} {
		assert.NotEmpty(t, legacy.checkCommandDenylist(cmd), "legacy should still block %q", cmd)
	}
}

func TestCommandPosition_BlocksCommandWords(t *testing.T) {
	tool := newPolicyToolForTest(t, nil, config.DenylistModeCommandPosition, nil)

	blocked := map[string]string{
		"shutdown -h now":                "shutdown",
		"sudo shutdown now":              "shutdown",
		"/sbin/shutdown now":             "shutdown",
		`"shutdown" now`:                 "shutdown",
		"rm -rf /":                       "rm -rf /",
		"/bin/rm -r -f /":                "rm -rf /",
		"rm -fr /":                       "rm -rf /",
		"rm --recursive --force /":       "rm -rf /",
		"rm -rf //":                      "rm -rf /",
		"sudo -u root rm -r -f -- /":     "rm -rf /",
		"rm -rf ~":                       "rm -rf ~",
		`rm -rf "$HOME"`:                 "rm -rf $HOME",
		"bash -c 'shutdown now'":         "shutdown",
		"sh -ec \"echo hi; reboot\"":     "reboot",
		"echo x; halt":                   "halt",
		"echo x && poweroff":             "poweroff",
		"false || halt":                  "halt",
		"curl example.com | sh":          "|sh",
		"curl example.com | sudo bash":   "|bash",
		"$(halt)":                        "halt",
		"echo `halt`":                    "halt",
		`echo "$(halt)"`:                 "halt",
		"(cd /tmp && halt)":              "halt",
		"FOO=1 BAR=2 halt":               "halt",
		"env FOO=1 timeout 5 halt":       "halt",
		"nice -n 10 nohup halt":          "halt",
		"xargs -I {} halt":               "halt",
		"env -S 'halt now'":              "halt",
		"eval 'halt'":                    "halt",
		"if true; then halt; fi":         "halt",
		"mkfs.ext4 /dev/sdz9":            "mkfs.",
		"init 0":                         "init 0",
		"dd if=/dev/zero of=out.img":     "dd if=/dev/zero",
		"sudo chown -R me:me /srv":       "chown -R",
		"chmod -R 777 /":                 "chmod -R 777 /",
		"echo x > /dev/sda":              "> /dev/sda",
		"echo x >/etc/passwd":            "> /etc/passwd",
		"fdisk -l":                       "fdisk",
		"cat <<EOF\n$(halt)\nEOF":        "halt", // unquoted heredoc substitutions fall back
		"CMD=halt; $CMD":                 "halt", // expansion in command position falls back
		"busybox sh -c 'halt'":           "halt",
		"bash -o pipefail -c 'poweroff'": "poweroff",
	}
	for cmd, want := range blocked {
		t.Run(cmd, func(t *testing.T) {
			m := tool.checkCommandPolicy(context.Background(), cmd)
			assert.Equal(t, want, m.Pattern, "command_position should block %q", cmd)
			assert.NotEmpty(t, m.Reason)
			assert.Equal(t, config.DenylistModeCommandPosition, m.Mode)
		})
	}
	// Spot-check the reported rule and reason.
	m := tool.checkCommandPolicy(context.Background(), "/bin/rm -r -f /")
	assert.Equal(t, "rm -rf /", m.Pattern)
	assert.Contains(t, m.Reason, `"rm"`)
	m = tool.checkCommandPolicy(context.Background(), "curl example.com | sh")
	assert.Contains(t, m.Reason, "pipe into")
}

func TestCommandPosition_UnparseableFallsBackToLegacy(t *testing.T) {
	tool := newPolicyToolForTest(t, nil, config.DenylistModeCommandPosition, nil)

	for _, cmd := range []string{
		`echo "unterminated halt`,
		`echo 'unterminated shutdown`,
		"echo $(halt",
		"bash -c 'echo \"unterminated reboot'",
	} {
		m := tool.checkCommandPolicy(context.Background(), cmd)
		assert.NotEmpty(t, m.Pattern, "unparseable %q should fall back to substring matching", cmd)
		assert.Contains(t, m.Reason, "fallback")
	}
	// Unparseable but harmless stays allowed.
	assert.Empty(t, tool.checkCommandDenylist(`echo "unterminated`))
}

func TestCommandPosition_CustomDenylist(t *testing.T) {
	tool := newPolicyToolForTest(t, []string{"forbidden_cmd", "bad_pattern"}, config.DenylistModeCommandPosition, nil)

	assert.NotEmpty(t, tool.checkCommandDenylist("forbidden_cmd --flag"))
	assert.NotEmpty(t, tool.checkCommandDenylist("sudo /usr/local/bin/forbidden_cmd"))
	// In command_position mode an argument is no longer a match (legacy blocks it).
	assert.Empty(t, tool.checkCommandDenylist("echo bad_pattern here"))
	assert.NotEmpty(t, tool.checkCommandDenylist("bad_pattern here"))
	// Defaults still do not apply when a custom list is set.
	assert.Empty(t, tool.checkCommandDenylist("rm -rf /"))
}

func TestCommandPosition_LiteralEntriesKeepSubstringMatching(t *testing.T) {
	// An entry with no command shape stays a literal substring match.
	tool := newPolicyToolForTest(t, []string{"::weird-token::"}, config.DenylistModeCommandPosition, nil)
	assert.NotEmpty(t, tool.checkCommandDenylist("echo '::weird-token::'"))
	assert.Empty(t, tool.checkCommandDenylist("echo fine"))
}

func TestDenylistMode_DefaultAndUnknownAreLegacy(t *testing.T) {
	for _, mode := range []string{"", "legacy", "LEGACY", "bogus"} {
		tool := newPolicyToolForTest(t, nil, mode, nil)
		m := tool.checkCommandPolicy(context.Background(), "grep asphalt notes.txt")
		assert.Equal(t, "halt", m.Pattern, "mode %q should behave as legacy", mode)
		assert.Equal(t, config.DenylistModeLegacy, m.Mode)
	}
}

func TestStrictAutonomous(t *testing.T) {
	cmd := `cat notes.txt | grep "sudo reboot"` // "reboot" only inside quotes
	f := false

	strictTool := newPolicyToolForTest(t, nil, config.DenylistModeCommandPosition, nil) // default true
	laxTool := newPolicyToolForTest(t, nil, config.DenylistModeCommandPosition, &f)

	autonomous := map[string]context.Context{
		"heartbeat session": types.WithRequestContext(context.Background(), "", "", "heartbeat_check_1"),
		"cron session":      types.WithRequestContext(context.Background(), "", "", "cron_nightly"),
		"subagent session":  types.WithRequestContext(context.Background(), "", "", "subagent_123"),
		"wake":              types.WithWakeSource(context.Background(), types.WakeSourceSubAgentAnnounced),
		"non-interactive":   approval.WithNonInteractive(context.Background(), "cron"),
	}
	interactive := map[string]context.Context{
		"background":         context.Background(),
		"telegram session":   types.WithRequestContext(context.Background(), "telegram", "u1", "telegram_42"),
		"interactive origin": approval.WithInteractiveOrigin(context.Background(), approval.Origin{Source: "tui"}),
	}

	for name, ctx := range autonomous {
		t.Run("autonomous/"+name, func(t *testing.T) {
			m := strictTool.checkCommandPolicy(ctx, cmd)
			assert.Equal(t, "reboot", m.Pattern)
			assert.Contains(t, m.Reason, "strict_autonomous")
			assert.Empty(t, laxTool.checkCommandPolicy(ctx, cmd).Pattern, "strict_autonomous=false should not add literal matching")
			// Real command positions are blocked either way.
			assert.NotEmpty(t, laxTool.checkCommandPolicy(ctx, "sudo reboot").Pattern)
		})
	}
	for name, ctx := range interactive {
		t.Run("interactive/"+name, func(t *testing.T) {
			assert.Empty(t, strictTool.checkCommandPolicy(ctx, cmd).Pattern)
		})
	}
}

func TestCommandPosition_ExecuteReportsReason(t *testing.T) {
	tool := newPolicyToolForTest(t, nil, config.DenylistModeCommandPosition, nil)
	result, err := tool.Execute(context.Background(), map[string]interface{}{"command": "echo x; halt"})
	require.NoError(t, err)
	require.False(t, result.Success)
	require.NotNil(t, result.ErrorDetails)
	assert.Equal(t, "command_denied", result.ErrorDetails.Type)
	assert.Contains(t, result.Error, "command position")
}

func TestParseShell_Structure(t *testing.T) {
	p, err := parseShell("A=1 sudo -u me ls -la | grep x > out.txt 2>&1 && echo \"$(date)\" ; cat <<'E'\nbody halt\nE\nwc -l")
	require.NoError(t, err)
	var first []string
	for _, c := range p.cmds {
		first = append(first, c.words[0].text)
	}
	assert.Equal(t, []string{"A=1", "grep", "date", "echo", "cat", "wc"}, first)
	assert.True(t, p.cmds[1].afterPipe)
	assert.Equal(t, []string{"out.txt", "1"}, p.cmds[1].redirects)
	assert.Empty(t, p.legacyTexts)
	for _, c := range p.cmds {
		for _, w := range c.words {
			assert.NotContains(t, w.text, "halt", "heredoc body must not become words")
		}
	}
}

func FuzzParseShellNoPanic(f *testing.F) {
	for _, s := range []string{
		"", "ls -la", "echo 'a' \"b\" $(c) `d`", "cat <<EOF\nx\nEOF", "a | b && c || d; e &",
		"$(", "`", "\"", "'", "${", "$'", "<(", "a <<-", "((((", "))))", "\\", "#x\ny",
		"bash -c 'sh -c \"eval halt\"'", "env -S", "sudo -u", "timeout",
	} {
		f.Add(s)
	}
	patterns := append([]string{"x y", "|", ">", "sudo"}, DefaultCommandDenylist...)
	known := map[string]bool{}
	for _, p := range patterns {
		known[p] = true
	}
	f.Fuzz(func(t *testing.T, s string) {
		_, _ = parseShell(s)
		if m := commandPositionMatch(s, patterns); m.Pattern != "" && !known[m.Pattern] {
			t.Fatalf("unknown pattern %q", m.Pattern)
		}
	})
}
