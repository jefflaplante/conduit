package ai

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComplexityLevel_String(t *testing.T) {
	assert.Equal(t, "simple", ComplexitySimple.String())
	assert.Equal(t, "standard", ComplexityStandard.String())
	assert.Equal(t, "complex", ComplexityComplex.String())
	assert.Equal(t, "unknown", ComplexityLevel(99).String())
}

func TestNewComplexityAnalyzer(t *testing.T) {
	ca := NewComplexityAnalyzer()
	require.NotNil(t, ca)
	assert.True(t, ca.complexToolNames["Bash"])
	assert.True(t, ca.simpleToolNames["Read"])
}

// --- AnalyzeMessage tests ---

func TestAnalyzeMessage_SimpleGreeting(t *testing.T) {
	ca := NewComplexityAnalyzer()

	result := ca.AnalyzeMessage("hello")
	assert.Equal(t, ComplexitySimple, result.Level)
	assert.LessOrEqual(t, result.Score, 14)
}

func TestAnalyzeMessage_SimpleQuestion(t *testing.T) {
	ca := NewComplexityAnalyzer()

	result := ca.AnalyzeMessage("What is Go?")
	assert.Equal(t, ComplexitySimple, result.Level)
}

func TestAnalyzeMessage_StandardTask(t *testing.T) {
	ca := NewComplexityAnalyzer()

	result := ca.AnalyzeMessage("Please implement a new function to parse JSON config files and test it")
	assert.GreaterOrEqual(t, result.Score, 15)
	assert.LessOrEqual(t, result.Level, ComplexityComplex) // standard or complex
}

func TestAnalyzeMessage_ComplexTask(t *testing.T) {
	ca := NewComplexityAnalyzer()

	msg := "Refactor the architecture of the codebase to migrate from the old design pattern. " +
		"Analyze the existing implementation, create a plan, and implement the changes across multiple files. " +
		"Then build the test suite and debug any issues."
	result := ca.AnalyzeMessage(msg)
	assert.Equal(t, ComplexityComplex, result.Level)
	assert.GreaterOrEqual(t, result.Score, 40)
	assert.NotEmpty(t, result.Reasons)
}

func TestAnalyzeMessage_LongMessage(t *testing.T) {
	ca := NewComplexityAnalyzer()

	// Generate a message with >200 words
	words := make([]string, 210)
	for i := range words {
		words[i] = "word"
	}
	msg := strings.Join(words, " ")

	result := ca.AnalyzeMessage(msg)
	assert.GreaterOrEqual(t, result.Score, 30)
}

func TestAnalyzeMessage_EmptyMessage(t *testing.T) {
	ca := NewComplexityAnalyzer()

	result := ca.AnalyzeMessage("")
	assert.Equal(t, ComplexitySimple, result.Level)
	assert.Equal(t, 0, result.Score)
}

// --- AnalyzeToolCalls tests ---

func TestAnalyzeToolCalls_NoTools(t *testing.T) {
	ca := NewComplexityAnalyzer()

	result := ca.AnalyzeToolCalls(nil)
	assert.Equal(t, ComplexitySimple, result.Level)
	assert.Equal(t, 0, result.Score)
}

func TestAnalyzeToolCalls_SingleSimpleTool(t *testing.T) {
	ca := NewComplexityAnalyzer()

	calls := []ToolCall{
		{ID: "1", Name: "Read", Args: map[string]interface{}{"path": "/tmp/file.txt"}},
	}
	result := ca.AnalyzeToolCalls(calls)
	// Single simple tool: 3 (single call) - 10 (all simple) = 0 (clamped)
	assert.Equal(t, ComplexitySimple, result.Level)
}

func TestAnalyzeToolCalls_SingleComplexTool(t *testing.T) {
	ca := NewComplexityAnalyzer()

	calls := []ToolCall{
		{ID: "1", Name: "Bash", Args: map[string]interface{}{"command": "make build"}},
	}
	result := ca.AnalyzeToolCalls(calls)
	// 3 (single) + 12 (one complex tool) = 15 -> standard
	assert.GreaterOrEqual(t, result.Score, 15)
	assert.Equal(t, ComplexityStandard, result.Level)
}

func TestAnalyzeToolCalls_ManyComplexTools(t *testing.T) {
	ca := NewComplexityAnalyzer()

	calls := []ToolCall{
		{ID: "1", Name: "Bash", Args: map[string]interface{}{"command": "ls"}},
		{ID: "2", Name: "Edit", Args: map[string]interface{}{"path": "file.go"}},
		{ID: "3", Name: "WebSearch", Args: map[string]interface{}{"query": "how to"}},
		{ID: "4", Name: "Write", Args: map[string]interface{}{"path": "out.txt"}},
		{ID: "5", Name: "Task", Args: map[string]interface{}{"task": "analyze"}},
	}
	result := ca.AnalyzeToolCalls(calls)
	assert.Equal(t, ComplexityComplex, result.Level)
	assert.GreaterOrEqual(t, result.Score, 40)
}

func TestAnalyzeToolCalls_ComplexParameters(t *testing.T) {
	ca := NewComplexityAnalyzer()

	// Long string parameter
	longContent := strings.Repeat("x", 600)
	calls := []ToolCall{
		{ID: "1", Name: "Write", Args: map[string]interface{}{
			"path":    "file.go",
			"content": longContent,
		}},
	}
	result := ca.AnalyzeToolCalls(calls)
	assert.True(t, result.Score > 15, "expected score > 15 for complex parameters, got %d", result.Score)
}

func TestAnalyzeToolCalls_NestedArguments(t *testing.T) {
	ca := NewComplexityAnalyzer()

	calls := []ToolCall{
		{ID: "1", Name: "Bash", Args: map[string]interface{}{
			"command": "echo hello",
			"env": map[string]interface{}{
				"PATH": "/usr/bin",
			},
		}},
	}
	result := ca.AnalyzeToolCalls(calls)
	// Should detect nested object in parameters
	assert.GreaterOrEqual(t, result.Score, 15)
}

// --- AnalyzeToolDefinitions tests ---

func TestAnalyzeToolDefinitions_NoTools(t *testing.T) {
	ca := NewComplexityAnalyzer()

	result := ca.AnalyzeToolDefinitions(nil)
	assert.Equal(t, ComplexitySimple, result.Level)
	assert.Equal(t, 0, result.Score)
}

func TestAnalyzeToolDefinitions_FewTools(t *testing.T) {
	ca := NewComplexityAnalyzer()

	tools := []Tool{
		{Name: "Read", Description: "Read a file"},
		{Name: "Grep", Description: "Search files"},
	}
	result := ca.AnalyzeToolDefinitions(tools)
	assert.Equal(t, ComplexitySimple, result.Level)
}

func TestAnalyzeToolDefinitions_ManyTools(t *testing.T) {
	ca := NewComplexityAnalyzer()

	tools := make([]Tool, 16)
	for i := range tools {
		tools[i] = Tool{Name: "Tool" + string(rune('A'+i)), Description: "tool"}
	}
	// Add some complex tools
	tools[0] = Tool{Name: "Bash", Description: "run commands"}
	tools[1] = Tool{Name: "Edit", Description: "edit files"}
	tools[2] = Tool{Name: "Write", Description: "write files"}

	result := ca.AnalyzeToolDefinitions(tools)
	assert.GreaterOrEqual(t, result.Score, 15)
}

// --- AnalyzeToolChainDepth tests ---

func TestAnalyzeToolChainDepth_NoHistory(t *testing.T) {
	ca := NewComplexityAnalyzer()

	result := ca.AnalyzeToolChainDepth(0, nil)
	assert.Equal(t, ComplexitySimple, result.Level)
	assert.Equal(t, 0, result.Score)
}

func TestAnalyzeToolChainDepth_ShallowChain(t *testing.T) {
	ca := NewComplexityAnalyzer()

	history := [][]ToolCall{
		{{ID: "1", Name: "Read"}},
	}
	result := ca.AnalyzeToolChainDepth(1, history)
	assert.Equal(t, ComplexitySimple, result.Level)
}

func TestAnalyzeToolChainDepth_DeepChain(t *testing.T) {
	ca := NewComplexityAnalyzer()

	history := [][]ToolCall{
		{{ID: "1", Name: "Read"}},
		{{ID: "2", Name: "Grep"}},
		{{ID: "3", Name: "Edit"}},
		{{ID: "4", Name: "Bash"}},
		{{ID: "5", Name: "Write"}},
	}
	result := ca.AnalyzeToolChainDepth(5, history)
	assert.GreaterOrEqual(t, result.Score, 25)
	assert.GreaterOrEqual(t, int(result.Level), int(ComplexityStandard))
}

func TestAnalyzeToolChainDepth_VeryDeepChain(t *testing.T) {
	ca := NewComplexityAnalyzer()

	history := make([][]ToolCall, 12)
	for i := range history {
		history[i] = []ToolCall{{ID: "1", Name: "Read"}}
	}
	result := ca.AnalyzeToolChainDepth(12, history)
	assert.Equal(t, ComplexityComplex, result.Level)
	assert.GreaterOrEqual(t, result.Score, 40)
}

func TestAnalyzeToolChainDepth_DiverseTools(t *testing.T) {
	ca := NewComplexityAnalyzer()

	history := [][]ToolCall{
		{{ID: "1", Name: "Read"}},
		{{ID: "2", Name: "Grep"}, {ID: "3", Name: "Glob"}},
		{{ID: "4", Name: "Edit"}, {ID: "5", Name: "Bash"}},
	}
	result := ca.AnalyzeToolChainDepth(3, history)
	// 3 steps (15) + 4+ unique tools (15) = 30+ -> standard
	assert.GreaterOrEqual(t, result.Score, 15)
}

// --- CombineScores tests ---

func TestCombineScores_Empty(t *testing.T) {
	ca := NewComplexityAnalyzer()

	result := ca.CombineScores()
	assert.Equal(t, ComplexitySimple, result.Level)
	assert.Equal(t, 0, result.Score)
}

func TestCombineScores_TakesMaximum(t *testing.T) {
	ca := NewComplexityAnalyzer()

	low := ComplexityScore{Level: ComplexitySimple, Score: 5, Reasons: []string{"low"}}
	high := ComplexityScore{Level: ComplexityComplex, Score: 60, Reasons: []string{"high"}}

	result := ca.CombineScores(low, high)
	assert.Equal(t, ComplexityComplex, result.Level)
	assert.Equal(t, 60, result.Score)
	assert.Len(t, result.Reasons, 2)
}

func TestCombineScores_AggregatesReasons(t *testing.T) {
	ca := NewComplexityAnalyzer()

	s1 := ComplexityScore{Score: 10, Reasons: []string{"reason A"}}
	s2 := ComplexityScore{Score: 20, Reasons: []string{"reason B", "reason C"}}

	result := ca.CombineScores(s1, s2)
	assert.Len(t, result.Reasons, 3)
}

// --- Edge cases ---

func TestScoreClamping(t *testing.T) {
	ca := NewComplexityAnalyzer()

	// Negative score should clamp to 0
	result := ca.scoreToResult(-10, nil)
	assert.Equal(t, 0, result.Score)
	assert.Equal(t, ComplexitySimple, result.Level)

	// Score > 100 should clamp to 100
	result = ca.scoreToResult(150, nil)
	assert.Equal(t, 100, result.Score)
	assert.Equal(t, ComplexityComplex, result.Level)
}

func TestAnalyzeParameterComplexity_EmptyArgs(t *testing.T) {
	ca := NewComplexityAnalyzer()

	score := ca.analyzeParameterComplexity(nil)
	assert.Equal(t, 0, score)

	score = ca.analyzeParameterComplexity(map[string]interface{}{})
	assert.Equal(t, 0, score)
}

func TestAnalyzeParameterComplexity_ManyParams(t *testing.T) {
	ca := NewComplexityAnalyzer()

	args := map[string]interface{}{
		"a": "1", "b": "2", "c": "3", "d": "4", "e": "5",
	}
	score := ca.analyzeParameterComplexity(args)
	assert.GreaterOrEqual(t, score, 8)
}

func TestAnalyzeParameterComplexity_ArrayValues(t *testing.T) {
	ca := NewComplexityAnalyzer()

	args := map[string]interface{}{
		"items": []interface{}{"a", "b", "c"},
	}
	score := ca.analyzeParameterComplexity(args)
	assert.GreaterOrEqual(t, score, 3)
}
