package brain

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// conduit-31jg.30: two users' working memory is isolated.
func TestWMIsolatedPerUser(t *testing.T) {
	b := newTestBrain(t, WithSpreadingEnabled(false))
	alice, bob := testCtx("alice"), testCtx("bob")
	require.NoError(t, b.Store(alice, "pref.color", "blue", TierWorking, "user"))

	got, err := b.Get(bob, "pref.color")
	require.NoError(t, err)
	assert.Nil(t, got, "bob must not see alice's WM")

	res, err := b.Recall(bob, "color", 10)
	require.NoError(t, err)
	assert.Empty(t, res)

	list, err := b.List(bob, "pref.", "")
	require.NoError(t, err)
	assert.Empty(t, list)

	got, err = b.Get(alice, "pref.color")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "blue", got.Value)
}

// A sub-agent reads its parent's WM (read-only copies, no access bump) and
// its own writes stay in its own bucket.
func TestWMSubAgentReadsParent(t *testing.T) {
	b := newTestBrain(t, WithSpreadingEnabled(false))
	parent := testCtx("alice")
	require.NoError(t, b.Store(parent, "task.context", "migrating the solar dashboard", TierWorking, "user"))

	child := WithParentUserID(WithUserID(context.Background(), "subagent:s1"), "alice")

	got, err := b.Get(child, "task.context")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "migrating the solar dashboard", got.Value)

	res, err := b.Recall(child, "solar dashboard", 10)
	require.NoError(t, err)
	require.NotEmpty(t, res)
	assert.Equal(t, "task.context", res[0].Key)

	list, err := b.List(child, "task.", "")
	require.NoError(t, err)
	require.Len(t, list, 1)

	// No access bump on the parent's entry from child reads.
	for _, e := range b.WorkingMemoryEntries(parent) {
		if e.Key == "task.context" {
			assert.Equal(t, 1, e.AccessCount)
		}
	}

	// Child writes go to the child's own bucket, not the parent's.
	require.NoError(t, b.Store(child, "task.result", "done", TierWorking, "sub-agent"))
	pg, err := b.Get(parent, "task.result")
	require.NoError(t, err)
	assert.Nil(t, pg, "sub-agent writes must not land in parent WM")

	// A second, unrelated sub-agent sees neither.
	other := WithUserID(context.Background(), "subagent:s2")
	og, err := b.Get(other, "task.context")
	require.NoError(t, err)
	assert.Nil(t, og)
}

// System writers without a user (heartbeat alerts) land in the shared bucket,
// which every user can read (read-only) — preserves sense.alerts.* visibility.
func TestWMSharedBucketReadableByUsers(t *testing.T) {
	b := newTestBrain(t, WithSpreadingEnabled(false))
	require.NoError(t, b.Store(context.Background(), "sense.alerts.cpu", "cpu high", TierWorking, "system:heartbeat"))

	alice := testCtx("alice")
	list, err := b.List(alice, "sense.alerts.", "")
	require.NoError(t, err)
	require.Len(t, list, 1)

	got, err := b.Get(alice, "sense.alerts.cpu")
	require.NoError(t, err)
	require.NotNil(t, got)

	res, err := b.Recall(alice, "cpu", 10)
	require.NoError(t, err)
	require.NotEmpty(t, res)

	// Alice's writes don't leak into the shared bucket.
	require.NoError(t, b.Store(alice, "sense.alerts.private", "x", TierWorking, "user"))
	shared, err := b.List(context.Background(), "sense.alerts.", "")
	require.NoError(t, err)
	assert.Len(t, shared, 1)
}

func TestWorkingMemoryUserIDs(t *testing.T) {
	b := newTestBrain(t)
	require.NoError(t, b.Store(testCtx("a"), "k", "v", TierWorking, ""))
	require.NoError(t, b.Store(testCtx("b"), "k", "v", TierWorking, ""))
	assert.ElementsMatch(t, []string{"a", "b"}, b.WorkingMemoryUserIDs())
}
