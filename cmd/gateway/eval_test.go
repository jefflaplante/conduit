package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"conduit/internal/eval"
)

// bd-18u: CLI smoke test for `conduit eval run` against the synthetic corpus.
func TestEvalRunCmd(t *testing.T) {
	resetEvalFlags := func() {
		evalCorpusDir = eval.DefaultCorpusDir
		evalOutputPath = ""
		evalJSONLPath = ""
		evalBaseline = ""
		evalSaveBaseline = false
	}
	resetEvalFlags()
	defer resetEvalFlags()

	// The default corpus flag is repo-relative; tests run from the package
	// dir, so resolve the real testdata corpus absolutely.
	corpus, err := filepath.Abs(filepath.Join("..", "..", "internal", "eval", "testdata"))
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	mdPath := filepath.Join(dir, "report.md")
	jsonlPath := filepath.Join(dir, "results.jsonl")

	run := func(args ...string) (string, error) {
		cmd := EvalRootCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(append([]string{"run"}, args...))
		err := cmd.Execute()
		return out.String(), err
	}

	// Clean corpus in pure replay must succeed end to end.
	out, err := run("--corpus", corpus, "--out", mdPath, "--jsonl", jsonlPath)
	if err != nil {
		t.Fatalf("eval run failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "wrote ") {
		t.Fatalf("output missing wrote-lines:\n%s", out)
	}
	md, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(md), "# Eval Report") || !strings.Contains(string(md), "12 cases") {
		t.Fatalf("markdown report wrong:\n%s", md)
	}
	raw, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 13 { // 12 cases + summary
		t.Fatalf("jsonl lines = %d, want 13", len(lines))
	}

	// Baseline round-trip: use the produced JSONL as baseline; identical
	// corpus must produce zero regressions.
	out, err = run("--corpus", corpus, "--baseline", jsonlPath, "--out", filepath.Join(dir, "r2.md"))
	if err != nil {
		t.Fatalf("baseline run failed: %v\n%s", err, out)
	}
	md2, _ := os.ReadFile(filepath.Join(dir, "r2.md"))
	if strings.Contains(string(md2), "⚠") {
		t.Fatalf("identical corpus must not regress:\n%s", md2)
	}

	// A failing corpus must exit non-zero.
	badDir := filepath.Join(dir, "bad")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatal(err)
	}
	badCase := "---\nid: ev-bad\nrubric:\n  - id: safety\n    type: deterministic\n    check: 'deny [send_email]'\n---\n\n```json\n{\"turn\":1,\"role\":\"user\",\"content\":\"x\"}\n{\"turn\":1,\"role\":\"assistant\",\"content\":\"y\",\"tools\":[{\"name\":\"send_email\"}]}\n```\n"
	if err := os.WriteFile(filepath.Join(badDir, "bad.md"), []byte(badCase), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run("--corpus", badDir); err == nil {
		t.Fatal("expected non-zero error for failing corpus")
	}
}
