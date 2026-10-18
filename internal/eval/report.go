package eval

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// WriteResultsJSONL emits per-case results, one JSON object per line, to w.
func WriteResultsJSONL(w io.Writer, s *Summary) error {
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	for i := range s.PerCase {
		if err := enc.Encode(s.PerCase[i]); err != nil {
			return err
		}
	}
	return bw.Flush()
}

// WriteResultsFile writes per-case results to path (JSONL, one case per
// line), creating parent directories as needed.
func WriteResultsFile(path string, s *Summary) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return WriteResultsJSONL(f, s)
}

// FormatSummaryMarkdown renders the aggregate summary: a score × dimension
// matrix plus pass counts, violations, and baseline regression flags.
func FormatSummaryMarkdown(s *Summary, flags []string) string {
	var b strings.Builder
	b.WriteString("# Conduit Eval Run\n\n")
	b.WriteString(fmt.Sprintf("- **Cases:** %d total, %d passed, %d failed\n",
		s.TotalCases, s.PassedCases, s.TotalCases-s.PassedCases))
	b.WriteString(fmt.Sprintf("- **Weighted score:** %.3f\n", s.WeightedScore))
	b.WriteString(fmt.Sprintf("- **Total cost:** $%.6f (synthetic token counts)\n", s.TotalCostUSD))
	if s.JudgeUsage.Calls > 0 {
		b.WriteString(fmt.Sprintf("- **Judge calls:** %d, $%.6f (priced=%t; prompt %d tok, completion %d tok, cache-read %d tok, cache-write %d tok)\n",
			s.JudgeUsage.Calls, s.JudgeUsage.CostUSD, s.JudgeUsage.CostPriced,
			s.JudgeUsage.PromptTokens, s.JudgeUsage.CompletionTokens,
			s.JudgeUsage.CacheReadTokens, s.JudgeUsage.CacheCreationTokens))
	} else {
		b.WriteString("- **Judge calls:** 0 (no provider spend)\n")
	}
	b.WriteString(fmt.Sprintf("- **Judge:** %s\n", s.JudgeID))
	if s.Runs > 1 {
		b.WriteString(fmt.Sprintf("- **Sampling:** median-of-%d judge calls per case\n", s.Runs))
	}
	b.WriteString(fmt.Sprintf("- **Generated:** %s\n\n", s.GeneratedAt.Format(time.RFC3339)))

	b.WriteString("## Dimensions (mean across cases)\n\n")
	b.WriteString("| Dimension | Mean |\n|---|---|\n")
	m := s.MeanScores
	b.WriteString(fmt.Sprintf("| goal | %.3f |\n", m.Goal))
	b.WriteString(fmt.Sprintf("| compliance | %.3f |\n", m.Compliance))
	b.WriteString(fmt.Sprintf("| fragments | %.3f |\n", m.Fragments))
	b.WriteString(fmt.Sprintf("| cost | %.3f |\n", m.Cost))
	b.WriteString(fmt.Sprintf("| budget | %.3f |\n\n", m.Budget))

	b.WriteString("## Per-case results\n\n")
	b.WriteString("| Case | Passed | Score | Goal | Runs | Compl | Frag | Cost | Budget | Violations |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|\n")
	for i := range s.PerCase {
		r := &s.PerCase[i]
		status := "yes"
		if !r.Passed {
			status = "**NO**"
		}
		viol := "—"
		if len(r.Violations) > 0 {
			parts := make([]string, 0, len(r.Violations))
			for _, v := range r.Violations {
				parts = append(parts, v.Check+": "+v.Detail)
			}
			viol = strings.Join(parts, "; ")
		}
		runsCol := "1"
		if r.Dimensions.GoalRuns > 1 {
			runsCol = fmt.Sprintf("%d (min %.2f / max %.2f)", r.Dimensions.GoalRuns,
				minScore(r.Dimensions.GoalScores), maxScore(r.Dimensions.GoalScores))
		}
		b.WriteString(fmt.Sprintf("| %s | %s | %.3f | %.2f | %s | %.2f | %.2f | %.2f | %.2f | %s |\n",
			r.CaseID, status, r.Score,
			r.Dimensions.Goal, runsCol, r.Dimensions.Compliance, r.Dimensions.Fragments,
			r.Dimensions.Cost, r.Dimensions.Budget, viol))
	}

	if len(flags) > 0 {
		b.WriteString("\n## ⚠ Regression flags\n\n")
		for _, f := range flags {
			b.WriteString("- " + f + "\n")
		}
	} else if s.TotalCases > 0 {
		b.WriteString("\nNo regressions flagged.\n")
	}
	return b.String()
}

// minScore / maxScore return the extremes of a score sample (0 when the
// sample is empty — the single-run report path never renders them).
func minScore(scores []float64) float64 {
	if len(scores) == 0 {
		return 0
	}
	m := scores[0]
	for _, v := range scores[1:] {
		if v < m {
			m = v
		}
	}
	return m
}

func maxScore(scores []float64) float64 {
	if len(scores) == 0 {
		return 0
	}
	m := scores[0]
	for _, v := range scores[1:] {
		if v > m {
			m = v
		}
	}
	return m
}

// LoadSummaryJSONL reads a baseline results file written by WriteResultsFile
// and rebuilds its Summary (per-case records; aggregate fields recomputed).
func LoadSummaryJSONL(path string) (*Summary, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	s := &Summary{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r CaseResult
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		s.PerCase = append(s.PerCase, r)
	}
	if err := sc.Err(); err != nil {
		return nil, err
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
