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

// readOnlyConfigFixture is the container layout: the config file lives in a
// read-only directory (an image's /etc), the state dir is writable (/data).
func readOnlyConfigFixture(t *testing.T) (path, state string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod cannot make a directory unwritable")
	}
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "etc", "conduit")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(cfgDir, "config.json")
	if err := os.WriteFile(path, []byte(approvedDoc), 0o644); err != nil {
		t.Fatal(err)
	}
	state = filepath.Join(dir, "data", "config")
	t.Cleanup(func() { os.Chmod(cfgDir, 0o755) }) // before TempDir cleanup (LIFO)
	if err := os.Chmod(cfgDir, 0o555); err != nil {
		t.Fatal(err)
	}
	return path, state
}

// First start with a read-only config dir (the container case): the
// bootstrap must still succeed, recording the snapshot in the state dir.
func TestVerifyApproved_BootstrapWithReadOnlyConfigDir(t *testing.T) {
	path, state := readOnlyConfigFixture(t)
	st, err := VerifyApproved(path, state)
	if err != nil || st != ApprovedBootstrapped {
		t.Fatalf("first start: %v %v", st, err)
	}
	for _, p := range []string{ApprovedStateSnapshotPath(path, state), ApprovedRecordPath(path, state)} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", p, fi, err)
		}
	}
	if _, err := os.Stat(ApprovedSnapshotPath(path)); !os.IsNotExist(err) {
		t.Errorf("next-to-config snapshot exists despite read-only dir: %v", err)
	}
	// Second start: the state-dir snapshot is honored as the approved one.
	if st, err := VerifyApproved(path, state); err != nil || st != ApprovedMatch {
		t.Fatalf("second start: %v %v", st, err)
	}
}

// The state-dir snapshot keeps the gate's teeth: a config changed outside
// update_config is refused, naming the state-dir snapshot as the last
// approved version.
func TestVerifyApproved_StateDirSnapshotRefusesChangedConfig(t *testing.T) {
	path, state := approvedFixture(t)
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ApprovedStateSnapshotPath(path, state), []byte(approvedDoc), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ApprovedRecordPath(path, state), []byte(hashHex([]byte(approvedDoc))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(approvedDoc, `"port": 19999`, `"port": 19998`, 1)
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := VerifyApproved(path, state)
	ue := mustUnapproved(t, err, "changed outside update_config")
	if ue.Snapshot != ApprovedStateSnapshotPath(path, state) {
		t.Errorf("snapshot = %q, want the state-dir snapshot", ue.Snapshot)
	}
	// Editing the state-dir snapshot against its record is refused too.
	if err := os.WriteFile(path, []byte(approvedDoc), 0o600); err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(approvedDoc, "19999", "19998", 1)
	if err := os.WriteFile(ApprovedStateSnapshotPath(path, state), []byte(tampered), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = VerifyApproved(path, state)
	mustUnapproved(t, err, "recorded hash")
}

// Deleting both snapshots while the record exists is still refused, and the
// message says where the snapshot was looked for.
func TestVerifyApproved_RefusesMissingSnapshotInBothLocations(t *testing.T) {
	path, state := readOnlyConfigFixture(t)
	if _, err := VerifyApproved(path, state); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(ApprovedStateSnapshotPath(path, state)); err != nil {
		t.Fatal(err)
	}
	_, err := VerifyApproved(path, state)
	ue := mustUnapproved(t, err, "missing")
	if !strings.Contains(ue.Reason, ApprovedStateSnapshotPath(path, state)) {
		t.Errorf("reason does not name the state-dir snapshot:\n%s", ue.Reason)
	}
}

// With nowhere writable at all the gate fails closed, naming the state dir,
// instead of starting without recording anything.
func TestVerifyApproved_NoWritableLocationFailsClosed(t *testing.T) {
	path, state := readOnlyConfigFixture(t)
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(state, 0o755) })
	if err := os.Chmod(state, 0o555); err != nil {
		t.Fatal(err)
	}
	_, err := VerifyApproved(path, state)
	if err == nil {
		t.Fatal("bootstrap succeeded with nothing writable")
	}
	if !strings.Contains(err.Error(), state) {
		t.Errorf("error does not name the state dir:\n%s", err)
	}
	var ue *UnapprovedConfigError
	if errors.As(err, &ue) {
		t.Errorf("unwritable disk reported as an approval mismatch:\n%s", err)
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
