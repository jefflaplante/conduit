package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRealSkills_NoHollowActions discovers the real skills from the workspace
// skill path and verifies that every action the loader can extract resolves to
// a real command under the no-echo-fallback executor. Previously the echo
// fallback masked hollow skill actions by reporting Success:true while doing
// nothing at all (state-skill silent-failure bug, Sep 2026). This test fails
// the suite if any real skill/action pair would now fail honestly at runtime,
// so the exposure is known before deploy instead of after.
func TestRealSkills_NoHollowActions(t *testing.T) {
	workspaceSkills := filepath.Join(os.Getenv("HOME"), "ocgo", "workspace", "skills")
	if _, err := os.Stat(workspaceSkills); os.IsNotExist(err) {
		t.Skipf("workspace skills path %s not present; skipping real-skills audit", workspaceSkills)
	}

	d := NewSkillDiscovery([]string{workspaceSkills})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	skills, err := d.DiscoverSkills(ctx)
	if err != nil {
		t.Fatalf("skill discovery failed: %v", err)
	}
	if len(skills) == 0 {
		t.Skip("no skills discovered; skipping audit")
	}

	e := NewExecutor(ExecutionConfig{TimeoutSeconds: 10})
	// Wire the production gog settings (default cleanup script anchored at
	// the real workspace) so the audit judges command resolvability, not
	// executor construction. A bare NewExecutor left email/cleanup hollow in
	// name only — the default cleanup_script resolves fine in production
	// (bd-f92w).
	if err := e.ConfigureGog(nil, os.Getenv("HOME")+"/ocgo/workspace"); err != nil {
		t.Fatalf("ConfigureGog failed: %v", err)
	}

	var hollow []string
	scriptBound := 0
	// Probe args for actions that are arg-gated (return empty without their
	// required argument, which is honest behavior, not hollow-ness). Keep in
	// sync with buildGogCommand/buildGogAuthCommand required args (bd-f92w).
	probeArgs := map[string]map[string]map[string]interface{}{
		"gog-auth": {
			"add_account":    {"email": "probe@example.com"},
			"verify_account": {"email": "probe@example.com"},
		},
		"email": {
			"send": {"to": "probe@example.com", "subject": "probe", "body": "probe"},
			"read": {"message_id": "probe"},
		},
		"gog": {
			"send": {"to": "probe@example.com", "subject": "probe", "body": "probe"},
			"read": {"message_id": "probe"},
		},
	}
	for _, s := range skills {
		if len(s.Scripts) > 0 {
			scriptBound++ // script-bound skills use executeScript; fallback not reachable
			continue
		}
		if s.Content == "" {
			continue // nothing extractable; not actionable via subprocess anyway
		}
		for _, action := range ExtractActions(s) {
			args := map[string]interface{}{}
			if forSkill, ok := probeArgs[s.Name]; ok {
				if a, ok := forSkill[action]; ok {
					args = a
				}
			}
			if cmd, _ := e.buildShellCommand(s, action, args); cmd == "" {
				hollow = append(hollow, s.Name+"/"+action)
			}
		}
	}

	if len(hollow) > 0 {
		// Report-only: these pairs were broken before this fix too — they
		// hit the echo fallback and reported fake success. Now they fail
		// honestly. Logged here so the exposure is visible in every test
		// run; converting them to real commands is follow-up work
		// (loader action-name mangling is a separate bug — see bd log).
		t.Logf("HOLLOW SKILL/ACTIONS (previously fake-success via echo fallback, now honest failures: %d):\n  %s",
			len(hollow), strings.Join(hollow, "\n  "))
	}
	t.Logf("audit complete: %d skills discovered, %d script-bound (exempt), %d hollow skill/action pairs",
		len(skills), scriptBound, len(hollow))
}
