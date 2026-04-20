package gateway

import (
	"context"
	"fmt"
	"strings"

	"conduit/internal/ai"
	"conduit/internal/tools/types"
)

// visionAdapter adapts the AI router to the types.VisionAnalyzer interface.
//
// It wraps ai.Router.GenerateResponse, injecting the image as a user-message
// attachment so providers that support multimodal input (e.g., Anthropic
// Claude vision via internal/ai/anthropic.go's convertMessagesToAnthropic)
// can process the bytes natively.
//
// The adapter uses the router's default provider/model, which on a standard
// Conduit deployment is Anthropic with a vision-capable Claude model. A
// follow-up could surface a tool-level "model" override; for now the
// configured default is used unconditionally.
type visionAdapter struct {
	router       *ai.Router
	providerName string
}

func newVisionAdapter(router *ai.Router) *visionAdapter {
	if router == nil {
		return nil
	}
	return &visionAdapter{
		router:       router,
		providerName: router.DefaultProviderName(),
	}
}

// AnalyzeImage implements types.VisionAnalyzer. It builds a single-turn user
// message with an image attachment + prompt and calls the default provider
// directly (bypassing session history, system prompts, and tools — this is a
// one-shot analysis, not a conversation turn).
func (a *visionAdapter) AnalyzeImage(ctx context.Context, image []byte, mediaType string, prompt string) (string, error) {
	if a == nil || a.router == nil {
		return "", fmt.Errorf("vision: AI router not available")
	}
	if len(image) == 0 {
		return "", fmt.Errorf("vision: empty image data")
	}
	if mediaType == "" {
		// Default to JPEG — Anthropic accepts this for most raw byte streams;
		// real magic-byte detection happens in the caller. Kept as a safety
		// net rather than a hard rejection.
		mediaType = "image/jpeg"
	}
	if prompt == "" {
		prompt = "Describe what you see in this image."
	}

	provider, ok := a.router.GetProvider(a.providerName)
	if !ok || provider == nil {
		return "", fmt.Errorf("vision: provider %q not available", a.providerName)
	}

	req := &ai.GenerateRequest{
		Messages: []ai.ChatMessage{
			{
				Role:    "user",
				Content: prompt,
				Attachments: []ai.Attachment{
					{
						Type:      "image",
						MediaType: mediaType,
						Data:      image,
					},
				},
			},
		},
		MaxTokens: 1024,
	}

	resp, err := provider.GenerateResponse(ctx, req)
	if err != nil {
		return "", fmt.Errorf("vision: provider call failed: %w", err)
	}
	content := strings.TrimSpace(resp.Content)
	if content == "" {
		return "", fmt.Errorf("vision: provider returned empty response")
	}
	return content, nil
}

// Ensure visionAdapter implements the VisionAnalyzer interface at compile time.
var _ types.VisionAnalyzer = (*visionAdapter)(nil)
