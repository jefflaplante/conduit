package gateway

import (
	"context"
	"strings"
	"time"

	"conduit/internal/ai"
	"conduit/internal/config"
	"conduit/internal/sessions"
)

// conduit-2xmt: pre-transcription of channel photo attachments.
//
// When the session's turn provider cannot take image blocks (text-only
// OpenAI-compatible providers reject content parts — glm-5.3 answers
// "messages.content.type is invalid, allowed values: ['text']" with a 400),
// the raw attachment used to kill the whole turn. Instead, ingress runs the
// photo through the metered vision side-call (ai.vision — here glm-5.3-flash)
// and hands the turn the description as text. Raw attachments still pass
// through when the turn provider is multimodal (anthropic/claude-code), and
// transcription failure never blocks the turn: the marker text keeps the
// turn alive and the Image tool remains available for a retry with the bytes
// the channel adapter still holds.

// photoTranscriptionTimeout bounds one ingress vision side-call. Long
// enough for a real multimodal model; short enough that a wedged provider
// degrades to the marker text instead of stalling the turn.
const photoTranscriptionTimeout = 60 * time.Second

// photoTranscriptionUnavailable is injected when transcription fails or no
// vision route exists, so the turn still runs on a text-only provider.
const photoTranscriptionUnavailable = "[photo transcription unavailable]"

// providerAcceptsImages reports whether the provider serving a turn with
// these session overrides natively accepts image attachments.
// anthropic-type providers do (convertMessagesToAnthropic emits image
// blocks); everything else is treated as text-only — conservative, because
// whether an OpenAI-compatible endpoint honors image_url blocks is only
// known for the ones configured as anthropic.
func providerAcceptsImages(router *ai.Router, providerOverride, modelOverride string) bool {
	if router == nil {
		return false
	}
	name, _ := router.EffectiveRoute(providerOverride, modelOverride)
	meta, ok := router.GetProviderMeta(name)
	if !ok {
		return false
	}
	return meta.Routable() && (meta.Type == "anthropic" || meta.Type == "claude-code")
}

// preparePhotoAttachments adapts image attachments for the turn's provider.
// It returns the text the model should see, the attachments to keep, and
// the metered usage of the transcription side calls (zero when none ran)
// for the pipeline to fold into the turn cost.
//
// Behavior:
//   - provider takes images → attachments unchanged, text unchanged;
//   - text-only provider → each image is transcribed via the vision
//     side-call, attachments are dropped and the descriptions injected as
//     text (failures degrade to a marker line, never blocking the turn);
//   - no images at all → inputs returned unchanged.
func (g *Gateway) preparePhotoAttachments(ctx context.Context, session *sessions.Session, text string, attachments []ai.Attachment) (string, []ai.Attachment, ai.Usage) {
	if len(attachments) == 0 || g.ai == nil {
		return text, attachments, ai.Usage{}
	}

	var providerOverride, modelOverride string
	if session != nil {
		providerOverride = session.Context["provider"]
		modelOverride = session.Context["model"]
	}
	if providerAcceptsImages(g.ai, providerOverride, modelOverride) {
		return text, attachments, ai.Usage{} // native vision turn: bytes pass through
	}

	adapter := newVisionAdapter(g.ai, g.visionConfig())
	var parts []string
	var usage ai.Usage
	images := 0
	for _, att := range attachments {
		if att.Type != "image" || len(att.Data) == 0 {
			continue
		}
		images++
		if desc, u := g.describeImageForModel(ctx, adapter, att); desc != "" {
			parts = append(parts, desc)
			usage.Add(u)
		} else {
			parts = append(parts, photoTranscriptionUnavailable)
		}
	}
	if images == 0 {
		return text, attachments, ai.Usage{} // nothing image-shaped to adapt
	}

	injected := strings.Join(parts, "\n\n")
	switch {
	case text == "":
		text = "[Photo transcription]\n" + injected
	default:
		text = "[Photo transcription]\n" + injected + "\n\n" + text
	}
	return text, nil, usage
}

// describeImageForModel runs one vision side-call and returns the text to
// inject plus its metered usage, or "" when transcription is unavailable
// (no dedicated route) or failed. The main model cannot see the photo: this
// text is all it gets.
func (g *Gateway) describeImageForModel(ctx context.Context, adapter *visionAdapter, att ai.Attachment) (string, ai.Usage) {
	if !adapter.hasDedicatedRoute() {
		return "", ai.Usage{}
	}
	cctx, cancel := context.WithTimeout(ctx, photoTranscriptionTimeout)
	defer cancel()
	resp, err := adapter.analyzeImageMetered(cctx, att.Data, att.MediaType, visionTranscriptionPrompt)
	if err != nil {
		g.logger.Warn("photo transcription failed; continuing with marker text",
			"media_type", att.MediaType, "bytes", len(att.Data), "error", err)
		return "", ai.Usage{}
	}
	desc := strings.TrimSpace(resp.content)
	if desc == "" {
		g.logger.Warn("photo transcription returned empty; continuing with marker text",
			"media_type", att.MediaType, "bytes", len(att.Data))
		return "", ai.Usage{}
	}
	return desc, resp.usage
}

// visionTranscriptionPrompt is what ingress asks the vision model for.
const visionTranscriptionPrompt = `You are transcribing an image for a text-only assistant that cannot see it. Describe everything relevant: text content (transcribe verbatim, preserving layout such as tables and calendars), visible objects, people, numbers, dates, and any other details needed to act on the image. Be thorough but organized; do not add interpretation beyond what is visible.`

// visionConfig returns the startup ai.vision config (restart-required),
// the same source visionStatus reads.
func (g *Gateway) visionConfig() *config.VisionConfig {
	if g.config == nil {
		return nil
	}
	return g.config.AI.Vision
}
