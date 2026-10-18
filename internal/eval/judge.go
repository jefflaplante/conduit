package eval

import (
	"fmt"
	"strings"
)

// Judge scores the subjective goal-completion dimension of a case. v0.1
// ships only the interface plus a deterministic stub for tests; a real
// LLM-backed judge lands later and must record its model identity in JudgeID
// for auditability (design doc §3b: every result records judge model + rubric
// version).
//
// A Judge must be safe to call concurrently and must never observe or mutate
// anything outside the case it is given.
type Judge interface {
	// JudgeGoal returns a 0..1 score for how well the transcript accomplishes
	// the case's Goal.
	JudgeGoal(c *Case) (float64, error)
	// ID names the judge implementation, including model identity where
	// applicable (e.g. "stub-v1", "llm/z-ai/glm-5.3@rubric-v1"). It is
	// recorded verbatim in every result.
	ID() string
}

// StubJudge is the deterministic test judge. It scores 1.0 when the
// transcript contains the case's expected fragments (the corpus author's
// proxy for "goal visibly accomplished") and scores
// len(present)/len(expected) otherwise — a content-grounded stand-in that
// keeps replay runs fully deterministic. It never errors.
type StubJudge struct{}

// JudgeGoal implements Judge.
func (StubJudge) JudgeGoal(c *Case) (float64, error) {
	if len(c.ExpectedFragments) == 0 {
		// No fragments declared: a transcript exists and parses, so credit
		// the goal at half — v0.1 has no better signal for this corner.
		return 0.5, nil
	}
	assistant := assistantText(c)
	present := 0
	for _, f := range c.ExpectedFragments {
		if strings.Contains(assistant, strings.ToLower(f)) {
			present++
		}
	}
	return float64(present) / float64(len(c.ExpectedFragments)), nil
}

// ID implements Judge.
func (StubJudge) ID() string { return "stub-v1" }

// assistantText concatenates all assistant turn content, lowercased.
func assistantText(c *Case) string {
	var b strings.Builder
	for _, t := range c.Turns {
		if t.Role == "assistant" {
			b.WriteString(strings.ToLower(t.Content))
			b.WriteString("\n")
		}
	}
	return b.String()
}

// JudgeByName returns the judge for a --judge value. v0.1 accepts only the
// stub (and "" for the default); wiring a real LLM judge here is the v0.1
// follow-up. Unknown names error rather than silently falling back.
func JudgeByName(name string) (Judge, error) {
	switch strings.TrimSpace(name) {
	case "", "stub":
		return StubJudge{}, nil
	default:
		return nil, fmt.Errorf("unknown judge %q (v0.1 supports only \"stub\")", name)
	}
}
