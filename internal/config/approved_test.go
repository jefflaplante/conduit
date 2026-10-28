package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const approvedDoc = `{
  "port": 19999,
  "mqtt": {"enabled": false, "broker_url": "tcp://broker.example.com:1883", "password": "${MQTT_PASSWORD}"},
  "tools": {"enabled_tools": ["Read"], "max_tool_chains": 25}
}
`

func approvedFixture(t *testing.T) (path, state string) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, "config.json")
	state = filepath.Join(dir, "data", "config")
	if err := os.WriteFile(path, []byte(approvedDoc), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, state
}

func mustUnapproved(t *testing.T, err error, reason string) *UnapprovedConfigError {
	t.Helper()
	var ue *UnapprovedConfigError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v, want *UnapprovedConfigError", err)
	}
	if !strings.Contains(ue.Reason, reason) {
		t.Fatalf("reason %q, want %q", ue.Reason, reason)
	}
	if !strings.Contains(err.Error(), "config accept") {
		t.Fatalf("error does not name the accept command:\n%s", err)
	}
	return ue
}

// First run records the file; the same file is then accepted.
func TestVerifyApproved_BootstrapThenMatch(t *testing.T) {
	path, state := approvedFixture(t)
	if st, err := VerifyApproved(path, state); err != nil || st != ApprovedBootstrapped {
		t.Fatalf("first run: %v %v", st, err)
	}
	for _, p := range []string{ApprovedSnapshotPath(path), ApprovedRecordPath(path, state)} {
		st, err := os.Stat(p)
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", p, st, err)
		}
	}
	if st, err := VerifyApproved(path, state); err != nil || st != ApprovedMatch {
		t.Fatalf("second run: %v %v", st, err)
	}
}

// A changed file is refused with key names, the sensitive ones and the
// lowered approval, never values.
func TestVerifyApproved_RefusesChangedConfig(t *testing.T) {
	path, state := approvedFixture(t)
	if _, err := VerifyApproved(path, state); err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(approvedDoc, `"enabled": false,`, `"enabled": false, "approval": {"required": false},`, 1)
	edited = strings.Replace(edited, `"port": 19999`, `"port": 19998`, 1)
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := VerifyApproved(path, state)
	ue := mustUnapproved(t, err, "changed outside update_config")
	if strings.Join(ue.Keys, ",") != "mqtt.approval,port" {
		t.Errorf("keys = %v", ue.Keys)
	}
	if strings.Join(ue.Sensitive, ",") != "mqtt.approval" {
		t.Errorf("sensitive = %v", ue.Sensitive)
	}
	if len(ue.Weakenings) != 1 || !strings.Contains(err.Error(), "LOWERS HUMAN APPROVAL") {
		t.Errorf("weakenings = %v\n%s", ue.Weakenings, err)
	}
	if strings.Contains(err.Error(), "19998") {
		t.Errorf("error shows a value:\n%s", err)
	}
}

// Deleting the snapshot after it existed is refused, not re-bootstrapped.
func TestVerifyApproved_RefusesMissingSnapshot(t *testing.T) {
	path, state := approvedFixture(t)
	if _, err := VerifyApproved(path, state); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(ApprovedSnapshotPath(path)); err != nil {
		t.Fatal(err)
	}
	_, err := VerifyApproved(path, state)
	mustUnapproved(t, err, "missing")
}

func TestVerifyApproved_RefusesCorruptOrEditedSnapshot(t *testing.T) {
	for name, content := range map[string]string{
		"corrupt": "{not json",
		"edited":  strings.Replace(approvedDoc, "19999", "19998", 1),
	} {
		t.Run(name, func(t *testing.T) {
			path, state := approvedFixture(t)
			if _, err := VerifyApproved(path, state); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(ApprovedSnapshotPath(path), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := VerifyApproved(path, state)
			mustUnapproved(t, err, map[string]string{"corrupt": "corrupt", "edited": "recorded hash"}[name])
		})
	}
}

// Rewriting config and snapshot together still fails: the hash record in
// the data directory does not match.
func TestVerifyApproved_ForgedSnapshotAndConfig(t *testing.T) {
	path, state := approvedFixture(t)
	if _, err := VerifyApproved(path, state); err != nil {
		t.Fatal(err)
	}
	forged := strings.Replace(approvedDoc, "25", "99", 1)
	for _, p := range []string{path, ApprovedSnapshotPath(path)} {
		if err := os.WriteFile(p, []byte(forged), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, err := VerifyApproved(path, state)
	mustUnapproved(t, err, "recorded hash")
}

// A snapshot left by an older build (no hash record) equal to the config
// is accepted and gains a record.
func TestVerifyApproved_SnapshotWithoutRecord(t *testing.T) {
	path, state := approvedFixture(t)
	if err := os.WriteFile(ApprovedSnapshotPath(path), []byte(approvedDoc), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if st, err := VerifyApproved(path, state); err != nil || st != ApprovedMatch {
		t.Fatalf("%v %v", st, err)
	}
	if _, err := os.Stat(ApprovedRecordPath(path, state)); err != nil {
		t.Fatal("record not written")
	}
}

func TestDiffConfigFiles_RedactsSecrets(t *testing.T) {
	old := []byte(`{"mqtt": {"password": "hunter2-OLD"}, "ai": {"providers": [{"name": "acme", "api_key": "sk-acme-OLD"}]}, "port": 1}`)
	next := []byte(`{"mqtt": {"password": "hunter2-NEW"}, "ai": {"providers": [{"name": "acme", "api_key": "sk-acme-NEW", "model": "m"}]}}`)
	lines := DiffConfigFiles(old, next)
	var all strings.Builder
	for _, l := range lines {
		all.WriteString(l.Path + " " + l.Old + " " + l.New + "\n")
	}
	got := all.String()
	for _, leak := range []string{"OLD", "NEW"} {
		if strings.Contains(got, leak) {
			t.Fatalf("diff leaks a secret:\n%s", got)
		}
	}
	for _, want := range []string{"ai.providers", "mqtt.password", "port 1 (absent)"} {
		if !strings.Contains(got, want) {
			t.Errorf("diff lacks %q:\n%s", want, got)
		}
	}
}
