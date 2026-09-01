package ai

import (
	"context"
	"errors"
	"testing"

	"conduit/internal/sessions"

	"github.com/stretchr/testify/assert"
)

// TestGenerateResponseWithTools_QuotaFallback tests the quota fallback retry logic (bd-6tb)
func TestGenerateResponseWithTools_QuotaFallback(t *testing.T) {
	tests := []struct {
		name               string
		modelOverride      string
		expectFallback     bool
		expectSuccess      bool
		expectSecondModel  string
		setupMockProvider  func(*mockProviderWithRetry)
		setupFallback      func(*MockProvider)
	}{
		{
			name:          "quota error - retries on fallback provider with correct model (bd-27ud)",
			modelOverride: "sonnet",
			expectFallback: true,
			expectSuccess:  true,
			expectSecondModel: "z-ai/glm-5.3",
			setupMockProvider: func(mp *mockProviderWithRetry) {
				mp.errorOnFirstCall = true
				mp.firstError = errors.New("API error: 400 - quota exceeded")
			},
		},
		{
			name:           "quota error - fallback provider also fails, original error surfaces (bd-27ud)",
			modelOverride:  "sonnet",
			expectFallback: true,
			expectSuccess:  false,
			expectSecondModel: "z-ai/glm-5.3",
			setupMockProvider: func(mp *mockProviderWithRetry) {
				mp.errorOnFirstCall = true
				mp.firstError = errors.New("API error: 400 - quota exceeded")
			},
			// bd-27ud: retry goes to the z-ai provider now, so the FALLBACK
			// provider must be the one configured to fail. The old test had
			// the default provider failing twice, but the fixed router never
			// retries on the failed provider.
			setupFallback: func(zai *MockProvider) {
				zai.AddErrorResponse(errors.New("API error: 500 - fallback provider down"))
			},
		},
		{
			name:           "non-quota error - no retry",
			modelOverride:  "sonnet",
			expectFallback: false,
			expectSuccess:  false,
			setupMockProvider: func(mp *mockProviderWithRetry) {
				mp.errorOnFirstCall = true
				mp.firstError = errors.New("API error: 500 - internal server error")
			},
		},
		{
			name:           "400 without quota indicators - no retry",
			modelOverride:  "sonnet",
			expectFallback: false,
			expectSuccess:  false,
			setupMockProvider: func(mp *mockProviderWithRetry) {
				mp.errorOnFirstCall = true
				mp.firstError = errors.New("API error: 400 - bad request")
			},
		},
		{
			name:           "no error - normal flow",
			modelOverride:  "sonnet",
			expectFallback: false,
			expectSuccess:  true,
			setupMockProvider: func(mp *mockProviderWithRetry) {
				mp.errorOnFirstCall = false
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a mock provider that tracks retry behavior
			mockProvider := &mockProviderWithRetry{
				generateFunc: func(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error) {
					return &GenerateResponse{
						Content: "test response",
						Usage: Usage{
							PromptTokens:      100,
							CompletionTokens:  50,
						},
					}, nil
				},
			}
			tt.setupMockProvider(mockProvider)

			// Create a minimal router with the mock provider plus a z-ai
			// fallback provider (bd-27ud: fallback must route there).
			zai := NewMockProvider("z-ai")
			if tt.setupFallback != nil {
				tt.setupFallback(zai)
			}
			router := &Router{
				providers: map[string]Provider{
					"default": mockProvider,
					"z-ai":    zai,
				},
				providerMeta: map[string]ProviderMeta{
					"default": {Name: "default", Type: "anthropic"},
					"z-ai":    {Name: "z-ai", Type: "openai", DefaultModel: "glm-5.3"},
				},
				default_: "default",
			}

			// Create a minimal session
			session := &sessions.Session{
				Key:       "test-key",
				UserID:    "test-user",
				ChannelID: "test-channel",
			}

			// Call generateResponseWithToolsLocked
			ctx := context.Background()
			result, err := router.generateResponseWithToolsLocked(ctx, session, "test message", "default", tt.modelOverride, nil)

			if tt.expectSuccess {
				assert.NoError(t, err, "expected success")
				assert.NotNil(t, result, "expected non-nil result")
			} else {
				assert.Error(t, err, "expected error")
				assert.Nil(t, result, "expected nil result")
			}

			if tt.expectFallback {
				assert.Equal(t, 1, zai.GetCallCount(), "expected fallback retry to land on z-ai provider (bd-27ud)")
				if calls := zai.GetCalls(); len(calls) == 1 {
					assert.Equal(t, tt.expectSecondModel, calls[0].Request.Model, "expected fallback model")
				}
				assert.False(t, mockProvider.secondCallMade, "expected NO retry on the failed provider (bd-27ud)")
			} else {
				assert.Equal(t, 0, zai.GetCallCount(), "expected no fallback retry")
			}
		})
	}
}

// mockProviderWithRetry is a mock provider that tracks retry behavior for testing bd-6tb
type mockProviderWithRetry struct {
	generateFunc       func(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error)
	errorOnFirstCall   bool
	errorOnSecondCall  bool
	firstError         error
	secondError        error
	firstCallMade      bool
	secondCallMade     bool
	firstCallModel     string
	secondCallModel    string
	callCount          int
}

func (m *mockProviderWithRetry) Name() string {
	return "mock-provider"
}

func (m *mockProviderWithRetry) GenerateResponse(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error) {
	m.callCount++

	if m.callCount == 1 {
		m.firstCallMade = true
		m.firstCallModel = req.Model
		if m.errorOnFirstCall {
			return nil, m.firstError
		}
		return m.generateFunc(ctx, req)
	}

	if m.callCount == 2 {
		m.secondCallMade = true
		m.secondCallModel = req.Model
		if m.errorOnSecondCall {
			return nil, m.secondError
		}
		return m.generateFunc(ctx, req)
	}

	return m.generateFunc(ctx, req)
}

func (m *mockProviderWithRetry) GenerateResponseStreaming(ctx context.Context, req *GenerateRequest, onDelta StreamCallback) (*GenerateResponse, error) {
	return m.GenerateResponse(ctx, req)
}

func (m *mockProviderWithRetry) GetContextWindow() int {
	return 100000
}

func (m *mockProviderWithRetry) SupportsStreaming() bool {
	return false
}