package gateway

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"conduit/internal/config"
	"conduit/internal/sandbox"
)

// The agent reads the redacted file, edits one value and proposes the whole
// file back: the patch holds only that key, the secret survives, and the
// write changes only that line of the owner's formatting.
func TestConfigFile_RoundTripKeepsFormattingAndSecrets(t *testing.T) {
	t.Cleanup(sandbox.ResetProtected)
	f := newReloadFixture(t)
	ctx := context.Background()
	red, err := f.gw.RedactedConfigFile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(red, "CONDUIT_TEST_ZAI_KEY") || strings.Contains(red, reloadSecret) {
		t.Fatalf("redacted file shows the key reference or secret:\n%s", red)
	}
	proposed := strings.Replace(red, `"max_tool_chains": 25`, `"max_tool_chains": 30`, 1)
	if proposed == red {
		t.Fatal("fixture: max_tool_chains not found in redacted file")
	}
	patch, err := f.gw.ConfigPatchFromFile(ctx, proposed)
	if err != nil {
		t.Fatal(err)
	}
	if len(patch) != 1 || patch["tools"] == nil {
		t.Fatalf("patch = %v, want only tools.max_tool_chains", patch)
	}
	plan, err := f.gw.PlanConfigUpdate(ctx, patch)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 1 || plan.Changes[0].Path != "tools.max_tool_chains" || !plan.Changes[0].SecuritySensitive ||
		plan.Changes[0].OldValue != "25" || plan.Changes[0].Value != "30" {
		t.Fatalf("changes = %+v", plan.Changes)
	}
	if _, err := f.gw.ApplyConfigUpdate(ctx, patch); err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(f.orig, `"max_tool_chains": 25`, `"max_tool_chains": 30`, 1)
	if got := f.file(t); got != want {
		t.Fatalf("file not minimally edited:\n%s\nwant:\n%s", got, want)
	}

	// The same proposal again is no change.
	again, err := f.gw.ConfigPatchFromFile(ctx, proposed)
	if err != nil || len(again) != 0 {
		t.Fatalf("second proposal: patch %v err %v", again, err)
	}
}

func TestConfigFile_RefusesOverwritingSecretWithPlaceholder(t *testing.T) {
	t.Cleanup(sandbox.ResetProtected)
	f := newReloadFixture(t)
	red, err := f.gw.RedactedConfigFile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Edit the providers array (replaced whole), carrying the placeholder.
	proposed := strings.Replace(red, `"timeout_seconds": 300`, `"timeout_seconds": 301`, 1)
	if _, err := f.gw.ConfigPatchFromFile(context.Background(), proposed); err == nil {
		t.Fatal("placeholder written into a changed array was accepted")
	}
	if f.file(t) != f.orig {
		t.Fatal("config changed")
	}
}

// SetConfigPath protects the config, its backup, its snapshot and the
// snapshot's hash record from the file tools.
func TestSetConfigPath_ProtectsApprovalFiles(t *testing.T) {
	t.Cleanup(sandbox.ResetProtected)
	t.Setenv("CONDUIT_DATA_DIR", "")
	f := newReloadFixture(t)
	state := approvedStateDir(f.gw.currentConfig().DataDir)
	for _, p := range []string{f.path, f.path + ".bak", config.ApprovedSnapshotPath(f.path), config.ApprovedRecordPath(f.path, state)} {
		if err := sandbox.CheckWrite(p); !errors.Is(err, sandbox.ErrProtected) {
			t.Errorf("%s not protected: %v", p, err)
		}
	}
}

// An approved update records the new file as approved, so the next start
// accepts it; a later change made outside update_config is refused.
func TestApplyConfigUpdate_RecordsApprovedConfig(t *testing.T) {
	t.Cleanup(sandbox.ResetProtected)
	t.Setenv("CONDUIT_DATA_DIR", "")
	f := newReloadFixture(t)
	state := approvedStateDir(f.gw.currentConfig().DataDir)
	if _, err := config.VerifyApproved(f.path, state); err != nil { // first start
		t.Fatal(err)
	}
	if _, err := f.gw.ApplyConfigUpdate(context.Background(), map[string]interface{}{"tools.max_tool_chains": 30.0}); err != nil {
		t.Fatal(err)
	}
	if st, err := config.VerifyApproved(f.path, state); err != nil || st != config.ApprovedMatch {
		t.Fatalf("after approved write: state %v err %v", st, err)
	}
	edited := strings.Replace(f.file(t), `"max_tool_chains": 30`, `"max_tool_chains": 31`, 1)
	if err := os.WriteFile(f.path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	var ue *config.UnapprovedConfigError
	if _, err := config.VerifyApproved(f.path, state); !errors.As(err, &ue) || len(ue.Keys) != 1 || ue.Keys[0] != "tools.max_tool_chains" {
		t.Fatalf("hand edit: %v", err)
	}
}

func (s *syncBuffer) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.b.Reset()
}
