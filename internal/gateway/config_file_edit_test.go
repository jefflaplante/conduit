package gateway

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

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

// SetConfigPath protects the config, its backup and its snapshot from the
// file tools, and records the first approved snapshot.
func TestSetConfigPath_ProtectsAndBaselines(t *testing.T) {
	t.Cleanup(sandbox.ResetProtected)
	f := newReloadFixture(t)
	for _, p := range []string{f.path, f.path + ".bak", approvedSnapshotPath(f.path)} {
		if err := sandbox.CheckWrite(p); !errors.Is(err, sandbox.ErrProtected) {
			t.Errorf("%s not protected: %v", p, err)
		}
	}
	snap, err := os.ReadFile(approvedSnapshotPath(f.path))
	if err != nil || string(snap) != f.orig {
		t.Fatalf("baseline snapshot = %q, %v", snap, err)
	}
	if st, _ := os.Stat(approvedSnapshotPath(f.path)); st.Mode().Perm() != 0o600 {
		t.Errorf("snapshot mode %v, want 0600", st.Mode().Perm())
	}
}

// An approved update moves the snapshot; a later change made outside
// update_config is logged at ERROR at the next start, with key names (no
// values) and the lowered approval, and then becomes the new baseline.
func TestConfigSnapshot_DetectsUnapprovedChange(t *testing.T) {
	t.Cleanup(sandbox.ResetProtected)
	f := newReloadFixture(t)
	if _, err := f.gw.ApplyConfigUpdate(context.Background(), map[string]interface{}{"tools.max_tool_chains": 30.0}); err != nil {
		t.Fatal(err)
	}
	snap, _ := os.ReadFile(approvedSnapshotPath(f.path))
	if string(snap) != f.file(t) {
		t.Fatal("snapshot not updated after an approved write")
	}

	// Restart with the approved file: nothing logged.
	f.logs.Reset()
	f.gw.SetConfigPath(f.path)
	if strings.Contains(f.logs.String(), "outside update_config") {
		t.Fatalf("approved file flagged:\n%s", f.logs.String())
	}

	// A hand edit (or an uncaught tool write) that turns MQTT approval off.
	edited := strings.Replace(f.file(t), `"tools": {`, `"mqtt": {"approval": {"required": false}},
  "tools": {`, 1)
	if err := os.WriteFile(f.path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	f.logs.Reset()
	f.gw.SetConfigPath(f.path)
	logs := f.logs.String()
	for _, want := range []string{"level=ERROR", "outside update_config", "mqtt.approval.required", "approval_lowered", "turned off"} {
		if !strings.Contains(logs, want) {
			t.Errorf("log lacks %q:\n%s", want, logs)
		}
	}
	if strings.Contains(logs, reloadSecret) {
		t.Fatal("log leaks a secret")
	}

	// Logged once: the edited file is the new baseline.
	f.logs.Reset()
	f.gw.SetConfigPath(f.path)
	if strings.Contains(f.logs.String(), "outside update_config") {
		t.Fatal("same change logged twice")
	}
}

func (s *syncBuffer) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.b.Reset()
}
