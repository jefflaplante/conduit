package core

import (
	"context"
	"testing"

	"conduit/internal/approval"
	"conduit/internal/fts"
	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// H4: Find scope=session only returns messages from sessions the turn may
// access (the gateway's CanAccessSession), unless the owner is asking.

type accessGateway struct {
	types.GatewayService
	allowed map[string]bool
	asked   []string
}

func (g *accessGateway) CanAccessSession(_ context.Context, key string) bool {
	g.asked = append(g.asked, key)
	return g.allowed[key]
}

func accessSearcher() *mockSearchService {
	return &mockSearchService{messages: []fts.MessageResult{
		{MessageID: "m1", SessionKey: "telegram_mine", Role: "user", Content: "secret plan mine", Rank: -9},
		{MessageID: "m2", SessionKey: "telegram_victim", Role: "user", Content: "secret plan victim", Rank: -8},
		{MessageID: "m3", SessionKey: "telegram_mine", Role: "assistant", Content: "secret plan reply", Rank: -7},
	}}
}

func TestFind_SessionScopeFiltersOtherUsers(t *testing.T) {
	for _, scope := range []string{"session", "all"} {
		t.Run(scope, func(t *testing.T) {
			gw := &accessGateway{allowed: map[string]bool{"telegram_mine": true}}
			tool := NewFindTool(&types.ToolServices{Searcher: accessSearcher(), Gateway: gw})
			ctx := approval.WithNonInteractive(types.WithRequestContext(context.Background(), "telegram", "7", "telegram_mine"), "wake")

			res, err := tool.Execute(ctx, map[string]interface{}{"query": "secret plan", "scope": scope})
			require.NoError(t, err)
			require.True(t, res.Success, res.Error)
			assert.Equal(t, 2, res.Data["result_count"])
			assert.Contains(t, res.Content, "telegram_mine")
			assert.NotContains(t, res.Content, "victim")
		})
	}
}

func TestFind_SessionScopeOwnerSeesAll(t *testing.T) {
	gw := &accessGateway{allowed: map[string]bool{}}
	tool := NewFindTool(&types.ToolServices{Searcher: accessSearcher(), Gateway: gw})
	ctx := approval.WithInteractiveOrigin(context.Background(), approval.Origin{Source: "telegram", ChannelID: "telegram", UserID: "7"})

	res, err := tool.Execute(ctx, map[string]interface{}{"query": "secret plan", "scope": "session"})
	require.NoError(t, err)
	assert.Equal(t, 3, res.Data["result_count"])
	assert.Contains(t, res.Content, "telegram_victim")
	assert.Empty(t, gw.asked, "the owner needs no per-session check")
}

func TestFind_SessionScopeFailsClosedWithoutChecker(t *testing.T) {
	tool := NewFindTool(&types.ToolServices{Searcher: accessSearcher()}) // no gateway
	ctx := approval.WithNonInteractive(context.Background(), "mcp")

	res, err := tool.Execute(ctx, map[string]interface{}{"query": "secret plan", "scope": "session"})
	require.NoError(t, err)
	assert.Equal(t, 0, res.Data["result_count"])
	assert.NotContains(t, res.Content, "telegram_")
}

// ownerFindCtx is a turn started by a live human message.
func ownerFindCtx() context.Context {
	return approval.WithInteractiveOrigin(context.Background(), approval.Origin{Source: "tui", ChannelID: "tui", UserID: "u1"})
}
