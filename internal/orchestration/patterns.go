package orchestration

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// FanOutConfig configures a fan-out operation where the same task is sent
// to multiple agents and all results are collected.
type FanOutConfig struct {
	Agents  []AgentConfig `json:"agents"`
	Message string        `json:"message"`
	Timeout time.Duration `json:"timeout"`
}

// FanOutResult holds the collected results from a fan-out operation.
type FanOutResult struct {
	Results  []AgentResult `json:"results"`
	Duration time.Duration `json:"duration"`
}

// FanOut sends the same message to multiple agents concurrently and collects all results.
func FanOut(ctx context.Context, orch *Orchestrator, cfg FanOutConfig) (*FanOutResult, error) {
	start := time.Now()

	if len(cfg.Agents) == 0 {
		return &FanOutResult{Duration: time.Since(start)}, nil
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}

	agentIDs := make([]string, 0, len(cfg.Agents))
	for _, agentCfg := range cfg.Agents {
		id, err := orch.CreateAgent(agentCfg)
		if err != nil {
			return nil, fmt.Errorf("failed to create agent %q: %w", agentCfg.Role, err)
		}
		agentIDs = append(agentIDs, id)
	}

	// Send the message to all agents
	for _, id := range agentIDs {
		if err := orch.SendToAgent(ctx, id, cfg.Message); err != nil {
			return nil, fmt.Errorf("failed to send to agent %s: %w", id, err)
		}
	}

	results := orch.CollectResults(timeout)

	return &FanOutResult{
		Results:  results,
		Duration: time.Since(start),
	}, nil
}

// PipelineStage defines a single stage in a sequential pipeline.
type PipelineStage struct {
	Agent AgentConfig `json:"agent"`
	// TransformInput optionally transforms the previous stage's output before
	// passing it to this stage. If nil, the raw output is used.
	TransformInput func(previousOutput string) string `json:"-"`
}

// PipelineConfig configures a sequential pipeline of agents.
type PipelineConfig struct {
	Stages       []PipelineStage `json:"stages"`
	InitialInput string          `json:"initial_input"`
	Timeout      time.Duration   `json:"timeout"`
}

// PipelineResult holds the output of a pipeline execution.
type PipelineResult struct {
	FinalOutput  string        `json:"final_output"`
	StageResults []AgentResult `json:"stage_results"`
	Duration     time.Duration `json:"duration"`
}

// Pipeline chains agents sequentially, feeding each agent's output as input
// to the next agent in the chain.
func Pipeline(ctx context.Context, orch *Orchestrator, cfg PipelineConfig) (*PipelineResult, error) {
	start := time.Now()

	if len(cfg.Stages) == 0 {
		return &PipelineResult{Duration: time.Since(start)}, nil
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 120 * time.Second
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	currentInput := cfg.InitialInput
	stageResults := make([]AgentResult, 0, len(cfg.Stages))

	for i, stage := range cfg.Stages {
		if stage.TransformInput != nil {
			currentInput = stage.TransformInput(currentInput)
		}

		agentID, err := orch.CreateAgent(stage.Agent)
		if err != nil {
			return nil, fmt.Errorf("failed to create pipeline stage %d agent: %w", i, err)
		}

		if err := orch.SendToAgent(timeoutCtx, agentID, currentInput); err != nil {
			return nil, fmt.Errorf("failed to send to pipeline stage %d: %w", i, err)
		}

		// Wait for this stage to complete
		result, err := waitForAgent(timeoutCtx, orch, agentID)
		if err != nil {
			return nil, fmt.Errorf("pipeline stage %d failed: %w", i, err)
		}

		stageResults = append(stageResults, *result)

		if result.Status != AgentStatusCompleted {
			return &PipelineResult{
				StageResults: stageResults,
				Duration:     time.Since(start),
			}, fmt.Errorf("pipeline stage %d did not complete successfully: status=%s", i, result.Status)
		}

		currentInput = result.Response
	}

	return &PipelineResult{
		FinalOutput:  currentInput,
		StageResults: stageResults,
		Duration:     time.Since(start),
	}, nil
}

// DebateConfig configures a debate pattern where two agents argue different
// sides and a synthesizer picks the best result.
type DebateConfig struct {
	ProAgent         AgentConfig   `json:"pro_agent"`
	ConAgent         AgentConfig   `json:"con_agent"`
	SynthesizerAgent AgentConfig   `json:"synthesizer_agent"`
	Topic            string        `json:"topic"`
	Rounds           int           `json:"rounds"`
	Timeout          time.Duration `json:"timeout"`
}

// DebateResult holds the output of a debate.
type DebateResult struct {
	ProArguments []string      `json:"pro_arguments"`
	ConArguments []string      `json:"con_arguments"`
	Synthesis    string        `json:"synthesis"`
	Duration     time.Duration `json:"duration"`
}

// Debate runs a structured debate between two agents, then a synthesizer
// evaluates the arguments and produces a final answer.
func Debate(ctx context.Context, orch *Orchestrator, cfg DebateConfig) (*DebateResult, error) {
	start := time.Now()

	rounds := cfg.Rounds
	if rounds <= 0 {
		rounds = 1
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 120 * time.Second
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	proArgs := make([]string, 0, rounds)
	conArgs := make([]string, 0, rounds)

	currentPrompt := cfg.Topic

	for round := 0; round < rounds; round++ {
		// Run pro and con agents concurrently for each round
		proID, err := orch.CreateAgent(cfg.ProAgent)
		if err != nil {
			return nil, fmt.Errorf("failed to create pro agent (round %d): %w", round, err)
		}
		conID, err := orch.CreateAgent(cfg.ConAgent)
		if err != nil {
			return nil, fmt.Errorf("failed to create con agent (round %d): %w", round, err)
		}

		var proResult, conResult *AgentResult
		var proErr, conErr error
		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			if sendErr := orch.SendToAgent(timeoutCtx, proID, currentPrompt); sendErr != nil {
				proErr = sendErr
				return
			}
			proResult, proErr = waitForAgent(timeoutCtx, orch, proID)
		}()

		go func() {
			defer wg.Done()
			if sendErr := orch.SendToAgent(timeoutCtx, conID, currentPrompt); sendErr != nil {
				conErr = sendErr
				return
			}
			conResult, conErr = waitForAgent(timeoutCtx, orch, conID)
		}()

		wg.Wait()

		if proErr != nil {
			return nil, fmt.Errorf("pro agent failed (round %d): %w", round, proErr)
		}
		if conErr != nil {
			return nil, fmt.Errorf("con agent failed (round %d): %w", round, conErr)
		}

		proArgs = append(proArgs, proResult.Response)
		conArgs = append(conArgs, conResult.Response)

		// For subsequent rounds, include previous arguments as context
		if round < rounds-1 {
			currentPrompt = fmt.Sprintf(
				"Previous PRO argument: %s\nPrevious CON argument: %s\nContinue the debate on: %s",
				proResult.Response, conResult.Response, cfg.Topic,
			)
		}
	}

	// Synthesis phase
	synthesisPrompt := buildSynthesisPrompt(cfg.Topic, proArgs, conArgs)
	synthID, err := orch.CreateAgent(cfg.SynthesizerAgent)
	if err != nil {
		return nil, fmt.Errorf("failed to create synthesizer agent: %w", err)
	}

	if err := orch.SendToAgent(timeoutCtx, synthID, synthesisPrompt); err != nil {
		return nil, fmt.Errorf("failed to send to synthesizer: %w", err)
	}

	synthResult, err := waitForAgent(timeoutCtx, orch, synthID)
	if err != nil {
		return nil, fmt.Errorf("synthesizer failed: %w", err)
	}

	return &DebateResult{
		ProArguments: proArgs,
		ConArguments: conArgs,
		Synthesis:    synthResult.Response,
		Duration:     time.Since(start),
	}, nil
}

// MapReduceConfig configures a map-reduce pattern: split work across agents,
// then merge the results.
type MapReduceConfig struct {
	MapAgent    AgentConfig   `json:"map_agent"`
	ReduceAgent AgentConfig   `json:"reduce_agent"`
	Inputs      []string      `json:"inputs"`
	Timeout     time.Duration `json:"timeout"`
}

// MapReduceResult holds the output of a map-reduce operation.
type MapReduceResult struct {
	MapResults   []AgentResult `json:"map_results"`
	ReduceResult AgentResult   `json:"reduce_result"`
	FinalOutput  string        `json:"final_output"`
	Duration     time.Duration `json:"duration"`
}

// MapReduce splits work across multiple agents (map phase), then combines
// their outputs with a reducer agent.
func MapReduce(ctx context.Context, orch *Orchestrator, cfg MapReduceConfig) (*MapReduceResult, error) {
	start := time.Now()

	if len(cfg.Inputs) == 0 {
		return &MapReduceResult{Duration: time.Since(start)}, nil
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 120 * time.Second
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Map phase: create one agent per input
	agentIDs := make([]string, 0, len(cfg.Inputs))
	for i := range cfg.Inputs {
		id, err := orch.CreateAgent(cfg.MapAgent)
		if err != nil {
			return nil, fmt.Errorf("failed to create map agent %d: %w", i, err)
		}
		agentIDs = append(agentIDs, id)
	}

	// Send inputs concurrently
	for i, id := range agentIDs {
		if err := orch.SendToAgent(timeoutCtx, id, cfg.Inputs[i]); err != nil {
			return nil, fmt.Errorf("failed to send to map agent %d: %w", i, err)
		}
	}

	// Collect map results
	mapResults := make([]AgentResult, len(agentIDs))
	var wg sync.WaitGroup
	var firstErr error
	var errOnce sync.Once

	for i, id := range agentIDs {
		wg.Add(1)
		go func(idx int, agentID string) {
			defer wg.Done()
			result, err := waitForAgent(timeoutCtx, orch, agentID)
			if err != nil {
				errOnce.Do(func() { firstErr = fmt.Errorf("map agent %d failed: %w", idx, err) })
				return
			}
			mapResults[idx] = *result
		}(i, id)
	}
	wg.Wait()

	if firstErr != nil {
		return nil, firstErr
	}

	// Reduce phase: combine all map outputs
	var mapOutputs []string
	for _, r := range mapResults {
		mapOutputs = append(mapOutputs, r.Response)
	}

	reduceInput := buildReducePrompt(mapOutputs)

	reduceID, err := orch.CreateAgent(cfg.ReduceAgent)
	if err != nil {
		return nil, fmt.Errorf("failed to create reduce agent: %w", err)
	}

	if err := orch.SendToAgent(timeoutCtx, reduceID, reduceInput); err != nil {
		return nil, fmt.Errorf("failed to send to reduce agent: %w", err)
	}

	reduceResult, err := waitForAgent(timeoutCtx, orch, reduceID)
	if err != nil {
		return nil, fmt.Errorf("reduce agent failed: %w", err)
	}

	return &MapReduceResult{
		MapResults:   mapResults,
		ReduceResult: *reduceResult,
		FinalOutput:  reduceResult.Response,
		Duration:     time.Since(start),
	}, nil
}

// waitForAgent polls until the agent is no longer running or the context is cancelled.
func waitForAgent(ctx context.Context, orch *Orchestrator, agentID string) (*AgentResult, error) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			status, err := orch.GetAgentStatus(agentID)
			if err != nil {
				return nil, err
			}
			if status != AgentStatusRunning {
				result, err := orch.GetAgentResult(agentID)
				if err != nil {
					return nil, err
				}
				if result == nil {
					return &AgentResult{AgentID: agentID, Status: status}, nil
				}
				if result.Error != nil {
					return result, result.Error
				}
				return result, nil
			}
		}
	}
}

// buildSynthesisPrompt creates a prompt for the synthesizer from debate arguments.
func buildSynthesisPrompt(topic string, proArgs, conArgs []string) string {
	var sb strings.Builder
	sb.WriteString("Evaluate the following debate and provide a synthesis.\n\n")
	sb.WriteString(fmt.Sprintf("Topic: %s\n\n", topic))

	sb.WriteString("PRO arguments:\n")
	for i, arg := range proArgs {
		sb.WriteString(fmt.Sprintf("Round %d: %s\n", i+1, arg))
	}

	sb.WriteString("\nCON arguments:\n")
	for i, arg := range conArgs {
		sb.WriteString(fmt.Sprintf("Round %d: %s\n", i+1, arg))
	}

	sb.WriteString("\nProvide a balanced synthesis considering both perspectives.")
	return sb.String()
}

// buildReducePrompt creates a prompt for the reduce agent from map outputs.
func buildReducePrompt(outputs []string) string {
	var sb strings.Builder
	sb.WriteString("Combine and synthesize the following results:\n\n")
	for i, output := range outputs {
		sb.WriteString(fmt.Sprintf("Result %d: %s\n\n", i+1, output))
	}
	sb.WriteString("Provide a unified summary.")
	return sb.String()
}
