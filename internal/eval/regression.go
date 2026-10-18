package eval

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Regression detection (bd-18u v0.5, design doc §3f): compare a fresh run
// against a stored baseline and flag per-case degradations. The point is to
// catch changes that keep unit tests green but make the agent worse at its
// job — a personality drift the stub judge cannot see, a safety check newly
// failing, or a cost curve quietly bending upward.
//
// Shape: `eval baseline save` persists the current run's per-case records
// (JSONL, same records `eval run --out` writes — one format, one loader).
// `eval regression check` re-runs the corpus and compares. The three flag
// classes are independent: verdict regressions (hard), judge score drops
// (thresholded), and cost regressions (ratio-thresholded, priced costs only).
//
// The comparison is by case ID. Cases missing from either side are reported,
// not silently ignored — a silently dropped case is exactly how a regression
// hides.

// CostRegressionRatio is the default per-case cost increase fraction that
// flags a regression: >50% over baseline. Deliberately coarse — token counts
// wobble run to run on live models, and the flag exists to catch structural
// cost growth, not jitter.
const CostRegressionRatio = 0.50

// RegressionIssue is one flagged difference between the current run and the
// baseline. Class separates hard regressions (verdict flips, newly failing
// checks, coverage changes) from thresholded ones (score drops, cost
// growth); Severity maps that split so reports can lead with the
// unambiguous failures.
type RegressionIssue struct {
	// CaseID is the case the issue belongs to; "(corpus)" for coverage
	// issues affecting the corpus as a whole.
	CaseID string `json:"case_id"`
	// Class is one of: verdict, score, check, cost, coverage.
	Class string `json:"class"`
	// Detail is a human-readable explanation with both numbers.
	Detail string `json:"detail"`
}

// Severity returns "hard" for unambiguous regressions (a passing case now
// failing, a check class newly failing, a case gone missing) and "soft" for
// thresholded drift (score drops, cost growth). Both count toward the exit
// code; the severity only orders the report.
func (i RegressionIssue) Severity() string {
	switch i.Class {
	case "verdict", "check", "coverage":
		return "hard"
	default:
		return "soft"
	}
}

// RegressionOptions tunes the thresholds. Zero fields mean defaults.
type RegressionOptions struct {
	// ScoreDrop is the absolute goal-score drop that flags a regression
	// (0 < cur < base, base-cur > ScoreDrop). 0 or negative uses
	// DefaultScoreDrop. Absolute, not relative: a 0.9 -> 0.75 drop must flag
	// the same whether the baseline was 0.9 or 0.5.
	ScoreDrop float64
	// CostIncrease is the per-case relative cost increase that flags a
	// regression. 0 or negative uses CostRegressionRatio. Evaluated only
	// when BOTH sides priced their costs — comparing a fallback-formula
	// number against a resolver-priced number compares two different
	// pricing models, and any ratio out of that is noise.
	CostIncrease float64
}

// DefaultScoreDrop is the absolute judge-score regression threshold.
const DefaultScoreDrop = 0.1

func (o RegressionOptions) scoreDrop() float64 {
	if o.ScoreDrop > 0 {
		return o.ScoreDrop
	}
	return DefaultScoreDrop
}

func (o RegressionOptions) costIncrease() float64 {
	if o.CostIncrease > 0 {
		return o.CostIncrease
	}
	return CostRegressionRatio
}

// CheckRegression compares the current run against the baseline and returns
// every regression issue found, ordered hard-first then by case ID. Passing
// nil options uses the defaults. The counts come back in the Summary; issues
// are the detail.
func CheckRegression(cur, base *Summary, opts RegressionOptions) []RegressionIssue {
	var issues []RegressionIssue
	if base == nil || cur == nil {
		return issues
	}

	curByID := resultsByID(cur.PerCase)
	baseByID := resultsByID(base.PerCase)

	// Deterministic iteration order: case IDs sorted, so reports are stable
	// regardless of map order.
	ids := make([]string, 0, len(baseByID))
	for id := range baseByID {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		b := baseByID[id]
		c, ok := curByID[id]
		if !ok {
			issues = append(issues, RegressionIssue{
				CaseID: id, Class: "coverage",
				Detail: "case in baseline but missing from current run",
			})
			continue
		}

		// (a) Verdict regression: pass -> fail. A fail -> pass flip is an
		// improvement; the new-failure side is already covered by the check
		// and score classes below (or is a pre-existing failure).
		if b.Passed && !c.Passed {
			issues = append(issues, RegressionIssue{
				CaseID: id, Class: "verdict",
				Detail: fmt.Sprintf("passed at baseline, fails now (score %.3f -> %.3f)", b.Score, c.Score),
			})
		}

		// (b) Score drop beyond the absolute threshold. Baseline 0 never
		// drops; both sides carry a judge ID and the median-of-N runs record
		// it in Dimensions.Goal, so like-for-like is the caller's contract
		// (same judge, same runs) — enforced by flags, see CLI wiring.
		if drop := b.Dimensions.Goal - c.Dimensions.Goal; drop > opts.scoreDrop()+1e-9 {
			issues = append(issues, RegressionIssue{
				CaseID: id, Class: "score",
				Detail: fmt.Sprintf("judge score dropped %.3f -> %.3f (threshold %.2f)",
					b.Dimensions.Goal, c.Dimensions.Goal, opts.scoreDrop()),
			})
		}

		// (c) Check class newly failing: the baseline case was clean for this
		// check class, the current run has violations for it. A check that
		// already failed at baseline carries no new signal — that case was
		// already failing.
		newlyFailing := newlyFailingChecks(b, c)
		if len(newlyFailing) > 0 {
			issues = append(issues, RegressionIssue{
				CaseID: id, Class: "check",
				Detail: "check class newly failing: " + strings.Join(newlyFailing, ", "),
			})
		}

		// (d) Cost regression beyond the ratio threshold, priced-only.
		if b.CostPriced && c.CostPriced && b.CostUSD > 0 {
			if inc := (c.CostUSD - b.CostUSD) / b.CostUSD; inc > opts.costIncrease()+1e-9 {
				issues = append(issues, RegressionIssue{
					CaseID: id, Class: "cost",
					Detail: fmt.Sprintf("cost $%.6f -> $%.6f (+%.0f%%, threshold +%.0f%%)",
						b.CostUSD, c.CostUSD, inc*100, opts.costIncrease()*100),
				})
			}
		}
	}

	// Cases that appeared only in the current run: coverage changed. Not a
	// quality regression, but a baseline comparison that silently ignores it
	// invites drift.
	for _, id := range sortedIDsNotIn(curByID, baseByID) {
		issues = append(issues, RegressionIssue{
			CaseID: id, Class: "coverage",
			Detail: "case in current run but not in baseline",
		})
	}

	sortIssues(issues)
	return issues
}

// newlyFailingChecks returns the check names with violations in cur that had
// none in base. Duplicate check names within one result are collapsed — the
// class is the signal, not the count.
func newlyFailingChecks(base, cur CaseResult) []string {
	baseChecks := make(map[string]bool)
	for _, v := range base.Violations {
		baseChecks[v.Check] = true
	}
	seen := make(map[string]bool)
	var out []string
	for _, v := range cur.Violations {
		if !baseChecks[v.Check] && !seen[v.Check] {
			seen[v.Check] = true
			out = append(out, v.Check)
		}
	}
	sort.Strings(out)
	return out
}

func resultsByID(results []CaseResult) map[string]CaseResult {
	m := make(map[string]CaseResult, len(results))
	for _, r := range results {
		m[r.CaseID] = r
	}
	return m
}

func sortedIDsNotIn(have, missing map[string]CaseResult) []string {
	var out []string
	for id := range have {
		if _, ok := missing[id]; !ok {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// sortIssues orders issues hard-first (verdict, check, coverage before score,
// cost), then by case ID, then class — stable, human-scannable output.
func sortIssues(issues []RegressionIssue) {
	rank := map[string]int{"verdict": 0, "check": 1, "coverage": 2, "score": 3, "cost": 4}
	sort.Slice(issues, func(i, j int) bool {
		ri, rj := rank[issues[i].Class], rank[issues[j].Class]
		if ri != rj {
			return ri < rj
		}
		if issues[i].CaseID != issues[j].CaseID {
			return issues[i].CaseID < issues[j].CaseID
		}
		return issues[i].Class < issues[j].Class
	})
}

// FormatRegressionMarkdown renders the regression report: header with
// pass counts and aggregate score deltas, then every issue grouped
// hard-first. Empty issues render a clean "no regressions" line.
func FormatRegressionMarkdown(cur, base *Summary, issues []RegressionIssue) string {
	var b strings.Builder
	b.WriteString("# Conduit Eval Regression Check\n\n")
	fmt.Fprintf(&b, "- **Cases:** %d total, %d passed (current) vs %d passed (baseline)\n",
		cur.TotalCases, cur.PassedCases, base.PassedCases)
	b.WriteString(fmt.Sprintf("- **Weighted score:** %.3f (baseline %.3f)\n", cur.WeightedScore, base.WeightedScore))
	b.WriteString(fmt.Sprintf("- **Total cost:** $%.6f (baseline $%.6f, synthetic token counts)\n",
		cur.TotalCostUSD, base.TotalCostUSD))
	if cur.JudgeID != base.JudgeID {
		// Different judges make score comparisons apples-to-oranges; say so
		// loudly instead of letting the numbers speak a false language.
		b.WriteString(fmt.Sprintf("- **Judge changed:** %s -> %s (score and verdict comparisons are unreliable across judges)\n",
			base.JudgeID, cur.JudgeID))
	}
	b.WriteString("\n")

	if len(issues) == 0 {
		b.WriteString("No regressions detected.\n")
		return b.String()
	}

	hard, soft := 0, 0
	for _, is := range issues {
		if is.Severity() == "hard" {
			hard++
		} else {
			soft++
		}
	}
	b.WriteString(fmt.Sprintf("## ⚠ %d regression(s): %d hard, %d thresholded\n\n", len(issues), hard, soft))
	b.WriteString("| Case | Class | Sev | Detail |\n|---|---|---|---|\n")
	for _, is := range issues {
		b.WriteString(fmt.Sprintf("| %s | %s | %s | %s |\n", is.CaseID, is.Class, is.Severity(), is.Detail))
	}
	return b.String()
}

// WriteBaselineFile persists the summary as a baseline: the same per-case
// JSONL records `eval run --out` writes, atomically (temp file + rename) so
// an interrupted save never clobbers a good baseline with a truncated one.
func WriteBaselineFile(path string, s *Summary) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename
	bw := bufio.NewWriter(tmp)
	enc := json.NewEncoder(bw)
	for i := range s.PerCase {
		if err := enc.Encode(s.PerCase[i]); err != nil {
			tmp.Close()
			return err
		}
	}
	if err := bw.Flush(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// LoadBaselineFile reads a baseline written by WriteBaselineFile. Baselines
// are per-case JSONL — the exact records the runner emits — so run outputs
// and baselines are the same format and either can seed the other. Malformed
// lines error with the line number; a file with zero records errors too
// (an empty baseline would make every comparison vacuous).
func LoadBaselineFile(path string) (*Summary, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	s := &Summary{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r CaseResult
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, fmt.Errorf("%s: line %d: %w", path, lineNo, err)
		}
		if r.CaseID == "" {
			return nil, fmt.Errorf("%s: line %d: baseline record has empty case_id", path, lineNo)
		}
		s.PerCase = append(s.PerCase, r)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(s.PerCase) == 0 {
		return nil, fmt.Errorf("%s: baseline contains no case records", path)
	}
	s.TotalCases = len(s.PerCase)
	for i := range s.PerCase {
		if s.PerCase[i].Passed {
			s.PassedCases++
		}
		s.TotalCostUSD += s.PerCase[i].CostUSD
	}
	s.MeanScores = meanScores(s.PerCase)
	s.WeightedScore = weighted(s.MeanScores)
	s.JudgeUsage = SumJudgeUsage(s.PerCase)
	if s.TotalCases > 0 {
		s.JudgeID = s.PerCase[0].JudgeID
	}
	return s, nil
}

// RegressionExitCode maps issues to a process exit code: 1 when any issue
// fired, 0 otherwise. Every issue class is a regression — thresholds exist
// to suppress noise, not to soften verdict flips.
func RegressionExitCode(issues []RegressionIssue) int {
	if len(issues) > 0 {
		return 1
	}
	return 0
}
