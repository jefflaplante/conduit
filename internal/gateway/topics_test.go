package gateway

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"conduit/internal/ai"
	"conduit/internal/channels"
	"conduit/internal/config"
	"conduit/internal/protocol"
	"conduit/internal/sessions"
	"conduit/internal/tools/types"
	"conduit/internal/topics"
)

// conduit-43vk: topic switcher tests.

// recAdapter records every outgoing message and lets tests inject incoming
// ones, standing in for the Telegram adapter.
type recAdapter struct {
	id string
	in chan *protocol.IncomingMessage

	mu   sync.Mutex
	sent []*protocol.OutgoingMessage
}

func newRecAdapter(id string) *recAdapter {
	return &recAdapter{id: id, in: make(chan *protocol.IncomingMessage, 16)}
}

func (a *recAdapter) ID() string                      { return a.id }
func (a *recAdapter) Name() string                    { return a.id }
func (a *recAdapter) Type() string                    { return "rec" }
func (a *recAdapter) Start(ctx context.Context) error { return nil }
func (a *recAdapter) Stop() error                     { return nil }
func (a *recAdapter) ReceiveMessages() <-chan *protocol.IncomingMessage {
	return a.in
}
func (a *recAdapter) Status() channels.ChannelStatus {
	return channels.ChannelStatus{Status: channels.StatusOnline}
}
func (a *recAdapter) IsHealthy() bool { return true }
func (a *recAdapter) SendMessage(m *protocol.OutgoingMessage) error {
	a.mu.Lock()
	a.sent = append(a.sent, m)
	a.mu.Unlock()
	return nil
}

func (a *recAdapter) texts() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, 0, len(a.sent))
	for _, m := range a.sent {
		out = append(out, m.Text)
	}
	return out
}

// waitSent waits until at least n messages were delivered and returns them.
func (a *recAdapter) waitSent(t *testing.T, n int) []string {
	t.Helper()
	waitForCond(t, "outgoing messages", func() bool { return len(a.texts()) >= n })
	return a.texts()
}

type recFactory struct{ a *recAdapter }

func (f *recFactory) SupportsType(t string) bool  { return t == "rec" }
func (f *recFactory) GetSupportedTypes() []string { return []string{"rec"} }
func (f *recFactory) CreateAdapter(channels.ChannelConfig) (channels.ChannelAdapter, error) {
	return f.a, nil
}

// attachRecorder replaces gw's channel manager with one routing to a
// recording adapter registered as "telegram".
func attachRecorder(t *testing.T, gw *Gateway) *recAdapter {
	t.Helper()
	ad := newRecAdapter("telegram")
	cm := channels.NewManager()
	cm.RegisterFactory(&recFactory{a: ad})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := cm.Start(ctx, []channels.ChannelConfig{{ID: "telegram", Type: "rec", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cm.Stop() })
	gw.channelManager = cm
	return ad
}

// stubSummaries makes topic summaries deterministic and LLM-free.
func stubSummaries(gw *Gateway) {
	gw.topicSummarize = func(_ context.Context, s *sessions.Session) string {
		return "Summary of " + s.Key + "."
	}
}

func newTopicTestGateway(t *testing.T) (*Gateway, *sessions.Store) {
	t.Helper()
	gw, store := newTestGatewayWithSessions(t)
	stubSummaries(gw)
	return gw, store
}

const (
	tgChannel = "telegram"
	tgUser    = "42"
	tgChat    = "telegram:42"
)

func TestResolveSessionForChat(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T, gw *Gateway, store *sessions.Store) string // returns expected key ("" = home)
		wantNotice bool
	}{
		{
			name:  "no pointer is home",
			setup: func(*testing.T, *Gateway, *sessions.Store) string { return "" },
		},
		{
			name: "pointer to active topic",
			setup: func(t *testing.T, gw *Gateway, _ *sessions.Store) string {
				return mustSwitch(t, gw, "alpha").SessionKey
			},
		},
		{
			name: "explicit home pointer",
			setup: func(t *testing.T, gw *Gateway, _ *sessions.Store) string {
				mustSwitch(t, gw, "alpha")
				if err := gw.topicStore().SetPointer(tgChat, ""); err != nil {
					t.Fatal(err)
				}
				return ""
			},
		},
		{
			name: "archived topic falls back home",
			setup: func(t *testing.T, gw *Gateway, _ *sessions.Store) string {
				mustSwitch(t, gw, "alpha")
				if err := gw.topicStore().SetStatus("alpha", topics.StatusArchived); err != nil {
					t.Fatal(err)
				}
				return ""
			},
			wantNotice: true,
		},
		{
			name: "parked topic falls back home",
			setup: func(t *testing.T, gw *Gateway, _ *sessions.Store) string {
				mustSwitch(t, gw, "alpha")
				if err := gw.topicStore().SetStatus("alpha", topics.StatusParked); err != nil {
					t.Fatal(err)
				}
				return ""
			},
			wantNotice: true,
		},
		{
			name: "missing topic falls back home",
			setup: func(t *testing.T, gw *Gateway, _ *sessions.Store) string {
				if err := gw.topicStore().SetPointer(tgChat, "ghost"); err != nil {
					t.Fatal(err)
				}
				return ""
			},
			wantNotice: true,
		},
		{
			name: "topic session recreated lazily",
			setup: func(t *testing.T, gw *Gateway, store *sessions.Store) string {
				tp := mustSwitch(t, gw, "alpha")
				if _, err := store.DB().Exec(`DELETE FROM sessions WHERE key = ?`, tp.SessionKey); err != nil {
					t.Fatal(err)
				}
				return tp.SessionKey
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gw, store := newTopicTestGateway(t)
			home, err := store.GetOrCreateSession(tgUser, tgChannel)
			if err != nil {
				t.Fatal(err)
			}
			wantKey := tt.setup(t, gw, store)
			if wantKey == "" {
				wantKey = home.Key
			}

			got, notice, err := gw.resolveSessionForChat(tgChannel, tgUser)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if got.Key != wantKey {
				t.Fatalf("resolved %q, want %q", got.Key, wantKey)
			}
			if got.ChannelID != tgChannel || got.UserID != tgUser {
				t.Fatalf("resolved session routes to %s/%s", got.ChannelID, got.UserID)
			}
			if (notice != "") != tt.wantNotice {
				t.Fatalf("notice = %q, wantNotice %v", notice, tt.wantNotice)
			}
			if tt.wantNotice {
				// Told once: the pointer was reset, so the next message is plain home.
				again, notice2, err := gw.resolveSessionForChat(tgChannel, tgUser)
				if err != nil || again.Key != home.Key || notice2 != "" {
					t.Fatalf("second resolve = %v %q %v, want home without notice", again.Key, notice2, err)
				}
			}
		})
	}
}

// Without topics the resolver must be byte-for-byte today's behavior.
func TestResolveSessionForChat_HomeMatchesGetOrCreate(t *testing.T) {
	gw, store := newTopicTestGateway(t)
	got, notice, err := gw.resolveSessionForChat(tgChannel, tgUser)
	if err != nil || notice != "" {
		t.Fatalf("resolve = %v, %q", err, notice)
	}
	want, err := store.GetOrCreateSession(tgUser, tgChannel)
	if err != nil {
		t.Fatal(err)
	}
	if got.Key != want.Key {
		t.Fatalf("resolver created %q, GetOrCreateSession returns %q", got.Key, want.Key)
	}
	if tps, _ := gw.topicStore().List(); len(tps) != 0 {
		t.Fatalf("resolver must not create topic rows: %v", tps)
	}
}

func mustSwitch(t *testing.T, gw *Gateway, slug string) *topics.Topic {
	t.Helper()
	if _, err := gw.topicSwitch(context.Background(), tgChannel, tgUser, slug); err != nil {
		t.Fatalf("switch %s: %v", slug, err)
	}
	tp, err := gw.topicStore().Get(slug)
	if err != nil {
		t.Fatal(err)
	}
	return tp
}

func runTopicCmd(t *testing.T, gw *Gateway, text string) string {
	t.Helper()
	reply, ok := gw.topicCommand(context.Background(), tgChannel, tgUser, text)
	if !ok {
		t.Fatalf("%q not handled as a topic command", text)
	}
	return reply
}

func TestTopicCommand(t *testing.T) {
	tests := []struct {
		name   string
		pre    []string
		cmd    string
		want   []string // substrings of the reply
		verify func(t *testing.T, gw *Gateway, store *sessions.Store)
	}{
		{
			name: "list empty",
			cmd:  "/topic",
			want: []string{"No topics yet", "📍 Current: [home]"},
		},
		{
			name: "switch creates",
			cmd:  "/topic alpha",
			want: []string{"Topic: [alpha] — new", "📍 Current: [alpha]"},
			verify: func(t *testing.T, gw *Gateway, store *sessions.Store) {
				tp, err := gw.topicStore().Get("alpha")
				if err != nil || tp.Status != topics.StatusActive {
					t.Fatalf("topic = %+v, %v", tp, err)
				}
				if slug, _ := gw.topicStore().Pointer(tgChat); slug != "alpha" {
					t.Fatalf("pointer = %q", slug)
				}
				sess, err := store.GetSession(tp.SessionKey)
				if err != nil || sess.ChannelID != tgChannel || sess.UserID != tgUser {
					t.Fatalf("topic session = %+v, %v", sess, err)
				}
			},
		},
		{
			name: "switch normalizes slug",
			cmd:  "/topic Solar_Panels",
			want: []string{"Topic: [solar-panels] — new"},
		},
		{
			name: "list marks current",
			pre:  []string{"/topic alpha", "/topic beta"},
			cmd:  "/topic",
			want: []string{"★ [beta]", "[alpha]", "active", "📍 Current: [beta]"},
		},
		{
			name: "switch to existing resumes with summary",
			pre:  []string{"/topic alpha", "/topic park alpha"},
			cmd:  "/topic alpha",
			want: []string{"Topic: [alpha] — Summary of", "📍 Current: [alpha]"},
			verify: func(t *testing.T, gw *Gateway, _ *sessions.Store) {
				tp, _ := gw.topicStore().Get("alpha")
				if tp.Status != topics.StatusActive {
					t.Fatalf("parked topic not unparked: %s", tp.Status)
				}
			},
		},
		{
			name: "switch away summarizes previous topic",
			pre:  []string{"/topic alpha"},
			cmd:  "/topic beta",
			verify: func(t *testing.T, gw *Gateway, _ *sessions.Store) {
				tp, _ := gw.topicStore().Get("alpha")
				if !strings.HasPrefix(tp.Summary, "Summary of ") {
					t.Fatalf("alpha summary after switch-away = %q", tp.Summary)
				}
			},
		},
		{
			name: "topic home",
			pre:  []string{"/topic alpha"},
			cmd:  "/topic home",
			want: []string{"📍 Current: [home]"},
			verify: func(t *testing.T, gw *Gateway, _ *sessions.Store) {
				if slug, _ := gw.topicStore().Pointer(tgChat); slug != "" {
					t.Fatalf("pointer = %q", slug)
				}
			},
		},
		{
			name: "home alias",
			pre:  []string{"/topic alpha"},
			cmd:  "/home",
			want: []string{"📍 Current: [home]"},
			verify: func(t *testing.T, gw *Gateway, _ *sessions.Store) {
				if slug, _ := gw.topicStore().Pointer(tgChat); slug != "" {
					t.Fatalf("pointer = %q", slug)
				}
			},
		},
		{
			name: "park current moves home",
			pre:  []string{"/topic alpha"},
			cmd:  "/topic park alpha",
			want: []string{"Parked [alpha]", "📍 Current: [home]"},
			verify: func(t *testing.T, gw *Gateway, _ *sessions.Store) {
				tp, _ := gw.topicStore().Get("alpha")
				if tp.Status != topics.StatusParked || tp.Summary == "" {
					t.Fatalf("parked topic = %+v", tp)
				}
				if slug, _ := gw.topicStore().Pointer(tgChat); slug != "" {
					t.Fatalf("pointer = %q", slug)
				}
			},
		},
		{
			name: "park other keeps pointer",
			pre:  []string{"/topic alpha", "/topic beta"},
			cmd:  "/topic park alpha",
			want: []string{"Parked [alpha]", "📍 Current: [beta]"},
		},
		{
			name: "close archives with final summary",
			pre:  []string{"/topic alpha"},
			cmd:  "/topic close alpha",
			want: []string{"Closed [alpha]", "📍 Current: [home]"},
			verify: func(t *testing.T, gw *Gateway, store *sessions.Store) {
				tp, _ := gw.topicStore().Get("alpha")
				if tp.Status != topics.StatusArchived || tp.Summary == "" {
					t.Fatalf("closed topic = %+v", tp)
				}
				sess, _ := store.GetSession(tp.SessionKey)
				if sess.Context[topicCtxSummary] != tp.Summary {
					t.Fatalf("final summary not in session context: %v", sess.Context)
				}
			},
		},
		{
			name: "rename display name only",
			pre:  []string{"/topic alpha"},
			cmd:  "/topic rename alpha Alpha",
			want: []string{"Renamed [alpha]", "📍 Current: [alpha]"},
			verify: func(t *testing.T, gw *Gateway, _ *sessions.Store) {
				tp, err := gw.topicStore().Get("alpha")
				if err != nil || tp.Name != "Alpha" {
					t.Fatalf("topic = %+v, %v", tp, err)
				}
			},
		},
		{
			name: "rename slug cascades pointer",
			pre:  []string{"/topic alpha"},
			cmd:  "/topic rename alpha Gamma Ray",
			want: []string{"gamma-ray", "📍 Current: [gamma-ray]"},
			verify: func(t *testing.T, gw *Gateway, _ *sessions.Store) {
				if slug, _ := gw.topicStore().Pointer(tgChat); slug != "gamma-ray" {
					t.Fatalf("pointer = %q", slug)
				}
				tp, err := gw.topicStore().Get("gamma-ray")
				if err != nil || tp.Name != "Gamma Ray" {
					t.Fatalf("renamed = %+v, %v", tp, err)
				}
				// Old slug 404s.
				reply, _ := gw.topicCommand(context.Background(), tgChannel, tgUser, "/topic park alpha")
				if !strings.Contains(reply, "No topic") {
					t.Fatalf("old slug still resolves: %q", reply)
				}
			},
		},
		{
			name: "rename collision",
			pre:  []string{"/topic alpha", "/topic beta"},
			cmd:  "/topic rename alpha beta",
			want: []string{"already exists"},
		},
		{
			name: "persona set",
			pre:  []string{"/topic alpha"},
			cmd:  "/topic persona alpha Be terse and cite files.",
			want: []string{"Persona set for [alpha]", "📍"},
			verify: func(t *testing.T, gw *Gateway, _ *sessions.Store) {
				tp, _ := gw.topicStore().Get("alpha")
				if tp.Persona != "Be terse and cite files." {
					t.Fatalf("persona = %q", tp.Persona)
				}
			},
		},
		{
			name: "persona clear",
			pre:  []string{"/topic alpha", "/topic persona alpha x"},
			cmd:  "/topic persona alpha",
			want: []string{"Persona cleared for [alpha]"},
			verify: func(t *testing.T, gw *Gateway, _ *sessions.Store) {
				tp, _ := gw.topicStore().Get("alpha")
				if tp.Persona != "" {
					t.Fatalf("persona = %q", tp.Persona)
				}
			},
		},
		{
			name: "stitch",
			pre:  []string{"/topic alpha", "/topic park alpha", "/topic beta"},
			cmd:  "/topic stitch alpha beta",
			want: []string{"Stitched [alpha] → [beta]", "📍 Current: [beta]"},
			verify: func(t *testing.T, gw *Gateway, store *sessions.Store) {
				a, _ := gw.topicStore().Get("alpha")
				b, _ := gw.topicStore().Get("beta")
				sess, _ := store.GetSession(b.SessionKey)
				if !strings.Contains(sess.Context[topicCtxStitched], a.Summary) {
					t.Fatalf("beta stitched context = %q, want alpha summary %q", sess.Context[topicCtxStitched], a.Summary)
				}
				if a.Status != topics.StatusParked {
					t.Fatalf("stitch changed source topic: %+v", a)
				}
			},
		},
		{
			name: "invalid slug",
			cmd:  "/topic !!!",
			want: []string{"❌", "📍 Current: [home]"},
		},
		{
			name: "reserved slug",
			cmd:  "/topic stitch",
			want: []string{"Usage"},
		},
		{
			name: "unknown subcommand",
			cmd:  "/topic frobnicate alpha beta",
			want: []string{"Usage", "/topic park <slug>", "📍 Current: [home]"},
		},
		{name: "park missing arg", cmd: "/topic park", want: []string{"Usage"}},
		{name: "close missing arg", cmd: "/topic close", want: []string{"Usage"}},
		{name: "rename missing arg", pre: []string{"/topic alpha"}, cmd: "/topic rename alpha", want: []string{"Usage"}},
		{name: "persona missing slug", cmd: "/topic persona", want: []string{"Usage"}},
		{name: "stitch missing arg", cmd: "/topic stitch alpha", want: []string{"Usage"}},
		{name: "park unknown", cmd: "/topic park nope", want: []string{"No topic", "nope"}},
		{name: "close unknown", cmd: "/topic close nope", want: []string{"No topic"}},
		{name: "persona unknown", cmd: "/topic persona nope x", want: []string{"No topic"}},
		{name: "stitch unknown", pre: []string{"/topic alpha"}, cmd: "/topic stitch nope alpha", want: []string{"No topic"}},
		{name: "stitch self", pre: []string{"/topic alpha"}, cmd: "/topic stitch alpha alpha", want: []string{"❌"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gw, store := newTopicTestGateway(t)
			for _, p := range tt.pre {
				runTopicCmd(t, gw, p)
			}
			reply := runTopicCmd(t, gw, tt.cmd)
			for _, w := range tt.want {
				if !strings.Contains(reply, w) {
					t.Errorf("reply to %q missing %q:\n%s", tt.cmd, w, reply)
				}
			}
			if !strings.Contains(reply, "📍 Current: ") {
				t.Errorf("reply to %q does not echo topic state:\n%s", tt.cmd, reply)
			}
			if tt.verify != nil {
				tt.verify(t, gw, store)
			}
		})
	}
}

func TestTopicCommand_NotTopic(t *testing.T) {
	gw, _ := newTopicTestGateway(t)
	for _, text := range []string{"/topics", "/homes", "/status", "hello /topic", "/hometown x"} {
		if _, ok := gw.topicCommand(context.Background(), tgChannel, tgUser, text); ok {
			t.Errorf("%q handled as a topic command", text)
		}
	}
}

// The fallback summary (no AI) is a short template, never the transcript.
func TestTopicSummary_TemplateFallback(t *testing.T) {
	gw, store := newTestGatewayWithSessions(t) // no AI, no stub
	tp := mustSwitch(t, gw, "alpha")
	long := strings.Repeat("word ", 200)
	for _, m := range []string{"first question", long} {
		if _, err := store.AddMessage(tp.SessionKey, "user", m, nil); err != nil {
			t.Fatal(err)
		}
	}
	runTopicCmd(t, gw, "/topic park alpha")
	got, _ := gw.topicStore().Get("alpha")
	if got.Summary == "" || len(got.Summary) > 400 {
		t.Fatalf("template summary = %q (len %d)", got.Summary, len(got.Summary))
	}
	if !strings.Contains(got.Summary, "2 messages") {
		t.Fatalf("template summary should count messages: %q", got.Summary)
	}
}

func TestTopicPrompt(t *testing.T) {
	gw, store := newTopicTestGateway(t)
	runTopicCmd(t, gw, "/topic alpha")
	runTopicCmd(t, gw, "/topic park alpha")
	runTopicCmd(t, gw, "/topic beta")
	runTopicCmd(t, gw, "/topic persona beta You are a release engineer.")
	runTopicCmd(t, gw, "/topic stitch alpha beta")

	b, _ := gw.topicStore().Get("beta")
	_ = gw.topicStore().SetSummary("beta", "Shipping v2")
	sess, _ := store.GetSession(b.SessionKey)
	sess.UpdatedAt = time.Now().Add(-3 * time.Hour)

	got := gw.topicPromptFor(sess)
	wantOrder := []string{
		"alpha", // stitched context first (prepended)
		"Topic: beta — Shipping v2. Last activity: 3h ago.",
		"You are a release engineer.", // persona after the topic prefix
	}
	pos := -1
	for _, w := range wantOrder {
		i := strings.Index(got, w)
		if i < 0 {
			t.Fatalf("topic prompt missing %q:\n%s", w, got)
		}
		if i < pos {
			t.Fatalf("topic prompt order wrong at %q:\n%s", w, got)
		}
		pos = i
	}
	if len(got) > 2000 {
		t.Fatalf("topic prompt not bounded (%d chars)", len(got))
	}

	home, _ := store.GetOrCreateSession(tgUser, tgChannel)
	if p := gw.topicPromptFor(home); p != "" {
		t.Fatalf("home session got a topic prompt: %q", p)
	}
}

func TestTopicTag(t *testing.T) {
	gw, store := newTopicTestGateway(t)
	tp := mustSwitch(t, gw, "alpha")
	home, _ := store.GetOrCreateSession(tgUser, tgChannel)

	tests := []struct {
		name                 string
		sessionKey, ch, user string
		text, want           string
	}{
		{"topic to shared chat", tp.SessionKey, tgChannel, tgUser, "Done — tests pass.", "[alpha] Done — tests pass."},
		{"home untagged", home.Key, tgChannel, tgUser, "hi", "hi"},
		{"no session untagged", "", tgChannel, tgUser, "hi", "hi"},
		{"topic to another chat untagged", tp.SessionKey, tgChannel, "7", "hi", "hi"},
		{"not double tagged", tp.SessionKey, tgChannel, tgUser, "[alpha] hi", "[alpha] hi"},
		{"empty stays empty", tp.SessionKey, tgChannel, tgUser, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := gw.topicTag(tt.sessionKey, tt.ch, tt.user, tt.text); got != tt.want {
				t.Fatalf("topicTag = %q, want %q", got, tt.want)
			}
		})
	}
}

// Proactive Message-tool sends from a topic session carry the tag too.
func TestTopicTag_ProactiveSend(t *testing.T) {
	gw, store := newTopicTestGateway(t)
	ad := attachRecorder(t, gw)
	tp := mustSwitch(t, gw, "alpha")
	home, _ := store.GetOrCreateSession(tgUser, tgChannel)

	ctx := types.WithRequestContext(context.Background(), tgChannel, tgUser, tp.SessionKey)
	if err := gw.SendMessage(ctx, tgChannel, tgUser, "from topic", nil); err != nil {
		t.Fatal(err)
	}
	ctx = types.WithRequestContext(context.Background(), tgChannel, tgUser, home.Key)
	if err := gw.SendMessage(ctx, tgChannel, tgUser, "from home", nil); err != nil {
		t.Fatal(err)
	}
	got := ad.waitSent(t, 2)
	if got[0] != "[alpha] from topic" || got[1] != "from home" {
		t.Fatalf("sent = %q", got)
	}
}

// Ingress routes through the resolver: commands see the topic session.
func TestIngress_CommandsUseResolvedSession(t *testing.T) {
	gw, store := newTopicTestGateway(t)
	ad := attachRecorder(t, gw)
	home, _ := store.GetOrCreateSession(tgUser, tgChannel)

	gw.handleIncomingMessage(context.Background(), tgMsg("/topic alpha"))
	gw.handleIncomingMessage(context.Background(), tgMsg("/status"))
	tp, _ := gw.topicStore().Get("alpha")
	got := ad.waitSent(t, 2)
	if !strings.Contains(got[0], "Topic: [alpha] — new") {
		t.Fatalf("switch reply = %q", got[0])
	}
	if !strings.Contains(got[1], tp.SessionKey) {
		t.Fatalf("/status inside topic shows %q, want topic session %s", got[1], tp.SessionKey)
	}

	gw.handleIncomingMessage(context.Background(), tgMsg("/home"))
	gw.handleIncomingMessage(context.Background(), tgMsg("/status"))
	got = ad.waitSent(t, 4)
	if !strings.Contains(got[3], home.Key) {
		t.Fatalf("/status after /home shows %q, want home %s", got[3], home.Key)
	}
}

// The archived-topic fallback tells the user once, on ingress.
func TestIngress_ArchivedFallbackNotice(t *testing.T) {
	gw, _ := newTopicTestGateway(t)
	ad := attachRecorder(t, gw)
	mustSwitch(t, gw, "alpha")
	_ = gw.topicStore().SetStatus("alpha", topics.StatusArchived)

	gw.handleIncomingMessage(context.Background(), tgMsg("/status"))
	gw.handleIncomingMessage(context.Background(), tgMsg("/status"))
	got := ad.waitSent(t, 3)
	notices := 0
	for _, s := range got {
		if strings.Contains(s, "no longer active") {
			notices++
		}
	}
	if notices != 1 {
		t.Fatalf("want exactly one fallback notice, got %d: %q", notices, got)
	}
}

// Full turns: topic messages land in the topic transcript, replies are tagged,
// home stays untagged and untouched.
func TestIngress_TopicTurnTranscriptAndTagging(t *testing.T) {
	gw, store, _ := newTurnTestGateway(t)
	stubSummaries(gw)
	ad := attachRecorder(t, gw)
	home, _ := store.GetOrCreateSession(tgUser, tgChannel)

	gw.handleIncomingMessage(context.Background(), tgMsg("/topic alpha"))
	gw.handleIncomingMessage(context.Background(), tgMsg("hello topic"))
	gw.handleIncomingMessage(context.Background(), tgMsg("/home"))
	gw.handleIncomingMessage(context.Background(), tgMsg("hello home"))

	tp, _ := gw.topicStore().Get("alpha")
	if got := transcript(t, store, tp.SessionKey); strings.Join(got, "|") != "user:hello topic|assistant:reply 1" {
		t.Fatalf("topic transcript = %v", got)
	}
	if got := transcript(t, store, home.Key); strings.Join(got, "|") != "user:hello home|assistant:reply 2" {
		t.Fatalf("home transcript = %v", got)
	}
	sent := ad.waitSent(t, 4)
	if !containsStr(sent, "[alpha] reply 1") {
		t.Fatalf("topic reply not tagged: %q", sent)
	}
	if !containsStr(sent, "reply 2") {
		t.Fatalf("home reply missing or tagged: %q", sent)
	}
}

// The channel-manager ingest loop (processMessages: adapter queue →
// semaphore → handleIncomingMessage) routes through the resolver too.
func TestIngestLoop_RoutesThroughResolver(t *testing.T) {
	gw, store, _ := newTurnTestGateway(t)
	stubSummaries(gw)
	ad := attachRecorder(t, gw)
	mustSwitch(t, gw, "alpha")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go gw.processMessages(ctx)

	ad.in <- &protocol.IncomingMessage{ChannelID: tgChannel, UserID: tgUser, Text: "via ingest"}
	tp, _ := gw.topicStore().Get("alpha")
	waitForCond(t, "topic turn via ingest loop", func() bool {
		return len(transcript(t, store, tp.SessionKey)) == 2
	})
	home, _ := store.GetOrCreateSession(tgUser, tgChannel)
	if got := transcript(t, store, home.Key); len(got) != 0 {
		t.Fatalf("ingest message leaked into home: %v", got)
	}
	waitForCond(t, "tagged reply", func() bool { return containsStr(ad.texts(), "[alpha] reply 1") })
}

// Two topics turn in parallel: a turn held open in alpha must not block beta
// (AcquireTurn is per session key).
func TestTopics_ParallelTurnsDoNotBlock(t *testing.T) {
	gw, store, p := newTurnTestGateway(t)
	stubSummaries(gw)
	attachRecorder(t, gw)
	p.hold(1)

	gw.handleIncomingMessage(context.Background(), tgMsg("/topic alpha"))
	doneA := make(chan struct{})
	go func() { defer close(doneA); gw.handleIncomingMessage(context.Background(), tgMsg("work in alpha")) }()
	waitEntered(t, p, 1)

	// Switching while alpha's turn runs is safe and immediate.
	gw.handleIncomingMessage(context.Background(), tgMsg("/topic beta"))
	doneB := make(chan struct{})
	go func() { defer close(doneB); gw.handleIncomingMessage(context.Background(), tgMsg("work in beta")) }()
	waitEntered(t, p, 2) // would time out if beta queued behind alpha
	<-doneB

	p.release(1)
	<-doneA

	a, _ := gw.topicStore().Get("alpha")
	b, _ := gw.topicStore().Get("beta")
	if got := transcript(t, store, a.SessionKey); len(got) != 2 || got[0] != "user:work in alpha" {
		t.Fatalf("alpha transcript = %v", got)
	}
	if got := transcript(t, store, b.SessionKey); len(got) != 2 || got[0] != "user:work in beta" {
		t.Fatalf("beta transcript = %v", got)
	}
}

// /reset inside a topic resets only that topic's session; the binding stays.
func TestTopics_ResetInsideTopic(t *testing.T) {
	gw, store, _ := newTurnTestGateway(t)
	stubSummaries(gw)
	attachRecorder(t, gw)
	home, _ := store.GetOrCreateSession(tgUser, tgChannel)
	_, _ = store.AddMessage(home.Key, "user", "home history", nil)

	gw.handleIncomingMessage(context.Background(), tgMsg("/topic alpha"))
	gw.handleIncomingMessage(context.Background(), tgMsg("topic msg"))
	gw.handleIncomingMessage(context.Background(), tgMsg("/reset"))

	tp, _ := gw.topicStore().Get("alpha")
	if got := transcript(t, store, tp.SessionKey); len(got) != 0 {
		t.Fatalf("topic transcript after /reset = %v", got)
	}
	if got := transcript(t, store, home.Key); len(got) != 1 {
		t.Fatalf("home transcript touched by topic /reset: %v", got)
	}
	if slug, _ := gw.topicStore().Pointer(tgChat); slug != "alpha" {
		t.Fatalf("binding lost after /reset: %q", slug)
	}
}

// promptAgent records the topic prompt each turn's ctx carried into
// BuildSystemPrompt.
type promptAgent struct {
	mu   sync.Mutex
	seen []string
}

func (a *promptAgent) BuildSystemPrompt(ctx context.Context, _ *sessions.Session) ([]ai.SystemBlock, error) {
	a.mu.Lock()
	a.seen = append(a.seen, types.TopicPrompt(ctx))
	a.mu.Unlock()
	return []ai.SystemBlock{{Type: "text", Text: "sys"}}, nil
}
func (a *promptAgent) GetToolDefinitions(*sessions.Session) []ai.Tool { return nil }
func (a *promptAgent) ProcessResponse(_ context.Context, r *ai.GenerateResponse) (*ai.AgentProcessedResponse, error) {
	return &ai.AgentProcessedResponse{Content: r.Content}, nil
}

// The turn runner hands the topic prefix to prompt build (topic turns only).
func TestTopics_TurnInjectsTopicPrompt(t *testing.T) {
	store, err := sessions.NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := config.AIConfig{
		DefaultProvider: "testprov",
		Providers:       []config.ProviderConfig{{Name: "testprov", Type: "ollama", Model: "llama3", ContextWindow: 8000}},
	}
	agent := &promptAgent{}
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

	gw.handleIncomingMessage(context.Background(), tgMsg("home turn"))
	gw.handleIncomingMessage(context.Background(), tgMsg("/topic alpha"))
	gw.handleIncomingMessage(context.Background(), tgMsg("/topic persona alpha Speak like a pirate."))
	gw.handleIncomingMessage(context.Background(), tgMsg("topic turn"))

	agent.mu.Lock()
	defer agent.mu.Unlock()
	if len(agent.seen) < 2 {
		t.Fatalf("agent saw %d turns", len(agent.seen))
	}
	if agent.seen[0] != "" {
		t.Fatalf("home turn got topic prompt %q", agent.seen[0])
	}
	last := agent.seen[len(agent.seen)-1]
	if !strings.Contains(last, "Topic: alpha") || !strings.Contains(last, "Speak like a pirate.") {
		t.Fatalf("topic turn prompt = %q", last)
	}
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Tool-facing wrappers resolve the chat from the request context.
func TestTopicManager_ToolWrappers(t *testing.T) {
	gw, _ := newTopicTestGateway(t)
	ctx := types.WithRequestContext(context.Background(), tgChannel, tgUser, "")

	if _, err := gw.TopicCreate(ctx, "alpha", "Alpha Project", "Be brief."); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := gw.TopicCreate(ctx, "alpha", "", ""); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate create err = %v", err)
	}
	tp, _ := gw.topicStore().Get("alpha")
	if tp.Name != "Alpha Project" || tp.Persona != "Be brief." {
		t.Fatalf("created = %+v", tp)
	}
	if slug, _ := gw.topicStore().Pointer(tgChat); slug != "" {
		t.Fatalf("create must not switch: pointer = %q", slug)
	}
	if out, err := gw.TopicSwitch(ctx, "alpha"); err != nil || !strings.Contains(out, "📍 Current: [alpha]") {
		t.Fatalf("switch = %q, %v", out, err)
	}
	if out, err := gw.TopicList(ctx); err != nil || !strings.Contains(out, "★ [alpha]") {
		t.Fatalf("list = %q, %v", out, err)
	}
	if _, err := gw.TopicRename(ctx, "alpha", "", "alpha-two"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, err := gw.TopicPersona(ctx, "alpha-two", ""); err != nil {
		t.Fatalf("persona: %v", err)
	}
	if _, err := gw.TopicCreate(ctx, "beta", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := gw.TopicStitch(ctx, "alpha-two", "beta"); err != nil {
		t.Fatalf("stitch: %v", err)
	}
	if _, err := gw.TopicPark(ctx, "beta"); err != nil {
		t.Fatalf("park: %v", err)
	}
	if out, err := gw.TopicClose(ctx, "alpha-two"); err != nil || !strings.Contains(out, "📍 Current: [home]") {
		t.Fatalf("close = %q, %v", out, err)
	}

	// Error paths surface as errors (tools turn them into failed results).
	for name, err := range map[string]error{
		"park missing":    second(gw.TopicPark(ctx, "nope")),
		"close missing":   second(gw.TopicClose(ctx, "nope")),
		"persona missing": second(gw.TopicPersona(ctx, "nope", "x")),
		"stitch missing":  second(gw.TopicStitch(ctx, "nope", "beta")),
		"rename missing":  second(gw.TopicRename(ctx, "nope", "x", "")),
		"bad slug":        second(gw.TopicSwitch(ctx, "!!!")),
	} {
		if err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if !errors.Is(second(gw.TopicPark(ctx, "nope")), topics.ErrNotFound) {
		t.Errorf("park missing should wrap ErrNotFound")
	}

	// No chat in context → switching cannot work.
	if _, err := gw.TopicSwitch(context.Background(), "beta"); err == nil {
		t.Errorf("switch without chat context should fail")
	}
}

func second(_ string, err error) error { return err }

func TestAnnounceToParentTopicTag(t *testing.T) {
	gw, store := newTopicTestGateway(t)
	tp := mustSwitch(t, gw, "alpha")
	home, _ := store.GetOrCreateSession(tgUser, tgChannel)

	tests := []struct {
		name                       string
		parentSessionKey, ch, user string
		text, want                 string
	}{
		{"topic parent tagged", tp.SessionKey, tgChannel, tgUser, "Sub-agent done.", "[alpha] Sub-agent done."},
		{"home parent untagged", home.Key, tgChannel, tgUser, "Sub-agent done.", "Sub-agent done."},
		{"empty session untagged", "", tgChannel, tgUser, "Sub-agent done.", "Sub-agent done."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := gw.topicTag(tt.parentSessionKey, tt.ch, tt.user, tt.text); got != tt.want {
				t.Fatalf("topicTag = %q, want %q", got, tt.want)
			}
		})
	}
}
