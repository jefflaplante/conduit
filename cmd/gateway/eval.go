package main

import (
	"fmt"

	"conduit/internal/eval"

	"github.com/spf13/cobra"
)

// Eval command: run the evaluation harness over a corpus (bd-18u).
//
//	conduit eval run --corpus internal/eval/testdata/corpus [--baseline results.jsonl] [--out results.jsonl]
//
// Replay mode only in v0.1: scores scripted transcripts. No provider calls,
// no side effects.
var (
	evalCorpus   string
	evalBaseline string
	evalOut      string
	evalJudge    string
)

var evalCmd = &cobra.Command{
	Use:   "eval",
	Short: "Evaluation harness for conversation quality (bd-18u)",
	Long: `Run evaluation cases against a corpus of scripted transcripts.

Replay mode scores pre-scripted transcripts through deterministic checks
(turn budget, tool allowlist, deny classes, content fragments, cost budget)
and the configured judge for the goal dimension. Emits per-case JSONL and
an aggregate markdown report to stdout.`,
}

var evalRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Run evaluation cases from a corpus directory",
	RunE:  runEval,
}

func init() {
	evalRunCmd.Flags().StringVar(&evalCorpus, "corpus", "internal/eval/testdata/corpus",
		"directory of case files (*.md with YAML frontmatter + ```json transcript)")
	evalRunCmd.Flags().StringVar(&evalBaseline, "baseline", "",
		"baseline results JSONL file to compare against (>5% weighted-score drop flags)")
	evalRunCmd.Flags().StringVar(&evalOut, "out", "",
		"write per-case results as JSONL to this path")
	evalRunCmd.Flags().StringVar(&evalJudge, "judge", "stub",
		"judge implementation (v0.1: stub only)")

	evalCmd.AddCommand(evalRunCmd)
	rootCmd.AddCommand(evalCmd)
}

func runEval(cmd *cobra.Command, args []string) error {
	judge, err := eval.JudgeByName(evalJudge)
	if err != nil {
		return err
	}
	cases, err := eval.LoadCorpus(evalCorpus)
	if err != nil {
		return fmt.Errorf("load corpus: %w", err)
	}

	runner := &eval.Runner{Judge: judge}
	summary, err := runner.Run(cases)
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}

	if evalOut != "" {
		if err := eval.WriteResultsFile(evalOut, summary); err != nil {
			return fmt.Errorf("write results: %w", err)
		}
	}

	var flags []string
	if evalBaseline != "" {
		base, err := eval.LoadSummaryJSONL(evalBaseline)
		if err != nil {
			return fmt.Errorf("load baseline: %w", err)
		}
		flags = eval.RegressionFlags(summary, base)
	}

	fmt.Fprintln(cmd.OutOrStdout(), eval.FormatSummaryMarkdown(summary, flags))
	if evalOut != "" {
		fmt.Fprintf(cmd.ErrOrStderr(), "results written to %s\n", evalOut)
	}
	return nil
}
