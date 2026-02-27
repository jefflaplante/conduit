package orchestration

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFanOut(t *testing.T) {
	runner := &mockRunner{}
	orch := NewOrchestrator(runner)
	ctx := context.Background()

	result, err := FanOut(ctx, orch, FanOutConfig{
		Agents: []AgentConfig{
			{Role: "analyst-1"},
			{Role: "analyst-2"},
			{Role: "analyst-3"},
		},
		Message: "analyze this data",
		Timeout: 5 * time.Second,
	})

	require.NoError(t, err)
	require.Len(t, result.Results, 3)
	assert.True(t, result.Duration > 0)

	for _, r := range result.Results {
		assert.Equal(t, AgentStatusCompleted, r.Status)
		assert.Contains(t, r.Response, "response from analyst-")
	}
}

func TestFanOutEmpty(t *testing.T) {
	runner := &mockRunner{}
	orch := NewOrchestrator(runner)
	ctx := context.Background()

	result, err := FanOut(ctx, orch, FanOutConfig{
		Agents:  nil,
		Message: "nothing",
		Timeout: time.Second,
	})

	require.NoError(t, err)
	assert.Empty(t, result.Results)
}

func TestFanOutAllSameMessage(t *testing.T) {
	runner := &mockRunner{}
	orch := NewOrchestrator(runner)
	ctx := context.Background()

	_, err := FanOut(ctx, orch, FanOutConfig{
		Agents: []AgentConfig{
			{Role: "a"},
			{Role: "b"},
		},
		Message: "same task",
		Timeout: 5 * time.Second,
	})

	require.NoError(t, err)

	calls := runner.getCalls()
	require.Len(t, calls, 2)
	for _, c := range calls {
		assert.Equal(t, "same task", c.Message)
	}
}

func TestPipeline(t *testing.T) {
	runner := &mockRunner{
		runFunc: func(ctx context.Context, agentID string, config AgentConfig, message string, shared *SharedContext) (AgentResult, error) {
			return AgentResult{
				Response: fmt.Sprintf("[%s processed: %s]", config.Role, message),
			}, nil
		},
	}
	orch := NewOrchestrator(runner)
	ctx := context.Background()

	result, err := Pipeline(ctx, orch, PipelineConfig{
		Stages: []PipelineStage{
			{Agent: AgentConfig{Role: "stage1"}},
			{Agent: AgentConfig{Role: "stage2"}},
			{Agent: AgentConfig{Role: "stage3"}},
		},
		InitialInput: "raw data",
		Timeout:      5 * time.Second,
	})

	require.NoError(t, err)
	require.Len(t, result.StageResults, 3)
	assert.Contains(t, result.FinalOutput, "stage3")
	assert.Contains(t, result.FinalOutput, "stage2")
	assert.Contains(t, result.FinalOutput, "raw data")
}

func TestPipelineEmpty(t *testing.T) {
	runner := &mockRunner{}
	orch := NewOrchestrator(runner)
	ctx := context.Background()

	result, err := Pipeline(ctx, orch, PipelineConfig{
		Stages:       nil,
		InitialInput: "nothing",
	})

	require.NoError(t, err)
	assert.Empty(t, result.StageResults)
	assert.Empty(t, result.FinalOutput)
}

func TestPipelineWithTransform(t *testing.T) {
	runner := &mockRunner{
		runFunc: func(ctx context.Context, agentID string, config AgentConfig, message string, shared *SharedContext) (AgentResult, error) {
			return AgentResult{Response: message + " -> processed"}, nil
		},
	}
	orch := NewOrchestrator(runner)
	ctx := context.Background()

	result, err := Pipeline(ctx, orch, PipelineConfig{
		Stages: []PipelineStage{
			{Agent: AgentConfig{Role: "first"}},
			{
				Agent: AgentConfig{Role: "second"},
				TransformInput: func(prev string) string {
					return "TRANSFORMED: " + prev
				},
			},
		},
		InitialInput: "start",
		Timeout:      5 * time.Second,
	})

	require.NoError(t, err)
	// The second stage should have received the transformed input
	assert.Contains(t, result.FinalOutput, "TRANSFORMED")
}

func TestPipelineStageFailure(t *testing.T) {
	var callCount int32
	runner := &mockRunner{
		runFunc: func(ctx context.Context, agentID string, config AgentConfig, message string, shared *SharedContext) (AgentResult, error) {
			n := atomic.AddInt32(&callCount, 1)
			if n == 2 {
				return AgentResult{}, fmt.Errorf("stage 2 exploded")
			}
			return AgentResult{Response: "ok"}, nil
		},
	}
	orch := NewOrchestrator(runner)
	ctx := context.Background()

	_, err := Pipeline(ctx, orch, PipelineConfig{
		Stages: []PipelineStage{
			{Agent: AgentConfig{Role: "s1"}},
			{Agent: AgentConfig{Role: "s2"}},
			{Agent: AgentConfig{Role: "s3"}},
		},
		InitialInput: "go",
		Timeout:      5 * time.Second,
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "stage 1")
}

func TestDebate(t *testing.T) {
	runner := &mockRunner{
		runFunc: func(ctx context.Context, agentID string, config AgentConfig, message string, shared *SharedContext) (AgentResult, error) {
			switch config.Role {
			case "pro":
				return AgentResult{Response: "I am in favor because..."}, nil
			case "con":
				return AgentResult{Response: "I am against because..."}, nil
			case "synthesizer":
				return AgentResult{Response: "After considering both sides..."}, nil
			}
			return AgentResult{Response: "unknown role"}, nil
		},
	}
	orch := NewOrchestrator(runner)
	ctx := context.Background()

	result, err := Debate(ctx, orch, DebateConfig{
		ProAgent:         AgentConfig{Role: "pro"},
		ConAgent:         AgentConfig{Role: "con"},
		SynthesizerAgent: AgentConfig{Role: "synthesizer"},
		Topic:            "Should we use microservices?",
		Rounds:           2,
		Timeout:          10 * time.Second,
	})

	require.NoError(t, err)
	assert.Len(t, result.ProArguments, 2)
	assert.Len(t, result.ConArguments, 2)
	assert.Contains(t, result.Synthesis, "After considering")
	assert.True(t, result.Duration > 0)
}

func TestDebateSingleRound(t *testing.T) {
	runner := &mockRunner{
		runFunc: func(ctx context.Context, agentID string, config AgentConfig, message string, shared *SharedContext) (AgentResult, error) {
			return AgentResult{Response: config.Role + " argument"}, nil
		},
	}
	orch := NewOrchestrator(runner)
	ctx := context.Background()

	result, err := Debate(ctx, orch, DebateConfig{
		ProAgent:         AgentConfig{Role: "pro"},
		ConAgent:         AgentConfig{Role: "con"},
		SynthesizerAgent: AgentConfig{Role: "judge"},
		Topic:            "topic",
		Rounds:           1,
		Timeout:          5 * time.Second,
	})

	require.NoError(t, err)
	assert.Len(t, result.ProArguments, 1)
	assert.Len(t, result.ConArguments, 1)
	assert.NotEmpty(t, result.Synthesis)
}

func TestMapReduce(t *testing.T) {
	runner := &mockRunner{
		runFunc: func(ctx context.Context, agentID string, config AgentConfig, message string, shared *SharedContext) (AgentResult, error) {
			if config.Role == "mapper" {
				return AgentResult{Response: "mapped: " + message}, nil
			}
			if config.Role == "reducer" {
				return AgentResult{Response: "reduced: " + message}, nil
			}
			return AgentResult{}, nil
		},
	}
	orch := NewOrchestrator(runner)
	ctx := context.Background()

	result, err := MapReduce(ctx, orch, MapReduceConfig{
		MapAgent:    AgentConfig{Role: "mapper"},
		ReduceAgent: AgentConfig{Role: "reducer"},
		Inputs:      []string{"chunk1", "chunk2", "chunk3"},
		Timeout:     5 * time.Second,
	})

	require.NoError(t, err)
	assert.Len(t, result.MapResults, 3)
	for _, mr := range result.MapResults {
		assert.Contains(t, mr.Response, "mapped:")
	}
	assert.Contains(t, result.FinalOutput, "reduced:")
	assert.True(t, result.Duration > 0)
}

func TestMapReduceEmpty(t *testing.T) {
	runner := &mockRunner{}
	orch := NewOrchestrator(runner)
	ctx := context.Background()

	result, err := MapReduce(ctx, orch, MapReduceConfig{
		MapAgent:    AgentConfig{Role: "mapper"},
		ReduceAgent: AgentConfig{Role: "reducer"},
		Inputs:      nil,
		Timeout:     time.Second,
	})

	require.NoError(t, err)
	assert.Empty(t, result.MapResults)
	assert.Empty(t, result.FinalOutput)
}

func TestMapReduceWithSharedContext(t *testing.T) {
	runner := &mockRunner{
		runFunc: func(ctx context.Context, agentID string, config AgentConfig, message string, shared *SharedContext) (AgentResult, error) {
			// Mappers write to shared context
			if config.Role == "mapper" {
				shared.Set("mapper_"+agentID, message)
				return AgentResult{Response: "processed " + message}, nil
			}
			// Reducer reads shared context
			if config.Role == "reducer" {
				keys := shared.Keys()
				mapperKeys := 0
				for _, k := range keys {
					if strings.HasPrefix(k, "mapper_") {
						mapperKeys++
					}
				}
				return AgentResult{Response: fmt.Sprintf("reduced %d mapper results", mapperKeys)}, nil
			}
			return AgentResult{}, nil
		},
	}
	orch := NewOrchestrator(runner)
	ctx := context.Background()

	result, err := MapReduce(ctx, orch, MapReduceConfig{
		MapAgent:    AgentConfig{Role: "mapper"},
		ReduceAgent: AgentConfig{Role: "reducer"},
		Inputs:      []string{"a", "b", "c"},
		Timeout:     5 * time.Second,
	})

	require.NoError(t, err)
	assert.Contains(t, result.FinalOutput, "reduced 3 mapper results")
}

func TestBuildSynthesisPrompt(t *testing.T) {
	prompt := buildSynthesisPrompt("test topic",
		[]string{"pro1", "pro2"},
		[]string{"con1", "con2"},
	)

	assert.Contains(t, prompt, "test topic")
	assert.Contains(t, prompt, "PRO arguments")
	assert.Contains(t, prompt, "CON arguments")
	assert.Contains(t, prompt, "Round 1: pro1")
	assert.Contains(t, prompt, "Round 2: con2")
}

func TestBuildReducePrompt(t *testing.T) {
	prompt := buildReducePrompt([]string{"result-A", "result-B"})

	assert.Contains(t, prompt, "Result 1: result-A")
	assert.Contains(t, prompt, "Result 2: result-B")
	assert.Contains(t, prompt, "Combine and synthesize")
}
