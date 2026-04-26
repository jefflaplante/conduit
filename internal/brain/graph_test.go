package brain

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedGraph stores three LTM entries and two edges, plus one orphan edge whose
// endpoints aren't in brain_ltm. Used by ListGraph tests below.
func seedGraph(t *testing.T, b *Brain) {
	t.Helper()
	ctx := testCtx("user1")
	require.NoError(t, b.Store(ctx, "solar.battery.config", strings.Repeat("x", 500), TierLongTerm, "file:solar.md"))
	require.NoError(t, b.Store(ctx, "solar.battery.plan", "plan body", TierLongTerm, "file:solar.md"))
	require.NoError(t, b.Store(ctx, "house.lighting", "kitchen + hallway", TierLongTerm, "file:house.md"))
	// Promote so they land in LTM (Store with TierLongTerm should be enough,
	// but Promote is a no-op when already there).
	_, err := b.db.Exec(`INSERT INTO brain_relationships (key_a, key_b, relationship, confidence) VALUES (?,?,?,?)`,
		"solar.battery.config", "solar.battery.plan", "namespace", 0.9)
	require.NoError(t, err)
	_, err = b.db.Exec(`INSERT INTO brain_relationships (key_a, key_b, relationship, confidence) VALUES (?,?,?,?)`,
		"solar.battery.config", "house.lighting", "related", 0.4)
	require.NoError(t, err)
	// Orphan edge: references a key that isn't in brain_ltm.
	_, err = b.db.Exec(`INSERT INTO brain_relationships (key_a, key_b, relationship, confidence) VALUES (?,?,?,?)`,
		"solar.battery.config", "ghost.key", "related", 0.7)
	require.NoError(t, err)
}

func TestListGraph_BasicShape(t *testing.T) {
	b := newTestBrain(t)
	seedGraph(t, b)

	g, err := b.ListGraph(testCtx("user1"), GraphOptions{})
	require.NoError(t, err)
	require.NotNil(t, g)
	assert.Len(t, g.Nodes, 3, "expected 3 LTM nodes")
	// Two valid edges; orphan edge to ghost.key must be stripped.
	assert.Len(t, g.Edges, 2, "orphan edges must be stripped")
	for _, e := range g.Edges {
		assert.NotEqual(t, "ghost.key", e.KeyA)
		assert.NotEqual(t, "ghost.key", e.KeyB)
	}
}

func TestListGraph_ValueTruncation(t *testing.T) {
	b := newTestBrain(t)
	seedGraph(t, b)

	g, err := b.ListGraph(testCtx("user1"), GraphOptions{ValueTruncate: 50})
	require.NoError(t, err)

	var bigNode *GraphNode
	for _, n := range g.Nodes {
		if n.Key == "solar.battery.config" {
			bigNode = n
			break
		}
	}
	require.NotNil(t, bigNode)
	assert.Equal(t, 50, len(bigNode.Value))
	assert.True(t, bigNode.Truncated)
}

func TestListGraph_ConfidenceFilter(t *testing.T) {
	b := newTestBrain(t)
	seedGraph(t, b)

	// Only the namespace edge (confidence 0.9) should survive a 0.8 threshold.
	g, err := b.ListGraph(testCtx("user1"), GraphOptions{MinConfidence: 0.8})
	require.NoError(t, err)
	assert.Len(t, g.Edges, 1)
	assert.Equal(t, "namespace", g.Edges[0].Relationship)
}

func TestListGraph_SourcePrefixFilter(t *testing.T) {
	b := newTestBrain(t)
	seedGraph(t, b)

	g, err := b.ListGraph(testCtx("user1"), GraphOptions{SourcePrefix: "file:solar"})
	require.NoError(t, err)
	assert.Len(t, g.Nodes, 2)
	// Edges spanning solar→house must be stripped because house.lighting is not
	// in the node set under this filter.
	for _, e := range g.Edges {
		assert.NotEqual(t, "house.lighting", e.KeyA)
		assert.NotEqual(t, "house.lighting", e.KeyB)
	}
}

func TestListGraph_NodeLimit(t *testing.T) {
	b := newTestBrain(t)
	seedGraph(t, b)

	g, err := b.ListGraph(testCtx("user1"), GraphOptions{NodeLimit: 1})
	require.NoError(t, err)
	assert.Len(t, g.Nodes, 1)
	// Edge endpoints must both be in the truncated node set.
	for _, e := range g.Edges {
		assert.Equal(t, g.Nodes[0].Key, e.KeyA)
	}
}

func TestListGraph_Empty(t *testing.T) {
	b := newTestBrain(t)
	g, err := b.ListGraph(testCtx("user1"), GraphOptions{})
	require.NoError(t, err)
	assert.Empty(t, g.Nodes)
	assert.Empty(t, g.Edges)
}
