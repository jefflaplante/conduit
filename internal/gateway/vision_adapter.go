package gateway

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"conduit/internal/ai"
	"conduit/internal/config"
	"conduit/internal/tools/types"
)

// visionAdapter adapts the AI router to the types.VisionAnalyzer interface.
//
// It wraps ai.Router.GenerateSideCall, injecting the image as a user-message
// attachment so providers that support multimodal input (Anthropic image
// blocks via convertMessagesToAnthropic, OpenAI-compatible image_url
// data-URI blocks via convertMessagesToOpenAI) can process the bytes.
//
// Provider selection (no-anthropic-routing), per call:
//
//  1. ai.vision set → exactly that provider and model (validated at load:
//     exists, routable, not claude-code). Never falls back elsewhere.
//  2. unset → the bd-1820 heuristic over ROUTABLE providers only: the
//     default provider when it is anthropic/claude-code, else the first
//     routable anthropic-type provider (by name), else the default.
//
// Selection runs per call so a live routable change (config reload) is seen
// at once; GenerateSideCall refuses a routable=false provider regardless.
type visionAdapter struct {
	router *ai.Router
	// cfg is ai.vision as loaded at startup (restart-required); nil = the
	// heuristic.
	cfg *config.VisionConfig
}

func newVisionAdapter(router *ai.Router, cfg *config.VisionConfig) *visionAdapter {
	if router == nil {
		return nil
	}
	var c *config.VisionConfig
	if cfg != nil {
		cp := *cfg
		c = &cp
	}
	return &visionAdapter{router: router, cfg: c}
}

// route returns the provider and model ("" = provider default) image
// analysis runs on.
func (a *visionAdapter) route() (provider, model string) {
	if a.cfg != nil && a.cfg.Provider != "" {
		return a.cfg.Provider, a.cfg.Model
	}
	return selectVisionProvider(a.router), ""
}

// selectVisionProvider is the heuristic used when ai.vision is unset. It
// never picks a routable=false provider.
func selectVisionProvider(router *ai.Router) string {
	defaultName := router.DefaultProviderName()
	if defaultName == "" {
		return ""
	}
	// If the default provider is anthropic (or claude-code), it already
	// supports vision — no selection needed.
	meta, ok := router.GetProviderMeta(defaultName)
	if ok && meta.Routable() && (meta.Type == "anthropic" || meta.Type == "claude-code") {
		return defaultName
	}
	// Default is text-only — find a routable vision-capable alternative.
	providers := router.ListProviders()
	sort.Slice(providers, func(i, j int) bool { return providers[i].Name < providers[j].Name })
	for _, meta := range providers {
		if meta.Type == "anthropic" && meta.Routable() {
			return meta.Name
		}
	}
	// No alternative: keep the default and let the provider surface the
	// error (the default is always routable — config validation).
	return defaultName
}

// VisionRoute reports the provider and model image analysis currently runs
// on (model "" = provider default), for status surfaces.
func (a *visionAdapter) VisionRoute() (provider, model string) {
	if a == nil || a.router == nil {
		return "", ""
	}
	return a.route()
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

	providerName, model := a.route()
	if p, ok := a.router.GetProvider(providerName); !ok || p == nil {
		return "", fmt.Errorf("vision: provider %q not available", providerName)
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
		Model:     model, // ai.vision.model; "" = the provider's default
		MaxTokens: 1024,
	}

	// conduit-31jg.75: metered side call — recorded once to the usage
	// tracker (fuel gauge) and priced on the provider + model that served
	// it (req.Model "" = that provider's configured default), cache tokens
	// included. Inside a turn the ctx carries the turn's SideCallLedger, so
	// the cost is added to the turn's request cost and session_total_cost.
	resp, err := a.router.GenerateSideCall(ai.WithSideCallLabel(ctx, "vision"), providerName, req)
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
