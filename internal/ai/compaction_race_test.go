package ai

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"conduit/internal/config"
	"conduit/internal/sessions"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// conduit-31jg.21: compaction must never drop messages stored while the
// summarizer runs, must be atomic, and must not double-summarize.

// blockingSummarizer is a Provider whose GenerateResponse signals `entered`
// and then blocks until `release` is closed.
type blockingSummarizer struct {
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
	once    sync.Once
}

func newBlockingSummarizer() *blockingSummarizer {
	return &blockingSummarizer{entered: make(chan struct{}), release: make(chan struct{})}
}

func (b *blockingSummarizer) Name() string { return "blocking" }

func (b *blockingSummarizer) GenerateResponse(ctx context.Context, _ *GenerateRequest) (*GenerateResponse, error) {
	b.calls.Add(1)
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &GenerateResponse{Content: "SUMMARY-TEXT"}, nil
}

func setupCompactionRace(t *testing.T, provider Provider, total, keep int) (*CompactionEngine, *sessions.Store, *sessions.Session) {
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

	router, err := NewRouter(config.AIConfig{DefaultProvider: "blocking"}, nil)
	require.NoError(t, err)
	router.RegisterProvider("blocking", provider)

	ce := NewCompactionEngine(router, store, config.CompactionConfig{
		Enabled:              true,
		RecentMessagesToKeep: keep,
	})
	return ce, store, session
}

func msgContents(t *testing.T, store *sessions.Store, key string) []string {
	t.Helper()
	msgs, err := store.GetMessages(key, 1000)
	require.NoError(t, err)
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Content
	}
	return out
}

func TestCompact_MessageDuringSummarizationSurvives(t *testing.T) {
	prov := newBlockingSummarizer()
	ce, store, session := setupCompactionRace(t, prov, 15, 5)

	type res struct {
		r   *CompactionResult
		err error
	}
	done := make(chan res, 1)
	go func() {
		r, err := ce.Compact(context.Background(), session)
		done <- res{r, err}
	}()

	<-prov.entered
	// A concurrent turn stores user + assistant messages mid-summarization.
	_, err := store.AddMessage(session.Key, "user", "NEW-USER", nil)
	require.NoError(t, err)
	_, err = store.AddMessage(session.Key, "assistant", "NEW-ASSISTANT", nil)
	require.NoError(t, err)
	close(prov.release)

	out := <-done
	require.NoError(t, out.err)
	require.NotNil(t, out.r)
	assert.Equal(t, 10, out.r.SummarizedCount)
	assert.Equal(t, 5, out.r.KeptCount)

	got := msgContents(t, store, session.Key)
	require.Len(t, got, 1+5+2)
	assert.True(t, strings.HasPrefix(got[0], "[Context Summary from 10 previous messages]"), got[0])
	assert.Contains(t, got[0], "SUMMARY-TEXT")
	assert.Equal(t, []string{"m10", "m11", "m12", "m13", "m14", "NEW-USER", "NEW-ASSISTANT"}, got[1:])

	s, err := store.GetSession(session.Key)
	require.NoError(t, err)
	assert.Equal(t, 8, s.MessageCount)
}

func TestCompact_TransactionFailureLeavesHistoryUnchanged(t *testing.T) {
	prov := newBlockingSummarizer()
	close(prov.release)
	ce, store, session := setupCompactionRace(t, prov, 12, 4)
	before := msgContents(t, store, session.Key)

	// Make the summary INSERT fail inside the transaction (after deletes ran).
	_, err := store.DB().Exec(`CREATE TRIGGER fail_summary BEFORE INSERT ON messages
		WHEN NEW.content LIKE '[Context Summary%' BEGIN SELECT RAISE(ABORT, 'injected'); END;`)
	require.NoError(t, err)

	r, err := ce.Compact(context.Background(), session)
	require.Error(t, err)
	assert.Nil(t, r)
	assert.Equal(t, before, msgContents(t, store, session.Key))

	s, err := store.GetSession(session.Key)
	require.NoError(t, err)
	assert.Equal(t, 12, s.MessageCount)
}

func TestCompact_ConcurrentSameSessionGuarded(t *testing.T) {
	prov := newBlockingSummarizer()
	ce, store, session := setupCompactionRace(t, prov, 15, 5)

	errs := make(chan error, 1)
	go func() {
		_, err := ce.Compact(context.Background(), session)
		errs <- err
	}()
	<-prov.entered

	// Second compaction of the same session while the first is in flight.
	r, err := ce.Compact(context.Background(), session)
	assert.ErrorIs(t, err, ErrCompactionInProgress)
	assert.Nil(t, r)

	close(prov.release)
	require.NoError(t, <-errs)
	assert.Equal(t, int32(1), prov.calls.Load(), "summarizer must run exactly once")

	got := msgContents(t, store, session.Key)
	require.Len(t, got, 6)
	assert.True(t, strings.HasPrefix(got[0], "[Context Summary"))
	assert.Equal(t, []string{"m10", "m11", "m12", "m13", "m14"}, got[1:])

	// Guard is released afterwards.
	_, err = ce.Compact(context.Background(), session)
	assert.NotErrorIs(t, err, ErrCompactionInProgress)
}

// Two independent engines (no shared in-flight guard, e.g. two gateway
// processes on one DB) racing on the same snapshot: exactly one wins, the
// other hits ErrCompactionStale, and no data is lost or double-summarized.
func TestCompact_ConcurrentEnginesNoDoubleSummary(t *testing.T) {
	prov := newBlockingSummarizer()
	ce1, store, session := setupCompactionRace(t, prov, 15, 5)
	ce2 := NewCompactionEngine(ce1.router, store, ce1.config)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, ce := range []*CompactionEngine{ce1, ce2} {
		wg.Add(1)
		go func(i int, ce *CompactionEngine) {
			defer wg.Done()
			_, errs[i] = ce.Compact(context.Background(), session)
		}(i, ce)
	}
	// Let both snapshot and enter the summarizer before releasing.
	deadline := time.After(5 * time.Second)
	for prov.calls.Load() < 2 {
		select {
		case <-deadline:
			t.Fatal("both compactions did not reach the summarizer")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	close(prov.release)
	wg.Wait()

	var ok, stale int
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case assert.ErrorIs(t, err, sessions.ErrCompactionStale):
			stale++
		}
	}
	assert.Equal(t, 1, ok)
	assert.Equal(t, 1, stale)

	got := msgContents(t, store, session.Key)
	require.Len(t, got, 6)
	assert.True(t, strings.HasPrefix(got[0], "[Context Summary"))
	assert.Equal(t, []string{"m10", "m11", "m12", "m13", "m14"}, got[1:])
}
