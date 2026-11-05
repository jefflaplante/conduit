package gateway

import (
	"testing"

	"conduit/internal/config"
)

// bd-8sib regression: with no workspace dir known, the hook must disarm
// (empty path, no write) rather than resolve the state file relative to
// the process CWD — a stray run must not litter memory/ into random dirs.
// Found the hard way: a mid-development smoke run left a dummy
// memory/jules-state.json inside the repo's package directory.
func TestStateHook_NoWorkspaceDirDisarms(t *testing.T) {
	gw, _, _ := newTestGatewayWithRouter(t)
	gw.config = &config.Config{
		Brain:     config.BrainConfig{SessionState: config.SessionStateConfig{Enabled: true}},
		Workspace: config.WorkspaceConfig{}, // no ContextDir
	}
	if p := gw.sessionStateFilePath(); p != "" {
		t.Fatalf("empty ContextDir must disarm the hook, got path %q", p)
	}
	if h := gw.stateHookFor("telegram_1_telegram"); h != nil {
		t.Fatal("stateHookFor must return nil with no workspace dir")
	}
}
