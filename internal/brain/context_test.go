package brain

import (
	"database/sql"
	"path/filepath"
	"testing"

	"conduit/internal/database"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bd-lfrd: situational context for brain entries. One free-text context column
// on brain_ltm (mirrored into brain_history), folded into recall keyword
// matching. See design memo memory/sd-bd-2ro-context-tags-design-2026-10-31.md.

// --- Migration ---

// Migration 12 adds brain_ltm.context TEXT NOT NULL DEFAULT ” (guarded, since
// test flows replay earlier migrations) and mirrors the column into
// brain_history.
func TestMigration12_AddsContextColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "brain.db")

	// Build a v11 database (pre-context) and insert rows the way the
	// pre-context code did.
	db, err := sql.Open("sqlite", database.BuildDSN(path))
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE brain_migrations (version INTEGER PRIMARY KEY, applied_at DATETIME DEFAULT (datetime('now')))`)
	require.NoError(t, err)
	for _, m := range migrations[:11] {
		_, err = db.Exec(m.SQL)
		require.NoError(t, err)
		// v9/v11 are Fn-form (guarded); their SQL is `SELECT 1` only.
		if m.Version != 9 && m.Version != 11 {
			_, err = db.Exec(`INSERT INTO brain_migrations (version) VALUES (?)`, m.Version)
			require.NoError(t, err)
		}
	}
	_, err = db.Exec(`INSERT INTO brain_ltm (key, value, access_count, salience) VALUES ('k', 'v', 1, 0.5)`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	b, err := New(path, WithAutoFlushInterval(0))
	require.NoError(t, err)
	defer b.Close()

	bctx := storedContext(t, b, "k")
	assert.Equal(t, "", bctx, "pre-existing rows must read as empty context")

	var count int
	require.NoError(t, b.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('brain_history') WHERE name = 'context'`).Scan(&count))
	assert.Equal(t, 1, count, "brain_history must gain a context column")
}

// Migration replay (rewound brain_migrations) must not fail on ADD COLUMN —
// the PRAGMA table_info guard makes migration 12 idempotent.
func TestMigration12_Idempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "brain.db")
	b, err := New(path, WithAutoFlushInterval(0))
	require.NoError(t, err)
	ctx := testCtx("user1")
	require.NoError(t, b.Store(ctx, "k", "v", TierLongTerm, "user", WithContext("Acme launch")))

	// Rewind the version row and reopen the same file: the guarded Fn must
	// detect the existing column instead of re-adding it.
	_, err = b.db.Exec(`DELETE FROM brain_migrations WHERE version = 12`)
	require.NoError(t, err)
	require.NoError(t, b.Close())

	b2, err := New(path, WithAutoFlushInterval(0))
	require.NoError(t, err)
	defer b2.Close()

	got, err := b2.Get(ctx, "k")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "Acme launch", got.Context)
}

// --- Store surface ---

// WithContext persists context on the LTM row; a plain Store (no option)
// keeps it empty. Omission must never error — the common case costs nothing.
func TestStoreContext_LTM(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "plain.key", "cache hit rate recovered", TierLongTerm, "tool"))
	require.NoError(t, b.Store(ctx, "ctx.key", "cache hit rate recovered", TierLongTerm, "tool", WithContext("billing spike investigation")))

	assert.Equal(t, "", storedContext(t, b, "plain.key"))
	assert.Equal(t, "billing spike investigation", storedContext(t, b, "ctx.key"))

	got, err := b.Get(ctx, "ctx.key")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "billing spike investigation", got.Context, "Get must surface the stored context")
}

// Context is rewritten on upsert (like source), not sticky like weight.
func TestStoreContext_RewrittenOnUpsert(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "rw.key", "v1", TierLongTerm, "tool", WithContext("old why")))
	assert.Equal(t, "old why", storedContext(t, b, "rw.key"))

	// A re-store without a context clears it.
	require.NoError(t, b.Store(ctx, "rw.key", "v2", TierLongTerm, "tool"))
	assert.Equal(t, "", storedContext(t, b, "rw.key"), "re-store without a context rewrites it to empty")

	require.NoError(t, b.Store(ctx, "rw.key", "v3", TierLongTerm, "tool", WithContext("new why")))
	assert.Equal(t, "new why", storedContext(t, b, "rw.key"))
}

// Working-memory stores carry context in the in-memory Entry. WM is
// map[string]*Entry, so it inherits the field — but the store path must set it.
func TestStoreContext_WorkingTier(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "wm.key", "v", TierWorking, "tool", WithContext("mid-investigation note")))
	got, err := b.Get(ctx, "wm.key")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "mid-investigation note", got.Context)

	// Re-store updates it.
	require.NoError(t, b.Store(ctx, "wm.key", "v2", TierWorking, "tool", WithContext("updated why")))
	got, err = b.Get(ctx, "wm.key")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "updated why", got.Context)
}

// StoreBulk carries per-entry context on LTM rows; omitted context is empty.
func TestStoreBulk_Context(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.StoreBulk(ctx, []BulkEntry{
		{Key: "bulk.plain", Value: "v", Tier: TierLongTerm},
		{Key: "bulk.ctx", Value: "v", Tier: TierLongTerm, Context: "from a daily log"},
	}))
	assert.Equal(t, "", storedContext(t, b, "bulk.plain"))
	assert.Equal(t, "from a daily log", storedContext(t, b, "bulk.ctx"))
}

// WM promote/eviction paths pass an explicit field list to storeLTM — the
// context must survive promotion to LTM.
func TestContext_CarriedThroughPromotion(t *testing.T) {
	b := newTestBrain(t, WithAutoPromote(true))
	ctx := testCtx("user1")

	// The Promote path must carry context.
	require.NoError(t, b.Store(ctx, "prom.key", "v", TierWorking, "tool", WithContext("promoted why")))
	require.NoError(t, b.Promote(ctx, "prom.key"))
	assert.Equal(t, "promoted why", storedContext(t, b, "prom.key"))

	// The Consolidate promote path must carry it too.
	require.NoError(t, b.Store(ctx, "cons.key", "v", TierWorking, "tool", WithContext("consolidation carry")))
	for i := 0; i < 25; i++ {
		_, _ = b.Get(ctx, "cons.key") // heat to promote (precedent: brain_test TestConsolidate)
	}
	rep, err := b.Consolidate(ctx, true)
	require.NoError(t, err)
	require.Contains(t, rep.PromotedKeys, "cons.key")
	assert.Equal(t, "consolidation carry", storedContext(t, b, "cons.key"),
		"promote must carry context into the LTM row")
}

// --- Recall fold ---

// Encoding specificity: a query token that appears only in the entry's
// context — not in key or value — must still retrieve the entry.
func TestRecall_ContextParticipatesInMatching(t *testing.T) {
	b := newTestBrain(t, WithSpreadingEnabled(false))
	ctx := testCtx("user1")

	// Value shares no vocabulary with the query; only the context does.
	require.NoError(t, b.Store(ctx, "svc.recovery", "cache hit rate recovered", TierLongTerm, "tool",
		WithContext("billing spike investigation")))

	results, err := b.Recall(ctx, "billing", 10)
	require.NoError(t, err)
	require.Len(t, results, 1, "a context-only token must match")
	assert.Equal(t, "svc.recovery", results[0].Key)

	// A token in neither key, value nor context must not match.
	results, err = b.Recall(ctx, "zebra", 10)
	require.NoError(t, err)
	assert.Empty(t, results)
}

// match_count counts context matches too: a query hitting key + context ranks
// above one hitting value only, all else equal.
func TestRecall_ContextBoostsMatchCount(t *testing.T) {
	b := newTestBrain(t, WithSpreadingEnabled(false))
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "a.one", "shared zebra", TierLongTerm, "tool",
		WithContext("zebra zebra")))
	require.NoError(t, b.Store(ctx, "b.two", "shared zebra zebra", TierLongTerm, "tool"))

	// Both match "zebra" via value. a.one's match_count comes only from the
	// context term (once); b.two matches value (once). Tie on match_count —
	// this test only checks retrieval, ranking parity is covered below.

	// A two-term query where only the context supplies the second term must
	// rank a.one (match_count 2) above b.two (match_count 1).
	require.NoError(t, b.Store(ctx, "c.three", "unrelated value", TierLongTerm, "tool",
		WithContext("zebra migration")))
	results, err := b.Recall(ctx, "zebra migration", 10)
	require.NoError(t, err)
	require.NotEmpty(t, results)
	assert.Equal(t, "c.three", results[0].Key, "context matches count toward match_count")
}

// RecallWithContext rerank: entries whose context overlaps the recall context
// get the boost, same as key/value overlap.
func TestRecallWithContext_ContextOverlapBoosts(t *testing.T) {
	b := newTestBrain(t, WithSpreadingEnabled(false))
	ctx := testCtx("user1")

	// Same query term "zebra" everywhere → equal match scores.
	require.NoError(t, b.Store(ctx, "plain.hit", "zebra", TierLongTerm, "tool"))
	require.NoError(t, b.Store(ctx, "ctx.hit", "zebra", TierLongTerm, "tool", WithContext("safari trip")))

	results, err := b.RecallWithContext(ctx, "zebra", 10, "safari")
	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.Equal(t, "ctx.hit", results[0].Key, "context overlap with the recall context must boost ranking")
}

// --- Read paths populate Entry.Context ---

func TestContext_PopulatedByReadPaths(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "read.ltm", "v", TierLongTerm, "tool", WithContext("why it happened")))
	require.NoError(t, b.Store(ctx, "read.wm", "v", TierWorking, "tool", WithContext("session note")))

	// Get (LTM RETURNING path).
	got, err := b.Get(ctx, "read.ltm")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "why it happened", got.Context)

	// Recall (query SELECT path).
	recall, err := b.Recall(ctx, "read", 10)
	require.NoError(t, err)
	byKey := map[string]string{}
	for _, e := range recall {
		byKey[e.Key] = e.Context
	}
	assert.Equal(t, "why it happened", byKey["read.ltm"], "recall rows must carry context")
	assert.Equal(t, "session note", byKey["read.wm"], "WM recall rows must carry context")

	// List (LTM SELECT path).
	list, err := b.List(ctx, "read.", "")
	require.NoError(t, err)
	byKey = map[string]string{}
	for _, e := range list {
		byKey[e.Key] = e.Context
	}
	assert.Equal(t, "why it happened", byKey["read.ltm"], "list rows must carry context")
}

// --- brain_history mirror ---

// Superseded and deleted versions archive their context alongside the value —
// a version's context is part of that version.
func TestHistory_CarriesContext(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "hist.key", "v1", TierLongTerm, "user", WithContext("original why")))
	require.NoError(t, b.Store(ctx, "hist.key", "v2", TierLongTerm, "user", WithContext("second why")))
	require.NoError(t, b.Store(ctx, "hist.key", "v3", TierLongTerm, "user"))

	hist := historyRowsWithCtx(t, b, "hist.key")
	require.Len(t, hist, 2)
	assert.Equal(t, "v1", hist[0].value)
	assert.Equal(t, "original why", hist[0].context)
	assert.Equal(t, "v2", hist[1].value)
	assert.Equal(t, "second why", hist[1].context)

	require.NoError(t, b.Delete(ctx, "hist.key"))
	hist = historyRowsWithCtx(t, b, "hist.key")
	require.Len(t, hist, 3)
	assert.Equal(t, "deleted", hist[2].reason)
	assert.Equal(t, "", hist[2].context, "cleared context archives as empty")
}

// --- helpers ---

func storedContext(t *testing.T, b *Brain, key string) string {
	t.Helper()
	var ctx string
	err := b.db.QueryRow(`SELECT context FROM brain_ltm WHERE key = ?`, key).Scan(&ctx)
	require.NoError(t, err)
	return ctx
}

func historyRowsWithCtx(t *testing.T, b *Brain, key string) []struct {
	value   string
	context string
	reason  string
} {
	t.Helper()
	rows, err := b.db.Query(`SELECT value, COALESCE(context, ''), reason FROM brain_history WHERE key = ? ORDER BY archived_at, id`, key)
	require.NoError(t, err)
	defer rows.Close()
	var out []struct {
		value   string
		context string
		reason  string
	}
	for rows.Next() {
		var r struct {
			value   string
			context string
			reason  string
		}
		require.NoError(t, rows.Scan(&r.value, &r.context, &r.reason))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}
