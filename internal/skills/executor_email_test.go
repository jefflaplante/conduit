package skills

import (
	"strings"
	"testing"
)

// emailCommand builds the shell command the executor would generate for the
// built-in "email" skill with the given action/args.
func emailCommand(t *testing.T, action string, args map[string]interface{}) string {
	t.Helper()
	e := NewExecutor(ExecutionConfig{TimeoutSeconds: 10})
	skill := Skill{Name: "email"}
	return e.buildShellCommand(skill, action, args)
}

// Regression: heading-derived action names (e.g. from SKILL.md headings like
// "send_email_(as_jules_—_via_jules's_own_account)") previously missed every
// case in buildGogCommand, fell through to the echo fallback, and their
// embedded quotes broke the generated shell command (Sep 2026 triage).
func TestNormalizeAction_MapsHeadingDerivedNames(t *testing.T) {
	cases := map[string]string{
		"send_email_(as_jules)":  "send",
		"send_email_(as_jeff)":   "send",
		"reply_to_thread":        "send",
		"compose_draft":          "send",
		"organize_inbox":         "cleanup",
		"cleanup_junk":           "cleanup",
		"search_mail":            "search",
		"thread_get":             "read",
		"read_thread":            "read",
		"list_messages":          "list",
		"inbox_check_protocol":   "inbox_check_protocol", // no canonical keyword → unchanged
		"weird_unknown_action":   "weird_unknown_action",
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
	if !strings.Contains(cmd, "--account $JULES_ACCOUNT") {
		t.Errorf("default send must use $JULES_ACCOUNT, got:\n%s", cmd)
	}

	// Explicit jules account → Jules
	cmd = emailCommand(t, "send", map[string]interface{}{
		"to": "x@y.z", "subject": "s", "body": "b", "account": "jules",
	})
	if !strings.Contains(cmd, "--account $JULES_ACCOUNT") {
		t.Errorf("explicit jules send must use $JULES_ACCOUNT, got:\n%s", cmd)
	}

	// Explicit Jeff identity → Jeff's account (requires Jeff's approval upstream)
	cmd = emailCommand(t, "send", map[string]interface{}{
		"to": "x@y.z", "subject": "s", "body": "b", "from": "jeff@laplante.dev",
	})
	if !strings.Contains(cmd, "--account $GOG_ACCOUNT") {
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

// The final-fallback echo must shell-quote the action so embedded quotes
// can't break the generated command.
func TestBuildShellCommand_FallbackEchoIsQuoted(t *testing.T) {
	// Unknown action on the email skill → buildGogCommand returns false,
	// no content/scripts → falls to the final echo.
	cmd := emailCommand(t, "totally'bogus'action", map[string]interface{}{})
	if !strings.Contains(cmd, "echo 'Executed action:") {
		t.Errorf("expected fallback echo, got:\n%s", cmd)
	}
	if strings.Contains(cmd, "action: totally'bogus'action'") {
		t.Errorf("fallback echo does not escape embedded quote — command would break:\n%s", cmd)
	}
	if !strings.Contains(cmd, `'\''`) {
		t.Errorf("expected escaped quote sequence in echo, got:\n%s", cmd)
	}
}
