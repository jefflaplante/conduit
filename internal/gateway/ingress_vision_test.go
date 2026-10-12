package gateway

import (
	"context"
	"math"
	"strconv"
	"strings"
	"sync"
	"testing"

	"conduit/internal/ai"
	"conduit/internal/config"
	"conduit/internal/protocol"
	"conduit/internal/sessions"
)

// conduit-2xmt: photo attachments on a text-only turn provider are
// pre-transcribed through the vision side-call and injected as text; raw
// bytes still pass through for image-capable providers; transcription
// failure degrades to a marker, never a failed turn.

// transcriptionVisionProvider stands in for glm-5.3-flash: records the
// prompt and attachments it was given, returns a canned description.
type transcriptionVisionProvider struct {
	mu      sync.Mutex
	prompts []string
	images  int
	reply   string
	usage   ai.Usage
	err     error
}

func (p *transcriptionVisionProvider) Name() string { return "vision" }

func (p *transcriptionVisionProvider) GenerateResponse(ctx context.Context, req *ai.GenerateRequest) (*ai.GenerateResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prompts = append(p.prompts, req.Messages[len(req.Messages)-1].Content)
	for _, m := range req.Messages {
		p.images += countImageAttachments(m.Attachments)
	}
	if p.err != nil {
		return nil, p.err
	}
	return &ai.GenerateResponse{Content: p.reply, FinishReason: "stop", Usage: p.usage}, nil
}

func (p *transcriptionVisionProvider) imageCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.images
}

func countImageAttachments(atts []ai.Attachment) int {
	n := 0
	for _, a := range atts {
		if a.Type == "image" && len(a.Data) > 0 {
			n++
		}
	}
	return n
}

// newPhotoIngressGateway builds a gateway whose chat provider is text-only
// (ollama/llama3) and whose vision runs on the registered "vision" provider
// via an explicit ai.vision config, mirroring the live z-ai/glm-5.3 +
// ai.vision glm-5.3-flash setup.
func newPhotoIngressGateway(t *testing.T, vision *transcriptionVisionProvider) (*Gateway, *sessions.Store, *turnProvider) {
	t.Helper()
	store, err := sessions.NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := config.AIConfig{
		DefaultProvider: "testprov",
		Providers: []config.ProviderConfig{
			{Name: "testprov", Type: "ollama", Model: "llama3", ContextWindow: 8000},
			{Name: "vision", Type: "ollama", Model: "vision-model", ContextWindow: 200000},
		},
		Vision: &config.VisionConfig{Provider: "vision", Model: "vision-model"},
	}
	router, err := ai.NewRouter(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	router.SetSessionStore(store)
	chat := newTurnProvider()
	router.RegisterProvider("testprov", chat)
	if vision != nil {
		router.RegisterProvider("vision", vision)
	}
	gw := newGatewayFromRouter(t, store, router, cfg)
	return gw, store, chat
}

// photoMsg builds an incoming Telegram message carrying one JPEG attachment.
func photoMsg(caption string) *protocol.IncomingMessage {
	msg := tgMsg(caption)
	msg.Attachments = []protocol.Attachment{{
		Type:      "image",
		MediaType: "image/jpeg",
		Data:      []byte{0xFF, 0xD8, 0xFF, 0xE0},
	}}
	return msg
}

// Text-only provider: the photo is transcribed, the description replaces the
// bytes, and the model sees it as text — no attachment reaches the provider.
func TestPhotoIngress_TextOnlyProvider_GetsTranscription(t *testing.T) {
	vision := &transcriptionVisionProvider{reply: "A calendar screenshot showing Dentist on Aug 18 at noon."}
	gw, store, chat := newPhotoIngressGateway(t, vision)

	gw.handleIncomingMessage(context.Background(), photoMsg("add these to my calendar"))

	if got := vision.imageCount(); got != 1 {
		t.Fatalf("vision side-call saw %d images, want 1", got)
	}
	req := chat.request(1)
	if n := countImageAttachments(req[len(req)-1].Attachments); n != 0 {
		t.Fatalf("text-only turn received %d raw image attachments, want 0", n)
	}
	last := req[len(req)-1]
	if !strings.Contains(last.Content, "Dentist on Aug 18 at noon") {
		t.Fatalf("model text missing transcription: %q", last.Content)
	}
	if !strings.HasPrefix(last.Content, "[Photo transcription]") {
		t.Fatalf("transcription prefix missing: %q", last.Content)
	}
	if !strings.Contains(last.Content, "add these to my calendar") {
		t.Fatalf("user caption dropped: %q", last.Content)
	}

	// Transcript keeps the human-readable marker form, not the transcription.
	sess, _ := store.GetOrCreateSession("42", "telegram")
	if got := transcript(t, store, sess.Key); got[0] != "user:[Photo] add these to my calendar" {
		t.Fatalf("stored user row = %q", got[0])
	}
}

// Vision failure degrades to the marker: the turn still runs, the model is
// told transcription is unavailable.
func TestPhotoIngress_VisionFailure_DegradesToMarker(t *testing.T) {
	vision := &transcriptionVisionProvider{err: context.DeadlineExceeded}
	gw, _, chat := newPhotoIngressGateway(t, vision)

	gw.handleIncomingMessage(context.Background(), photoMsg("what is this"))

	req := chat.request(1)
	last := req[len(req)-1]
	if !strings.Contains(last.Content, photoTranscriptionUnavailable) {
		t.Fatalf("marker missing after vision failure: %q", last.Content)
	}
	if n := countImageAttachments(last.Attachments); n != 0 {
		t.Fatalf("failed transcription still passed %d attachments through", n)
	}
}

// No vision route (no ai.vision config, heuristic finds no anthropic): the
// photo still degrades to the marker instead of a 400.
func TestPhotoIngress_NoVisionRoute_StillProceeds(t *testing.T) {
	store, err := sessions.NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := config.AIConfig{
		DefaultProvider: "testprov",
		Providers:       []config.ProviderConfig{{Name: "testprov", Type: "ollama", Model: "llama3", ContextWindow: 8000}},
	}
	router, err := ai.NewRouter(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	router.SetSessionStore(store)
	chat := newTurnProvider()
	router.RegisterProvider("testprov", chat)
	gw := newGatewayFromRouter(t, store, router, cfg)

	gw.handleIncomingMessage(context.Background(), photoMsg(""))

	req := chat.request(1)
	last := req[len(req)-1]
	if !strings.Contains(last.Content, photoTranscriptionUnavailable) {
		t.Fatalf("marker missing without vision route: %q", last.Content)
	}
}

// Image-capable provider (anthropic): raw bytes pass through untouched and
// no vision side-call is made at ingress.
func TestPhotoIngress_ImageCapableProvider_PassesThrough(t *testing.T) {
	vision := &transcriptionVisionProvider{reply: "should not be called"}
	gw, chat := makeAnthropicDefaultGateway(t, vision)

	gw.handleIncomingMessage(context.Background(), photoMsg("what is this"))

	if got := vision.imageCount(); got != 0 {
		t.Fatalf("ingress made %d vision calls for an image-capable provider, want 0", got)
	}
	req := chat.request(1)
	last := req[len(req)-1]
	if n := countImageAttachments(last.Attachments); n != 1 {
		t.Fatalf("raw attachment not passed through: %d", n)
	}
	if strings.Contains(last.Content, "[Photo transcription]") {
		t.Fatalf("transcription injected on a native-vision turn: %q", last.Content)
	}
}

// The transcription side-call's metered cost is folded into the turn and
// session totals (IngestSideCalls), exactly like in-turn tool vision calls.
func TestPhotoIngress_TranscriptionCostFoldedIntoTurn(t *testing.T) {
	// claude-haiku-4-5 is priced in the default matrix ($1/$5 per M): a
	// 1000/200-token side call prices to 0.002 via meterCall.
	vision := &transcriptionVisionProvider{
		reply: "A calendar screenshot.",
		usage: ai.Usage{PromptTokens: 1_000, CompletionTokens: 200, TotalTokens: 1_200},
	}
	gw, store, _ := newPhotoIngressGateway(t, vision)
	// Point ai.vision at the priced model.
	gw.config.AI.Vision.Model = "claude-haiku-4-5"
	gw.config.AI.Providers[1].Model = "claude-haiku-4-5"

	gw.handleIncomingMessage(context.Background(), photoMsg("add these"))

	waitForCond(t, "session cost recorded", func() bool {
		sess, err := store.GetOrCreateSession("42", "telegram")
		return err == nil && sess.Context["session_total_cost"] != ""
	})
	sess, _ := store.GetOrCreateSession("42", "telegram")
	got, _ := strconv.ParseFloat(sess.Context["session_total_cost"], 64)
	const want = (1_000*1.0 + 200*5.0) / 1e6
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("session_total_cost = %v, want transcription cost %v", got, want)
	}
}

// makeAnthropicDefaultGateway builds the harness with an anthropic-type
// chat provider so providerAcceptsImages is true for the default route.
func makeAnthropicDefaultGateway(t *testing.T, vision *transcriptionVisionProvider) (*Gateway, *turnProvider) {
	t.Helper()
	store, err := sessions.NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := config.AIConfig{
		DefaultProvider: "antprov",
		Providers: []config.ProviderConfig{
			{Name: "antprov", Type: "anthropic", Model: "claude-haiku-4-5", ContextWindow: 200000, APIKey: "test-key"},
			{Name: "vision", Type: "ollama", Model: "vision-model", ContextWindow: 200000},
		},
		Vision: &config.VisionConfig{Provider: "vision", Model: "vision-model"},
	}
	router, err := ai.NewRouter(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	router.SetSessionStore(store)
	// Rebind the recorded chat provider under the anthropic-type name.
	chat := newTurnProvider()
	router.RegisterProvider("antprov", chat)
	router.RegisterProvider("vision", vision)
	gw := newGatewayFromRouter(t, store, router, cfg)
	return gw, chat
}
