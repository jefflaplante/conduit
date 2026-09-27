package skills

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// emailCommand builds the shell command the executor would generate for the
// built-in "email" skill with the given action/args.
func emailCommand(t *testing.T, action string, args map[string]interface{}) string {
	t.Helper()
	cmd, err := emailCommandErr(action, args)
	if err != nil {
		t.Fatalf("buildShellCommand(%q, %v) unexpected error: %v", action, args, err)
	}
	return cmd
}

// emailCommandErr is emailCommand without failing on validation errors.
func emailCommandErr(action string, args map[string]interface{}) (string, error) {
	e := NewExecutor(ExecutionConfig{TimeoutSeconds: 10})
	// Default aliases/env vars; pin the binary and workspace so commands are
	// deterministic regardless of the host's PATH (conduit-31jg.40).
	if err := e.ConfigureGog(&GogConfig{Binary: testGogBinary}, testWorkspace); err != nil {
		return "", err
	}
	skill := Skill{Name: "email"}
	return e.buildShellCommand(skill, action, args)
}

// Regression: heading-derived action names (e.g. from SKILL.md headings like
// "send_email_(as_jules_—_via_jules's_own_account)") previously missed every
// case in buildGogCommand, fell through to the echo fallback, and their
// embedded quotes broke the generated shell command (Sep 2026 triage).
func TestNormalizeAction_MapsHeadingDerivedNames(t *testing.T) {
	cases := map[string]string{
		"send_email_(as_jules)": "send",
		"send_email_(as_jeff)":  "send",
		"reply_to_thread":       "send",
		"compose_draft":         "send",
		"organize_inbox":        "cleanup",
		"cleanup_junk":          "cleanup",
		"search_mail":           "search",
		"thread_get":            "read",
		"read_thread":           "read",
		"list_messages":         "list",
		"inbox_check_protocol":  "inbox_check_protocol", // no canonical keyword → unchanged
		"weird_unknown_action":  "weird_unknown_action",
	}

	for in, want := range cases {
		if got := normalizeAction(in); got != want {
			t.Errorf("normalizeAction(%q) = %q, want %q", in, got, want)
		}
	}
}

// The full path: a heading-derived send action on the email skill must build
// a real gog send command, not the echo fallback.
func TestEmailSkill_HeadingDerivedSendActionBuildsGogSend(t *testing.T) {
	cmd := emailCommand(t, "send_email_(as_jules)", map[string]interface{}{
		"to":      "someone@example.com",
		"subject": "Test subject",
		"body":    "Test body",
	})
	if !strings.Contains(cmd, "gog gmail send") {
		t.Errorf("expected gog gmail send in command, got:\n%s", cmd)
	}
	if strings.Contains(cmd, "echo 'Executed action") {
		t.Errorf("heading-derived send fell through to echo fallback:\n%s", cmd)
	}
}

// Safety default (email-safety policy / bd-or5): autonomous sends without an
// explicit identity MUST go from Jules's account, never Jeff's.
func TestEmailSkill_SendDefaultsToJulesAccount(t *testing.T) {
	// No from, no account → Jules
	cmd := emailCommand(t, "send", map[string]interface{}{
		"to": "x@y.z", "subject": "s", "body": "b",
	})
	if !strings.Contains(cmd, `--account "$JULES_ACCOUNT"`) {
		t.Errorf("default send must use $JULES_ACCOUNT, got:\n%s", cmd)
	}

	// Explicit jules account → Jules
	cmd = emailCommand(t, "send", map[string]interface{}{
		"to": "x@y.z", "subject": "s", "body": "b", "account": "jules",
	})
	if !strings.Contains(cmd, `--account "$JULES_ACCOUNT"`) {
		t.Errorf("explicit jules send must use $JULES_ACCOUNT, got:\n%s", cmd)
	}

	// Explicit Jeff identity → Jeff's account (requires Jeff's approval upstream)
	cmd = emailCommand(t, "send", map[string]interface{}{
		"to": "x@y.z", "subject": "s", "body": "b", "from": "owner@example.com",
	})
	if !strings.Contains(cmd, `--account "$GOG_ACCOUNT"`) {
		t.Errorf("explicit jeff from must use $GOG_ACCOUNT, got:\n%s", cmd)
	}

	// Regression: values must be quoted exactly once. getArg pre-quoted them
	// and the send case quoted again, yielding --to ''\''x@y.z'\''' garbage.
	if strings.Contains(cmd, `'\'`) {
		t.Errorf("double-quoting detected in send command:\n%s", cmd)
	}
	if !strings.Contains(cmd, "--to 'x@y.z'") {
		t.Errorf("expected singly-quoted --to value, got:\n%s", cmd)
	}
}

// Cleanup must run the real blocklist junk sweep, not an echo stub.
func TestEmailSkill_CleanupRunsJunkSweep(t *testing.T) {
	cmd := emailCommand(t, "cleanup", map[string]interface{}{})
	if !strings.Contains(cmd, "hygiene-junk-sweep.sh") {
		t.Errorf("cleanup should invoke the junk sweep script, got:\n%s", cmd)
	}
	if strings.Contains(cmd, "echo 'cleanup action") {
		t.Errorf("cleanup is still an echo stub:\n%s", cmd)
	}
}

// No real command exists for an unknown action on a scriptless skill.
// The executor must fail honestly (empty command → executeSubprocess error)
// instead of echoing "Executed action: X" and exiting 0 — the old fallback
// faked success while doing nothing (state-skill silent-failure bug, Sep 2026).
func TestBuildShellCommand_UnknownActionReturnsEmpty(t *testing.T) {
	cmd := emailCommand(t, "totally'bogus'action", map[string]interface{}{})
	if cmd != "" {
		t.Errorf("unknown action must return empty command (honest failure), got:\n%s", cmd)
	}
}

// Real-content skills without scripts must still resolve a command through
// content extraction; known actions must never hit the empty return.
func TestBuildShellCommand_EmailActionsResolveRealCommands(t *testing.T) {
	// Argless-capable actions resolve with empty args
	for _, action := range []string{"search", "list", "cleanup", "status"} {
		cmd := emailCommand(t, action, map[string]interface{}{})
		if cmd == "" {
			t.Errorf("email action %q must resolve a real command, got empty", action)
			continue
		}
		if strings.Contains(cmd, "echo 'Executed action") {
			t.Errorf("email action %q fell through to echo fallback:\n%s", action, cmd)
		}
	}

	// Arg-requiring actions resolve with required args present
	if cmd := emailCommand(t, "read", map[string]interface{}{"message_id": "18c123"}); !strings.Contains(cmd, "gog gmail read") {
		t.Errorf("email read with message_id must resolve gog gmail read, got:\n%s", cmd)
	}
	if cmd := emailCommand(t, "send", map[string]interface{}{"to": "x@y.z", "subject": "s", "body": "b"}); !strings.Contains(cmd, "gog gmail send") {
		t.Errorf("email send with to must resolve gog gmail send, got:\n%s", cmd)
	}
}

// Missing required args must fail honestly (empty command), not fake success.
// Previously argless read/send hit the echo fallback and reported Success:true.
func TestBuildShellCommand_MissingRequiredArgsReturnsEmpty(t *testing.T) {
	for _, action := range []string{"read", "send"} {
		if cmd := emailCommand(t, action, map[string]interface{}{}); cmd != "" {
			t.Errorf("email action %q with missing required args must return empty (honest failure), got:\n%s", action, cmd)
		}
	}
}

// --- conduit-31jg.2: shell injection via gog/email skill args ---

var injectionPayloads = []string{
	"5; touch PWNED",
	"$(touch PWNED)",
	"`touch PWNED`",
	"5' ; touch PWNED ; echo '",
	"5\ntouch PWNED",
	"5 && touch PWNED",
	"5 | touch PWNED",
}

// shellQuote must yield a single literal word for any input under bash.
func TestShellQuote_RoundTripsThroughBash(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	cases := []struct {
		in   string
		want string // expected quoted form
	}{
		{"", "''"},
		{"plain", "'plain'"},
		{"it's", `'it'\''s'`},
		{"''", `''\'''\'''`},
		{"$(touch PWNED)", "'$(touch PWNED)'"},
		{"`touch PWNED`", "'`touch PWNED`'"},
		{"a;b|c&d>e<f", "'a;b|c&d>e<f'"},
		{`back\slash "dq" $HOME *`, `'back\slash "dq" $HOME *'`},
		{"multi\nline", "'multi\nline'"},
		{"x' ; touch PWNED ; echo '", `'x'\'' ; touch PWNED ; echo '\'''`},
	}
	dir := t.TempDir()
	for _, tc := range cases {
		got := shellQuote(tc.in)
		if got != tc.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tc.in, got, tc.want)
		}
		cmd := exec.Command(bash, "-c", "printf '%s' "+got)
		cmd.Dir = dir
		out, err := cmd.Output()
		if err != nil {
			t.Errorf("bash failed for %q: %v", tc.in, err)
			continue
		}
		if string(out) != tc.in {
			t.Errorf("round trip of %q through bash gave %q", tc.in, out)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "PWNED")); err == nil {
		t.Fatal("shellQuote round trip executed an injected command")
	}
}

// Non-numeric max/limit must be rejected (never interpolated).
func TestGogCommand_RejectsInjectedMaxAndLimit(t *testing.T) {
	for _, action := range []string{"search", "list"} {
		for _, key := range []string{"max", "limit"} {
			for _, p := range injectionPayloads {
				cmd, err := emailCommandErr(action, map[string]interface{}{key: p})
				if err == nil {
					t.Errorf("%s %s=%q: expected validation error, got command:\n%s", action, key, p, cmd)
				}
				if strings.Contains(cmd, "PWNED") {
					t.Errorf("%s %s=%q: payload reached command:\n%s", action, key, p, cmd)
				}
			}
		}
	}
	for _, bad := range []interface{}{"abc", "5.5", 5.5, true, []interface{}{1}, map[string]interface{}{}} {
		if _, err := emailCommandErr("search", map[string]interface{}{"max": bad}); err == nil {
			t.Errorf("max=%#v should be rejected", bad)
		}
	}
}

// Unknown account/inbox values must be rejected; they are never interpolated.
func TestGogCommand_RejectsUnknownAccount(t *testing.T) {
	payloads := append([]string{"someone@evil.com", "$GOG_ACCOUNT"}, injectionPayloads...)
	for _, action := range []string{"search", "list", "read", "send", "status"} {
		for _, key := range []string{"account", "inbox"} {
			for _, p := range payloads {
				args := map[string]interface{}{key: p, "message_id": "m1", "to": "x@y.z"}
				if cmd, err := emailCommandErr(action, args); err == nil {
					t.Errorf("%s %s=%q: expected validation error, got:\n%s", action, key, p, cmd)
				}
			}
		}
	}
	if _, err := emailCommandErr("search", map[string]interface{}{"account": 42}); err == nil {
		t.Error("non-string account should be rejected")
	}
}

// Valid numeric forms are accepted and clamped to 1..100.
func TestGogCommand_NumericMaxParsing(t *testing.T) {
	cases := []struct {
		args map[string]interface{}
		want string
	}{
		{map[string]interface{}{}, "--max 20\n"},
		{map[string]interface{}{"max": ""}, "--max 20\n"},
		{map[string]interface{}{"max": "7"}, "--max 7\n"},
		{map[string]interface{}{"max": " 12 "}, "--max 12\n"},
		{map[string]interface{}{"max": float64(15)}, "--max 15\n"},
		{map[string]interface{}{"max": 3}, "--max 3\n"},
		{map[string]interface{}{"max": json.Number("9")}, "--max 9\n"},
		{map[string]interface{}{"limit": "30"}, "--max 30\n"},
		{map[string]interface{}{"limit": float64(40)}, "--max 40\n"},
		{map[string]interface{}{"max": "5", "limit": "50"}, "--max 5\n"},
		{map[string]interface{}{"max": float64(0)}, "--max 1\n"},
		{map[string]interface{}{"max": "-3"}, "--max 1\n"},
		{map[string]interface{}{"max": float64(5000)}, "--max 100\n"},
		{map[string]interface{}{"max": "999999999999"}, "--max 100\n"},
	}
	for _, action := range []string{"search", "list"} {
		for _, tc := range cases {
			cmd := emailCommand(t, action, tc.args)
			if !strings.HasSuffix(cmd, tc.want) {
				t.Errorf("%s %v: want suffix %q, got:\n%s", action, tc.args, tc.want, cmd)
			}
		}
	}
}

// Account values only ever select a fixed, double-quoted env expansion.
func TestGogCommand_AccountSelectionQuoted(t *testing.T) {
	cmd := emailCommand(t, "search", map[string]interface{}{"account": "agent@example.com"})
	if !strings.Contains(cmd, `--account "$JULES_ACCOUNT"`) {
		t.Errorf("jules account not selected/quoted:\n%s", cmd)
	}
	cmd = emailCommand(t, "list", map[string]interface{}{"inbox": "jules"})
	if !strings.Contains(cmd, `--account "$JULES_ACCOUNT"`) {
		t.Errorf("jules inbox not selected/quoted:\n%s", cmd)
	}
	cmd = emailCommand(t, "status", map[string]interface{}{"account": "jeff"})
	if !strings.Contains(cmd, `--account "$GOG_ACCOUNT"`) {
		t.Errorf("owner account not selected/quoted:\n%s", cmd)
	}
	cmd = emailCommand(t, "send", map[string]interface{}{"to": "x@y.z", "account": "owner@example.com"})
	if !strings.Contains(cmd, `--account "$GOG_ACCOUNT"`) {
		t.Errorf("owner send account not selected/quoted:\n%s", cmd)
	}
}

// End-to-end: run the generated script under bash with a fake gog binary
// and confirm free-text payloads arrive as literal argv entries and nothing
// injected executes.
func TestGogCommand_InjectionDoesNotExecute(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	dir := t.TempDir()
	t.Setenv("HOME", dir) // no secrets file sourced
	fakeGog := filepath.Join(dir, "fake-gog")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '[%s]' \"$a\"; done\n"
	if err := os.WriteFile(fakeGog, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	payloads := append([]string{"a'b", `a\'b`, "$HOME", "*"}, injectionPayloads...)
	for _, p := range payloads {
		runs := []struct {
			action string
			args   map[string]interface{}
			want   string
		}{
			{"search", map[string]interface{}{"query": p, "max": "5"},
				"[gmail][search][" + p + "][--account][owner@example.com][--max][5]"},
			{"list", map[string]interface{}{"query": p, "inbox": "jules", "max": float64(3)},
				"[gmail][search][" + p + "][--account][jules@example.com][--max][3]"},
			{"read", map[string]interface{}{"message_id": p},
				"[gmail][read][" + p + "][--account][owner@example.com]"},
			{"read", map[string]interface{}{"thread_id": p},
				"[gmail][thread][get][" + p + "][--account][owner@example.com]"},
			{"send", map[string]interface{}{"to": p, "subject": p, "body": p},
				"[gmail][send][--to][" + p + "][--subject][" + p + "][--body][" + p + "][--account][jules@example.com][--force]"},
		}
		for _, r := range runs {
			cmdStr := emailCommand(t, r.action, r.args)
			cmdStr = strings.ReplaceAll(cmdStr, testGogBinary+" gmail", shellQuote(fakeGog)+" gmail")
			cmd := exec.Command(bash, "-c", cmdStr)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(),
				"GOG_ACCOUNT=owner@example.com", "JULES_ACCOUNT=jules@example.com")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Errorf("%s payload %q: bash error %v, output %q", r.action, p, err, out)
				continue
			}
			if string(out) != r.want {
				t.Errorf("%s payload %q:\n got  %q\n want %q", r.action, p, out, r.want)
			}
			if _, err := os.Stat(filepath.Join(dir, "PWNED")); err == nil {
				t.Fatalf("%s payload %q executed an injected command", r.action, p)
			}
		}
	}
}

// Validation errors surface as a failed ExecutionResult, not a run.
func TestExecuteSkill_InvalidArgsReturnsError(t *testing.T) {
	e := NewExecutor(ExecutionConfig{TimeoutSeconds: 10})
	dir := t.TempDir()
	res, err := e.ExecuteSkill(context.Background(), Skill{Name: "email", Location: dir}, "search",
		map[string]interface{}{"max": "5; touch PWNED"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Success || !strings.Contains(res.Error, "invalid arguments") {
		t.Errorf("expected invalid-arguments failure, got %+v", res)
	}
	if _, err := os.Stat(filepath.Join(dir, "PWNED")); err == nil {
		t.Fatal("injected command executed")
	}
}
