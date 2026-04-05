package brain

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestBrain(t *testing.T, opts ...Option) *Brain {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "brain.db")
	defaults := []Option{WithAutoFlushInterval(0)} // disable auto-flush in tests
	opts = append(defaults, opts...)
	b, err := New(dbPath, opts...)
	require.NoError(t, err)
	t.Cleanup(func() { b.Close() })
	return b
}

func testCtx(userID string) context.Context {
	return WithUserID(context.Background(), userID)
}

// Silence unused import warnings for packages used only in specific tests.
var (
	_ = fmt.Sprintf
	_ = time.Now
	_ = sync.WaitGroup{}
)

func TestStoreGetWorkingMemory(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	err := b.Store(ctx, "solar.panel_count", "30", TierWorking, "test")
	require.NoError(t, err)

	entry, err := b.Get(ctx, "solar.panel_count")
	require.NoError(t, err)
	require.NotNil(t, entry)
	assert.Equal(t, "30", entry.Value)
	assert.Equal(t, TierWorking, entry.Tier)
	assert.Equal(t, "test", entry.Source)
	assert.GreaterOrEqual(t, entry.AccessCount, 1)
}

func TestStoreGetLTM(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	err := b.Store(ctx, "jeff.birthday", "Oct 5", TierLongTerm, "user")
	require.NoError(t, err)

	entry, err := b.Get(ctx, "jeff.birthday")
	require.NoError(t, err)
	require.NotNil(t, entry)
	assert.Equal(t, "Oct 5", entry.Value)
	assert.Equal(t, TierLongTerm, entry.Tier)
}

func TestGetWorkingMemoryFirst(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	// Store in both tiers with different values.
	require.NoError(t, b.Store(ctx, "key1", "ltm-value", TierLongTerm, ""))
	require.NoError(t, b.Store(ctx, "key1", "wm-value", TierWorking, ""))

	// Get should return working memory (hot cache) first.
	entry, err := b.Get(ctx, "key1")
	require.NoError(t, err)
	require.NotNil(t, entry)
	assert.Equal(t, "wm-value", entry.Value)
	assert.Equal(t, TierWorking, entry.Tier)
}

func TestRecallByKey(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "solar.today", "4.2kWh", TierWorking, ""))
	require.NoError(t, b.Store(ctx, "solar.panel_count", "30", TierLongTerm, ""))
	require.NoError(t, b.Store(ctx, "pets.theo", "golden retriever", TierWorking, ""))

	results, err := b.Recall(ctx, "solar", 10)
	require.NoError(t, err)
	assert.Len(t, results, 2)
	for _, r := range results {
		assert.Contains(t, r.Key, "solar")
	}
}

func TestRecallByValue(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "pet.breed", "golden retriever", TierWorking, ""))
	require.NoError(t, b.Store(ctx, "pet.name", "Theo", TierLongTerm, ""))

	results, err := b.Recall(ctx, "golden", 10)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(results), 1)
	assert.Equal(t, "golden retriever", results[0].Value)
}

func TestList(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "solar.today", "4.2kWh", TierWorking, ""))
	require.NoError(t, b.Store(ctx, "solar.panel_count", "30", TierLongTerm, ""))
	require.NoError(t, b.Store(ctx, "pets.theo", "golden", TierWorking, ""))

	results, err := b.List(ctx, "solar.", "")
	require.NoError(t, err)
	assert.Len(t, results, 2)
}

func TestDelete(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "temp.key", "value", TierWorking, ""))
	require.NoError(t, b.Store(ctx, "temp.key2", "value", TierLongTerm, ""))

	require.NoError(t, b.Delete(ctx, "temp.key"))
	require.NoError(t, b.Delete(ctx, "temp.key2"))

	entry, err := b.Get(ctx, "temp.key")
	require.NoError(t, err)
	assert.Nil(t, entry)

	entry, err = b.Get(ctx, "temp.key2")
	require.NoError(t, err)
	assert.Nil(t, entry)
}

func TestScratchpadLIFO(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Push(ctx, "user1", "A"))
	require.NoError(t, b.Push(ctx, "user1", "B"))
	require.NoError(t, b.Push(ctx, "user1", "C"))

	// Peek returns top without removing.
	val, err := b.Peek(ctx, "user1")
	require.NoError(t, err)
	assert.Equal(t, "C", val)

	// Pop returns LIFO order.
	val, err = b.Pop(ctx, "user1")
	require.NoError(t, err)
	assert.Equal(t, "C", val)

	val, err = b.Pop(ctx, "user1")
	require.NoError(t, err)
	assert.Equal(t, "B", val)

	val, err = b.Pop(ctx, "user1")
	require.NoError(t, err)
	assert.Equal(t, "A", val)

	// Pop on empty returns error.
	_, err = b.Pop(ctx, "user1")
	assert.Error(t, err)
}

func TestPromote(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "solar.panel_count", "30", TierWorking, "file"))

	// Promote from working to longterm.
	require.NoError(t, b.Promote(ctx, "solar.panel_count"))

	// Should now be in LTM, not WM.
	// Store a different value in WM to check Get returns LTM.
	entry, err := b.Get(ctx, "solar.panel_count")
	require.NoError(t, err)
	require.NotNil(t, entry)
	assert.Equal(t, TierLongTerm, entry.Tier)
	assert.Equal(t, "30", entry.Value)
}

func TestConsolidate(t *testing.T) {
	b := newTestBrain(t, WithConsolidateThreshold(0.5), WithEvictThreshold(0.3))
	ctx := testCtx("user1")

	// Store a key and access it many times to raise salience.
	require.NoError(t, b.Store(ctx, "hot.key", "important", TierWorking, ""))
	for i := 0; i < 50; i++ {
		b.Get(ctx, "hot.key") // bump access count
	}

	// Store a key that won't be accessed (low salience will develop over time).
	require.NoError(t, b.Store(ctx, "cold.key", "unimportant", TierWorking, ""))

	report, err := b.Consolidate(ctx, true)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, report.PromotedCount, 1)
	assert.Contains(t, report.PromotedKeys, "hot.key")
}

func TestLTMPersistence(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "brain.db")
	ctx := testCtx("user1")

	// Create brain, store, close.
	b1, err := New(dbPath, WithAutoFlushInterval(0))
	require.NoError(t, err)
	require.NoError(t, b1.Store(ctx, "persist.key", "persist.value", TierLongTerm, "test"))
	require.NoError(t, b1.Close())

	// Reopen and verify.
	b2, err := New(dbPath, WithAutoFlushInterval(0))
	require.NoError(t, err)
	defer b2.Close()

	entry, err := b2.Get(ctx, "persist.key")
	require.NoError(t, err)
	require.NotNil(t, entry)
	assert.Equal(t, "persist.value", entry.Value)
}

func TestConcurrentAccess(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("concurrent.key.%d", i)
			_ = b.Store(ctx, key, "value", TierWorking, "")
			_, _ = b.Get(ctx, key)
			_, _ = b.Recall(ctx, "concurrent", 5)
			_, _ = b.List(ctx, "concurrent.", "")
		}(i)
	}
	wg.Wait()

	// Verify no corruption.
	status, err := b.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, 10, status.WMEntries)
}

func TestMaxLTMEntries(t *testing.T) {
	b := newTestBrain(t, WithMaxLTMEntries(5))
	ctx := testCtx("user1")

	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("entry.%d", i)
		require.NoError(t, b.Store(ctx, key, "value", TierLongTerm, ""))
	}

	status, err := b.Status(ctx)
	require.NoError(t, err)
	assert.LessOrEqual(t, status.LTMEntries, 5)
}

func TestUserIsolation(t *testing.T) {
	b := newTestBrain(t)
	ctx1 := testCtx("alice")
	ctx2 := testCtx("bob")

	require.NoError(t, b.Store(ctx1, "user.name", "Alice", TierWorking, ""))
	require.NoError(t, b.Store(ctx2, "user.name", "Bob", TierWorking, ""))

	entry1, err := b.Get(ctx1, "user.name")
	require.NoError(t, err)
	require.NotNil(t, entry1)
	assert.Equal(t, "Alice", entry1.Value)

	entry2, err := b.Get(ctx2, "user.name")
	require.NoError(t, err)
	require.NotNil(t, entry2)
	assert.Equal(t, "Bob", entry2.Value)
}

func TestStatus(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "wm.key1", "v1", TierWorking, ""))
	require.NoError(t, b.Store(ctx, "wm.key2", "v2", TierWorking, ""))
	require.NoError(t, b.Store(ctx, "ltm.key1", "v3", TierLongTerm, ""))
	require.NoError(t, b.Push(ctx, "user1", "scratch1"))

	status, err := b.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, status.WMEntries)
	assert.Equal(t, 1, status.LTMEntries)
	assert.Equal(t, 1, status.ScratchDepth)
}

func TestGetNonexistent(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	entry, err := b.Get(ctx, "does.not.exist")
	require.NoError(t, err)
	assert.Nil(t, entry)
}

func TestPromoteNonexistent(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	err := b.Promote(ctx, "does.not.exist")
	assert.Error(t, err)
}

func TestConfigurableSalienceWeights(t *testing.T) {
	// Access-heavy weighting
	b := newTestBrain(t, WithAccessWeight(0.8), WithRecencyWeight(0.1), WithTierWeight(0.1))

	ctx := testCtx("user")
	for i := 0; i < 50; i++ {
		b.Store(ctx, "hot.key", "value", TierWorking, "test")
	}

	entry, err := b.Get(ctx, "hot.key")
	require.NoError(t, err)
	require.NotNil(t, entry)
	assert.Greater(t, entry.Salience, 0.3)
}

func TestRecencyDecayRate(t *testing.T) {
	b := newTestBrain(t, WithRecencyDecayRate(2.0))

	ctx := testCtx("user")
	b.Store(ctx, "test.key", "value", TierWorking, "test")

	entry, err := b.Get(ctx, "test.key")
	require.NoError(t, err)
	require.NotNil(t, entry)
	assert.Greater(t, entry.Salience, 0.2)
}

func TestAccessCountCap(t *testing.T) {
	b := newTestBrain(t, WithAccessCountCap(10))

	ctx := testCtx("user")
	for i := 0; i < 20; i++ {
		b.Store(ctx, "capped", "v", TierWorking, "test")
	}

	entry, err := b.Get(ctx, "capped")
	require.NoError(t, err)
	require.NotNil(t, entry)
	assert.LessOrEqual(t, entry.Salience, 1.0)
}

func TestLTMSalienceWithConfigurableWeights(t *testing.T) {
	b := newTestBrain(t, WithAccessWeight(0.6), WithRecencyWeight(0.3), WithTierWeight(0.1))

	ctx := testCtx("user")
	b.Store(ctx, "ltm.key", "persistent", TierLongTerm, "test")

	entry, err := b.Get(ctx, "ltm.key")
	require.NoError(t, err)
	require.NotNil(t, entry)
	assert.Greater(t, entry.Salience, 0.0)
}

func TestSubAgentWMSharing(t *testing.T) {
	b := newTestBrain(t)

	parentCtx := testCtx("parent-user")
	childCtx := WithUserID(context.Background(), "child-user")
	childCtx = WithParentUserID(childCtx, "parent-user")

	// Parent stores a fact
	require.NoError(t, b.Store(parentCtx, "project.name", "Conduit", TierWorking, "test"))

	// Child can read parent's WM
	entry, err := b.Get(childCtx, "project.name")
	require.NoError(t, err)
	require.NotNil(t, entry)
	assert.Equal(t, "Conduit", entry.Value)

	// Child's own WM takes priority
	require.NoError(t, b.Store(childCtx, "project.name", "ChildOverride", TierWorking, "test"))
	entry, err = b.Get(childCtx, "project.name")
	require.NoError(t, err)
	assert.Equal(t, "ChildOverride", entry.Value)

	// Parent's value unchanged
	entry, err = b.Get(parentCtx, "project.name")
	require.NoError(t, err)
	assert.Equal(t, "Conduit", entry.Value)
}

func TestSubAgentWMRecall(t *testing.T) {
	b := newTestBrain(t)

	parentCtx := testCtx("parent")
	childCtx := WithUserID(context.Background(), "child")
	childCtx = WithParentUserID(childCtx, "parent")

	require.NoError(t, b.Store(parentCtx, "solar.production", "5000W", TierWorking, "test"))
	require.NoError(t, b.Store(parentCtx, "solar.panels", "30", TierWorking, "test"))

	results, err := b.Recall(childCtx, "solar", 10)
	require.NoError(t, err)
	assert.Len(t, results, 2)
}

func TestSubAgentWMWriteIsolation(t *testing.T) {
	b := newTestBrain(t)

	parentCtx := testCtx("parent")
	childCtx := WithUserID(context.Background(), "child")
	childCtx = WithParentUserID(childCtx, "parent")

	// Child writes don't affect parent
	require.NoError(t, b.Store(childCtx, "child.secret", "hidden", TierWorking, "test"))

	entry, err := b.Get(parentCtx, "child.secret")
	require.NoError(t, err)
	assert.Nil(t, entry) // Parent should not see child's WM
}

func TestSubAgentWMList(t *testing.T) {
	b := newTestBrain(t)

	parentCtx := testCtx("parent")
	childCtx := WithUserID(context.Background(), "child")
	childCtx = WithParentUserID(childCtx, "parent")

	require.NoError(t, b.Store(parentCtx, "env.temp", "72F", TierWorking, "test"))
	require.NoError(t, b.Store(parentCtx, "env.humidity", "45%", TierWorking, "test"))
	require.NoError(t, b.Store(childCtx, "env.temp", "override", TierWorking, "test")) // Override one

	results, err := b.List(childCtx, "env.", "")
	require.NoError(t, err)
	assert.Len(t, results, 2) // child's env.temp + parent's env.humidity (deduped)

	// Verify child's override takes priority
	for _, r := range results {
		if r.Key == "env.temp" {
			assert.Equal(t, "override", r.Value)
		}
	}
}

func TestSubAgentWMGetReturnsCopy(t *testing.T) {
	b := newTestBrain(t)

	parentCtx := testCtx("parent")
	childCtx := WithUserID(context.Background(), "child")
	childCtx = WithParentUserID(childCtx, "parent")

	require.NoError(t, b.Store(parentCtx, "shared.key", "original", TierWorking, "test"))

	// Get from child returns a copy
	entry, err := b.Get(childCtx, "shared.key")
	require.NoError(t, err)
	require.NotNil(t, entry)

	// Mutating the returned copy must not affect parent's entry
	entry.Value = "mutated"

	parentEntry, err := b.Get(parentCtx, "shared.key")
	require.NoError(t, err)
	assert.Equal(t, "original", parentEntry.Value)
}

func TestLTMUpsertDoesNotViolateConstraint(t *testing.T) {
	b := newTestBrain(t)
	ctx := WithUserID(context.Background(), "user1")

	// Store a key in LTM
	err := b.Store(ctx, "jeff.birthday", "January 1", TierLongTerm, "test")
	require.NoError(t, err)

	// Upsert same key with new value — previously failed with NOT NULL on salience
	err = b.Store(ctx, "jeff.birthday", "January 2", TierLongTerm, "test")
	require.NoError(t, err)

	// Verify value was updated
	entry, err := b.Get(ctx, "jeff.birthday")
	require.NoError(t, err)
	require.NotNil(t, entry)
	assert.Equal(t, "January 2", entry.Value)
	assert.Equal(t, 3, entry.AccessCount) // 1 insert + 1 upsert + 1 get
	assert.Greater(t, entry.Salience, 0.0)
}

func TestRecallMatchesValueContent(t *testing.T) {
	b := newTestBrain(t)
	ctx := WithUserID(context.Background(), "user1")

	// Store with key that doesn't contain the full query
	err := b.Store(ctx, "travel.paris", "June 21-July 3, 2026, with Cam. Delta nonstop SEA-CDG.", TierLongTerm, "test")
	require.NoError(t, err)

	// "paris trip" — "paris" is in key, "trip" is not in key or value,
	// but "paris" alone should match via the key
	results, err := b.Recall(ctx, "paris", 10)
	require.NoError(t, err)
	assert.Len(t, results, 1, "should find entry by key substring")

	// Search by value content
	results, err = b.Recall(ctx, "Delta nonstop", 10)
	require.NoError(t, err)
	assert.Len(t, results, 1, "should find entry by value content")

	// Multi-term: "paris june" — paris in key, june in value
	results, err = b.Recall(ctx, "paris june", 10)
	require.NoError(t, err)
	assert.Len(t, results, 1, "should find entry when terms span key and value")
}

func TestRecallMultiTermWM(t *testing.T) {
	b := newTestBrain(t)
	ctx := WithUserID(context.Background(), "user1")

	err := b.Store(ctx, "project.alpha", "deadline is friday, team lead is Bob", TierWorking, "test")
	require.NoError(t, err)

	// Both terms present across key+value — full match
	results, err := b.Recall(ctx, "alpha friday", 10)
	require.NoError(t, err)
	assert.Len(t, results, 1)

	// One term present (OR logic) — partial match still returns result
	results, err = b.Recall(ctx, "alpha saturday", 10)
	require.NoError(t, err)
	assert.Len(t, results, 1, "OR logic: 'alpha' matches even though 'saturday' does not")

	// No terms present at all
	results, err = b.Recall(ctx, "zebra saturday", 10)
	require.NoError(t, err)
	assert.Len(t, results, 0, "should not match when no terms are found")
}

func TestRecallNaturalLanguageQuery(t *testing.T) {
	b := newTestBrain(t)
	ctx := WithUserID(context.Background(), "user1")

	err := b.Store(ctx, "food.bourbon", "Maker's Mark, neat. Jeff's go-to.", TierLongTerm, "test")
	require.NoError(t, err)

	// Natural language query with stopwords
	results, err := b.Recall(ctx, "what bourbon does Jeff drink", 10)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(results), 1, "stopwords stripped, 'bourbon' and 'jeff' should match")
	assert.Equal(t, "food.bourbon", results[0].Key)
}

func TestRecallDelimiterSplitting(t *testing.T) {
	b := newTestBrain(t)
	ctx := WithUserID(context.Background(), "user1")

	err := b.Store(ctx, "work.deck.prd", "Uses Helm/Kustomize for K8s deployments", TierLongTerm, "test")
	require.NoError(t, err)

	// Query individual terms that appear as compound token in value
	results, err := b.Recall(ctx, "Helm Kustomize", 10)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(results), 1, "delimiter splitting should match Helm/Kustomize")
	assert.Equal(t, "work.deck.prd", results[0].Key)
}

func TestRecallORRanking(t *testing.T) {
	b := newTestBrain(t)
	ctx := WithUserID(context.Background(), "user1")

	// Entry matching 2 of 3 terms
	err := b.Store(ctx, "food.bourbon", "Maker's Mark bourbon, Jeff's favorite", TierLongTerm, "test")
	require.NoError(t, err)
	// Entry matching 1 of 3 terms
	err = b.Store(ctx, "food.wine", "Jeff prefers red", TierLongTerm, "test")
	require.NoError(t, err)

	// After stopword stripping: ["bourbon", "jeff", "drink"]
	results, err := b.Recall(ctx, "what bourbon does Jeff drink", 10)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(results), 2)
	// The entry with more matching terms should rank first
	assert.Equal(t, "food.bourbon", results[0].Key, "2-term match should rank above 1-term match")
}

func TestRecallAllStopwordsQuery(t *testing.T) {
	b := newTestBrain(t)
	ctx := WithUserID(context.Background(), "user1")

	err := b.Store(ctx, "meta.note", "what is it about this thing", TierLongTerm, "test")
	require.NoError(t, err)

	// All stopwords — falls back to original tokens
	results, err := b.Recall(ctx, "what is it", 10)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(results), 1, "all-stopwords fallback should still search")
}
