package gateway

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"

	"conduit/internal/ai"
	"conduit/internal/config"
	"conduit/internal/sessions"
	"conduit/internal/tools/types"
	"conduit/internal/topics"
)

// conduit-331g: per-topic prompt profiles.

func TestTopicProfile_SetValidateClear(t *testing.T) {
	gw, _ := newTopicTestGateway(t)
	ctx := types.WithRequestContext(context.Background(), tgChannel, tgUser, "")
	if _, err := gw.TopicCreate(ctx, "alpha", "", ""); err != nil {
		t.Fatal(err)
	}

	out, err := gw.TopicProfile(ctx, "alpha", `{"skills": ["gog", "email"], "skip_context": ["agents.md", "SOUL.md"]}`)
	if err != nil {
		t.Fatalf("set profile: %v", err)
	}
	if !strings.Contains(out, "[alpha]") || !strings.Contains(out, "gog, email") || !strings.Contains(out, "AGENTS.md") {
		t.Fatalf("set reply = %q", out)
	}
	if !strings.Contains(out, "SOUL.md") {
		t.Fatalf("reply should say SOUL.md is always loaded: %q", out)
	}
	want := &topics.Profile{Skills: []string{"gog", "email"}, SkipContext: []string{"AGENTS.md"}}
	tp, _ := gw.topicStore().Get("alpha")
	if !reflect.DeepEqual(tp.Profile(), want) {
		t.Fatalf("stored profile = %+v", tp.Profile())
	}

	// Malformed JSON is rejected at set time and leaves the profile alone.
	if _, err := gw.TopicProfile(ctx, "alpha", `{"skills": [`); err == nil || !errors.Is(err, topics.ErrInvalidProfile) {
		t.Fatalf("malformed profile err = %v", err)
	}
	if tp, _ := gw.topicStore().Get("alpha"); !reflect.DeepEqual(tp.Profile(), want) {
		t.Fatalf("malformed set changed the profile: %+v", tp.Profile())
	}

	// Empty clears.
	if out, err := gw.TopicProfile(ctx, "alpha", ""); err != nil || !strings.Contains(out, "cleared") {
		t.Fatalf("clear = %q, %v", out, err)
	}
	if tp, _ := gw.topicStore().Get("alpha"); tp.PromptProfile != "" {
		t.Fatalf("profile not cleared: %q", tp.PromptProfile)
	}

	if _, err := gw.TopicProfile(ctx, "nope", `{"skills":["gog"]}`); !errors.Is(err, topics.ErrNotFound) {
		t.Fatalf("missing topic err = %v", err)
	}
}

func TestTopicProfileSession(t *testing.T) {
	gw, store := newTopicTestGateway(t)
	ctx := types.WithRequestContext(context.Background(), tgChannel, tgUser, "")
	alpha := mustSwitch(t, gw, "alpha")
	beta := mustSwitch(t, gw, "beta")
	if _, err := gw.TopicProfile(ctx, "alpha", `{"skills": ["gog"], "skip_context": ["TOOLS.md", "MEMORY.md"]}`); err != nil {
		t.Fatal(err)
	}

	aSess, _ := store.GetSession(alpha.SessionKey)
	before := map[string]string{}
	for k, v := range aSess.Context {
		before[k] = v
	}
	got := gw.topicProfileSession(aSess)
	if got.Context[sessions.ContextSkillFilter] != "gog" || got.Context[sessions.ContextSkip] != "TOOLS.md,MEMORY.md" {
		t.Fatalf("profiled session context = %v", got.Context)
	}
	if !reflect.DeepEqual(aSess.Context, before) {
		t.Fatalf("input session context mutated: %v", aSess.Context)
	}
	// Per-turn only: nothing is written to the stored session.
	if stored, _ := store.GetSession(alpha.SessionKey); stored.Context[sessions.ContextSkillFilter] != "" {
		t.Fatalf("profile persisted into session context: %v", stored.Context)
	}

	// No profile, home, and nil sessions pass through untouched.
	bSess, _ := store.GetSession(beta.SessionKey)
	if got := gw.topicProfileSession(bSess); got != bSess {
		t.Fatalf("unprofiled topic session was rewritten: %v", got.Context)
	}
	home, _ := store.GetOrCreateSession(tgUser, tgChannel)
	if got := gw.topicProfileSession(home); got != home {
		t.Fatalf("home session was rewritten: %v", got.Context)
	}
	if gw.topicProfileSession(nil) != nil {
		t.Fatal("nil session")
	}

	// A malformed stored profile falls back to the full prompt.
	if _, err := store.DB().Exec(`UPDATE topics SET prompt_profile = '{oops' WHERE slug = 'alpha'`); err != nil {
		t.Fatal(err)
	}
	if got := gw.topicProfileSession(aSess); got != aSess {
		t.Fatalf("malformed profile applied: %v", got.Context)
	}
}

// profileAgent records the prompt-scoping keys each turn's session carried
// into BuildSystemPrompt.
type profileAgent struct {
	mu   sync.Mutex
	seen []string
}

func (a *profileAgent) BuildSystemPrompt(_ context.Context, s *sessions.Session) ([]ai.SystemBlock, error) {
	a.mu.Lock()
	a.seen = append(a.seen, s.Context[sessions.ContextSkillFilter]+"|"+s.Context[sessions.ContextSkip])
	a.mu.Unlock()
	return []ai.SystemBlock{{Type: "text", Text: "sys"}}, nil
}
func (a *profileAgent) GetToolDefinitions(*sessions.Session) []ai.Tool { return nil }
func (a *profileAgent) ProcessResponse(_ context.Context, r *ai.GenerateResponse) (*ai.AgentProcessedResponse, error) {
	return &ai.AgentProcessedResponse{Content: r.Content}, nil
}

// The turn runner hands the topic's profile to prompt build: home and
// unprofiled topic turns carry no scoping keys.
func TestTopics_TurnAppliesProfile(t *testing.T) {
	store, err := sessions.NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := config.AIConfig{
		DefaultProvider: "testprov",
		Providers:       []config.ProviderConfig{{Name: "testprov", Type: "ollama", Model: "llama3", ContextWindow: 8000}},
	}
	agent := &profileAgent{}
	router, err := ai.NewRouter(cfg, agent)
	if err != nil {
		t.Fatal(err)
	}
	router.SetSessionStore(store)
	router.RegisterProvider("testprov", newTurnProvider())
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	gw := &Gateway{
		sessions: store, ai: router, logger: logger,
		ws:     NewWebSocketService(logger, websocket.Upgrader{}, 16),
		config: &config.Config{AI: cfg},
	}
	stubSummaries(gw)
	attachRecorder(t, gw)
	ctx := types.WithRequestContext(context.Background(), tgChannel, tgUser, "")

	gw.handleIncomingMessage(context.Background(), tgMsg("home turn"))
	gw.handleIncomingMessage(context.Background(), tgMsg("/topic alpha"))
	gw.handleIncomingMessage(context.Background(), tgMsg("plain topic turn"))
	if _, err := gw.TopicProfile(ctx, "alpha", `{"skills": ["gog"], "skip_context": ["TOOLS.md"]}`); err != nil {
		t.Fatal(err)
	}
	gw.handleIncomingMessage(context.Background(), tgMsg("lean topic turn"))
	gw.handleIncomingMessage(context.Background(), tgMsg("/home"))
	gw.handleIncomingMessage(context.Background(), tgMsg("home again"))

	agent.mu.Lock()
	defer agent.mu.Unlock()
	want := []string{"|", "|", "gog|TOOLS.md", "|"}
	if !reflect.DeepEqual(agent.seen, want) {
		t.Fatalf("turn scoping keys = %q, want %q", agent.seen, want)
	}
}
