package ai

import (
	"context"
	"sync"
)

// MockProvider is a test provider that records calls and returns configurable responses
type MockProvider struct {
	name      string
	responses []MockResponse
	calls     []MockCall
	mu        sync.Mutex
	respIndex int
}

// MockResponse represents a pre-configured response for the mock provider
type MockResponse struct {
	Content   string
	ToolCalls []ToolCall
	Usage     Usage
	Error     error
}

// MockCall records information about a call to GenerateResponse
type MockCall struct {
	Request *GenerateRequest
}

// NewMockProvider creates a new mock provider for testing
func NewMockProvider(name string) *MockProvider {
	return &MockProvider{
		name:      name,
		responses: []MockResponse{},
		calls:     []MockCall{},
	}
}

// Name returns the provider name
func (m *MockProvider) Name() string {
	return m.name
}

// GenerateResponse records the call and returns the next configured response
func (m *MockProvider) GenerateResponse(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Record the call
	m.calls = append(m.calls, MockCall{Request: req})

	// Return configured response or default
	if m.respIndex < len(m.responses) {
		resp := m.responses[m.respIndex]
		m.respIndex++

		if resp.Error != nil {
			return nil, resp.Error
		}

		return &GenerateResponse{
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
			Usage:     resp.Usage,
		}, nil
	}

	// Default response when no responses configured
	return &GenerateResponse{
		Content: "Mock response",
		Usage: Usage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
		},
	}, nil
}

// SetResponses configures the responses that will be returned by GenerateResponse
// Responses are returned in order, cycling back to the beginning when exhausted
func (m *MockProvider) SetResponses(responses []MockResponse) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.responses = responses
	m.respIndex = 0
}

// AddResponse adds a single response to the queue
func (m *MockProvider) AddResponse(content string, toolCalls []ToolCall) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.responses = append(m.responses, MockResponse{
		Content:   content,
		ToolCalls: toolCalls,
		Usage: Usage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
		},
	})
}

// AddErrorResponse adds an error response to the queue
func (m *MockProvider) AddErrorResponse(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.responses = append(m.responses, MockResponse{Error: err})
}

// GetCalls returns all recorded calls to GenerateResponse
func (m *MockProvider) GetCalls() []MockCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]MockCall{}, m.calls...)
}

// GetCallCount returns the number of times GenerateResponse was called
func (m *MockProvider) GetCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

// Reset clears all recorded calls and responses
func (m *MockProvider) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = []MockCall{}
	m.responses = []MockResponse{}
	m.respIndex = 0
}

// LastCall returns the most recent call, or nil if no calls have been made
func (m *MockProvider) LastCall() *MockCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		return nil
	}
	return &m.calls[len(m.calls)-1]
}
