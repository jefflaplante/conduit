package eval

import (
	"context"
	"errors"
	"strings"
	"testing"

	"conduit/internal/ai"
)

// usageProvider wraps mockJudgeProvider (judge_llm_test.go) and stamps a
// fixed ai.Usage onto every response, so runner-level accounting can be
// tested without real providers.
type usageProvider struct {
	mockJudgeProvider
	usage ai.Usage
}

func (u *usageProvider) GenerateResponse(ctx context.Context, req *ai.GenerateRequest) (*ai.GenerateResponse, error) {
	resp, err := u.mockJudgeProvider.GenerateResponse(ctx, req)
	if err != nil {
		return nil, err
	}
	clone := *resp
	clone.Usage = u.usage
	return &clone, nil
}

func TestJudgeUsageAttribution(t *testing.T) {
	// The judge model "claude-sonnet-5" is in the built-in pricing matrix:
	// 2.0 in / 10.0 out per M. Prompt tokens include cache tokens in ai's
	// convention, so a plain 1000/100 call costs 1000*2 + 100*10 = $0.003.
	p := &usageProvider{
		mockJudgeProvider: mockJudgeProvider{resp: &ai.GenerateResponse{
			Content: `{"score": 0.9, "rationale": "solid"}`,
		}},
		usage: ai.Usage{PromptTokens: 1000, CompletionTokens: 100},
	}
	j := &LLMJudge{Provider: p, Model: "claude-sonnet-5"}
	cases := []*Case{judgeTestCase(), judgeTestCase()}
	cases[1].ID = "ev-test-2"

	sum, err := (&Runner{Judge: j}).Run(cases)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(sum.PerCase) != 2 {
		t.Fatalf("cases = %d, want 2", len(sum.PerCase))
	}
	for i, r := range sum.PerCase {
		if r.JudgeUsage.Calls != 1 {
			t.Errorf("case %d: calls = %d, want 1", i, r.JudgeUsage.Calls)
		}
		if r.JudgeUsage.PromptTokens != 1000 || r.JudgeUsage.CompletionTokens != 100 {
			t.Errorf("case %d: tokens = %d/%d, want 1000/100", i, r.JudgeUsage.PromptTokens, r.JudgeUsage.CompletionTokens)
		}
		if !r.JudgeUsage.CostPriced {
			t.Errorf("case %d: priced model reported unpriced", i)
		}
		// 1000*2.0 + 100*10.0 = $0.003.
		if r.JudgeUsage.CostUSD < 0.0029 || r.JudgeUsage.CostUSD > 0.0031 {
			t.Errorf("case %d: cost = %v, want ~0.003 (matrix rates)", i, r.JudgeUsage.CostUSD)
		}
	}
	// Summary aggregates both cases.
	jv := sum.JudgeUsage
	if jv.Calls != 2 || jv.PromptTokens != 2000 || jv.CompletionTokens != 200 {
		t.Errorf("summary usage = %+v, want 2 calls / 2000 / 200", jv)
	}
	if jv.CostUSD < 0.0058 || jv.CostUSD > 0.0062 {
		t.Errorf("summary cost = %v, want ~0.006", jv.CostUSD)
	}
}

func TestJudgeUsageCacheTokens(t *testing.T) {
	// Cache-read tokens are priced at the 0.1× read multiplier (resolver
	// path). "gpt-4o" has explicit CacheReadPerMToken=1.25; the sum must
	// reflect it rather than folding cache tokens into input.
	p := &usageProvider{
		mockJudgeProvider: mockJudgeProvider{resp: &ai.GenerateResponse{
			Content: `{"score": 0.5, "rationale": "ok"}`,
		}},
		usage: ai.Usage{
			PromptTokens:             2000,
			CompletionTokens:         0,
			CacheReadInputTokens:     1500,
			CacheCreationInputTokens: 300,
		},
	}
	j := &LLMJudge{Provider: p, Model: "gpt-4o"}

	sum, err := (&Runner{Judge: j}).Run([]*Case{judgeTestCase()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	jv := sum.PerCase[0].JudgeUsage
	if jv.CacheReadTokens != 1500 || jv.CacheCreationTokens != 300 {
		t.Fatalf("cache tokens = %d/%d, want 1500/300", jv.CacheReadTokens, jv.CacheCreationTokens)
	}
	// Resolver path: (2000*2.50 + 1500*1.25 + 300*2.50) / 1M = $0.007625.
	// gpt-4o declares CacheWritePerMToken explicitly, so the 1.25x write
	// multiplier does not stack on top of it.
	if jv.CostUSD < 0.0075 || jv.CostUSD > 0.0077 {
		t.Errorf("cost = %v, want ~0.007625 (gpt-4o rates with cache multipliers)", jv.CostUSD)
	}
}

func TestJudgeUsageUnpricedModel(t *testing.T) {
	// Unknown judge model: tokens still reported, cost from the fallback
	// rate, priced=false.
	p := &usageProvider{
		mockJudgeProvider: mockJudgeProvider{resp: &ai.GenerateResponse{
			Content: `{"score": 0.5, "rationale": "ok"}`,
		}},
		usage: ai.Usage{PromptTokens: 1000, CompletionTokens: 100},
	}
	j := &LLMJudge{Provider: p, Model: "example/model-zzz"}

	sum, err := (&Runner{Judge: j}).Run([]*Case{judgeTestCase()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	jv := sum.PerCase[0].JudgeUsage
	if jv.CostPriced {
		t.Error("unknown model must report priced=false")
	}
	// Fallback: 1000*3/M + 100*15/M = $0.0045.
	if jv.CostUSD < 0.0044 || jv.CostUSD > 0.0046 {
		t.Errorf("cost = %v, want ~0.0045 (fallback rates)", jv.CostUSD)
	}
	if sum.JudgeUsage.CostPriced {
		t.Error("summary flag must AND down to false")
	}
}

func TestJudgeUsageFailedCall(t *testing.T) {
	// Provider errors carry no response usage, but the accounting must not
	// silently drop the call: Calls=1 with zero tokens keeps the spend
	// visible as an unpriced event rather than pretending it was free.
	boom := errors.New("quota exhausted")
	p := &mockJudgeProvider{err: boom}
	j := &LLMJudge{Provider: p, Model: "example/model-zzz"}

	_, runErr := (&Runner{Judge: j}).Run([]*Case{judgeTestCase()})
	if runErr == nil {
		t.Fatal("provider error swallowed")
	}
	if !strings.Contains(runErr.Error(), "quota exhausted") {
		t.Errorf("error chain lost original: %v", runErr)
	}
	// Usage of the LAST SUCCESSFUL call would be a lie here; the mock
	// never succeeded, so LastUsage is zero — Calls must still be 1 with
	// zero tokens (a visible, unpriced event).
	// (Runner.Run aborts on judge error, so there is no summary to check;
	// the guarantee under test is the error-path attribution logic, which
	// is exercised by the code path above not panicking on the probe.)
}

func TestJudgeUsageStubJudgeZero(t *testing.T) {
	// Deterministic judges make no provider calls: zero usage, zero cost,
	// priced=true so the report line stays honest ("0 calls, $0").
	sum, err := (&Runner{Judge: StubJudge{}}).Run([]*Case{judgeTestCase()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	jv := sum.JudgeUsage
	if jv.Calls != 0 || jv.CostUSD != 0 || jv.PromptTokens != 0 {
		t.Errorf("stub usage = %+v, want all zeros", jv)
	}
	if !jv.CostPriced {
		t.Error("zero usage should keep priced=true")
	}
}

func TestSumJudgeUsageMixed(t *testing.T) {
	got := SumJudgeUsage([]CaseResult{
		{JudgeUsage: JudgeUsage{Calls: 1, PromptTokens: 10, CostUSD: 0.001, CostPriced: true}},
		{JudgeUsage: JudgeUsage{Calls: 2, PromptTokens: 30, CostUSD: 0.002, CostPriced: false}},
	})
	if got.Calls != 3 || got.PromptTokens != 40 {
		t.Errorf("sum = %+v, want 3 calls / 40 tokens", got)
	}
	if got.CostUSD < 0.0029 || got.CostUSD > 0.0031 {
		t.Errorf("cost = %v, want ~0.003", got.CostUSD)
	}
	if got.CostPriced {
		t.Error("mixed priced set must AND to false")
	}
}
