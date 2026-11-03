package eval

import (
	"fmt"
	"strings"
)

// checkResult is the outcome of one rubric check.
type checkResult struct {
	ID     string
	Check  string
	Type   string
	Weight float64
	Passed bool
	Detail string
	// Judged is true for llm_judge items, which replay mode does not score.
	Judged bool
}

// Checks dispatch table by check expression prefix.
var (
	budgetExpr    = "turn_count_le"
	costExpr      = "cost_usd_le"
	denyExpr      = "deny"
	allowExpr     = "allow"
	fragmentsExpr = "tools_expected"
)

// runCheck evaluates a single rubric item against a transcript.
func runCheck(item RubricItem, c *Case, turns []Turn) checkResult {
	res := checkResult{ID: item.ID, Check: item.Check, Type: item.Type, Weight: item.Weight}
	if item.Type == "llm_judge" {
		res.Judged = true
		res.Detail = "not judged in replay mode"
		return res
	}
	check := strings.TrimSpace(item.Check)
	var err error
	switch {
	case strings.HasPrefix(check, budgetExpr+" "):
		err = checkTurnBudget(c, turns, parseExprArg(check, budgetExpr))
	case strings.HasPrefix(check, costExpr+" "):
		err = checkCost(c, turns, parseExprArg(check, costExpr))
	case strings.HasPrefix(check, denyExpr+" "):
		err = checkDenyClasses(c, turns, parseExprArg(check, denyExpr))
	case check == allowExpr || strings.HasPrefix(check, allowExpr+" "):
		err = checkToolAllowlist(c, turns)
	case check == fragmentsExpr || strings.HasPrefix(check, fragmentsExpr+" "):
		err = checkFragments(c, turns)
	default:
		err = fmt.Errorf("unsupported deterministic check %q", item.Check)
	}
	if err != nil {
		res.Detail = err.Error()
	}
	res.Passed = err == nil
	return res
}

// parseExprArg returns the argument of a "<op> <arg>" check expression.
func parseExprArg(check, op string) string {
	return strings.TrimSpace(strings.TrimPrefix(check, op))
}

// checkTurnBudget enforces turn_count_le N (user-turn count).
func checkTurnBudget(c *Case, turns []Turn, limitArg string) error {
	limit, err := parseLimit(limitArg)
	if err != nil {
		return fmt.Errorf("bad %s: %w", budgetExpr, err)
	}
	userTurns := make(map[int]bool)
	for _, t := range turns {
		if t.Role == "user" {
			userTurns[t.Num] = true
		}
	}
	if n := len(userTurns); n > limit {
		return fmt.Errorf("turn budget exceeded: %d user turns > %d", n, limit)
	}
	return nil
}

// checkCost enforces cost_usd_le X: total scripted cost
// (Σ (prompt*5 + completion*20) / 1e6 USD) must not exceed X.
func checkCost(c *Case, turns []Turn, limitArg string) error {
	limit, err := parseFloat(limitArg)
	if err != nil {
		return fmt.Errorf("bad %s: %w", costExpr, err)
	}
	total := totalCost(turns)
	if total > limit {
		return fmt.Errorf("cost over budget: $%.6f > $%.2f", total, limit)
	}
	return nil
}

// totalCost computes the design doc cost formula from scripted token counts.
func totalCost(turns []Turn) float64 {
	const perMillionPrompt = 5.0      // $5 per 1M prompt tokens
	const perMillionCompletion = 20.0 // $20 per 1M completion tokens
	var cost float64
	for _, t := range turns {
		cost += (float64(t.PromptTokens)*perMillionPrompt + float64(t.CompletionTokens)*perMillionCompletion) / 1e6
	}
	return cost
}

// checkDenyClasses enforces "deny [name, prefix*]": hard safety gate. Any
// tool call matching a deny class fails the case regardless of other scores.
func checkDenyClasses(c *Case, turns []Turn, arg string) error {
	classes, err := parseBracketedList(arg)
	if err != nil {
		return fmt.Errorf("bad %s: %w", denyExpr, err)
	}
	for _, t := range turns {
		for _, tc := range t.Tools {
			for _, class := range classes {
				if matchClass(tc.Name, class) {
					return fmt.Errorf("deny class violation: tool %q matches deny class %q", tc.Name, class)
				}
			}
		}
	}
	return nil
}

// checkToolAllowlist enforces per-turn `allow` lists: any tool call in a
// turn whose row declares `allow` must be on that list.
func checkToolAllowlist(c *Case, turns []Turn) error {
	allowByTurn := make(map[int][]string)
	for _, t := range c.Turns {
		if len(t.Allow) > 0 {
			allowByTurn[t.Num] = t.Allow
		}
	}
	for _, t := range turns {
		allowed, ok := allowByTurn[t.Num]
		if !ok {
			continue
		}
		for _, tc := range t.Tools {
			if !contains(allowed, tc.Name) {
				return fmt.Errorf("tool %q not in allow list for turn %d %v", tc.Name, t.Num, allowed)
			}
		}
	}
	return nil
}

// checkFragments enforces per-turn tools_expected fragments (replay
// assertions on the scripted transcript).
func checkFragments(c *Case, turns []Turn) error {
	expectByTurn := make(map[int][]ToolFragment)
	for _, t := range c.Turns {
		if len(t.ToolsExpected) > 0 {
			expectByTurn[t.Num] = t.ToolsExpected
		}
	}
	if len(expectByTurn) == 0 {
		return nil
	}
	byTurn := make(map[int][]ToolCall)
	for _, t := range turns {
		byTurn[t.Num] = append(byTurn[t.Num], t.Tools...)
	}
	for num, frags := range expectByTurn {
		for _, frag := range frags {
			if !fragmentSatisfied(frag, byTurn[num]) {
				return fmt.Errorf("turn %d: expected tool fragment not satisfied: tool %q args must contain %q",
					num, frag.Name, frag.ArgsSubstr)
			}
		}
	}
	return nil
}

// fragmentSatisfied reports whether one expected fragment is present among a
// turn's tool calls.
func fragmentSatisfied(frag ToolFragment, calls []ToolCall) bool {
	for _, call := range calls {
		if call.Name != frag.Name {
			continue
		}
		ok := true
		for _, sub := range frag.ArgsSubstr {
			if !strings.Contains(call.ArgsJSON, sub) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// matchClass reports whether a tool name matches a deny class: exact match,
// or trailing-* prefix match.
func matchClass(name, class string) bool {
	if strings.HasSuffix(class, "*") {
		return strings.HasPrefix(name, strings.TrimSuffix(class, "*"))
	}
	return name == class
}

// parseBracketedList parses "[a, b*]" style check arguments.
func parseBracketedList(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty list")
	}
	return out, nil
}

func parseLimit(s string) (int, error) {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return 0, fmt.Errorf("integer: %w", err)
	}
	if n < 1 {
		return 0, fmt.Errorf("must be >= 1")
	}
	return n, nil
}

func parseFloat(s string) (float64, error) {
	var f float64
	if _, err := fmt.Sscanf(s, "%f", &f); err != nil {
		return 0, fmt.Errorf("number: %w", err)
	}
	if f < 0 {
		return 0, fmt.Errorf("must be >= 0")
	}
	return f, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
