package core

import (
	"context"
	"testing"

	"conduit/internal/fts"
	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// panickingMessageSearch panics in SearchMessages, which Find runs in its
// own goroutine.
type panickingMessageSearch struct{ mockSearchService }

func (p *panickingMessageSearch) SearchMessages(context.Context, string, int) ([]fts.MessageResult, error) {
	panic("boom in message search")
}

// conduit-31jg.73: a panic in a Find search goroutine is recovered (a panic
// in a tool-spawned goroutine is not covered by Registry.ExecuteTool's
// recover and would crash the gateway) and reported as that backend's error
// while the other backends' results are still returned.
func TestFindTool_PanicInSearchGoroutineRecovered(t *testing.T) {
	s := &panickingMessageSearch{mockSearchService{
		documents: []fts.DocumentResult{{FilePath: "doc1.md", Content: "doc", Rank: -10}},
	}}
	tool := NewFindTool(&types.ToolServices{Searcher: s})

	result, err := tool.Execute(context.Background(), map[string]interface{}{"query": "test"})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Contains(t, result.Content, "doc1.md", "other backends' results must survive")
}

func TestRecoverPanic(t *testing.T) {
	var got error
	func() {
		defer types.RecoverPanic("unit", func(err error) { got = err })
		panic("kaboom")
	}()
	require.Error(t, got)
	assert.Contains(t, got.Error(), "unit panicked: kaboom")

	called := false
	func() {
		defer types.RecoverPanic("quiet", func(error) { called = true })
	}()
	assert.False(t, called, "no panic, no callback")
}
