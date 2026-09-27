package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fixed values for deterministic gog command tests (conduit-31jg.40).
const (
	testGogBinary = "/test/bin/gog"
	testWorkspace = "/srv/conduit/workspace"
)

// Defaults must reproduce the pre-31jg.40 hardcoded behavior exactly, so an
// existing config without a skills.gog block is unaffected.
func TestGogDefaults_MatchLegacyBehavior(t *testing.T) {
	ws := "/home/owner/ocgo/workspace"
	s, err := resolveGogSettings(nil, ws)
	if err != nil {
		t.Fatal(err)
	}
	if s.ownerAccount != `"$GOG_ACCOUNT"` || s.agentAccount != `"$JULES_ACCOUNT"` {
		t.Errorf("account expansions = %s / %s", s.ownerAccount, s.agentAccount)
	}
	for _, a := range []string{"jeff", "owner@example.com", "owner-alt@example.com"} {
		if !s.isOwnerAlias(a) {
			t.Errorf("default owner alias %q missing", a)
		}
	}
	for _, a := range []string{"jules", "agent@example.com"} {
		if !s.isAgentAlias(a) {
			t.Errorf("default agent alias %q missing", a)
		}
	}
	if want := filepath.Join(ws, "scripts", "hygiene-junk-sweep.sh"); s.cleanupScript != want {
		t.Errorf("cleanup script = %q, want %q", s.cleanupScript, want)
	}
	home, _ := os.UserHomeDir()
	wantEnv := []string{filepath.Join(home, "ocgo", ".ocgo-secrets.env"), filepath.Join(home, ".conduit-secrets.env")}
	if strings.Join(s.envFiles, ",") != strings.Join(wantEnv, ",") {
		t.Errorf("env files = %v, want %v", s.envFiles, wantEnv)
	}
	if s.binary == "" {
		t.Error("binary must default to something")
	}
}

func TestGogDefaults_CommandsUseLegacyIdentities(t *testing.T) {
	cmd := emailCommand(t, "send", map[string]interface{}{"to": "a@b.c", "account": "jeff"})
	if !strings.Contains(cmd, `--account "$GOG_ACCOUNT"`) {
		t.Errorf("owner alias must select $GOG_ACCOUNT:\n%s", cmd)
	}
	cmd = emailCommand(t, "cleanup", nil)
	if !strings.Contains(cmd, testWorkspace+"/scripts/hygiene-junk-sweep.sh\n") {
		t.Errorf("cleanup must run the workspace sweep script:\n%s", cmd)
	}
	if !strings.Contains(emailCommand(t, "status", nil), testGogBinary+" gmail search") {
		t.Error("configured binary not used")
	}
}

func TestGogConfig_CustomOverridesDefaults(t *testing.T) {
	e := NewExecutor(ExecutionConfig{TimeoutSeconds: 5})
	err := e.ConfigureGog(&GogConfig{
		Binary:          "/opt/gog/bin/gog",
		OwnerAccountEnv: "OWNER_GOG",
		AgentAccountEnv: "BOT_GOG",
		OwnerAliases:    []string{"me", "me@example.com"},
		AgentAliases:    []string{"bot"},
		EnvFiles:        []string{"secrets.env"},
		CleanupScript:   "/usr/local/libexec/sweep.sh",
	}, "/ws")
	if err != nil {
		t.Fatal(err)
	}
	build := func(action string, args map[string]interface{}) (string, error) {
		return e.buildShellCommand(Skill{Name: "gog"}, action, args)
	}

	cmd, err := build("send", map[string]interface{}{"to": "x@y.z", "account": "me"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cmd, "/opt/gog/bin/gog gmail send") || !strings.Contains(cmd, `--account "$OWNER_GOG"`) {
		t.Errorf("custom owner send:\n%s", cmd)
	}
	if !e.isOwnerEmailSend(Skill{Name: "gog"}, "send", map[string]interface{}{"to": "x", "from": "me@example.com"}) {
		t.Error("custom owner alias must be approval-gated")
	}

	cmd, _ = build("send", map[string]interface{}{"to": "x@y.z"})
	if !strings.Contains(cmd, `--account "$BOT_GOG"`) {
		t.Errorf("default send must use the agent account:\n%s", cmd)
	}

	// Legacy aliases no longer apply once aliases are configured.
	if _, err := build("send", map[string]interface{}{"to": "x@y.z", "account": "jeff"}); err == nil {
		t.Error("legacy alias accepted despite custom owner_aliases")
	} else if !strings.Contains(err.Error(), "allowed: bot, me, me@example.com") {
		t.Errorf("error should list configured aliases: %v", err)
	}

	cmd, _ = build("cleanup", nil)
	if !strings.Contains(cmd, "/usr/local/libexec/sweep.sh\n") {
		t.Errorf("custom cleanup script:\n%s", cmd)
	}
	if got := e.gog.envFiles; len(got) != 1 || got[0] != "/ws/secrets.env" {
		t.Errorf("relative env file should anchor at workspace: %v", got)
	}
}

func TestGogConfig_Invalid(t *testing.T) {
	if _, err := resolveGogSettings(&GogConfig{OwnerAccountEnv: "X; rm -rf /"}, ""); err == nil {
		t.Error("shell metacharacters in env var name must be rejected")
	}
	if _, err := resolveGogSettings(&GogConfig{OwnerAliases: []string{"a"}, AgentAliases: []string{"a"}}, ""); err == nil {
		t.Error("alias in both sets must be rejected")
	}
}

func TestGogCleanup_NoWorkspaceFailsHonestly(t *testing.T) {
	e := NewExecutor(ExecutionConfig{TimeoutSeconds: 5})
	if _, err := e.buildShellCommand(Skill{Name: "gog"}, "cleanup", nil); err == nil {
		t.Error("cleanup without workspace or cleanup_script must error")
	}
}

func TestNewManager_WiresGogConfig(t *testing.T) {
	m := NewManager(SkillsConfig{
		WorkspaceDir: "/ws",
		Gog:          &GogConfig{OwnerAliases: []string{"boss"}},
	})
	if !m.executor.gog.isOwnerAlias("boss") || m.executor.gog.isOwnerAlias("jeff") {
		t.Error("manager did not apply skills.gog")
	}
	if m.executor.gog.cleanupScript != "/ws/scripts/hygiene-junk-sweep.sh" {
		t.Errorf("cleanup = %q", m.executor.gog.cleanupScript)
	}

	// A bad block falls back to defaults instead of disabling skills.
	m = NewManager(SkillsConfig{Gog: &GogConfig{AgentAccountEnv: "bad name"}})
	if !m.executor.gog.isOwnerAlias("jeff") {
		t.Error("invalid skills.gog should fall back to defaults")
	}
}

func TestShellWord(t *testing.T) {
	for in, want := range map[string]string{
		"/usr/local/bin/gog": "/usr/local/bin/gog",
		"/a b/gog":           "'/a b/gog'",
		"x;rm -rf /":         "'x;rm -rf /'",
		"$(id)":              "'$(id)'",
		"~/x":                "'~/x'",
		"":                   "''",
	} {
		if got := shellWord(in); got != want {
			t.Errorf("shellWord(%q) = %s, want %s", in, got, want)
		}
	}
}
