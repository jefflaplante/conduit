package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"conduit/internal/ai"
)

// rubricVersion is stamped into every LLM judge ID (design doc §3b: every
// result records judge model + rubric version, so score comparisons stay
// auditable across rubric edits). Bump when the prompt text changes meaning.
const rubricVersion = "rubric-v1"

// judgeTimeout bounds a single judge call (design doc §4: judge calls must
// never stall a run; the runner aborts the case on error either way).
const judgeTimeout = 60 * time.Second

// judgeMaxTokens caps the judge's completion: a score verdict is a small
// JSON object, so 512 is generous headroom over {"score":0.75,"rationale":
// "..."} and keeps a runaway judge cheap.
const judgeMaxTokens = 512

// LLMJudge scores the goal dimension with a real model call (design doc
// §3b). It wraps any ai.Provider — production wiring hands it a provider
// built from the gateway config; tests inject a mock. One provider call per
// case, no shared mutable state: safe for concurrent JudgeGoal calls.
type LLMJudge struct {
	// Provider is the backing provider (used only for Name() and calls).
	Provider ai.Provider
	// Model is the model ID sent with each judge request (e.g. "z-ai/glm-5.3").
	// May carry a "provider/" prefix; providers strip their own prefix.
	Model string
}

// compile-time check: LLMJudge must satisfy Judge.
var _ Judge = (*LLMJudge)(nil)

// judgeVerdict is the JSON object the judge prompt demands back.
type judgeVerdict struct {
	Score     float64 `json:"score"`
	Rationale string  `json:"rationale"`
}

// JudgeGoal implements Judge. It sends the rubric plus the full transcript
// to the provider and parses the verdict. Parse failures and provider
// errors are returned, never guessed around: a silently-wrong judge score
// poisons every comparison built on it (design doc §4, judge bias risk).
func (j *LLMJudge) JudgeGoal(c *Case) (float64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), judgeTimeout)
	defer cancel()

	resp, err := j.Provider.GenerateResponse(ctx, &ai.GenerateRequest{
		Model: j.Model,
		Messages: []ai.ChatMessage{
			{Role: "system", Content: judgeSystemPrompt()},
			{Role: "user", Content: judgeUserPrompt(c)},
		},
		MaxTokens: judgeMaxTokens,
	})
	if err != nil {
		return 0, fmt.Errorf("llm judge (%s): %w", j.ID(), err)
	}

	verdict, err := parseJudgeVerdict(resp.Content)
	if err != nil {
		return 0, fmt.Errorf("llm judge (%s): %w", j.ID(), err)
	}
	return clamp01(verdict.Score), nil
}

// ID implements Judge: "llm/<provider-name>/<model>@rubric-v1". Recorded
// verbatim in every CaseResult and Summary (auditability invariant, §3b).
func (j *LLMJudge) ID() string {
	model := j.Model
	if model == "" {
		model = "default"
	}
	return fmt.Sprintf("llm/%s/%s@%s", j.Provider.Name(), model, rubricVersion)
}

// LLMJudgeByName builds the LLM-backed judge. It validates the required
// pieces (model for the request, provider to call) and returns the judge —
// no provider call happens here, only wiring. The CLI resolves the provider
// from gateway config; tests inject a mock.
func LLMJudgeByName(name, model string, provider ai.Provider) (Judge, error) {
	if name != "llm" {
		return nil, fmt.Errorf("LLMJudgeByName handles only %q, got %q", "llm", name)
	}
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("judge %q requires --judge-model (e.g. --judge-model provider/model)", name)
	}
	if provider == nil {
		return nil, fmt.Errorf("judge %q requires a configured provider; check --judge-provider against the gateway config", name)
	}
	return &LLMJudge{Provider: provider, Model: model}, nil
}

// judgeSystemPrompt is the fixed rubric (rubric-v1): strict scoring anchors
// and a JSON-only output contract. Keep it stable per rubricVersion — the
// anchor wording IS the rubric.
func judgeSystemPrompt() string {
	return `You are a strict evaluation judge for a conversational AI assistant. Score how well the assistant transcript accomplishes the stated GOAL on a scale from 0.0 to 1.0.

Scoring anchors:
- 0.0 = The goal is not accomplished at all: the assistant failed to address it, refused without cause, or acted contrary to it.
- 0.5 = Partially accomplished: some requirements are met but material parts are missing, wrong, or incomplete.
- 1.0 = Fully accomplished: the goal is met completely and correctly, with no relevant gaps.

Be strict: when in doubt between two anchors, choose the lower score. Do not reward length, confidence, or politeness — only goal accomplishment.

Respond with ONLY a JSON object, no other text, in exactly this form:
{"score": 0.75, "rationale": "one short sentence explaining the score"}`
}

// judgeUserPrompt renders the case for the judge: the goal plus the full
// transcript with roles (design doc §3a format).
func judgeUserPrompt(c *Case) string {
	var b strings.Builder
	fmt.Fprintf(&b, "GOAL: %s\n\nTRANSCRIPT:\n", c.Goal)
	for _, t := range c.Turns {
		fmt.Fprintf(&b, "%s: %s\n", strings.ToUpper(t.Role), t.Content)
	}
	return b.String()
}

// parseJudgeVerdict extracts the score from judge output. Models habitually
// wrap JSON in markdown code fences despite instructions, so fences are
// stripped before parsing. Anything unparseable is an error: never silently
// guess a score (auditability invariant).
func parseJudgeVerdict(content string) (judgeVerdict, error) {
	var v judgeVerdict
	trimmed := strings.TrimSpace(content)
	trimmed = stripCodeFence(trimmed)

	// The verdict must be a bare JSON object; find its bounds in case the
	// model leaked prose around it despite the contract.
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start < 0 || end <= start {
		return v, fmt.Errorf("unparseable judge output (no JSON object found): %q", truncateForError(content))
	}
	if err := json.Unmarshal([]byte(trimmed[start:end+1]), &v); err != nil {
		return v, fmt.Errorf("unparseable judge output: %w (raw: %q)", err, truncateForError(content))
	}
	// Out-of-range but well-formed scores are returned as-is: JudgeGoal
	// clamps into [0,1] — a judge that says 1.5 clearly means "max".
	return v, nil
}

// stripCodeFence removes a leading ```... fence and trailing ``` if present.
func stripCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// Drop the first line (```lang) and the closing fence.
	if idx := strings.Index(s, "\n"); idx >= 0 {
		s = s[idx+1:]
	} else {
		return strings.TrimPrefix(s, "```")
	}
	s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	return strings.TrimSpace(s)
}

// truncateForError keeps raw judge output in errors bounded (it can be long).
func truncateForError(s string) string {
	const max = 200
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
