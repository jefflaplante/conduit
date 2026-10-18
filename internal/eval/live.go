package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"conduit/internal/ai"
)

// Live mode (bd-18u v0.4, design doc §3d): instead of scoring pre-scripted
// assistant turns, the runner sends each corpus case's user turns to a REAL
// model under test through the same ai.Provider abstraction the gateway uses,
// captures the assistant messages and tool calls it gets back, and scores the
// captured transcript with the unmodified scoring pipeline (scoring.go:
// checks, judge, cost). Checks and judge stay provider-agnostic — they only
// ever see a *Case.
//
// Scope discipline (v0.4): one provider call per corpus user turn. The
// transcript shape is preserved (user turn N, assistant turn N, ...); the
// scripted user simulator replays the corpus's user lines in order. No
// gateway session, policy engine, or skill machinery is invoked — the
// provider client is called directly, mirroring how the LLM judge calls it.
// The full session-pipeline live drive (eval- session keys, policy sandbox)
// is design doc §3d later work.

// liveMaxTokens caps the model-under-test's completion per call. Corpus
// assistant turns are short; 1024 is generous headroom and bounds a runaway
// generation cheaply.
const liveMaxTokens = 1024

// liveTimeout bounds one provider call for the model under test. Mirror of
// judgeTimeout: a hung provider must slow the run, not hang it.
const liveTimeout = 120 * time.Second

// LiveRunner executes corpus cases against a live provider and scores the
// captured transcripts with the shared scoring pipeline. One case at a time
// (Run is serial), one provider call per corpus user turn.
type LiveRunner struct {
	// Provider is the model-under-test client. Required.
	Provider ai.Provider
	// Model is the model ID requested with each call (e.g. "z-ai/glm-5.3").
	// May carry a "provider/" prefix; providers strip their own prefix.
	Model string
	// Judge scores the goal dimension (same contract as Runner.Judge).
	Judge Judge
	// Pricing prices actual token usage; nil uses the ai package default
	// resolver.
	Pricing *ai.PricingResolver
	// Runs is the median-of-N sample size for the judge (same contract as
	// Runner.Runs). Deterministic checks still run once.
	Runs int

	// liveUsage accumulates the model-under-test's provider usage across the
	// most recent Run (tokens + priced cost fields). Consumed by Usage().
	// mu serializes accumulation and probes; Run itself is serial today, the
	// mutex keeps Usage() safe if sampling gains concurrency later.
	liveUsage ai.Usage
	mu        sync.Mutex
}

// Usage returns the model-under-test's provider usage accumulated across the
// most recent Run — distinct from the judge's spend (Summary.JudgeUsage) and
// from case costs priced off scripted counts.
func (r *LiveRunner) Usage() ai.Usage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.liveUsage
}

// Run executes every case live and produces the same Summary shape as replay,
// plus the live-run accounting fields (Summary.LiveModel, Summary.LiveUsage).
func (r *LiveRunner) Run(cases []*Case) (*Summary, error) {
	if r.Provider == nil {
		return nil, fmt.Errorf("live runner: provider is required")
	}
	if strings.TrimSpace(r.Model) == "" {
		return nil, fmt.Errorf("live runner: model is required")
	}
	if r.Judge == nil {
		return nil, fmt.Errorf("live runner: judge is required")
	}

	summary := &Summary{
		GeneratedAt: time.Now().UTC(),
		JudgeID:     r.Judge.ID(),
		Runs:        r.Runs,
		PerCase:     make([]CaseResult, 0, len(cases)),
	}
	for _, c := range cases {
		res, err := r.runCase(c)
		if err != nil {
			return nil, fmt.Errorf("case %s: %w", c.ID, err)
		}
		summary.PerCase = append(summary.PerCase, *res)
		if res.Passed {
			summary.PassedCases++
		}
		summary.TotalCostUSD += res.CostUSD
	}
	summary.TotalCases = len(summary.PerCase)
	summary.MeanScores = meanScores(summary.PerCase)
	summary.WeightedScore = weighted(summary.MeanScores)
	summary.JudgeUsage = SumJudgeUsage(summary.PerCase)
	summary.LiveModel = r.Model
	summary.LiveUsage = judgeUsageFor(r.Model, r.Usage(), r.Pricing)
	return summary, nil
}

// runCase executes one case live and scores the captured transcript.
func (r *LiveRunner) runCase(c *Case) (*CaseResult, error) {
	transcript, usage, err := r.execute(c)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.liveUsage.Add(usage)
	r.mu.Unlock()

	// Score a copy with the transcript swapped for the captured one: the
	// scoring pipeline is shared verbatim with replay, and the caller's
	// corpus stays untouched.
	scored := *c
	scored.Turns = transcript
	scored.Cost = liveCostInputs(r.Model, usage)

	res, err := scoreCase(r.Judge, r.Pricing, r.Runs, &scored)
	if err != nil {
		return nil, err
	}
	// The model-under-test's spend is real too. JudgeUsage carries the case's
	// judge accounting; LiveUsage carries the generation accounting.
	lu := judgeUsageFor(r.Model, usage, r.Pricing)
	res.LiveUsage = &lu
	return res, nil
}

// execute drives the scripted user simulator: for every user turn in the
// corpus transcript it sends the conversation so far to the provider and
// captures the assistant reply. Scripted assistant turns are skipped — live
// mode generates its own. A corpus with no user turns is a corpus defect and
// errors rather than silently scoring an empty transcript.
func (r *LiveRunner) execute(c *Case) ([]Turn, ai.Usage, error) {
	var (
		out     []Turn
		usage   ai.Usage
		hasUser bool
	)
	for _, t := range c.Turns {
		if t.Role != "user" {
			continue
		}
		hasUser = true
		resp, callUsage, err := r.call(r.messages(c, out, t))
		if err != nil {
			return nil, usage, fmt.Errorf("turn %d: %w", t.Turn, err)
		}
		usage.Add(callUsage)
		out = append(out,
			Turn{Turn: t.Turn, Role: "user", Content: t.Content},
			Turn{Turn: t.Turn, Role: "assistant", Content: resp.Content, Tools: liveToolCalls(resp.ToolCalls)},
		)
	}
	if !hasUser {
		return nil, usage, fmt.Errorf("case has no user turns to execute live")
	}
	return out, usage, nil
}

// messages builds the request history: a fixed system message, then the
// captured conversation so far, then the user turn being sent. The system
// message keeps the model under test in the eval's conversational frame
// without leaking rubric scoring language into the generation.
func (r *LiveRunner) messages(c *Case, captured []Turn, user Turn) []ai.ChatMessage {
	msgs := make([]ai.ChatMessage, 0, len(captured)+2)
	msgs = append(msgs, ai.ChatMessage{
		Role: "system",
		Content: "You are the assistant under evaluation. Answer the user's request directly. " +
			"Task context: " + c.Goal,
	})
	for _, t := range captured {
		msgs = append(msgs, ai.ChatMessage{Role: t.Role, Content: t.Content})
	}
	msgs = append(msgs, ai.ChatMessage{Role: "user", Content: user.Content})
	return msgs
}

// call makes one provider call and reports its usage. Live call failures are
// errors, never guesses: a fabricated transcript would poison every score
// built on it (receipts-or-it-didn't-happen, applied to eval data).
func (r *LiveRunner) call(msgs []ai.ChatMessage) (*ai.GenerateResponse, ai.Usage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), liveTimeout)
	defer cancel()
	resp, err := r.Provider.GenerateResponse(ctx, &ai.GenerateRequest{
		Model:     r.Model,
		Messages:  msgs,
		MaxTokens: liveMaxTokens,
	})
	if err != nil {
		return nil, ai.Usage{}, fmt.Errorf("live call (%s): %w", r.Provider.Name(), err)
	}
	if resp == nil {
		return nil, ai.Usage{}, fmt.Errorf("live call (%s): provider returned nil response", r.Provider.Name())
	}
	return resp, resp.Usage, nil
}

// liveToolCalls normalizes the provider's tool-call structs into the
// transcript shape checks and the judge already consume (types.go ToolCall).
// Args is serialized as compact JSON when present. No new wire format is
// invented here: ai.ToolCall comes straight off the provider response.
func liveToolCalls(calls []ai.ToolCall) []ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]ToolCall, 0, len(calls))
	for _, tc := range calls {
		args := ""
		if len(tc.Args) > 0 {
			if b, err := json.Marshal(tc.Args); err == nil {
				args = string(b)
			} else {
				// Deterministic checks match on Name only and the judge reads
				// content, so an unrenderable Args map degrades to a marker
				// instead of failing the run.
				args = "<unrenderable args>"
			}
		}
		out = append(out, ToolCall{Name: tc.Name, Args: args})
	}
	return out
}

// liveCostInputs converts the model-under-test's actual usage into the
// CostInputs shape the shared cost check prices (cost.go). Live runs use the
// real token counts; the pricing pipeline (resolver or documented fallback)
// is identical to replay.
func liveCostInputs(model string, u ai.Usage) CostInputs {
	return CostInputs{
		Model:               model,
		PromptTokens:        u.PromptTokens,
		CompletionTokens:    u.CompletionTokens,
		CacheReadTokens:     u.CacheReadInputTokens,
		CacheCreationTokens: u.CacheCreationInputTokens,
	}
}
