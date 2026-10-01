package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"conduit/internal/policy"
)

func writeTestDecisions(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "logs", policy.DecisionLogFile)
	rec, err := policy.NewFileRecorder(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	rec.Record(policy.Record{Time: now.Add(-48 * time.Hour), Tool: "Message", Class: policy.MessageGroup, Decision: policy.Ask})
	rec.Record(policy.Record{Time: now.Add(-2 * time.Hour), Tool: "Message", Class: policy.MessageSelf, Decision: policy.Allow, Origin: "telegram"})
	rec.Record(policy.Record{Time: now.Add(-time.Hour), Tool: "Message", Class: policy.MessageGroup, Target: "telegram:-100", Decision: policy.Deny, Rule: "class:message.group+non_interactive", Origin: "cron", Purpose: "weekly digest"})
	return path
}

func TestPolicyReport_Text(t *testing.T) {
	path := writeTestDecisions(t)
	cmd := PolicyRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"report", "--log", path})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{"decisions since", ": 2\n", "message.group", "message.self", "telegram:-100", "weekly digest", "none of these calls were blocked"} {
		if !strings.Contains(s, want) {
			t.Errorf("report missing %q:\n%s", want, s)
		}
	}
}

func TestPolicyReport_JSONAndSince(t *testing.T) {
	path := writeTestDecisions(t)
	cmd := PolicyRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"report", "--log", path, "--since", "72h", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Total   int             `json:"total"`
		Flagged []policy.Record `json:"flagged"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Total != 3 || len(got.Flagged) != 2 || got.Flagged[0].Decision != policy.Deny {
		t.Fatalf("report = %+v (flagged newest first)", got)
	}
}

func TestPolicyReport_EmptyLog(t *testing.T) {
	cmd := PolicyRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"report", "--log", filepath.Join(t.TempDir(), "none.jsonl")})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No decisions recorded") {
		t.Fatalf("got %q", out.String())
	}
}
