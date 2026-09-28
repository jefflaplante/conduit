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
// Provider selection (bd-1820): the adapter previously used the router's
// default provider unconditionally, which 400s when the default is a
// text-only endpoint ("messages.content.type is invalid, allowed values
// [text]" — glm-5.3 via the OpenAI shim on z-ai). It now prefers, in order:
//  1. an explicitly configured vision provider (NewVisionAdapterWithModel),
//  2. any configured anthropic-type provider (vision-capable by default),
//  3. the router default.
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
		providerName: selectVisionProvider(router),
	}
}

// NewVisionAdapterWithModel returns a vision adapter pinned to a specific
// provider, bypassing auto-selection.
func NewVisionAdapterWithModel(router *ai.Router, providerName string) *visionAdapter {
	if router == nil {
		return nil
	}
	return &visionAdapter{router: router, providerName: providerName}
}

// selectVisionProvider picks the provider used for image analysis.
func selectVisionProvider(router *ai.Router) string {
	defaultName := router.DefaultProviderName()
	if defaultName == "" {
		return ""
	}
	// If the default provider is anthropic (or claude-code), it already
	// supports vision — no selection needed.
	meta, ok := router.GetProviderMeta(defaultName)
	if ok && (meta.Type == "anthropic" || meta.Type == "claude-code") {
		return defaultName
	}
	// Default is text-only — find a vision-capable alternative.
	for _, meta := range router.ListProviders() {
		if meta.Type == "anthropic" {
			return meta.Name
		}
	}
	// No alternative: keep the default and let the provider surface the error.
	return defaultName
}

// AnalyzeImage implements types.VisionAnalyzer. It builds a single-turn user
// message with an image attachment + prompt and sends it to the selected
// vision provider as a metered side call (bypassing session history, system
// prompts, and tools — this is a one-shot analysis, not a conversation turn).
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

	if p, ok := a.router.GetProvider(a.providerName); !ok || p == nil {
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

	// conduit-31jg.75: metered side call — recorded once to the usage
	// tracker (fuel gauge) and priced on the provider + model that served
	// it (req.Model "" = that provider's configured default), cache tokens
	// included. Inside a turn the ctx carries the turn's SideCallLedger, so
	// the cost is added to the turn's request cost and session_total_cost.
	resp, err := a.router.GenerateSideCall(ai.WithSideCallLabel(ctx, "vision"), a.providerName, req)
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
