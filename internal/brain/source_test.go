package brain

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSource(t *testing.T) {
	tests := []struct {
		input      string
		wantPrefix string
		wantDetail string
	}{
		{"file:MEMORY.md#About Jeff", "file", "MEMORY.md#About Jeff"},
		{"skill:brain-seed-memory", "skill", "brain-seed-memory"},
		{"tool", "tool", ""},
		{"user:manual", "user", "manual"},
		{"llm:generated", "llm", "generated"},
		{"sub-agent:research", "sub-agent", "research"},
		{"", "", ""},
		{"unknown:something", "unknown", "something"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			prefix, detail := ParseSource(tt.input)
			assert.Equal(t, tt.wantPrefix, prefix)
			assert.Equal(t, tt.wantDetail, detail)
		})
	}
}

func TestValidateSource(t *testing.T) {
	// Known prefixes should pass
	for _, src := range []string{"file:test.md", "skill:foo", "tool", "user:manual", "llm:gen", "sub-agent:x"} {
		assert.NoError(t, ValidateSource(src), "source %q should be valid", src)
	}
	// Empty is valid (legacy)
	assert.NoError(t, ValidateSource(""))

	// Unknown prefix returns error
	assert.Error(t, ValidateSource("bogus:thing"))
}

func TestListWithSourcePrefix(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	// Store entries with different sources
	require.NoError(t, b.Store(ctx, "jeff.birthday", "Oct 5", TierLongTerm, "file:MEMORY.md"))
	require.NoError(t, b.Store(ctx, "jeff.location", "Portland", TierLongTerm, "file:USER.md"))
	require.NoError(t, b.Store(ctx, "jeff.role", "engineer", TierLongTerm, "skill:profile"))
	require.NoError(t, b.Store(ctx, "jeff.hobby", "coding", TierLongTerm, "tool"))

	// List all jeff. entries — should return 4
	all, err := b.List(ctx, "jeff.", "")
	require.NoError(t, err)
	assert.Len(t, all, 4)

	// List only file: sourced entries
	fileOnly, err := b.List(ctx, "jeff.", "file:")
	require.NoError(t, err)
	assert.Len(t, fileOnly, 2)
	for _, e := range fileOnly {
		assert.True(t, strings.HasPrefix(e.Source, "file:"), "expected file: source, got %q", e.Source)
	}

	// List only skill: sourced entries
	skillOnly, err := b.List(ctx, "jeff.", "skill:")
	require.NoError(t, err)
	assert.Len(t, skillOnly, 1)
	assert.Equal(t, "jeff.role", skillOnly[0].Key)

	// List with non-matching source prefix
	none, err := b.List(ctx, "jeff.", "llm:")
	require.NoError(t, err)
	assert.Len(t, none, 0)
}

func TestListWithSourcePrefixWorkingMemory(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	// Store WM entries with different sources
	require.NoError(t, b.Store(ctx, "wm.fact1", "val1", TierWorking, "file:test.md"))
	require.NoError(t, b.Store(ctx, "wm.fact2", "val2", TierWorking, "skill:foo"))

	// Without filter — both
	all, err := b.List(ctx, "wm.", "")
	require.NoError(t, err)
	assert.Len(t, all, 2)

	// With source filter
	fileOnly, err := b.List(ctx, "wm.", "file:")
	require.NoError(t, err)
	assert.Len(t, fileOnly, 1)
	assert.Equal(t, "wm.fact1", fileOnly[0].Key)
}
