package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"conduit/internal/config"
)

const acceptDoc = `{
  "port": 19999,
  "mqtt": {"enabled": false, "broker_url": "tcp://broker.example.com:1883", "password": "${MQTT_PASSWORD}"},
  "tools": {"enabled_tools": ["Read"], "max_tool_chains": 25}
}
`

// acceptFixture is an approved config that was then hand-edited: the port,
// the MQTT password reference and MQTT approval changed.
func acceptFixture(t *testing.T) acceptEnv {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	state := filepath.Join(dir, "state")
	if err := os.WriteFile(path, []byte(acceptDoc), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.RecordApproved(path, state, []byte(acceptDoc)); err != nil {
		t.Fatal(err)
	}
	edited := strings.NewReplacer(`"port": 19999`, `"port": 19998`,
		`"${MQTT_PASSWORD}"}`, `"${MQTT_PASSWORD_NEW}", "approval": {"required": false}}`).Replace(acceptDoc)
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	return acceptEnv{path: path, stateDir: state, out: &bytes.Buffer{}, interactive: true}
}

func verify(e acceptEnv) error {
	_, err := config.VerifyApproved(e.path, e.stateDir)
	return err
}

func TestConfigAccept_ShowsRedactedDiffAndApprovesOnYes(t *testing.T) {
	e := acceptFixture(t)
	e.in = strings.NewReader("yes\n")
	if err := runConfigAccept(e); err != nil {
		t.Fatal(err)
	}
	out := e.out.(*bytes.Buffer).String()
	for _, want := range []string{"port: 19999 -> 19998", "mqtt.password: [redacted] -> [redacted]", "SECURITY-SENSITIVE", "LOWERS HUMAN APPROVAL", "Approved"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "MQTT_PASSWORD") {
		t.Errorf("output shows the secret reference:\n%s", out)
	}
	if err := verify(e); err != nil {
		t.Fatalf("not approved after accept: %v", err)
	}
}

func TestConfigAccept_AnythingButYesChangesNothing(t *testing.T) {
	for _, answer := range []string{"y\n", "no\n", "\n", ""} {
		e := acceptFixture(t)
		e.in = strings.NewReader(answer)
		if err := runConfigAccept(e); !errors.Is(err, errNotApproved) {
			t.Errorf("answer %q: err %v", answer, err)
		}
		if verify(e) == nil {
			t.Errorf("answer %q approved the config", answer)
		}
	}
}

// Without a terminal it needs --yes; with --yes it does not prompt.
func TestConfigAccept_NonInteractiveNeedsYes(t *testing.T) {
	e := acceptFixture(t)
	e.interactive, e.in = false, strings.NewReader("yes\n")
	if err := runConfigAccept(e); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("err = %v", err)
	}
	if verify(e) == nil {
		t.Fatal("approved without a terminal or --yes")
	}
	e.yes = true
	if err := runConfigAccept(e); err != nil {
		t.Fatal(err)
	}
	if err := verify(e); err != nil {
		t.Fatal(err)
	}
}

// From the agent's shell it refuses even with --yes.
func TestConfigAccept_RefusesInAgentShell(t *testing.T) {
	e := acceptFixture(t)
	e.yes, e.agentShell = true, config.ToolShellEnv+" is set"
	if err := runConfigAccept(e); err == nil || !strings.Contains(err.Error(), "gateway's own tools") {
		t.Fatalf("err = %v", err)
	}
	if verify(e) == nil {
		t.Fatal("approved from the agent shell")
	}
}

func TestAgentShellReason_EnvMarker(t *testing.T) {
	t.Setenv(config.ToolShellEnv, "")
	os.Unsetenv(config.ToolShellEnv)
	if r := agentShellReason(); r != "" {
		t.Fatalf("outside a tool shell: %q", r)
	}
	t.Setenv(config.ToolShellEnv, "1")
	if r := agentShellReason(); r == "" {
		t.Fatal("marker not detected")
	}
}

// A missing or corrupt snapshot can be repaired by accepting the file;
// an invalid config cannot be accepted.
func TestConfigAccept_RepairsSnapshotAndRefusesInvalidConfig(t *testing.T) {
	e := acceptFixture(t)
	if err := os.WriteFile(config.ApprovedSnapshotPath(e.path), []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.yes = true
	if err := runConfigAccept(e); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.out.(*bytes.Buffer).String(), "corrupt") {
		t.Errorf("output does not explain the snapshot problem:\n%s", e.out)
	}
	if err := verify(e); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(e.path, []byte(`{"port": "nope"`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runConfigAccept(e); err == nil {
		t.Fatal("invalid config accepted")
	}
}

// The server's startup check: first start records, a hand edit refuses
// with the accept command in the message.
func TestVerifyApprovedConfig_Startup(t *testing.T) {
	t.Setenv("CONDUIT_DATA_DIR", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(acceptDoc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{DataDir: filepath.Join(dir, "data")}
	if err := verifyApprovedConfig(path, cfg); err != nil {
		t.Fatalf("first start: %v", err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(acceptDoc, "25", "26", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	err := verifyApprovedConfig(path, cfg)
	if err == nil || !strings.Contains(err.Error(), "conduit --config "+path+" config accept") || !strings.Contains(err.Error(), "tools.max_tool_chains") {
		t.Fatalf("err = %v", err)
	}
}
