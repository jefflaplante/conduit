package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// bd-v9q8: CLI smoke tests for the harness eval command, ported from the
// consolidate-cron-tz-eval branch's TestEvalRunCmd. That file lives at
// cmd/gateway/eval_test.go on the other branch, so this port uses
// eval_cli_test.go to avoid a same-path collision at merge time.
//
// Three behaviors carried over, adapted to the harness CLI's actual contract
// (cmd/gateway/eval.go):
//   - a clean replay run writes per-case JSONL via --out and prints the
//     markdown summary (with the case count) to stdout;
//   - `eval baseline save` + `eval regression check` on an identical corpus
//     exits zero;
//   - a failing corpus makes `eval regression check` exit non-zero
//     (exitError code 1).
//
// Contract differences vs the legacy CLI, all deliberate:
//   - `eval run --out` writes per-case records only — there is no trailing
//     summary line, so the file has exactly len(cases) lines (legacy had
//     cases+1).
//   - The markdown report goes to stdout, not to a file; --out is JSONL.
//   - `eval run` never exits non-zero for failing cases (it is
//     report-only); the exit-code contract belongs to
//     `eval regression check` (0 = clean, 1 = regression, 2 = setup error).

// resetEvalCLIFlags restores every flag variable the eval command tree
// binds. The variables are shared across `eval run`, `eval baseline save`,
// and `eval regression check`, and pflag does not re-apply defaults on
// repeated parses within one process — a value set by one subcommand's run
// would silently leak into the next invocation's flags.
func resetEvalCLIFlags() {
	evalCorpus = "internal/eval/testdata/corpus"
	evalBaseline = ""
	evalOut = ""
	evalJudge = "stub"
	evalJudgeModel = ""
	evalJudgeProvider = ""
	evalRuns = 1
	evalMode = "replay"
	evalLiveModel = ""
	evalLiveProvider = ""
	evalBaseOut = "internal/eval/testdata/baseline.jsonl"
	evalRegBaseline = ""
	evalRegScoreDrop = 0.1
	evalRegCostIncrease = 0.5
}

// runEvalCLI executes the root command with the eval subcommand args and
// captures combined stdout/stderr. Flags are reset first so every invocation
// starts from the registered defaults.
func runEvalCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	resetEvalCLIFlags()
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs(append([]string{"eval"}, args...))
	err := rootCmd.Execute()
	return out.String(), err
}

// TestEvalCLIRunSmoke replays the default corpus in pure stub mode and
// verifies the report/JSONL outputs end to end.
func TestEvalCLIRunSmoke(t *testing.T) {
	resetEvalCLIFlags()
	defer resetEvalCLIFlags()

	// The default corpus flag is repo-relative; tests run from the package
	// dir, so resolve the real testdata corpus absolutely.
	corpus, err := filepath.Abs(filepath.Join("..", "..", "internal", "eval", "testdata", "corpus"))
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	jsonlPath := filepath.Join(dir, "results.jsonl")

	out, err := runEvalCLI(t, "run", "--corpus", corpus, "--out", jsonlPath)
	if err != nil {
		t.Fatalf("eval run failed: %v\n%s", err, out)
	}

	// Markdown summary on stdout, with the aggregate header and the per-case
	// table.
	if !strings.Contains(out, "# Conduit Eval Run") {
		t.Errorf("output missing report header:\n%s", out)
	}
	if !strings.Contains(out, "Per-case results") {
		t.Errorf("output missing per-case table:\n%s", out)
	}

	// The report must state the case count, and it must agree with the
	// JSONL record count (per-case lines only — the harness writes no
	// summary line, unlike the legacy CLI).
	m := regexp.MustCompile(`\*\*Cases:\*\* (\d+) total`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("report missing case count:\n%s", out)
	}
	raw, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) == 0 {
		t.Fatal("JSONL file empty")
	}
	if strconv.Itoa(len(lines)) != m[1] {
		t.Errorf("report says %s cases, JSONL has %d records", m[1], len(lines))
	}
	for i, line := range lines {
		var rec struct {
			CaseID string `json:"case_id"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("JSONL line %d: %v", i+1, err)
		}
		if rec.CaseID == "" {
			t.Fatalf("JSONL line %d: empty case_id", i+1)
		}
	}
}

// TestEvalCLIBaselineRoundTrip saves a baseline from the default corpus and
// regression-checks an identical run against it: zero regressions, exit 0.
func TestEvalCLIBaselineRoundTrip(t *testing.T) {
	resetEvalCLIFlags()
	defer resetEvalCLIFlags()

	corpus, err := filepath.Abs(filepath.Join("..", "..", "internal", "eval", "testdata", "corpus"))
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	basePath := filepath.Join(dir, "baseline.jsonl")

	out, err := runEvalCLI(t, "baseline", "save", "--corpus", corpus, "--out", basePath)
	if err != nil {
		t.Fatalf("eval baseline save failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "baseline saved") || !strings.Contains(out, basePath) {
		t.Errorf("baseline save output missing confirmation:\n%s", out)
	}
	if raw, err := os.ReadFile(basePath); err != nil {
		t.Fatal(err)
	} else if n := len(strings.Split(strings.TrimSpace(string(raw)), "\n")); n == 0 {
		t.Fatal("baseline file empty")
	}

	out, err = runEvalCLI(t, "regression", "check", "--corpus", corpus, "--baseline", basePath)
	if err != nil {
		t.Fatalf("identical corpus must not regress, got: %v\n%s", err, out)
	}
	if !strings.Contains(out, "No regressions detected") {
		t.Errorf("clean check output missing clean line:\n%s", out)
	}
}

// TestEvalCLIRegressionExitCodes pins the exit-code contract of
// `eval regression check` over a checked-in fixture pair: the pass corpus
// (clean case) and the fail corpus (same case ID, assistant now makes a
// send-class call that deny_classes forbids). The baseline is saved from the
// passing side, so the failing side is a pure verdict+check degradation.
func TestEvalCLIRegressionExitCodes(t *testing.T) {
	resetEvalCLIFlags()
	defer resetEvalCLIFlags()

	passCorpus := filepath.Join("testdata", "eval-cli", "pass")
	failCorpus := filepath.Join("testdata", "eval-cli", "fail")
	basePath := filepath.Join(t.TempDir(), "baseline.jsonl")

	if out, err := runEvalCLI(t, "baseline", "save", "--corpus", passCorpus, "--out", basePath); err != nil {
		t.Fatalf("baseline save failed: %v\n%s", err, out)
	}

	tests := []struct {
		name     string
		corpus   string
		wantCode int // 0 = nil error; otherwise exitError.code
	}{
		{"identical corpus exits zero", passCorpus, 0},
		{"failing corpus exits one", failCorpus, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := runEvalCLI(t, "regression", "check", "--corpus", tt.corpus, "--baseline", basePath)
			if tt.wantCode == 0 {
				if err != nil {
					t.Fatalf("want exit 0, got: %v\n%s", err, out)
				}
				if !strings.Contains(out, "No regressions detected") {
					t.Errorf("clean corpus missing clean line:\n%s", out)
				}
				return
			}
			var ee exitError
			if !errors.As(err, &ee) {
				t.Fatalf("want exitError, got %T: %v\n%s", err, err, out)
			}
			if ee.code != tt.wantCode {
				t.Fatalf("exit code = %d, want %d\n%s", ee.code, tt.wantCode, out)
			}
			// The report must name the regression class: the fixture's only
			// degradation is the deny_class violation (and its verdict flip).
			if !strings.Contains(out, "verdict") || !strings.Contains(out, "deny_class") {
				t.Errorf("failing-corpus report missing verdict/deny_class detail:\n%s", out)
			}
		})
	}

	// Documented contract: `eval run` is report-only and must NOT exit
	// non-zero even when cases fail — the failure shows in the report as a
	// **NO** row instead.
	out, err := runEvalCLI(t, "run", "--corpus", failCorpus)
	if err != nil {
		t.Fatalf("eval run must not fail on a failing corpus: %v\n%s", err, out)
	}
	if !strings.Contains(out, "**NO**") {
		t.Errorf("failing case not marked in run report:\n%s", out)
	}
}
