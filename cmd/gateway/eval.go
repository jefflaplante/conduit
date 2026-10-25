package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"conduit/internal/eval"

	"github.com/spf13/cobra"
)

// evalCorpusFlag default points at the in-repo synthetic corpus.
const evalCorpusFlag = eval.DefaultCorpusDir

var (
	evalCorpusDir    string
	evalOutputPath   string
	evalJSONLPath    string
	evalBaseline     string
	evalSaveBaseline bool
)

// idSanitizer strips characters that are unsafe in output filenames.
var idSanitizer = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// EvalRootCmd is `conduit eval` (bd-18u v0.1: replay mode only).
func EvalRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "eval",
		Short: "Run evaluation cases against replayed transcripts",
		Long: `Run the eval corpus through replay mode: each case's scripted
transcript is validated against its deterministic rubric checks (turn budget,
tool allowlist, deny side-effect classes, expected fragments, cost formula).

Replay mode is free and side-effect free — no model is called. Live replay
against a model under test is a later milestone (bd-18u).

Produces a markdown report (stdout or --out) and a JSONL report (--jsonl)
that can later be fed back via --baseline to flag regressions.`,
	}

	run := &cobra.Command{
		Use:   "run",
		Short: "Run the eval corpus and write reports",
		Example: `  conduit eval run
  conduit eval run --corpus internal/eval/testdata
  conduit eval run --baseline results.jsonl --out report.md
  conduit eval run --jsonl results.jsonl --save-baseline`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEval(cmd)
		},
	}
	run.Flags().StringVar(&evalCorpusDir, "corpus", evalCorpusFlag, "Corpus directory of *.md eval cases")
	run.Flags().StringVar(&evalOutputPath, "out", "", "Write the markdown report to a file (default: stdout)")
	run.Flags().StringVar(&evalJSONLPath, "jsonl", "", "Write a JSONL report to a file (default: none)")
	run.Flags().StringVar(&evalBaseline, "baseline", "", "JSONL report from a previous run to compare against")
	run.Flags().BoolVar(&evalSaveBaseline, "save-baseline", false, "Also write the JSONL report to <corpus-basename>.jsonl next to --jsonl output")

	cmd.AddCommand(run)
	return cmd
}

// evalResult is the rendered output of one eval run, decoupled from the
// command flags so the CLI smoke test can call runEvals directly.
type evalResult struct {
	Markdown    string
	JSONL       string
	Failed      int
	Regressions int
}

// runEval loads the corpus, runs replay, applies baseline comparison, and
// writes the reports.
func runEval(cmd *cobra.Command) error {
	res, err := runEvals(evalCorpusDir, evalBaseline)
	if err != nil {
		return err
	}

	if evalOutputPath != "" {
		if err := os.WriteFile(evalOutputPath, []byte(res.Markdown), 0o644); err != nil {
			return fmt.Errorf("write markdown report: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", evalOutputPath)
	} else {
		fmt.Fprint(cmd.OutOrStdout(), res.Markdown)
	}

	if evalJSONLPath != "" {
		if err := os.WriteFile(evalJSONLPath, []byte(res.JSONL), 0o644); err != nil {
			return fmt.Errorf("write JSONL report: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", evalJSONLPath)
	}
	if evalSaveBaseline {
		if evalJSONLPath == "" {
			return fmt.Errorf("--save-baseline requires --jsonl")
		}
		base := filepath.Join(filepath.Dir(evalJSONLPath), idSanitizer.ReplaceAllString(filepath.Base(evalCorpusDir), "-")+".baseline.jsonl")
		if err := os.WriteFile(base, []byte(res.JSONL), 0o644); err != nil {
			return fmt.Errorf("write baseline: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "wrote baseline %s\n", base)
	}

	// Non-zero exit code when cases failed or regressed, so CI can gate on it.
	if res.Failed > 0 || res.Regressions > 0 {
		return fmt.Errorf("%d case(s) failed, %d regression(s)", res.Failed, res.Regressions)
	}
	return nil
}

// runEvals is the testable core of `eval run`: load corpus → replay →
// optional baseline compare → render reports.
func runEvals(corpusDir, baselinePath string) (*evalResult, error) {
	cases, err := eval.LoadCorpus(corpusDir)
	if err != nil {
		return nil, fmt.Errorf("load corpus: %w", err)
	}
	opts := eval.RunOptions{CorpusDir: corpusDir}
	if baselinePath != "" {
		baseline, err := eval.LoadBaseline(baselinePath)
		if err != nil {
			return nil, fmt.Errorf("load baseline: %w", err)
		}
		opts.Baseline = baseline
	}
	runRes, err := eval.Run(cases, opts)
	if err != nil {
		return nil, fmt.Errorf("run: %w", err)
	}

	var jsonl strings.Builder
	if err := eval.WriteJSONLReport(&jsonl, *runRes); err != nil {
		return nil, fmt.Errorf("render jsonl: %w", err)
	}
	return &evalResult{
		Markdown:    runRes.Markdown(),
		JSONL:       jsonl.String(),
		Failed:      runRes.Failed,
		Regressions: runRes.Regressions,
	}, nil
}
