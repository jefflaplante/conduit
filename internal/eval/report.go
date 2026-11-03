package eval

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// runAllChecks runs every rubric item of a case against a transcript and
// returns the results in rubric order.
func runAllChecks(c *Case, turns []Turn) []checkResult {
	out := make([]checkResult, 0, len(c.Rubric))
	for _, item := range c.Rubric {
		out = append(out, runCheck(item, c, turns))
	}
	return out
}

// casePassed reports the case-level verdict: every deterministic item passes
// (llm_judge items are unscored in replay and cannot fail a case). Deny
// violations are deterministic failures, so the hard gate falls out of this
// rule naturally.
func casePassed(results []checkResult) bool {
	for _, r := range results {
		if r.Judged {
			continue
		}
		if !r.Passed {
			return false
		}
	}
	return true
}

// caseScore is the weighted rubric score 0–1 from design doc §3c, over
// deterministic items only (judge items are unjudged in replay and excluded
// from both numerator and denominator).
func caseScore(c *Case, results []checkResult) float64 {
	var num, den float64
	byID := make(map[string]checkResult, len(results))
	for _, r := range results {
		byID[r.ID] = r
	}
	for _, item := range c.Rubric {
		if item.Type == "llm_judge" {
			continue
		}
		weight := item.Weight
		if weight == 0 {
			weight = 1
		}
		den += weight
		if r, ok := byID[item.ID]; ok && r.Passed {
			num += weight
		}
	}
	if den == 0 {
		return 0
	}
	return num / den
}

// CaseResult is the per-case outcome of one eval run.
type CaseResult struct {
	CaseID    string        `json:"case_id"`
	Title     string        `json:"title,omitempty"`
	Family    string        `json:"family,omitempty"`
	Mode      string        `json:"mode"`
	Passed    bool          `json:"passed"`
	Score     float64       `json:"score"`
	Checks    []CheckRecord `json:"checks"`
	Turns     int           `json:"turns"`
	ToolCalls int           `json:"tool_calls"`
	CostUSD   float64       `json:"cost_usd"`
	Error     string        `json:"error,omitempty"`
	// Baseline comparison (filled by the runner when a baseline is given).
	BaseScore  float64 `json:"baseline_score,omitempty"`
	BasePassed *bool   `json:"baseline_passed,omitempty"`
	Regression bool    `json:"regression,omitempty"`
}

// CheckRecord is the reported form of one check result.
type CheckRecord struct {
	ID     string  `json:"id"`
	Type   string  `json:"type"`
	Check  string  `json:"check,omitempty"`
	Weight float64 `json:"weight"`
	Passed bool    `json:"passed"`
	Detail string  `json:"detail,omitempty"`
	Judged bool    `json:"judged,omitempty"`
}

// RunResult is the aggregate outcome of one eval run.
type RunResult struct {
	GeneratedAt string       `json:"generated_at"`
	CorpusDir   string       `json:"corpus_dir"`
	Total       int          `json:"total"`
	Passed      int          `json:"passed"`
	Failed      int          `json:"failed"`
	Regressions int          `json:"regressions"`
	Score       float64      `json:"score"`
	Results     []CaseResult `json:"results"`
}

// aggregateScore is the unweighted mean of per-case scores.
func aggregateScore(results []CaseResult) float64 {
	if len(results) == 0 {
		return 0
	}
	var sum float64
	for _, r := range results {
		sum += r.Score
	}
	return sum / float64(len(results))
}

// compareBaseline fills the baseline fields of each result and counts
// regressions. Design doc: --baseline flags a >5% weighted-score drop, and a
// case that regressed from pass to fail is always flagged.
func compareBaseline(results []CaseResult, baseline map[string]CaseResult) (regressions int) {
	const regressionThreshold = 0.05
	for i := range results {
		r := &results[i]
		base, ok := baseline[r.CaseID]
		if !ok {
			continue
		}
		bs := base.Score
		r.BaseScore = bs
		basePassed := base.Passed
		r.BasePassed = &basePassed
		switch {
		case base.Passed && !r.Passed:
			r.Regression = true
		case r.Score < bs-regressionThreshold:
			r.Regression = true
		}
		if r.Regression {
			regressions++
		}
	}
	return regressions
}

// WriteJSONLReport writes one JSON line per case plus a final summary line.
func WriteJSONLReport(w io.Writer, res RunResult) error {
	enc := json.NewEncoder(w)
	for _, r := range res.Results {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return enc.Encode(map[string]interface{}{
		"type":         "summary",
		"generated_at": res.GeneratedAt,
		"corpus_dir":   res.CorpusDir,
		"total":        res.Total,
		"passed":       res.Passed,
		"failed":       res.Failed,
		"regressions":  res.Regressions,
		"score":        res.Score,
	})
}

// Markdown renders the aggregate human-readable report.
func (res RunResult) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Eval Report — %s\n\n", res.GeneratedAt)
	fmt.Fprintf(&b, "Corpus: `%s` · %d cases · %d passed · %d failed · %d regressions · score %.3f\n\n",
		res.CorpusDir, res.Total, res.Passed, res.Failed, res.Regressions, res.Score)
	if res.Total == 0 {
		b.WriteString("No cases ran.\n")
		return b.String()
	}

	families := map[string][]CaseResult{}
	var order []string
	for _, r := range res.Results {
		f := r.Family
		if f == "" {
			f = "other"
		}
		if _, ok := families[f]; !ok {
			order = append(order, f)
		}
		families[f] = append(families[f], r)
	}
	sort.Strings(order)

	b.WriteString("| Case | Family | Passed | Score | Baseline | Δ | Turns | Tools | Cost (USD) |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|\n")
	for _, f := range order {
		for _, r := range families[f] {
			title := r.Title
			if title == "" {
				title = r.CaseID
			}
			base := "—"
			delta := "—"
			if r.BasePassed != nil {
				base = fmt.Sprintf("%.3f", r.BaseScore)
				sign := "+"
				if r.Score < r.BaseScore {
					sign = ""
				}
				delta = fmt.Sprintf("%s%.3f", sign, r.Score-r.BaseScore)
				if r.Regression {
					delta += " ⚠"
				}
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %.3f | %s | %s | %d | %d | %.4f |\n",
				title, f, passMark(r.Passed), r.Score, base, delta, r.Turns, r.ToolCalls, r.CostUSD)
		}
	}

	var failed []CaseResult
	for _, r := range res.Results {
		if !r.Passed {
			failed = append(failed, r)
		}
	}
	if len(failed) > 0 {
		b.WriteString("\n## Failures\n\n")
		for _, r := range failed {
			fmt.Fprintf(&b, "### %s\n\n", r.Title)
			for _, chk := range r.Checks {
				if chk.Passed || chk.Judged {
					continue
				}
				detail := chk.Detail
				if detail == "" {
					detail = "check returned failure without detail"
				}
				fmt.Fprintf(&b, "- **%s** (`%s`): %s\n", chk.ID, chk.Check, detail)
			}
		}
	}
	return b.String()
}

// passMark avoids red/green-only signaling (and works in plain text).
func passMark(passed bool) string {
	if passed {
		return "PASS"
	}
	return "FAIL"
}
