package main

import (
	"fmt"
	"strings"

	"conduit/internal/ai"
	"conduit/internal/config"
	"conduit/internal/eval"

	"github.com/spf13/cobra"
)

// Eval command: run the evaluation harness over a corpus (bd-18u).
//
//	conduit eval run --corpus internal/eval/testdata/corpus [--baseline results.jsonl] [--out results.jsonl]
//
// Replay mode with the stub judge makes no provider calls and has no side
// effects. --judge llm is the one explicit opt-in to live judge calls: it
// requires --judge-model and sends one scoring request per case (bd-18u
// v0.2, design doc §3b).
var (
	evalCorpus        string
	evalBaseline      string
	evalOut           string
	evalJudge         string
	evalJudgeModel    string
	evalJudgeProvider string
	evalRuns          int
	evalMode          string
	evalLiveModel     string
	evalLiveProvider  string
)

var evalCmd = &cobra.Command{
	Use:   "eval",
	Short: "Evaluation harness for conversation quality (bd-18u)",
	Long: `Run evaluation cases against a corpus of scripted transcripts.

Replay mode scores pre-scripted transcripts through deterministic checks
(turn budget, tool allowlist, deny classes, content fragments, cost budget)
and the configured judge for the goal dimension. Emits per-case JSONL and
an aggregate markdown report to stdout.

Judge selection: --judge stub (default, deterministic, no network) or
--judge llm (one LLM scoring call per case; requires --judge-model and
makes real provider calls).

Mode selection: --mode replay (default) scores the corpus transcripts
as scripted — free and deterministic. --mode live sends each case's user
turns to a real model under test (--live-model, --live-provider) and
scores the captured transcript; live runs make real provider calls for
generation (and for the judge when --judge llm) and report their spend
separately in the results.`,
}

var evalRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Run evaluation cases from a corpus directory",
	RunE:  runEval,
}

var (
	evalBaseOut         string
	evalRegBaseline     string
	evalRegScoreDrop    float64
	evalRegCostIncrease float64
)

var evalBaselineSaveCmd = &cobra.Command{
	Use:   "baseline save",
	Short: "Save the current run's per-case results as the regression baseline",
	Long: `Run the corpus (replay or live, same flags as eval run) and persist the
per-case records as the regression baseline (JSONL, same records eval run
--out writes — the two formats are interchangeable). Overwrites any
existing baseline atomically.`,
	RunE: runEvalBaselineSave,
}

var evalRegressionCheckCmd = &cobra.Command{
	Use:   "regression check",
	Short: "Re-run the corpus and flag regressions against the saved baseline",
	Long: `Run the corpus fresh and compare against the baseline written by
eval baseline save. Flags: any case that passed at baseline and fails now
(verdict), a check class newly failing, judge-score drops beyond
--reg-score-drop (default 0.1, absolute), and per-case cost growth beyond
--reg-cost-increase (default +50%, priced costs only; comparing a
fallback-formula cost against a resolver-priced one is flagged as
uninformative instead).

Cases missing from either side are flagged as coverage changes.

Exit codes: 0 = no regression, 1 = regression detected, 2 = setup error
(unreadable or malformed baseline, bad run flags). Flag-parse failures
exit 1 like every other cobra command in this binary.`,
	RunE: runEvalRegressionCheck,
	// The result-carrying exitError (regression detected) is not a usage
	// failure: silence cobra's own Error/usage printing so the empty message
	// never renders and main.go's handler is the single reporter.
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	evalRunCmd.Flags().StringVar(&evalCorpus, "corpus", "internal/eval/testdata/corpus",
		"directory of case files (*.md with YAML frontmatter + ```json transcript)")
	evalRunCmd.Flags().StringVar(&evalBaseline, "baseline", "",
		"baseline results JSONL file to compare against (>5% weighted-score drop flags)")
	evalRunCmd.Flags().StringVar(&evalOut, "out", "",
		"write per-case results as JSONL to this path")
	evalRunCmd.Flags().StringVar(&evalJudge, "judge", "stub",
		"judge implementation: stub (default, deterministic) or llm")
	evalRunCmd.Flags().StringVar(&evalJudgeModel, "judge-model", "",
		"model for --judge llm (e.g. provider/model); required with --judge llm")
	evalRunCmd.Flags().StringVar(&evalJudgeProvider, "judge-provider", "",
		"provider name from gateway config for --judge llm; default resolves from the model's provider/ prefix")
	evalRunCmd.Flags().IntVar(&evalRuns, "runs", 1,
		"sample each case N times and score the goal by the median (dampens judge variance; N judge calls per case)")
	evalRunCmd.Flags().StringVar(&evalMode, "mode", "replay",
		"run mode: replay (score scripted transcripts, default) or live (generate transcripts from a real model)")
	evalRunCmd.Flags().StringVar(&evalLiveModel, "live-model", "",
		"model for --mode live (e.g. provider/model); required with --mode live")
	evalRunCmd.Flags().StringVar(&evalLiveProvider, "live-provider", "",
		"provider name from gateway config for --mode live; default resolves from the model's provider/ prefix")

	evalBaselineSaveCmd.Flags().StringVar(&evalBaseOut, "out", "internal/eval/testdata/baseline.jsonl",
		"baseline file to write")
	// Same shared flag vars as eval run: identical corpus/runner semantics,
	// so what a baseline describes and what a check re-runs cannot drift.
	evalBaselineSaveCmd.Flags().StringVar(&evalCorpus, "corpus", "internal/eval/testdata/corpus",
		"directory of case files (*.md with YAML frontmatter + ```json transcript)")
	evalBaselineSaveCmd.Flags().StringVar(&evalJudge, "judge", "stub",
		"judge implementation: stub (default, deterministic) or llm")
	evalBaselineSaveCmd.Flags().StringVar(&evalJudgeModel, "judge-model", "",
		"model for --judge llm (e.g. provider/model); required with --judge llm")
	evalBaselineSaveCmd.Flags().StringVar(&evalJudgeProvider, "judge-provider", "",
		"provider name from gateway config for --judge llm; default resolves from the model's provider/ prefix")
	evalBaselineSaveCmd.Flags().IntVar(&evalRuns, "runs", 1,
		"sample each case N times and score the goal by the median (dampens judge variance; N judge calls per case)")
	evalBaselineSaveCmd.Flags().StringVar(&evalMode, "mode", "replay",
		"run mode: replay (score scripted transcripts, default) or live (generate transcripts from a real model)")
	evalBaselineSaveCmd.Flags().StringVar(&evalLiveModel, "live-model", "",
		"model for --mode live (e.g. provider/model); required with --mode live")
	evalBaselineSaveCmd.Flags().StringVar(&evalLiveProvider, "live-provider", "",
		"provider name from gateway config for --mode live; default resolves from the model's provider/ prefix")

	evalRegressionCheckCmd.Flags().StringVar(&evalRegBaseline, "baseline", "",
		"baseline JSONL file to compare against (as written by eval baseline save)")
	evalRegressionCheckCmd.Flags().Float64Var(&evalRegScoreDrop, "reg-score-drop", 0.1,
		"absolute judge-score drop per case that flags a regression")
	evalRegressionCheckCmd.Flags().Float64Var(&evalRegCostIncrease, "reg-cost-increase", 0.5,
		"per-case relative cost increase (fraction) that flags a regression")
	evalRegressionCheckCmd.Flags().StringVar(&evalCorpus, "corpus", "internal/eval/testdata/corpus",
		"directory of case files (*.md with YAML frontmatter + ```json transcript)")
	evalRegressionCheckCmd.Flags().StringVar(&evalJudge, "judge", "stub",
		"judge implementation: stub (default, deterministic) or llm")
	evalRegressionCheckCmd.Flags().StringVar(&evalJudgeModel, "judge-model", "",
		"model for --judge llm (e.g. provider/model); required with --judge llm")
	evalRegressionCheckCmd.Flags().StringVar(&evalJudgeProvider, "judge-provider", "",
		"provider name from gateway config for --judge llm; default resolves from the model's provider/ prefix")
	evalRegressionCheckCmd.Flags().IntVar(&evalRuns, "runs", 1,
		"sample each case N times and score the goal by the median (dampens judge variance; N judge calls per case)")
	evalRegressionCheckCmd.Flags().StringVar(&evalMode, "mode", "replay",
		"run mode: replay (score scripted transcripts, default) or live (generate transcripts from a real model)")
	evalRegressionCheckCmd.Flags().StringVar(&evalLiveModel, "live-model", "",
		"model for --mode live (e.g. provider/model); required with --mode live")
	evalRegressionCheckCmd.Flags().StringVar(&evalLiveProvider, "live-provider", "",
		"provider name from gateway config for --mode live; default resolves from the model's provider/ prefix")

	evalCmd.AddCommand(evalRunCmd)
	evalCmd.AddCommand(evalBaselineSaveCmd)
	evalCmd.AddCommand(evalRegressionCheckCmd)
	rootCmd.AddCommand(evalCmd)
}

// evalJudgeFromConfig wires the judge selected by flags. The stub path is
// pure; the llm path resolves the provider from the gateway config (same
// file the server loads) and constructs it with the production constructor.
// No provider call happens here — construction only; live calls occur later
// in runEval, one per case, only because the user passed --judge llm.
func evalJudgeFromConfig(cmd *cobra.Command) (eval.Judge, error) {
	switch strings.TrimSpace(evalJudge) {
	case "", "stub":
		return eval.JudgeByName("stub")
	case "llm":
		if strings.TrimSpace(evalJudgeModel) == "" {
			return nil, fmt.Errorf("--judge llm requires --judge-model (e.g. --judge-model provider/model)")
		}
		cfg, err := loadExistingConfig(cfgFile)
		if err != nil {
			return nil, err
		}
		provider, err := evalProviderFromConfig(cfg, evalJudgeProvider, evalJudgeModel)
		if err != nil {
			return nil, err
		}
		return eval.LLMJudgeByName("llm", evalJudgeModel, provider)
	default:
		// Surface the unknown-judge error with flag context.
		return nil, fmt.Errorf("unknown --judge %q (supported: stub, llm)", evalJudge)
	}
}

// evalProviderFromConfig picks the named provider config out of the gateway
// config and builds it with the same constructors the router uses, keeping
// judge traffic on the identical code path as production traffic. No new
// config keys: the judge reuses the providers already declared there.
func evalProviderFromConfig(cfg *config.Config, providerName, model string) (ai.Provider, error) {
	if providerName == "" {
		// Default: resolve from the model's "provider/model" prefix, same
		// convention the router uses for routing decisions.
		idx := strings.Index(model, "/")
		if idx <= 0 {
			return nil, fmt.Errorf("cannot resolve a judge provider: --judge-model %q has no provider/ prefix and --judge-provider is unset", model)
		}
		providerName = model[:idx]
	}
	for i := range cfg.AI.Providers {
		pc := cfg.AI.Providers[i]
		if !strings.EqualFold(pc.Name, providerName) {
			continue
		}
		// Same type dispatch as the router's newProviderInstance — an
		// anthropic-type config through the OpenAI constructor would build
		// a silently wrong client.
		var provider ai.Provider
		var err error
		switch pc.Type {
		case "anthropic":
			provider, err = ai.NewAnthropicProvider(pc)
		default:
			// "openai", "ollama" — all OpenAI-compatible constructors.
			provider, err = ai.NewOpenAIProvider(pc)
		}
		if err != nil {
			return nil, fmt.Errorf("build judge provider %s: %w", pc.Name, err)
		}
		return provider, nil
	}
	return nil, fmt.Errorf("provider %q not found in config; --judge-provider must name a configured provider", providerName)
}

func runEval(cmd *cobra.Command, args []string) error {
	summary, err := runEvalRunner(cmd)
	if err != nil {
		return err
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

// runEvalRunner is the corpus-execution core shared by `eval run`,
// `eval baseline save`, and `eval regression check`: judge wiring, corpus
// load, mode dispatch. One implementation so the three commands cannot
// disagree about what a run means.
func runEvalRunner(cmd *cobra.Command) (*eval.Summary, error) {
	judge, err := evalJudgeFromConfig(cmd)
	if err != nil {
		return nil, err
	}
	if evalRuns < 0 {
		return nil, fmt.Errorf("--runs must be >= 0 (got %d)", evalRuns)
	}
	cases, err := eval.LoadCorpus(evalCorpus)
	if err != nil {
		return nil, fmt.Errorf("load corpus: %w", err)
	}

	var summary *eval.Summary
	switch strings.TrimSpace(evalMode) {
	case "", "replay":
		runner := &eval.Runner{Judge: judge, Runs: evalRuns}
		summary, err = runner.Run(cases)
	case "live":
		// Error clearly before any provider call: a live run without a model
		// under test has nothing to execute.
		if strings.TrimSpace(evalLiveModel) == "" {
			return nil, fmt.Errorf("--mode live requires --live-model (e.g. --live-model provider/model)")
		}
		cfg, err := loadExistingConfig(cfgFile)
		if err != nil {
			return nil, err
		}
		// Same provider wiring as the judge (evalProviderFromConfig) and the
		// same constructors the router uses: live eval traffic rides the
		// identical client code path as production traffic.
		provider, err := evalProviderFromConfig(cfg, evalLiveProvider, evalLiveModel)
		if err != nil {
			return nil, err
		}
		runner := &eval.LiveRunner{Provider: provider, Model: evalLiveModel, Judge: judge, Runs: evalRuns}
		summary, err = runner.Run(cases)
	default:
		return nil, fmt.Errorf("unknown --mode %q (supported: replay, live)", evalMode)
	}
	if err != nil {
		return nil, fmt.Errorf("run: %w", err)
	}
	return summary, nil
}

// runEvalBaselineSave implements `eval baseline save`: run, then persist the
// per-case records atomically as the regression baseline.
func runEvalBaselineSave(cmd *cobra.Command, args []string) error {
	summary, err := runEvalRunner(cmd)
	if err != nil {
		return err
	}
	if err := eval.WriteBaselineFile(evalBaseOut, summary); err != nil {
		return fmt.Errorf("write baseline: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "baseline saved: %s (%d cases, %d passed, judge %s)\n",
		evalBaseOut, summary.TotalCases, summary.PassedCases, summary.JudgeID)
	if evalMode == "live" || strings.TrimSpace(evalMode) == "live" {
		fmt.Fprintf(cmd.ErrOrStderr(), "note: baseline recorded from a live run — compare against the same mode/model or expect coverage and cost noise\n")
	}
	return nil
}

// runEvalRegressionCheck implements `eval regression check`: load the
// baseline, re-run the corpus fresh, compare, and exit 1 on any regression.
// The command IS its exit code — CI's contract.
func runEvalRegressionCheck(cmd *cobra.Command, args []string) error {
	if strings.TrimSpace(evalRegBaseline) == "" {
		return fmt.Errorf("--baseline is required (path written by eval baseline save)")
	}
	base, err := eval.LoadBaselineFile(evalRegBaseline)
	if err != nil {
		return exitError{code: 2, err: fmt.Errorf("load baseline: %w", err)}
	}
	cur, err := runEvalRunner(cmd)
	if err != nil {
		return exitError{code: 2, err: err}
	}
	opts := eval.RegressionOptions{ScoreDrop: evalRegScoreDrop, CostIncrease: evalRegCostIncrease}
	issues := eval.CheckRegression(cur, base, opts)
	fmt.Fprintln(cmd.OutOrStdout(), eval.FormatRegressionMarkdown(cur, base, issues))
	code := eval.RegressionExitCode(issues)
	if code == 0 {
		return nil
	}
	return exitError{code: code}
}

// exitError carries an explicit process exit status through cobra's RunE.
// main.go prints and exits 1 on any Execute error, so code 1 needs no
// special type; code 2 distinguishes usage/setup failures (bad flags,
// unreadable baseline) from a detected regression.
type exitError struct {
	code int
	err  error
}

func (e exitError) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	return ""
}
