package eval

import (
	"testing"
)

// corpusRealV1Dir is the first real evaluation corpus (bd-18u v0.6): 15 cases
// exercising Conduit's actual surface — lookups, multi-step tool sequences,
// refusals, tone, and error reporting — with synthetic identities only
// (example.com / *.example.test hosts, no real people or infrastructure).
//
// Category design:
//   - lookup        (rc-0001..0003): one tool call, deterministic fragments
//   - multi-step    (rc-0004..0006): 2-3 sequenced calls, per-turn allowlists
//   - refusal/safety(rc-0007..0010): decline or hold; deny_classes encode the
//     side-effect boundary the transcript must respect
//   - tone          (rc-0011..0013): NO expected_fragments by design — the
//     judge rubric (Case.Goal) is the only scorer, so the stub
//     judge yields its documented 0.5 for fragment-less cases
//   - error-handling(rc-0014..0015): report the failure, no silent retry
const corpusRealV1Dir = "testdata/corpus-real-v1"

// TestLoadCorpus_RealV1 loads the real corpus through the production loader
// and validates count, ordering, and ID uniqueness.
func TestLoadCorpus_RealV1(t *testing.T) {
	cases, err := LoadCorpus(corpusRealV1Dir)
	if err != nil {
		t.Fatalf("LoadCorpus(%s): %v", corpusRealV1Dir, err)
	}
	const want = 15
	if len(cases) != want {
		t.Fatalf("got %d cases, want %d", len(cases), want)
	}
	seen := make(map[string]bool, len(cases))
	for _, c := range cases {
		if seen[c.ID] {
			t.Errorf("duplicate case id %s", c.ID)
		}
		seen[c.ID] = true
	}
	// Sorted-by-filename ordering implies rc-0001 first, rc-0015 last.
	if cases[0].ID != "rc-0001" {
		t.Errorf("first case = %s, want rc-0001", cases[0].ID)
	}
	if cases[len(cases)-1].ID != "rc-0015" {
		t.Errorf("last case = %s, want rc-0015", cases[len(cases)-1].ID)
	}
}

// TestCorpus_RealV1_CategoryCoverage requires at least two cases per category
// tag so no family is single-sample.
func TestCorpus_RealV1_CategoryCoverage(t *testing.T) {
	cases, err := LoadCorpus(corpusRealV1Dir)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	counts := map[string]int{}
	for _, c := range cases {
		for _, tag := range c.Tags {
			counts[tag]++
		}
	}
	for _, cat := range []string{"lookup", "multi-step", "refusal", "tone", "error-handling"} {
		if counts[cat] < 2 {
			t.Errorf("category %q has %d cases, want >= 2 (counts: %v)", cat, counts[cat], counts)
		}
	}
}

// TestCorpus_RealV1_ToneCasesAreJudgeScored pins the design rule that tone
// cases carry no expected_fragments: their personality dimension must be
// scored by the judge rubric, never by deterministic string checks.
func TestCorpus_RealV1_ToneCasesAreJudgeScored(t *testing.T) {
	cases, err := LoadCorpus(corpusRealV1Dir)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	tone := 0
	for _, c := range cases {
		isTone := false
		for _, tag := range c.Tags {
			if tag == "tone" {
				isTone = true
			}
		}
		if !isTone {
			continue
		}
		tone++
		if len(c.ExpectedFragments) != 0 {
			t.Errorf("%s: tone case has expected_fragments %v; tone must be judge-scored", c.ID, c.ExpectedFragments)
		}
		if c.Goal == "" {
			t.Errorf("%s: tone case has no goal rubric", c.ID)
		}
	}
	if tone < 2 {
		t.Fatalf("found %d tone cases, want >= 2", tone)
	}
}

// TestCorpus_RealV1_ScoresClean runs the real corpus through the full
// scoring pipeline (stub judge + every deterministic check) and requires
// every case to pass with zero violations. This proves the corpus is
// internally consistent: fragments actually appear in the scripted replies,
// deny classes never collide with the tools positive cases invoke,
// per-turn allowlists cover every scripted call, and turn/cost budgets hold.
func TestCorpus_RealV1_ScoresClean(t *testing.T) {
	cases, err := LoadCorpus(corpusRealV1Dir)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	for _, c := range cases {
		res, err := scoreCase(StubJudge{}, nil, 1, c)
		if err != nil {
			t.Fatalf("%s: scoreCase: %v", c.ID, err)
		}
		if !res.Passed {
			t.Errorf("%s: did not pass; violations: %+v", c.ID, res.Violations)
		}
		if len(res.Violations) != 0 {
			t.Errorf("%s: expected zero violations, got %+v", c.ID, res.Violations)
		}
		// Fragment-bearing cases must score a full 1.0 on fragments and
		// (under the stub) on goal; tone cases have no fragments and take
		// the stub's documented 0.5 on goal instead.
		if len(c.ExpectedFragments) > 0 {
			if res.Dimensions.Fragments != 1.0 {
				t.Errorf("%s: fragments = %v, want 1.0", c.ID, res.Dimensions.Fragments)
			}
			if res.Dimensions.Goal != 1.0 {
				t.Errorf("%s: stub goal = %v, want 1.0", c.ID, res.Dimensions.Goal)
			}
		}
	}
}
