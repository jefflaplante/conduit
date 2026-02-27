package tools

import (
	"context"

	"conduit/internal/ai"
)

// ExecutionEngineAdapter adapts the tools ExecutionEngine to the ai.ExecutionEngine interface
type ExecutionEngineAdapter struct {
	engine *ExecutionEngine
}

// NewExecutionEngineAdapter creates a new adapter
func NewExecutionEngineAdapter(engine *ExecutionEngine) *ExecutionEngineAdapter {
	return &ExecutionEngineAdapter{
		engine: engine,
	}
}

// HandleToolCallFlow implements ai.ExecutionEngine interface
func (a *ExecutionEngineAdapter) HandleToolCallFlow(ctx context.Context, provider ai.Provider, initialReq *ai.GenerateRequest, initialResp *ai.GenerateResponse) (ai.ConversationResponse, error) {
	// Use the internal engine's flow handling
	response, err := a.engine.HandleToolCallFlow(ctx, provider, initialReq, initialResp)
	if err != nil {
		return nil, err
	}

	// Convert response to the ai package interface
	return &ConversationResponseAdapter{response: response}, nil
}

// ConversationResponseAdapter adapts tools.ConversationResponse to ai.ConversationResponse interface

// ConversationResponseAdapter adapts tools.ConversationResponse to ai.ConversationResponse interface
type ConversationResponseAdapter struct {
	response *ConversationResponse
}

func (a *ConversationResponseAdapter) GetContent() string {
	return a.response.Content
}

func (a *ConversationResponseAdapter) GetUsage() *ai.Usage {
	return a.response.Usage
}

func (a *ConversationResponseAdapter) GetSteps() int {
	return a.response.Steps
}

func (a *ConversationResponseAdapter) HasToolResults() bool {
	return len(a.response.ToolResults) > 0
}
