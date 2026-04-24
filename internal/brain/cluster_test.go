package brain

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNamespacePrefixes(t *testing.T) {
	tests := []struct {
		name   string
		key    string
		minLen int
		want   []string
	}{
		{
			name:   "three segment key",
			key:    "learned.memory.spreading_activation",
			minLen: 4,
			want:   []string{"learned.memory", "learned"},
		},
		{
			name:   "two segment key",
			key:    "solar.battery",
			minLen: 4,
			want:   []string{"solar"},
		},
		{
			name:   "single segment key returns empty",
			key:    "solar",
			minLen: 4,
			want:   nil,
		},
		{
			name:   "minLen filters short prefixes",
			key:    "a.b.c",
			minLen: 4,
			want:   nil, // "a" is only 1 char, "a.b" is 3 chars — both < 4
		},
		{
			name:   "four segment key",
			key:    "learned.memory.architecture.spreading_activation",
			minLen: 4,
			want:   []string{"learned.memory.architecture", "learned.memory", "learned"},
		},
		{
			name:   "minLen zero includes all",
			key:    "a.b",
			minLen: 0,
			want:   []string{"a"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := namespacePrefixes(tt.key, tt.minLen)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestClusterNeighbours_SameParent(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	// Store entries under learned.memory.* namespace
	require.NoError(t, b.Store(ctx, "learned.memory.spreading_activation", "Collins & Loftus 1975 - activation spreads", TierLongTerm, "research"))
	require.NoError(t, b.Store(ctx, "learned.memory.bi_temporal", "tracks when event occurred AND when ingested", TierLongTerm, "research"))
	require.NoError(t, b.Store(ctx, "learned.memory.encoding_specificity", "recall succeeds when context matches", TierLongTerm, "research"))
	require.NoError(t, b.Store(ctx, "learned.memory.forgetting_curve", "Ebbinghaus exponential decay", TierLongTerm, "research"))
	// Unrelated entry
	require.NoError(t, b.Store(ctx, "jeff.birthday", "Oct 5", TierLongTerm, "user"))

	// Query should match "spreading_activation" directly.
	// Cluster should discover the other learned.memory.* entries.
	matchedKeys := map[string]bool{
		"learned.memory.spreading_activation": true,
	}

	results, err := b.clusterNeighbours(
		[]string{"learned.memory.spreading_activation"},
		matchedKeys,
		defaultClusterConfig,
	)
	require.NoError(t, err)

	// Should find bi_temporal, encoding_specificity, forgetting_curve
	assert.GreaterOrEqual(t, len(results), 3, "should find namespace siblings")

	foundKeys := make(map[string]bool)
	for _, e := range results {
		foundKeys[e.Key] = true
		t.Logf("  cluster hit: %s (salience=%.2f)", e.Key, e.Salience)
	}
	assert.True(t, foundKeys["learned.memory.bi_temporal"])
	assert.True(t, foundKeys["learned.memory.encoding_specificity"])
	assert.True(t, foundKeys["learned.memory.forgetting_curve"])
	assert.False(t, foundKeys["jeff.birthday"], "should not include unrelated entries")
}

func TestClusterNeighbours_BFSExpand(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	// Store a deep hierarchy
	require.NoError(t, b.Store(ctx, "solar.battery.config", "2x 14.6 kWh", TierLongTerm, "config"))
	require.NoError(t, b.Store(ctx, "solar.battery.plan", "charge from grid overnight", TierLongTerm, "config"))
	require.NoError(t, b.Store(ctx, "solar.inverter", "EG4 18kPV", TierLongTerm, "config"))
	require.NoError(t, b.Store(ctx, "solar.system_specs", "30 panels, 11.4 kW array", TierLongTerm, "config"))
	require.NoError(t, b.Store(ctx, "solar.net_metering", "SnoPUD", TierLongTerm, "config"))

	// Seed with solar.battery.config. At depth 0, it finds solar.battery.plan
	// (same parent "solar.battery"). At depth 1, it finds solar.inverter,
	// solar.system_specs, solar.net_metering (parent "solar").
	matchedKeys := map[string]bool{
		"solar.battery.config": true,
	}

	results, err := b.clusterNeighbours(
		[]string{"solar.battery.config"},
		matchedKeys,
		defaultClusterConfig,
	)
	require.NoError(t, err)

	foundKeys := make(map[string]bool)
	for _, e := range results {
		foundKeys[e.Key] = true
		t.Logf("  cluster hit: %s", e.Key)
	}

	// Same-parent entries should always be found
	assert.True(t, foundKeys["solar.battery.plan"], "should find same-parent sibling")

	// Broader namespace entries should also be found with BFS depth 2
	assert.True(t, foundKeys["solar.inverter"], "should find namespace cousin")
	assert.True(t, foundKeys["solar.system_specs"], "should find namespace cousin")
}

func TestClusterNeighbours_ShortPrefixFiltered(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	// Store entries under short prefix "a.b"
	require.NoError(t, b.Store(ctx, "a.b.c", "value1", TierLongTerm, "test"))
	require.NoError(t, b.Store(ctx, "a.b.d", "value2", TierLongTerm, "test"))

	// With default MinPrefixLength=4, prefix "a" (1 char) and "a.b" (3 chars)
	// are too short, so clustering should find nothing.
	matchedKeys := map[string]bool{"a.b.c": true}
	results, err := b.clusterNeighbours(
		[]string{"a.b.c"},
		matchedKeys,
		defaultClusterConfig,
	)
	require.NoError(t, err)
	assert.Empty(t, results, "short prefixes should be filtered out")
}

func TestRecallWithCluster(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	// Store entries in two namespaces
	require.NoError(t, b.Store(ctx, "learned.memory.spreading_activation", "Collins & Loftus 1975", TierLongTerm, "research"))
	require.NoError(t, b.Store(ctx, "learned.memory.bi_temporal", "Zep/Graphiti temporal model", TierLongTerm, "research"))
	require.NoError(t, b.Store(ctx, "learned.memory.encoding_specificity", "Tulving 1973 context-dependent recall", TierLongTerm, "research"))
	require.NoError(t, b.Store(ctx, "jeff.birthday", "Oct 5", TierLongTerm, "user"))

	// Recall "spreading" should find spreading_activation directly,
	// and cluster should expand to include bi_temporal and encoding_specificity.
	result, err := b.RecallWithCluster(ctx, "spreading", 10)
	require.NoError(t, err)

	// Direct match
	assert.GreaterOrEqual(t, len(result.Direct), 1, "should find direct match")
	t.Logf("Direct results: %d", len(result.Direct))
	for _, e := range result.Direct {
		t.Logf("  direct: %s", e.Key)
	}

	// Cluster expansion
	assert.GreaterOrEqual(t, len(result.Cluster), 1, "should find cluster neighbours")
	t.Logf("Cluster results: %d", len(result.Cluster))
	for _, e := range result.Cluster {
		t.Logf("  cluster: %s", e.Key)
	}

	// Verify cluster doesn't include direct matches
	directKeys := make(map[string]bool)
	for _, e := range result.Direct {
		directKeys[e.Key] = true
	}
	for _, e := range result.Cluster {
		assert.False(t, directKeys[e.Key], "cluster should not include direct matches")
	}
}

func TestRecallWithCluster_NoDirectMatches(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "jeff.birthday", "Oct 5", TierLongTerm, "user"))

	// Query for something that matches nothing
	result, err := b.RecallWithCluster(ctx, "nonexistent_xyzzy", 10)
	require.NoError(t, err)
	assert.Empty(t, result.Direct)
	assert.Empty(t, result.Cluster, "no seeds → no clustering")
}

func TestRecallWithCluster_WMEntriesExcluded(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	// Store LTM entries
	require.NoError(t, b.Store(ctx, "solar.battery.config", "2x 14.6 kWh", TierLongTerm, "config"))
	require.NoError(t, b.Store(ctx, "solar.inverter", "EG4 18kPV", TierLongTerm, "config"))

	// Store WM entry in same namespace
	require.NoError(t, b.Store(ctx, "solar.today.production", "4.2kWh", TierWorking, "observation"))

	result, err := b.RecallWithCluster(ctx, "battery config", 10)
	require.NoError(t, err)

	// WM entry should not appear in cluster results
	for _, e := range result.Cluster {
		assert.NotEqual(t, "solar.today.production", e.Key, "WM entry should not appear in cluster")
	}
}

func TestClusterNeighbours_RespectsMaxEntries(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	// Store many entries under same namespace
	for i := 0; i < 20; i++ {
		require.NoError(t, b.Store(ctx, fmt.Sprintf("solar.entry_%02d", i), "value", TierLongTerm, "test"))
	}

	cfg := clusterConfig{
		MaxDepth:          2,
		MaxClusterEntries: 5,
		MinPrefixLength:   4,
	}
	matchedKeys := map[string]bool{"solar.entry_00": true}
	results, err := b.clusterNeighbours([]string{"solar.entry_00"}, matchedKeys, cfg)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(results), 5, "should respect MaxClusterEntries")
}
