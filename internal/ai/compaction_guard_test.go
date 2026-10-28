package ai

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"conduit/internal/config"
	"conduit/internal/sessions"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// H5: compaction hard-deletes the summarized messages, so it must refuse to
// apply a summary that is empty, made of tool calls, or cut off at
// max_tokens; and the summary call must run on the configured compaction
// model, with no tool definitions.

// setupCompactionGuard builds a router whose default provider "mock" is the
// returned MockProvider, with an agent system that exposes one tool, and a
// session holding total messages (m00..).
func setupCompactionGuard(t *testing.T, total int, cfg config.CompactionConfig) (*CompactionEngine, *MockProvider, *sessions.Store, *sessions.Session) {
	t.Helper()
	store, err := sessions.NewStore(filepath.Join(t.TempDir(), "gw.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	session, err := store.GetOrCreateSession("u1", "c1")
	require.NoError(t, err)
	for i := 0; i < total; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		_, err := store.AddMessage(session.Key, role, fmt.Sprintf("m%02d", i), nil)
		require.NoError(t, err)
	}

	agent := &MockAgentSystem{tools: []Tool{{Name: "Bash", Description: "run a command"}}}
	router, err := NewRouter(config.AIConfig{DefaultProvider: "mock"}, agent)
	require.NoError(t, err)
	mock := NewMockProvider("mock")
	router.RegisterProvider("mock", mock)

	cfg.Enabled = true
	ce := NewCompactionEngine(router, store, cfg)
	return ce, mock, store, session
}

func originalContents(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("m%02d", i)
	}
	return out
}

func TestCompact_RefusesUnusableSummary(t *testing.T) {
	cases := []struct {
		name  string
		setup func(m *MockProvider)
	}{
		{"empty text", func(m *MockProvider) { m.AddResponse("", nil) }},
		{"whitespace text", func(m *MockProvider) { m.AddResponse(" \n\t ", nil) }},
		{"tool call only", func(m *MockProvider) {
			m.AddResponse("", []ToolCall{{ID: "t1", Name: "Bash", Args: map[string]interface{}{"command": "ls"}}})
		}},
		{"text plus tool call", func(m *MockProvider) {
			m.AddResponse("Let me look first.", []ToolCall{{ID: "t1", Name: "Bash", Args: map[string]interface{}{"command": "ls"}}})
		}},
		{"length truncated", func(m *MockProvider) {
			// Every auto-continue also hits the cap, so the final reply is
			// still FinishReason=length.
			for i := 0; i < maxLengthAutoContinues+1; i++ {
				m.AddResponseWithFinishReason(fmt.Sprintf("partial %d ", i), nil, "length")
			}
		}},
		{"max_tokens truncated", func(m *MockProvider) {
			m.AddResponseWithFinishReason("partial summary", nil, "max_tokens")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ce, mock, store, session := setupCompactionGuard(t, 15, config.CompactionConfig{RecentMessagesToKeep: 5})
			tc.setup(mock)

			res, err := ce.Compact(context.Background(), session)
			require.Error(t, err, "an unusable summary must not be applied")
			assert.Nil(t, res)
			assert.Equal(t, originalContents(15), msgContents(t, store, session.Key),
				"history must be untouched when the summary is refused")

			ctxVals, err := store.GetSession(session.Key)
			require.NoError(t, err)
			assert.Empty(t, ctxVals.Context["compact_timestamp"], "no compaction metadata on refusal")
		})
	}
}

func TestCompact_SummaryCallUsesConfiguredModelAndNoTools(t *testing.T) {
	ce, mock, store, session := setupCompactionGuard(t, 15, config.CompactionConfig{
		RecentMessagesToKeep: 5,
		Model:                "summary-model-x",
	})
	mock.AddResponse("A good summary.", nil)

	res, err := ce.Compact(context.Background(), session)
	require.NoError(t, err)
	require.NotNil(t, res)

	require.Equal(t, 1, mock.GetCallCount())
	req := mock.LastCall().Request
	assert.Equal(t, "summary-model-x", req.Model, "configured compaction model must reach the provider")
	assert.Empty(t, req.Tools, "the summary side call must carry no tool definitions")

	got := msgContents(t, store, session.Key)
	require.Len(t, got, 1+5)
	assert.True(t, strings.HasPrefix(got[0], "[Context Summary from 10 previous messages]"), got[0])
	assert.Contains(t, got[0], "A good summary.")
	assert.Equal(t, originalContents(15)[10:], got[1:])
}

func TestCompact_ConfiguredModelRoutesToItsProvider(t *testing.T) {
	ce, mock, _, session := setupCompactionGuard(t, 15, config.CompactionConfig{
		RecentMessagesToKeep: 5,
		Model:                "cheap/tiny-model",
	})
	cheap := NewMockProvider("cheap")
	cheap.AddResponse("Cheap summary.", nil)
	ce.router.RegisterProvider("cheap", cheap)
	ce.router.providerMeta["cheap"] = ProviderMeta{Name: "cheap", Type: "openai"}

	_, err := ce.Compact(context.Background(), session)
	require.NoError(t, err)
	assert.Equal(t, 0, mock.GetCallCount(), "default provider must not serve a model routed elsewhere")
	require.Equal(t, 1, cheap.GetCallCount())
	assert.Equal(t, "cheap/tiny-model", cheap.LastCall().Request.Model)
}

func TestCompact_DefaultModelFallsBackWhenHaikuUnroutable(t *testing.T) {
	// No compaction model configured and no provider can serve the
	// built-in haiku default: the summary runs on the default route
	// instead of failing (or sending a claude-* model to a non-Anthropic
	// provider).
	ce, mock, _, session := setupCompactionGuard(t, 15, config.CompactionConfig{RecentMessagesToKeep: 5})
	mock.AddResponse("Default-route summary.", nil)

	_, err := ce.Compact(context.Background(), session)
	require.NoError(t, err)
	require.Equal(t, 1, mock.GetCallCount())
	assert.Empty(t, mock.LastCall().Request.Model)
}

func TestCompact_DefaultModelUsesHaikuWhenRoutable(t *testing.T) {
	ce, mock, _, session := setupCompactionGuard(t, 15, config.CompactionConfig{RecentMessagesToKeep: 5})
	anth := NewMockProvider("anthropic")
	anth.AddResponse("Haiku summary.", nil)
	ce.router.RegisterProvider("anthropic", anth)
	ce.router.providerMeta["anthropic"] = ProviderMeta{Name: "anthropic", Type: "anthropic"}

	_, err := ce.Compact(context.Background(), session)
	require.NoError(t, err)
	assert.Equal(t, 0, mock.GetCallCount())
	require.Equal(t, 1, anth.GetCallCount())
	assert.Equal(t, defaultCompactionModel, anth.LastCall().Request.Model)
}

func TestRouterGenerateResponse_HonoursSessionModel(t *testing.T) {
	// The side-call callers (topic summary, workspace summary) put the
	// model in session.Context["model"]; it must reach the provider.
	router, err := NewRouter(config.AIConfig{DefaultProvider: "mock"}, nil)
	require.NoError(t, err)
	mock := NewMockProvider("mock")
	router.RegisterProvider("mock", mock)

	sess := &sessions.Session{Key: "side", Context: map[string]string{"model": "side-model"}}
	_, err = router.GenerateResponse(context.Background(), sess, "hi", "")
	require.NoError(t, err)
	assert.Equal(t, "side-model", mock.LastCall().Request.Model)

	// No model in the session: default route, empty model (unchanged).
	_, err = router.GenerateResponse(context.Background(), &sessions.Session{Key: "side2"}, "hi", "")
	require.NoError(t, err)
	assert.Empty(t, mock.LastCall().Request.Model)
}
