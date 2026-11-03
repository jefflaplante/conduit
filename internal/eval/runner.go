package eval

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// ReplayFunc transforms a case's scripted transcript. In v0.1 replay mode the
// identity function suffices (checks validate the scripted transcript); a
// real implementation will drive a model under test and return the produced
// transcript. Returning an error fails that case without aborting the run.
type ReplayFunc func(turns []Turn) ([]Turn, error)

// identityReplay is the default replay function: v0.1 replay mode.
func identityReplay(turns []Turn) ([]Turn, error) {
	return turns, nil
}

// RunOptions configures a Run.
type RunOptions struct {
	// Replay transforms each case transcript before checks run. Nil means
	// identity (pure replay).
	Replay ReplayFunc
	// Baseline, when non-nil, enables regression comparison (design doc:
	// >5% weighted-score drop, plus pass→fail flips, are flagged).
	Baseline map[string]CaseResult
	// CorpusDir labels the run in reports (diagnostics only).
	CorpusDir string
	// Now overrides the run timestamp (tests).
	Now func() time.Time
}

// Run executes every case against replay and collects results, in the order
// the cases were loaded.
func Run(cases []*Case, opts RunOptions) (*RunResult, error) {
	if opts.Replay == nil {
		opts.Replay = identityReplay
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	res := &RunResult{GeneratedAt: now().UTC().Format(time.RFC3339), CorpusDir: opts.CorpusDir}
	for _, c := range cases {
		cr := runCase(c, opts.Replay)
		res.Results = append(res.Results, cr)
		if cr.Passed {
			res.Passed++
		} else {
			res.Failed++
		}
	}
	res.Total = len(res.Results)
	res.Score = aggregateScore(res.Results)
	if opts.Baseline != nil {
		res.Regressions = compareBaseline(res.Results, opts.Baseline)
	}
	return res, nil
}

// runCase replays one case and applies all its rubric checks.
func runCase(c *Case, replay ReplayFunc) CaseResult {
	cr := CaseResult{
		CaseID: c.ID,
		Title:  c.Title,
		Family: c.Family,
		Mode:   c.Mode,
	}
	turns, err := replay(c.Turns)
	if err != nil {
		cr.Error = err.Error()
		return cr
	}

	results := runAllChecks(c, turns)
	for _, r := range results {
		cr.Checks = append(cr.Checks, CheckRecord{
			ID:     r.ID,
			Type:   r.Type,
			Check:  r.Check,
			Weight: r.Weight,
			Passed: r.Passed,
			Detail: r.Detail,
			Judged: r.Judged,
		})
	}
	cr.Passed = casePassed(results)
	cr.Score = caseScore(c, results)
	cr.Turns = distinctTurnCount(turns)
	cr.ToolCalls = countToolCalls(turns)
	cr.CostUSD = totalCost(turns)
	return cr
}

// distinctTurnCount counts distinct turn numbers in a transcript.
func distinctTurnCount(turns []Turn) int {
	seen := make(map[int]bool)
	for _, t := range turns {
		seen[t.Num] = true
	}
	return len(seen)
}

// countToolCalls counts all tool calls across a transcript.
func countToolCalls(turns []Turn) int {
	n := 0
	for _, t := range turns {
		n += len(t.Tools)
	}
	return n
}

// LoadBaseline loads a previous JSONL report as a baseline map keyed by case
// id, skipping non-case lines (e.g. the summary line).
func LoadBaseline(path string) (map[string]CaseResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open baseline: %w", err)
	}
	defer f.Close()

	out := make(map[string]CaseResult)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var r CaseResult
		if err := json.Unmarshal(line, &r); err != nil {
			continue // not a case line (e.g. the summary line)
		}
		if r.CaseID == "" {
			continue
		}
		out[r.CaseID] = r
	}
	return out, sc.Err()
}
