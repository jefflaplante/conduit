package ai

import (
	"strings"
)

// ComplexityLevel represents the estimated complexity tier of a task.
type ComplexityLevel int

const (
	// ComplexitySimple is a straightforward query that needs no tools or a single
	// simple tool call (e.g., greeting, factual lookup, single file read).
	ComplexitySimple ComplexityLevel = iota

	// ComplexityStandard is a moderate task that may involve a small number of tool
	// calls or light reasoning (e.g., search + summarize, edit a file).
	ComplexityStandard

	// ComplexityComplex is a multi-step task requiring deep reasoning, many tool
	// calls, or chained operations (e.g., code refactoring, research report,
	// multi-file edit with planning).
	ComplexityComplex
)

// String returns a human-readable name for the complexity level.
func (c ComplexityLevel) String() string {
	switch c {
	case ComplexitySimple:
		return "simple"
	case ComplexityStandard:
		return "standard"
	case ComplexityComplex:
		return "complex"
	default:
		return "unknown"
	}
}

// ComplexityScore holds the numeric score and derived complexity level
// for a given request or tool chain.
type ComplexityScore struct {
	// Level is the derived complexity tier.
	Level ComplexityLevel `json:"level"`

	// Score is the raw numeric score (0-100). Higher = more complex.
	Score int `json:"score"`

	// Reasons records why the score was set (useful for debugging / logging).
	Reasons []string `json:"reasons,omitempty"`
}

// ComplexityAnalyzer evaluates the complexity of a request or tool chain.
type ComplexityAnalyzer struct {
	// complexToolNames are tools known to require deeper reasoning.
	complexToolNames map[string]bool

	// simpleToolNames are tools known to be lightweight.
	simpleToolNames map[string]bool
}

// NewComplexityAnalyzer creates a new ComplexityAnalyzer with default tool
// classification tables.
func NewComplexityAnalyzer() *ComplexityAnalyzer {
	return &ComplexityAnalyzer{
		complexToolNames: map[string]bool{
			"Bash":          true,
			"Edit":          true,
			"Write":         true,
			"WebSearch":     true,
			"WebFetch":      true,
			"Task":          true,
			"NotebookEdit":  true,
			"SessionsSpawn": true,
		},
		simpleToolNames: map[string]bool{
			"Read":         true,
			"Glob":         true,
			"Grep":         true,
			"TodoWrite":    true,
			"MemorySearch": true,
		},
	}
}

// AnalyzeMessage estimates the complexity of a user message before any tool
// calls have been made. This uses heuristics on message length and keywords.
func (ca *ComplexityAnalyzer) AnalyzeMessage(message string) ComplexityScore {
	score := 0
	var reasons []string

	// Factor 1: Message length (longer messages often mean more complex tasks)
	wordCount := len(strings.Fields(message))
	switch {
	case wordCount > 200:
		score += 30
		reasons = append(reasons, "very long message (>200 words)")
	case wordCount > 80:
		score += 15
		reasons = append(reasons, "long message (>80 words)")
	case wordCount > 30:
		score += 5
		reasons = append(reasons, "moderate message length")
	}

	// Factor 2: Complexity keywords
	lower := strings.ToLower(message)
	complexKeywords := []struct {
		keyword string
		points  int
		reason  string
	}{
		{"refactor", 20, "mentions refactoring"},
		{"implement", 15, "mentions implementation"},
		{"create a", 10, "mentions creation"},
		{"build", 10, "mentions building"},
		{"analyze", 15, "mentions analysis"},
		{"research", 15, "mentions research"},
		{"compare", 10, "mentions comparison"},
		{"debug", 15, "mentions debugging"},
		{"fix the", 10, "mentions fixing"},
		{"multiple files", 15, "mentions multiple files"},
		{"step by step", 10, "mentions step-by-step"},
		{"plan", 10, "mentions planning"},
		{"architecture", 15, "mentions architecture"},
		{"design", 10, "mentions design"},
		{"migrate", 15, "mentions migration"},
		{"test", 5, "mentions testing"},
	}

	matchedKeywords := 0
	for _, kw := range complexKeywords {
		if strings.Contains(lower, kw.keyword) {
			score += kw.points
			reasons = append(reasons, kw.reason)
			matchedKeywords++
		}
	}

	// Multiple complexity keywords compound the difficulty
	if matchedKeywords >= 3 {
		score += 10
		reasons = append(reasons, "multiple complexity indicators")
	}

	// Factor 3: Question simplicity markers (reduce score)
	simplePatterns := []string{
		"what is", "what's", "how do i", "hello", "hi ",
		"thanks", "thank you", "yes", "no", "ok",
	}
	for _, pat := range simplePatterns {
		if strings.HasPrefix(lower, pat) || lower == strings.TrimSpace(pat) {
			score -= 15
			reasons = append(reasons, "simple question/greeting pattern")
			break
		}
	}

	return ca.scoreToResult(score, reasons)
}

// AnalyzeToolCalls estimates complexity based on a set of tool calls that were
// requested (or are anticipated) in a single turn.
func (ca *ComplexityAnalyzer) AnalyzeToolCalls(toolCalls []ToolCall) ComplexityScore {
	if len(toolCalls) == 0 {
		return ComplexityScore{Level: ComplexitySimple, Score: 0, Reasons: []string{"no tool calls"}}
	}

	score := 0
	var reasons []string

	// Factor 1: Number of tool calls
	numCalls := len(toolCalls)
	switch {
	case numCalls >= 5:
		score += 35
		reasons = append(reasons, "many tool calls (>=5)")
	case numCalls >= 3:
		score += 20
		reasons = append(reasons, "several tool calls (>=3)")
	case numCalls >= 2:
		score += 10
		reasons = append(reasons, "multiple tool calls")
	default:
		score += 3
		reasons = append(reasons, "single tool call")
	}

	// Factor 2: Tool complexity classification
	complexCount := 0
	simpleCount := 0
	for _, tc := range toolCalls {
		if ca.complexToolNames[tc.Name] {
			complexCount++
		}
		if ca.simpleToolNames[tc.Name] {
			simpleCount++
		}
	}

	if complexCount > 0 {
		score += complexCount * 12
		reasons = append(reasons, "includes complex tool(s)")
	}
	if simpleCount == numCalls {
		score -= 10
		reasons = append(reasons, "all tools are simple")
	}

	// Factor 3: Parameter complexity (deep/nested arguments)
	for _, tc := range toolCalls {
		paramScore := ca.analyzeParameterComplexity(tc.Args)
		if paramScore > 0 {
			score += paramScore
			reasons = append(reasons, "complex parameters detected")
			break // count once
		}
	}

	return ca.scoreToResult(score, reasons)
}

// AnalyzeToolDefinitions estimates the potential complexity of a task based
// on which tools are available. More available tools suggests the system is
// configured for more complex workflows.
func (ca *ComplexityAnalyzer) AnalyzeToolDefinitions(tools []Tool) ComplexityScore {
	if len(tools) == 0 {
		return ComplexityScore{Level: ComplexitySimple, Score: 0, Reasons: []string{"no tools available"}}
	}

	score := 0
	var reasons []string

	// More tools available => higher potential complexity
	switch {
	case len(tools) >= 15:
		score += 15
		reasons = append(reasons, "many tools available (>=15)")
	case len(tools) >= 8:
		score += 8
		reasons = append(reasons, "several tools available")
	}

	// Count complex tools in the available set
	complexAvailable := 0
	for _, t := range tools {
		if ca.complexToolNames[t.Name] {
			complexAvailable++
		}
	}
	if complexAvailable >= 3 {
		score += 10
		reasons = append(reasons, "multiple complex tools available")
	}

	return ca.scoreToResult(score, reasons)
}

// AnalyzeToolChainDepth estimates complexity from an ongoing tool chain
// (number of iterations so far, tool call history).
func (ca *ComplexityAnalyzer) AnalyzeToolChainDepth(steps int, toolCallHistory [][]ToolCall) ComplexityScore {
	score := 0
	var reasons []string

	// Factor 1: Chain depth
	switch {
	case steps >= 10:
		score += 40
		reasons = append(reasons, "very deep tool chain (>=10 steps)")
	case steps >= 5:
		score += 25
		reasons = append(reasons, "deep tool chain (>=5 steps)")
	case steps >= 3:
		score += 15
		reasons = append(reasons, "moderate tool chain (>=3 steps)")
	case steps >= 1:
		score += 5
		reasons = append(reasons, "some tool chain depth")
	}

	// Factor 2: Unique tools used across the chain
	uniqueTools := make(map[string]bool)
	totalCalls := 0
	for _, calls := range toolCallHistory {
		for _, tc := range calls {
			uniqueTools[tc.Name] = true
			totalCalls++
		}
	}

	if len(uniqueTools) >= 4 {
		score += 15
		reasons = append(reasons, "diverse tool usage (>=4 unique tools)")
	}
	if totalCalls >= 8 {
		score += 10
		reasons = append(reasons, "high total tool call count")
	}

	return ca.scoreToResult(score, reasons)
}

// CombineScores merges multiple complexity scores into a single composite.
// The resulting score is the maximum of the individual scores (not the sum),
// since any single high-complexity signal should drive the routing decision.
func (ca *ComplexityAnalyzer) CombineScores(scores ...ComplexityScore) ComplexityScore {
	if len(scores) == 0 {
		return ComplexityScore{Level: ComplexitySimple, Score: 0}
	}

	maxScore := 0
	var allReasons []string

	for _, s := range scores {
		if s.Score > maxScore {
			maxScore = s.Score
		}
		allReasons = append(allReasons, s.Reasons...)
	}

	return ca.scoreToResult(maxScore, allReasons)
}

// analyzeParameterComplexity scores the complexity of tool call arguments.
func (ca *ComplexityAnalyzer) analyzeParameterComplexity(args map[string]interface{}) int {
	if len(args) == 0 {
		return 0
	}

	score := 0

	// Many parameters
	if len(args) >= 5 {
		score += 8
	}

	// Check for long string values (e.g., large code blocks, prompts)
	for _, v := range args {
		if s, ok := v.(string); ok && len(s) > 500 {
			score += 5
			break
		}
		// Nested objects
		if _, ok := v.(map[string]interface{}); ok {
			score += 5
			break
		}
		// Array values
		if _, ok := v.([]interface{}); ok {
			score += 3
			break
		}
	}

	return score
}

// scoreToResult converts a raw score to a ComplexityScore with the appropriate level.
func (ca *ComplexityAnalyzer) scoreToResult(score int, reasons []string) ComplexityScore {
	// Clamp to 0-100
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}

	var level ComplexityLevel
	switch {
	case score >= 40:
		level = ComplexityComplex
	case score >= 15:
		level = ComplexityStandard
	default:
		level = ComplexitySimple
	}

	return ComplexityScore{
		Level:   level,
		Score:   score,
		Reasons: reasons,
	}
}
