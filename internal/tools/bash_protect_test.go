package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"conduit/internal/sandbox"
)

// protectFixture protects $HOME/app/config.json (HOME is a temp dir) and
// returns its path and a working directory that holds an unrelated
// other/config.json and a symlink to the protected file.
func protectFixture(t *testing.T) (cfg, cwd string) {
	t.Helper()
	t.Cleanup(sandbox.ResetProtected)
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg = filepath.Join(home, "app", "config.json")
	cwd = filepath.Join(home, "work")
	for _, d := range []string{filepath.Dir(cfg), filepath.Join(cwd, "other")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{cfg, filepath.Join(cwd, "other", "config.json")} {
		if err := os.WriteFile(f, []byte(`{"port": 19999}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(cfg, filepath.Join(cwd, "cfglink")); err != nil {
		t.Fatal(err)
	}
	sandbox.Protect(cfg)
	return cfg, cwd
}

func TestProtectedPathDenial_Bypasses(t *testing.T) {
	cfg, cwd := protectFixture(t)
	for _, cmd := range []string{
		"echo x > ~/app/config.json",
		"echo x >> $HOME/app/config.json",
		"echo x > ${HOME}/app/config.json",
		"echo x >| " + cfg,
		"echo x | tee " + cfg,
		"echo x | tee -a ~/app/config.json",
		"cp /tmp/x ~/app/config.json",
		"install -m 600 /tmp/x ~/app/config.json",
		"mv /tmp/x " + cfg,
		"mv ~/app ~/app2",
		"rm -rf ~/app",
		"sed -i 's/19999/1/' ~/app/config.json",
		"perl -pi -e 's/a/b/' ~/app/config.json",
		"dd if=/dev/zero of=~/app/config.json count=1",
		"truncate -s0 ~/app/config.json",
		"ln -f " + cfg + " /tmp/hard",
		`python3 -c "open('` + cfg + `','w').write('{}')"`,
		"sh -c 'echo x > ~/app/config.json'",
		`bash -lc "cp /tmp/x ../app/config.json"`,
		`eval "echo x > ~/app/config.json"`,
		"env -S 'tee ~/app/config.json'",
		"sudo tee ~/app/config.json",
		"cd ~/app && sed -i s/a/b/ config.json",
		"cd ../app; echo x > config.json",
		"pushd ~/app && cp /tmp/x .",
		"echo x > cfglink",
		"cp /tmp/x ~/app/conf*.json",
		"rm ~/ap?/*",
		"F=~/app/config.json; echo x > $F",
		"export F=$HOME/app/config.json",
		`cp /tmp/x "$D/config.json"`,
		"find ~ -name config.json -delete",
		"cat <<'EOF' | python3\nopen('" + cfg + "','w')\nEOF",
		"echo x > \"$(echo ~/app/config.json)\"",
	} {
		if why := protectedPathDenial(cmd, cwd); why == "" {
			t.Errorf("not refused: %s", cmd)
		}
	}
}

func TestProtectedPathDenial_AllowsReadsAndOtherFiles(t *testing.T) {
	_, cwd := protectFixture(t)
	for _, cmd := range []string{
		"cat ~/app/config.json",
		"jq .port ~/app/config.json",
		"grep -n port ~/app/config.json | head -5",
		"diff ~/app/config.json other/config.json",
		"sha256sum ~/app/config.json",
		"ls -la ~/app",
		"cat ~/app/config.json > /tmp/copy.json",
		"cd ~/app && cat config.json",
		"sed -i s/a/b/ other/config.json",
		"cp other/config.json /tmp/x.json",
		"go test ./...",
		"echo hi > out.txt",
		"make build",
	} {
		if why := protectedPathDenial(cmd, cwd); why != "" {
			t.Errorf("refused %q: %s", cmd, why)
		}
	}
}

func TestProtectedPathDenial_NothingProtected(t *testing.T) {
	sandbox.ResetProtected()
	if why := protectedPathDenial("echo x > ~/app/config.json", t.TempDir()); why != "" {
		t.Fatalf("refused with nothing protected: %s", why)
	}
}

// End to end through the Bash tool: the command is refused and the file
// is unchanged.
func TestExecTool_RefusesProtectedConfigWrite(t *testing.T) {
	cfg, _ := protectFixture(t)
	tool := newExecToolForTest(t, nil)
	res, err := tool.Execute(context.Background(), map[string]interface{}{"command": "echo pwned > " + cfg})
	if err != nil {
		t.Fatal(err)
	}
	if res.Success || res.ErrorDetails == nil || res.ErrorDetails.Type != "path_protected" {
		t.Fatalf("result = %+v, want path_protected", res)
	}
	if b, _ := os.ReadFile(cfg); string(b) != `{"port": 19999}` {
		t.Fatalf("config changed: %s", b)
	}
}

// The Write tool refuses the protected file by any name.
func TestWriteFileTool_RefusesProtectedFile(t *testing.T) {
	registry, tempDir := setupTestRegistry(t, "workspace", "sandbox")
	t.Cleanup(sandbox.ResetProtected)
	cfg := filepath.Join(tempDir, "workspace", "config.json")
	if err := os.WriteFile(cfg, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	sandbox.Protect(cfg)
	link := filepath.Join(tempDir, "link.json")
	hard := filepath.Join(tempDir, "hard.json")
	if err := os.Symlink(cfg, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(cfg, hard); err != nil {
		t.Fatal(err)
	}
	tool := &WriteFileTool{registry: registry}
	for _, p := range []string{cfg, "config.json", "sub/../config.json", link, hard} {
		res, err := tool.Execute(context.Background(), map[string]interface{}{"path": p, "content": "pwned"})
		if err != nil {
			t.Fatal(err)
		}
		if res.Success || res.ErrorDetails == nil || res.ErrorDetails.Type != "path_protected" {
			t.Errorf("Write %s: %+v, want path_protected", p, res)
		}
	}
	if b, _ := os.ReadFile(cfg); string(b) != "{}" {
		t.Fatalf("config changed: %s", b)
	}
	res, _ := tool.Execute(context.Background(), map[string]interface{}{"path": "notes.md", "content": "ok"})
	if !res.Success {
		t.Fatalf("unprotected write failed: %+v", res)
	}
}

// Approving the config is the owner's step: the Bash tool refuses
// `conduit config accept` however it is spelled or wrapped, and any command
// that names the tool-shell marker.
func TestProtectedPathDenial_ConfigAccept(t *testing.T) {
	sandbox.ResetProtected() // refused even with nothing protected
	cwd := t.TempDir()
	for _, cmd := range []string{
		"conduit config accept",
		"./bin/conduit --config ~/app/config.json config accept --yes",
		"go run ./cmd/gateway config  accept --yes",
		`conduit "config" 'accept' --yes`,
		`sh -c "conduit config accept --yes"`,
		"echo yes | conduit config acc\\ept",
		"CONDUIT_TOOL_SHELL= conduit config show",
		"env -u CONDUIT_TOOL_SHELL ./bin/conduit",
		"unset CONDUIT_TOOL_SHELL; true",
	} {
		if why := protectedPathDenial(cmd, cwd); why == "" {
			t.Errorf("not refused: %s", cmd)
		}
	}
	for _, cmd := range []string{"conduit version", "conduit config --help", "grep -rn accept internal/config"} {
		if why := protectedPathDenial(cmd, cwd); why != "" {
			t.Errorf("refused %q: %s", cmd, why)
		}
	}
}

// Every command the Bash tool runs carries the tool-shell marker, which
// `conduit config accept` checks.
func TestExecTool_SetsToolShellMarker(t *testing.T) {
	sandbox.ResetProtected()
	tool := newExecToolForTest(t, nil)
	res, err := tool.Execute(context.Background(), map[string]interface{}{"command": "printenv"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Success || !strings.Contains(res.Content, "CONDUIT_TOOL_SHELL=1") {
		t.Fatalf("marker missing: %+v", res)
	}
}

// System cron jobs run outside the gateway, so their command gets the same
// protected-file and accept checks.
func TestScheduledCommandDenial_ProtectedConfigAndAccept(t *testing.T) {
	cfg, _ := protectFixture(t)
	tool := newExecToolForTest(t, nil)
	for _, cmd := range []string{"echo x > " + cfg, "conduit config accept --yes"} {
		if why := tool.scheduledCommandDenial(context.Background(), cmd); why == "" {
			t.Errorf("cron command not refused: %s", cmd)
		}
	}
	if why := tool.scheduledCommandDenial(context.Background(), "echo ok"); why != "" {
		t.Errorf("harmless cron command refused: %s", why)
	}
}
