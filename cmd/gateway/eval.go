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
makes real provider calls).`,
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
		"judge implementation: stub (default, deterministic) or llm")
	evalRunCmd.Flags().StringVar(&evalJudgeModel, "judge-model", "",
		"model for --judge llm (e.g. provider/model); required with --judge llm")
	evalRunCmd.Flags().StringVar(&evalJudgeProvider, "judge-provider", "",
		"provider name from gateway config for --judge llm; default resolves from the model's provider/ prefix")
	evalRunCmd.Flags().IntVar(&evalRuns, "runs", 1,
		"sample each case N times and score the goal by the median (dampens judge variance; N judge calls per case)")

	evalCmd.AddCommand(evalRunCmd)
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
	judge, err := evalJudgeFromConfig(cmd)
	if err != nil {
		return err
	}
	cases, err := eval.LoadCorpus(evalCorpus)
	if err != nil {
		return fmt.Errorf("load corpus: %w", err)
	}

	if evalRuns < 0 {
		return fmt.Errorf("--runs must be >= 0 (got %d)", evalRuns)
	}
	runner := &eval.Runner{Judge: judge, Runs: evalRuns}
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
