package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// corpusRealV1Dir is the first real evaluation corpus (bd-18u v0.6): 15 cases
// exercising Conduit's actual surface — lookups, multi-step tool sequences,
// refusals, tone, and error reporting — with synthetic identities only
// (example.com / *.example.test hosts, no real people or infrastructure).
//
// Category design:
//   - lookup         (rc-0001..0003): one tool call, deterministic fragments
//   - multi-step     (rc-0004..0006): 2-3 sequenced calls, per-turn allowlists
//   - refusal/safety (rc-0007..0010): decline or hold; deny_classes encode the
//     side-effect boundary the transcript must respect
//   - tone           (rc-0011..0013): NO expected_fragments by design —
//     personality is scored by the goal rubric only
//   - error-handling (rc-0014..0015): report the failure, no silent retry
//
// The case files were authored against the v0.6 corpus format: case-level
// turn_budget / deny_classes / expected_fragments / cost / goal frontmatter,
// and transcript rows carrying plain "args" strings plus per-row "allow"
// lists. The eval-scaffold loader (bd-18u v0.1) instead models expectations
// as rubric items plus per-turn tools_expected, and tool args as JSON
// strings. loadRealV1 adapts the former to the latter without touching the
// case files:
//
//	turn_budget N      -> rubric "turn_count_le N" (deterministic)
//	cost_budget_usd X  -> rubric "cost_usd_le X" (deterministic; the case's
//	                      scripted token totals are spread across assistant
//	                      rows so the cost formula sums to the same value)
//	deny_classes [...] -> rubric "deny [a, b]" (deterministic hard gate)
//	goal "..."         -> rubric llm_judge item (unscored in replay mode)
//	expected_fragments -> test-side content assertions (legacy semantics:
//	                      fragments must appear in the scripted replies;
//	                      deterministic arg-fragment checks never applied to
//	                      reply text, so they stay out of the rubric)
//	row args "..."     -> ToolCall.ArgsJSON (JSON-encoded) plus a per-turn
//	                      tools_expected fragment pinning that exact call
//	row allow [...]    -> Turn.Allow (harness per-turn allowlist check)
const corpusRealV1Dir = "testdata/corpus-real-v1"

// legacyCase is the v0.6 frontmatter shape of a real-corpus case file.
type legacyCase struct {
	ID                string   `yaml:"id"`
	Title             string   `yaml:"title"`
	Tags              []string `yaml:"tags"`
	ModelsUnderTest   []string `yaml:"models_under_test"`
	TurnBudget        int      `yaml:"turn_budget"`
	DenyClasses       []string `yaml:"deny_classes"`
	ExpectedFragments []string `yaml:"expected_fragments"`
	CostBudgetUSD     float64  `yaml:"cost_budget_usd"`
	Cost              struct {
		Model            string `yaml:"model"`
		PromptTokens     int64  `yaml:"prompt_tokens"`
		CompletionTokens int64  `yaml:"completion_tokens"`
	} `yaml:"cost"`
	Goal string `yaml:"goal"`
}

// legacyTool is one tool call in a v0.6 transcript row: plain args text.
type legacyTool struct {
	Name string `json:"name"`
	Args string `json:"args,omitempty"`
}

// legacyTurn is one v0.6 transcript row.
type legacyTurn struct {
	Num     int          `json:"turn"`
	Role    string       `json:"role"`
	Content string       `json:"content"`
	Tools   []legacyTool `json:"tools,omitempty"`
	Allow   []string     `json:"allow,omitempty"`
}

// loadRealV1 loads every case in corpusRealV1Dir through the legacy-format
// parser and adapts it to the current Case/Case rubric model. It returns the
// cases in file-name order plus each case's legacy expected_fragments (reply
// content assertions, enforced test-side).
func loadRealV1(t *testing.T) ([]*Case, map[string][]string) {
	t.Helper()
	entries, err := os.ReadDir(corpusRealV1Dir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", corpusRealV1Dir, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		t.Fatalf("no case files (*.md) in %s", corpusRealV1Dir)
	}
	sort.Strings(names)

	var cases []*Case
	fragments := make(map[string][]string, len(names))
	for _, name := range names {
		path := filepath.Join(corpusRealV1Dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", path, err)
		}
		fm, body, err := splitFrontmatter(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var lc legacyCase
		if err := yaml.Unmarshal(fm, &lc); err != nil {
			t.Fatalf("%s: frontmatter: %v", name, err)
		}
		if lc.ID == "" {
			t.Fatalf("%s: case id is required", name)
		}
		rows := parseLegacyRows(t, name, string(body))
		c := adaptLegacyCase(lc, rows)
		c.Source = path
		cases = append(cases, c)
		fragments[lc.ID] = lc.ExpectedFragments
	}
	return cases, fragments
}

// parseLegacyRows extracts the fenced JSONL block from a v0.6 case body and
// decodes the rows in their original shape (plain args strings).
func parseLegacyRows(t *testing.T, name, body string) []legacyTurn {
	t.Helper()
	const fence = "```"
	var inside bool
	var rows []legacyTurn
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(strings.TrimSpace(line), fence) {
			if inside {
				break
			}
			inside = true
			continue
		}
		if !inside {
			continue
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var r legacyTurn
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("%s: transcript row: %v", name, err)
		}
		if r.Role == "" {
			t.Fatalf("%s: transcript row without role", name)
		}
		rows = append(rows, r)
	}
	if !inside {
		t.Fatalf("%s: no fenced JSONL block found", name)
	}
	if len(rows) == 0 {
		t.Fatalf("%s: turn transcript is empty", name)
	}
	return rows
}

// adaptLegacyCase converts one v0.6 case into the current Case model: legacy
// case-level frontmatter becomes rubric items, legacy rows become Turns with
// ArgsJSON tool calls, per-turn allowlists, and per-turn expected fragments.
func adaptLegacyCase(lc legacyCase, rows []legacyTurn) *Case {
	c := &Case{
		ID:              lc.ID,
		Title:           lc.Title,
		Tags:            lc.Tags,
		ModelsUnderTest: lc.ModelsUnderTest,
		Mode:            "replay",
	}
	if lc.TurnBudget > 0 {
		c.Rubric = append(c.Rubric, RubricItem{
			ID:    lc.ID + "-budget",
			Type:  "deterministic",
			Check: fmt.Sprintf("turn_count_le %d", lc.TurnBudget),
			// Detail could restate the original key; keep the check itself
			// the single source of truth.
			Detail: "from turn_budget",
			Weight: 1,
		})
	}
	if lc.CostBudgetUSD > 0 {
		c.Rubric = append(c.Rubric, RubricItem{
			ID:     lc.ID + "-cost",
			Type:   "deterministic",
			Check:  fmt.Sprintf("cost_usd_le %g", lc.CostBudgetUSD),
			Detail: "from cost_budget_usd",
			Weight: 1,
		})
	}
	if len(lc.DenyClasses) > 0 {
		c.Rubric = append(c.Rubric, RubricItem{
			ID:     lc.ID + "-deny",
			Type:   "deterministic",
			Check:  "deny [" + strings.Join(lc.DenyClasses, ", ") + "]",
			Detail: "from deny_classes",
			Weight: 1,
		})
	}
	if lc.Goal != "" {
		c.Rubric = append(c.Rubric, RubricItem{
			ID:     lc.ID + "-goal",
			Type:   "llm_judge",
			Check:  "goal",
			Detail: lc.Goal,
			Weight: 1,
		})
	}

	// Spread the case-level scripted cost across assistant rows so the cost
	// formula (Σ (prompt*5 + completion*20) / 1e6) still totals the same
	// value the legacy budget was written against. Remainder on the last row.
	var asst []int
	for i, r := range rows {
		if r.Role == "assistant" {
			asst = append(asst, i)
		}
	}
	lastAsst := -1
	pt, ct := int64(0), int64(0)
	var lastPT, lastCT int64
	if len(asst) > 0 {
		n := int64(len(asst))
		lastAsst = asst[len(asst)-1]
		pt = lc.Cost.PromptTokens / n
		ct = lc.Cost.CompletionTokens / n
		lastPT = lc.Cost.PromptTokens - pt*(n-1)
		lastCT = lc.Cost.CompletionTokens - ct*(n-1)
	}

	for i, r := range rows {
		nt := Turn{Num: r.Num, Role: r.Role, Content: r.Content}
		if r.Role == "assistant" {
			if i == lastAsst {
				nt.PromptTokens, nt.CompletionTokens = lastPT, lastCT
			} else {
				nt.PromptTokens, nt.CompletionTokens = pt, ct
			}
		}
		if len(r.Allow) > 0 {
			nt.Allow = append([]string(nil), r.Allow...)
		}
		for _, tl := range r.Tools {
			argsJSON, err := json.Marshal(tl.Args)
			if err != nil {
				// Marshal of a plain string cannot fail; guard anyway.
				argsJSON = []byte(`""`)
			}
			nt.Tools = append(nt.Tools, ToolCall{Name: tl.Name, ArgsJSON: string(argsJSON)})
			// Pin the scripted call as this turn's expected fragment so the
			// harness's tools_expected check has real assertions to verify.
			nt.ToolsExpected = append(nt.ToolsExpected, ToolFragment{
				Name:       tl.Name,
				ArgsSubstr: []string{tl.Args},
			})
		}
		c.Turns = append(c.Turns, nt)
	}
	normalizeCase(c)
	return c
}

// assistantContent concatenates the scripted assistant reply text of a case.
func assistantContent(c *Case) string {
	var b strings.Builder
	for _, t := range c.Turns {
		if t.Role == "assistant" {
			b.WriteString(t.Content)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// TestLoadCorpus_RealV1 loads the real corpus through the legacy adapter and
// validates count, ordering, ID uniqueness, and basic shape.
func TestLoadCorpus_RealV1(t *testing.T) {
	cases, _ := loadRealV1(t)
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
		if c.Mode != "replay" {
			t.Errorf("%s: mode = %q, want replay", c.ID, c.Mode)
		}
		if len(c.Turns) == 0 {
			t.Errorf("%s: no turns after adaptation", c.ID)
		}
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
	cases, _ := loadRealV1(t)
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
// scored by the goal rubric (llm_judge), never by deterministic string
// checks.
func TestCorpus_RealV1_ToneCasesAreJudgeScored(t *testing.T) {
	cases, fragments := loadRealV1(t)
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
		if frags := fragments[c.ID]; len(frags) != 0 {
			t.Errorf("%s: tone case has expected_fragments %v; tone must be judge-scored", c.ID, frags)
		}
		goal := false
		for _, item := range c.Rubric {
			if item.Type == "llm_judge" && item.Detail != "" {
				goal = true
			}
		}
		if !goal {
			t.Errorf("%s: tone case has no goal rubric item", c.ID)
		}
	}
	if tone < 2 {
		t.Fatalf("found %d tone cases, want >= 2", tone)
	}
}

// TestCorpus_RealV1_ScoresClean runs the real corpus through the full scoring
// pipeline (Run + every deterministic check) and requires every case to pass
// with score 1.0. This proves the adapted corpus is internally consistent:
// fragments actually appear in the scripted replies, deny classes never
// collide with the tools positive cases invoke, per-turn allowlists cover
// every scripted call, and turn/cost budgets hold. Judge-only goal items are
// verified to be present but unscored in replay mode.
func TestCorpus_RealV1_ScoresClean(t *testing.T) {
	cases, fragments := loadRealV1(t)
	run, err := Run(cases, RunOptions{CorpusDir: corpusRealV1Dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.Total != len(cases) {
		t.Fatalf("ran %d cases, want %d", run.Total, len(cases))
	}
	byID := make(map[string]CaseResult, len(run.Results))
	for _, res := range run.Results {
		byID[res.CaseID] = res
	}
	for _, c := range cases {
		res, ok := byID[c.ID]
		if !ok {
			t.Fatalf("%s: no result from Run", c.ID)
		}
		if !res.Passed {
			t.Errorf("%s: did not pass", c.ID)
		}
		for _, chk := range res.Checks {
			if chk.Judged {
				if chk.Passed {
					t.Errorf("%s: check %s is judge-scored but reports Passed", c.ID, chk.ID)
				}
				continue
			}
			if !chk.Passed {
				t.Errorf("%s: check %s (%s) failed: %s", c.ID, chk.ID, chk.Check, chk.Detail)
			}
		}
		if res.Score != 1.0 {
			t.Errorf("%s: score = %v, want 1.0 (all deterministic items must pass)", c.ID, res.Score)
		}
		if res.Turns == 0 {
			t.Errorf("%s: zero turns in result", c.ID)
		}
		if res.CostUSD <= 0 {
			t.Errorf("%s: cost = %v, want > 0", c.ID, res.CostUSD)
		}
		// Legacy expected_fragments are reply-content assertions (v0.6
		// semantics): every fragment must appear in the scripted replies,
		// matched case-insensitively (the original matcher lowercased both
		// sides — several expectations are lowercase while their reply opens
		// the sentence capitalized).
		content := strings.ToLower(assistantContent(c))
		for _, frag := range fragments[c.ID] {
			if !strings.Contains(content, strings.ToLower(frag)) {
				t.Errorf("%s: expected fragment %q not found in scripted replies", c.ID, frag)
			}
		}
	}
	if run.Failed != 0 {
		t.Errorf("run reports %d failed cases, want 0", run.Failed)
	}
	if run.Score != 1.0 {
		t.Errorf("aggregate score = %v, want 1.0", run.Score)
	}
}
