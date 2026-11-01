package workspace

import "testing"

// bd-3ia: the configured state file is gateway-rendered and read-only, so it
// is readable from every session type; other .json files stay denied.
func TestSecurityManager_StateFileAllSessionTypes(t *testing.T) {
	sm := NewSecurityManager()
	sm.AllowStateFile("memory/jules-state.json")

	// main, group chats, cron/heartbeat and sub-agent sessions.
	for _, st := range []string{"main", "shared", "isolated", "cron", "heartbeat", "subagent"} {
		sc := SecurityContext{SessionType: st}
		if !sm.IsAccessible("memory/jules-state.json", sc) {
			t.Errorf("state file should be accessible for session type %q", st)
		}
		for _, other := range []string{"memory/other.json", "other.json", "memory/jules-state.json.bak"} {
			if sm.IsAccessible(other, sc) {
				t.Errorf("%s should stay denied for session type %q", other, st)
			}
		}
	}
}

func TestSecurityManager_JSONDeniedWithoutStateFile(t *testing.T) {
	sm := NewSecurityManager()
	if sm.IsAccessible("memory/jules-state.json", SecurityContext{SessionType: "main"}) {
		t.Error("state file must not be accessible unless allowed")
	}
}

func TestSecurityManager_AllowStateFileIgnoresEmpty(t *testing.T) {
	sm := NewSecurityManager()
	n := len(sm.GetRules())
	sm.AllowStateFile("")
	if len(sm.GetRules()) != n {
		t.Error("empty state file should not add a rule")
	}
}
