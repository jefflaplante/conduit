package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeScript creates an executable script in dir and returns the Skill.
func writeScript(t *testing.T, dir, filename, content string) Skill {
	t.Helper()
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return Skill{
		Name:     "test-skill-" + strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename)),
		Location: dir,
		Scripts: []SkillScript{
			{
				Name:     strings.TrimSuffix(filename, filepath.Ext(filename)),
				Path:     filename,
				Language: "bash",
			},
		},
	}
}

// TestExecutor_ScriptReceivesActionOnStdin verifies that script-executed skills
// receive the action name via stdin JSON envelope even when no args are passed
// (previously: stdin only piped when len(args) > 0, so zero-arg actions
// left scripts with no input at all).
func TestExecutor_ScriptReceivesActionOnStdin(t *testing.T) {
	dir := t.TempDir()
	skill := writeScript(t, dir, "dispatch.sh", `#!/bin/bash
line=$(cat)
action=$(echo "$line" | python3 -c "import sys,json; print(json.load(sys.stdin).get('action','MISSING'))")
echo "ACTION=$action"
`)

	e := NewExecutor(ExecutionConfig{TimeoutSeconds: 10})
	result, err := e.ExecuteSkill(context.Background(), skill, "status", map[string]interface{}{})
	if err != nil {
		t.Fatalf("ExecuteSkill error: %v", err)
	}
	if !result.Success {
		t.Fatalf("script failed: %s (output: %s)", result.Error, result.Output)
	}
	if !strings.Contains(result.Output, "ACTION=status") {
		t.Errorf("action not forwarded on stdin; got output: %q", result.Output)
	}
}

// TestExecutor_ScriptStdinEnvelopeMergesArgs verifies args merge into the
// envelope alongside the action key.
func TestExecutor_ScriptStdinEnvelopeMergesArgs(t *testing.T) {
	dir := t.TempDir()
	skill := writeScript(t, dir, "dispatch.sh", `#!/bin/bash
line=$(cat)
echo "$line" | python3 -c "
import sys, json
d = json.load(sys.stdin)
assert d['action'] == 'collect', d
assert d['payload'] == 'abc123', d
print('OK')
"
`)

	e := NewExecutor(ExecutionConfig{TimeoutSeconds: 10})
	result, err := e.ExecuteSkill(context.Background(), skill, "collect", map[string]interface{}{"payload": "abc123"})
	if err != nil {
		t.Fatalf("ExecuteSkill error: %v", err)
	}
	if !result.Success {
		t.Fatalf("script failed: %s (output: %s)", result.Error, result.Output)
	}
	if !strings.Contains(result.Output, "OK") {
		t.Errorf("envelope assertion failed; output: %q", result.Output)
	}
}

// TestExecutor_SubprocessNoStdinWithoutArgs verifies the subprocess path
// keeps legacy behavior: no stdin piped when there are no args. Uses a
// stdin-sensitive probe command instead of the removed echo fallback —
// if args JSON were piped with empty args, the probe exits 1.
func TestExecutor_SubprocessNoStdinWithoutArgs(t *testing.T) {
	dir := t.TempDir()
	probe := `if [ -t 0 ]; then echo NO_STDIN_OK; else data=$(cat); if [ -z "$data" ]; then echo NO_STDIN_OK; else echo "STDIN_LEAKED: $data"; exit 1; fi; fi`
	skill := Skill{
		Name:     "subproc-skill",
		Location: dir,
		Content:  "# Test skill\n```\n" + probe + "\n```\n",
		// No scripts → subprocess method
	}

	e := NewExecutor(ExecutionConfig{TimeoutSeconds: 10})
	result, err := e.ExecuteSkill(context.Background(), skill, "echo", map[string]interface{}{})
	if err != nil {
		t.Fatalf("ExecuteSkill error: %v", err)
	}
	if !result.Success {
		t.Fatalf("subprocess failed: %s (output: %s)", result.Error, result.Output)
	}
	if strings.Contains(result.Output, "STDIN_LEAKED") {
		t.Errorf("args JSON leaked to stdin with empty args: %q", result.Output)
	}
	if !strings.Contains(result.Output, "NO_STDIN_OK") {
		t.Errorf("probe did not run (no command resolved?); got: %q", result.Output)
	}
}
