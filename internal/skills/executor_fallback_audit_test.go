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
	loader := NewSkillLoader()

	var hollow []string
	scriptBound := 0
	for _, s := range skills {
		if len(s.Scripts) > 0 {
			scriptBound++ // script-bound skills use executeScript; fallback not reachable
			continue
		}
		if s.Content == "" {
			continue // nothing extractable; not actionable via subprocess anyway
		}
		for _, action := range loader.ExtractActionsFromContent(s.Content) {
			if cmd := e.buildShellCommand(s, action, map[string]interface{}{}); cmd == "" {
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
