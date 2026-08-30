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
		firstError         error
		expectFallback     bool
		expectSuccess      bool
		setupMockProvider  func(*mockProviderWithRetry)
	}{
		{
			name:          "quota error with model override - retries with fallback",
			modelOverride: "sonnet",
			firstError:    errors.New("API error: 400 - quota exceeded"),
			expectFallback: true,
			expectSuccess:  true,
			setupMockProvider: func(mp *mockProviderWithRetry) {
				mp.errorOnFirstCall = true
				mp.firstError = errors.New("API error: 400 - quota exceeded")
			},
		},
		{
			name:           "quota error without model override - no retry",
			modelOverride:  "",
			firstError:     errors.New("API error: 400 - quota exceeded"),
			expectFallback: false,
			expectSuccess:  false,
			setupMockProvider: func(mp *mockProviderWithRetry) {
				mp.errorOnFirstCall = true
				mp.firstError = errors.New("API error: 400 - quota exceeded")
			},
		},
		{
			name:           "non-quota error - no retry",
			modelOverride:  "sonnet",
			firstError:     errors.New("API error: 500 - internal server error"),
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
			firstError:     errors.New("API error: 400 - bad request"),
			expectFallback: false,
			expectSuccess:  false,
			setupMockProvider: func(mp *mockProviderWithRetry) {
				mp.errorOnFirstCall = true
				mp.firstError = errors.New("API error: 400 - bad request")
			},
		},
		{
			name:           "quota error fallback also fails - returns error",
			modelOverride:  "sonnet",
			firstError:     errors.New("API error: 400 - quota exceeded"),
			expectFallback: true,
			expectSuccess:  false,
			setupMockProvider: func(mp *mockProviderWithRetry) {
				mp.errorOnFirstCall = true
				mp.errorOnSecondCall = true
				mp.firstError = errors.New("API error: 400 - quota exceeded")
				mp.secondError = errors.New("API error: 401 - quota limit reached")
			},
		},
		{
			name:           "no error - normal flow",
			modelOverride:  "sonnet",
			firstError:     nil,
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

			// Create a minimal router with the mock provider
			router := &Router{
				providers: map[string]Provider{
					"default": mockProvider,
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
				assert.True(t, mockProvider.secondCallMade, "expected fallback retry to be made")
				assert.Equal(t, "z-ai/glm-5.3", mockProvider.secondCallModel, "expected fallback model to be z-ai/glm-5.3")
			} else {
				assert.False(t, mockProvider.secondCallMade, "expected no fallback retry")
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