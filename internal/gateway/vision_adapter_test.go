package gateway

import (
	"context"
	"errors"
	"testing"

	"conduit/internal/ai"
	"conduit/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newVisionTestRouter(t *testing.T, provider ai.Provider, name string) *ai.Router {
	t.Helper()
	router, err := ai.NewRouter(config.AIConfig{
		DefaultProvider: name,
	}, nil)
	require.NoError(t, err)
	router.RegisterProvider(name, provider)
	return router
}

func TestVisionAdapter_AnalyzeImage_Success(t *testing.T) {
	mock := ai.NewMockProvider("anthropic")
	mock.AddResponse("A serene mountain lake at dusk.", nil)

	router := newVisionTestRouter(t, mock, "anthropic")
	adapter := newVisionAdapter(router)
	require.NotNil(t, adapter)

	result, err := adapter.AnalyzeImage(
		context.Background(),
		[]byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10},
		"image/jpeg",
		"What is this?",
	)
	require.NoError(t, err)
	assert.Equal(t, "A serene mountain lake at dusk.", result)

	// Verify the provider received the attachment and prompt.
	calls := mock.GetCalls()
	require.Len(t, calls, 1)
	require.Len(t, calls[0].Request.Messages, 1)
	msg := calls[0].Request.Messages[0]
	assert.Equal(t, "user", msg.Role)
	assert.Equal(t, "What is this?", msg.Content)
	require.Len(t, msg.Attachments, 1)
	assert.Equal(t, "image", msg.Attachments[0].Type)
	assert.Equal(t, "image/jpeg", msg.Attachments[0].MediaType)
	assert.NotEmpty(t, msg.Attachments[0].Data)
}

func TestVisionAdapter_AnalyzeImage_DefaultMediaTypeAndPrompt(t *testing.T) {
	mock := ai.NewMockProvider("anthropic")
	mock.AddResponse("Some description.", nil)

	router := newVisionTestRouter(t, mock, "anthropic")
	adapter := newVisionAdapter(router)

	_, err := adapter.AnalyzeImage(context.Background(), []byte{0x00, 0x01}, "", "")
	require.NoError(t, err)

	calls := mock.GetCalls()
	require.Len(t, calls, 1)
	msg := calls[0].Request.Messages[0]
	// Empty prompt defaults.
	assert.Equal(t, "Describe what you see in this image.", msg.Content)
	// Empty media type defaults to image/jpeg.
	assert.Equal(t, "image/jpeg", msg.Attachments[0].MediaType)
}

func TestVisionAdapter_AnalyzeImage_ProviderError(t *testing.T) {
	mock := ai.NewMockProvider("anthropic")
	mock.AddErrorResponse(errors.New("rate limited"))

	router := newVisionTestRouter(t, mock, "anthropic")
	adapter := newVisionAdapter(router)

	_, err := adapter.AnalyzeImage(context.Background(), []byte{0xFF, 0xD8}, "image/jpeg", "prompt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rate limited")
}

func TestVisionAdapter_AnalyzeImage_EmptyImage(t *testing.T) {
	mock := ai.NewMockProvider("anthropic")
	router := newVisionTestRouter(t, mock, "anthropic")
	adapter := newVisionAdapter(router)

	_, err := adapter.AnalyzeImage(context.Background(), nil, "image/jpeg", "prompt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty image")

	// Provider should not have been called.
	assert.Equal(t, 0, mock.GetCallCount())
}

func TestVisionAdapter_AnalyzeImage_EmptyResponse(t *testing.T) {
	mock := ai.NewMockProvider("anthropic")
	mock.AddResponse("   ", nil) // whitespace only

	router := newVisionTestRouter(t, mock, "anthropic")
	adapter := newVisionAdapter(router)

	_, err := adapter.AnalyzeImage(context.Background(), []byte{0xFF}, "image/jpeg", "prompt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty response")
}

func TestVisionAdapter_AnalyzeImage_NilRouter(t *testing.T) {
	// newVisionAdapter returns nil for a nil router, so constructing directly.
	adapter := &visionAdapter{router: nil}
	_, err := adapter.AnalyzeImage(context.Background(), []byte{0xFF}, "image/jpeg", "prompt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not available")
}

func TestVisionAdapter_AnalyzeImage_MissingProvider(t *testing.T) {
	mock := ai.NewMockProvider("anthropic")
	router := newVisionTestRouter(t, mock, "anthropic")
	adapter := newVisionAdapter(router)
	// Override to point at a non-existent provider.
	adapter.providerName = "nonexistent"

	_, err := adapter.AnalyzeImage(context.Background(), []byte{0xFF}, "image/jpeg", "prompt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provider")
	assert.Contains(t, err.Error(), "not available")
}

func TestNewVisionAdapter_NilRouter(t *testing.T) {
	assert.Nil(t, newVisionAdapter(nil))
}
