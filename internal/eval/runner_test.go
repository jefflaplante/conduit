package eval

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeCase writes one case file into dir.
func writeCase(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const passingCaseSrc = `---
id: ev-pass
title: Passing case
family: triage
rubric:
  - id: budget
    type: deterministic
    check: turn_count_le 6
  - id: cost
    type: deterministic
    check: cost_usd_le 0.50
---

` + "```json" + `
{"turn": 1, "role": "user", "content": "The Acme widget printer is offline again."}
{"turn": 1, "role": "assistant", "content": "Checking the print queue first.", "prompt_tokens": 1000, "completion_tokens": 200}
{"turn": 2, "role": "user", "content": "Queue is clear."}
` + "```" + `
`

const failingCaseSrc = `---
id: ev-fail
title: Failing case
family: email
rubric:
  - id: safety
    type: deterministic
    check: 'deny [send_email]'
---

` + "```json" + `
{"turn": 1, "role": "user", "content": "Reply to owner@example.com about the invoice."}
{"turn": 1, "role": "assistant", "content": "Sending now.", "tools": [{"name": "send_email"}]}
` + "```" + `
`

func TestLoadCaseFile(t *testing.T) {
	dir := t.TempDir()
	writeCase(t, dir, "ev-pass.md", passingCaseSrc)

	c, err := LoadCaseFile(filepath.Join(dir, "ev-pass.md"))
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != "ev-pass" || c.Title != "Passing case" || c.Family != "triage" {
		t.Fatalf("metadata not parsed: %+v", c)
	}
	if c.Mode != "replay" {
		t.Fatalf("default mode = %q, want replay", c.Mode)
	}
	if len(c.Rubric) != 2 || c.Rubric[0].Check != "turn_count_le 6" {
		t.Fatalf("rubric not parsed: %+v", c.Rubric)
	}
	if len(c.Turns) != 3 {
		t.Fatalf("turns = %d, want 3", len(c.Turns))
	}
	if c.Turns[0].Content != "The Acme widget printer is offline again." {
		t.Fatalf("turn content: %+v", c.Turns[0])
	}
	if c.Turns[1].PromptTokens != 1000 {
		t.Fatalf("token counts not parsed: %+v", c.Turns[1])
	}
	if c.Source == "" {
		t.Fatal("source path not recorded")
	}
}

func TestLoadCaseFileErrors(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{"missing frontmatter", "just text\n", "missing frontmatter"},
		{"unterminated frontmatter", "---\nid: x\n", "unterminated"},
		{"missing id", "---\ntitle: x\n---\n\n```json\n{\"turn\":1,\"role\":\"user\"}\n```\n", "id is required"},
		{"no fenced block", "---\nid: x\n---\n\nno transcript here\n", "no fenced JSONL block"},
		{"empty transcript", "---\nid: x\n---\n\n```json\n```\n", "empty"},
		{"bad turn json", "---\nid: x\n---\n\n```json\n{not json}\n```\n", "turn line 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "case.md")
			if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadCaseFile(path)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want contains %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadCorpus(t *testing.T) {
	dir := t.TempDir()
	writeCase(t, dir, "b-second.md", passingCaseSrc)
	writeCase(t, dir, "a-first.md", failingCaseSrc)
	writeCase(t, dir, "notes.txt", "not a case")

	cases, err := LoadCorpus(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 2 {
		t.Fatalf("loaded %d cases, want 2 (only .md)", len(cases))
	}
	if cases[0].ID != "ev-fail" || cases[1].ID != "ev-pass" {
		t.Fatalf("cases not sorted by filename: %s, %s", cases[0].ID, cases[1].ID)
	}
}

func TestLoadCorpusEmpty(t *testing.T) {
	_, err := LoadCorpus(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "no eval cases") {
		t.Fatalf("err = %v, want no-eval-cases error", err)
	}
}

// fakeReplayer is the stub ReplayFunc: identity replay, counting invocations.
type fakeReplayer struct {
	calls  int
	last   []Turn
	retErr error
}

func (f *fakeReplayer) Replay(turns []Turn) ([]Turn, error) {
	f.calls++
	f.last = turns
	if f.retErr != nil {
		return nil, f.retErr
	}
	return turns, nil
}

func TestRunReplay(t *testing.T) {
	dir := t.TempDir()
	writeCase(t, dir, "pass.md", passingCaseSrc)
	writeCase(t, dir, "fail.md", failingCaseSrc)
	cases, err := LoadCorpus(dir)
	if err != nil {
		t.Fatal(err)
	}

	var rp fakeReplayer
	res, err := Run(cases, RunOptions{Replay: rp.Replay})
	if err != nil {
		t.Fatal(err)
	}

	if rp.calls != 2 {
		t.Fatalf("replay calls = %d, want 2", rp.calls)
	}
	if res.Total != 2 || res.Passed != 1 || res.Failed != 1 {
		t.Fatalf("totals: %+v", res)
	}
	if res.Passed+res.Failed != res.Total {
		t.Fatal("pass+fail != total")
	}
	if res.Score <= 0 || res.Score >= 1 {
		t.Fatalf("aggregate score = %v, want in (0,1)", res.Score)
	}

	byID := map[string]CaseResult{}
	for _, r := range res.Results {
		byID[r.CaseID] = r
	}
	p := byID["ev-pass"]
	if !p.Passed || p.Score != 1 {
		t.Fatalf("passing case: %+v", p)
	}
	if p.Turns != 2 {
		t.Fatalf("distinct turns = %d, want 2", p.Turns)
	}
	if p.CostUSD != (1000*5+200*20)/1e6 {
		t.Fatalf("cost = %v", p.CostUSD)
	}
	f := byID["ev-fail"]
	if f.Passed {
		t.Fatalf("deny violation must fail the case: %+v", f)
	}
	foundDeny := false
	for _, chk := range f.Checks {
		if chk.ID == "safety" && !chk.Passed && strings.Contains(chk.Detail, "send_email") {
			foundDeny = true
		}
	}
	if !foundDeny {
		t.Fatalf("deny failure detail not reported: %+v", f.Checks)
	}
}

func TestRunReplayError(t *testing.T) {
	dir := t.TempDir()
	writeCase(t, dir, "pass.md", passingCaseSrc)
	cases, err := LoadCorpus(dir)
	if err != nil {
		t.Fatal(err)
	}
	rp := fakeReplayer{retErr: errCheckFake}
	res, err := Run(cases, RunOptions{Replay: rp.Replay})
	if err != nil {
		t.Fatalf("run-level error not expected for per-case replay failure: %v", err)
	}
	if res.Passed != 0 || res.Failed != 1 {
		t.Fatalf("totals: %+v", res)
	}
	if res.Results[0].Error == "" || res.Results[0].Passed {
		t.Fatalf("per-case error not recorded: %+v", res.Results[0])
	}
}

func TestRunNilReplayIsIdentity(t *testing.T) {
	dir := t.TempDir()
	writeCase(t, dir, "pass.md", passingCaseSrc)
	cases, err := LoadCorpus(dir)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(cases, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Results[0].Passed {
		t.Fatalf("identity replay should pass: %+v", res.Results[0])
	}
}

func TestCompareBaseline(t *testing.T) {
	passed := CaseResult{CaseID: "same", Passed: true, Score: 0.9}
	flipped := CaseResult{CaseID: "flipped", Passed: true, Score: 1.0}

	base := map[string]CaseResult{
		"same":    passed,
		"dropped": {CaseID: "dropped", Passed: true, Score: 0.99}, // 0.9 < 0.99-0.05 → regression
		"flipped": flipped,
		"gone":    {CaseID: "gone", Passed: false, Score: 0}, // in baseline, not in run: ignored
	}
	results := []CaseResult{
		{CaseID: "same", Passed: true, Score: 0.92},
		{CaseID: "dropped", Passed: true, Score: 0.9},
		{CaseID: "flipped", Passed: false, Score: 0.4},
		{CaseID: "brand-new", Passed: true, Score: 1.0},
	}
	got := compareBaseline(results, base)

	if got != 2 {
		t.Fatalf("regressions = %d, want 2", got)
	}
	byID := map[string]CaseResult{}
	for _, r := range results {
		byID[r.CaseID] = r
	}
	if byID["same"].Regression {
		t.Fatal("within-threshold change must not be a regression")
	}
	if byID["same"].BasePassed == nil || !*byID["same"].BasePassed {
		t.Fatal("baseline pass state not recorded")
	}
	if !byID["dropped"].Regression || byID["dropped"].BaseScore != 0.99 {
		t.Fatalf(">5%% score drop not flagged: %+v", byID["dropped"])
	}
	if !byID["flipped"].Regression {
		t.Fatal("pass→fail must be flagged")
	}
	if byID["brand-new"].Regression || byID["brand-new"].BasePassed != nil {
		t.Fatal("case without baseline entry must not be flagged")
	}
}

func TestLoadBaseline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "baseline.jsonl")
	content := `{"case_id":"a","passed":true,"score":0.9}
{"type":"summary","total":1}
{"case_id":"b","passed":false,"score":0.2}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	base, err := LoadBaseline(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(base) != 2 {
		t.Fatalf("loaded %d baseline entries, want 2 (summary line skipped)", len(base))
	}
	if base["a"].Score != 0.9 || base["b"].Passed {
		t.Fatalf("baseline content: %+v", base)
	}

	if _, err := LoadBaseline(filepath.Join(dir, "missing.jsonl")); err == nil {
		t.Fatal("missing baseline file should error")
	}
}

func TestWriteJSONLReport(t *testing.T) {
	res := RunResult{
		GeneratedAt: "2026-10-25T00:00:00Z",
		CorpusDir:   "testdata",
		Total:       2,
		Passed:      1,
		Failed:      1,
		Score:       0.75,
		Results: []CaseResult{
			{CaseID: "a", Passed: true, Score: 1},
			{CaseID: "b", Passed: false, Score: 0.5},
		},
	}
	var buf bytes.Buffer
	if err := WriteJSONLReport(&buf, res); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %d, want 2 results + 1 summary", len(lines))
	}
	var first CaseResult
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("first line not a CaseResult: %v", err)
	}
	if first.CaseID != "a" {
		t.Fatalf("first case: %+v", first)
	}
	var summary map[string]interface{}
	if err := json.Unmarshal([]byte(lines[2]), &summary); err != nil {
		t.Fatalf("last line not a summary: %v", err)
	}
	if summary["type"] != "summary" || summary["total"].(float64) != 2 {
		t.Fatalf("summary: %+v", summary)
	}
}

func TestMarkdownReport(t *testing.T) {
	yes := true
	res := RunResult{
		GeneratedAt: "2026-10-25T00:00:00Z",
		CorpusDir:   "testdata",
		Total:       2,
		Passed:      1,
		Failed:      1,
		Score:       0.75,
		Results: []CaseResult{
			{CaseID: "a", Title: "Good case", Family: "triage", Passed: true, Score: 1, Turns: 2, ToolCalls: 1, CostUSD: 0.01},
			{CaseID: "b", Title: "Bad case", Family: "email", Passed: false, Score: 0.5, Checks: []CheckRecord{
				{ID: "safety", Type: "deterministic", Check: "deny [send_email]", Passed: false, Detail: "deny class violation: tool \"send_email\""},
			}},
		},
	}
	res.Results[1].BasePassed = &yes
	res.Results[1].BaseScore = 1
	res.Results[1].Regression = true

	md := res.Markdown()
	for _, want := range []string{
		"# Eval Report",
		"| Case | Family | Passed | Score |",
		"Good case", "Bad case",
		"PASS", "FAIL",
		"⚠",
		"## Failures",
		"deny [send_email]",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q\n---\n%s", want, md)
		}
	}
}

func TestMarkdownReportEmpty(t *testing.T) {
	md := RunResult{GeneratedAt: "now", CorpusDir: "x"}.Markdown()
	if !strings.Contains(md, "No cases ran") {
		t.Fatalf("empty corpus markdown: %s", md)
	}
}
