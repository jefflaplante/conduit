package vecgo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// H12: Stop closed workCh while submitters could still select on a send to
// it, so an IndexFile racing (or following) Stop panicked with "send on
// closed channel". M3: the embed worker wrote idx.hashes without idx.mu
// after the submitter had timed out and released the lock.

// slowEmbedder ignores ctx and takes delay per call, like a slow local model.
type slowEmbedder struct {
	delay time.Duration
	calls atomic.Int32
}

func (s *slowEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	s.calls.Add(1)
	time.Sleep(s.delay)
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{1, 0, 0, 0}
	}
	return out, nil
}
func (s *slowEmbedder) Dimensions() int { return 4 }
func (s *slowEmbedder) Name() string    { return "slow" }

func TestIndexer_IndexFileAfterStopNeverPanics(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "a.md")
	require.NoError(t, os.WriteFile(f, []byte("hello"), 0o644))

	for i := 0; i < 50; i++ {
		idx := NewIndexer(nil, IndexerConfig{})
		require.NoError(t, idx.Start(context.Background()))
		idx.Stop()

		var wg sync.WaitGroup
		var panics atomic.Int32
		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() {
					if r := recover(); r != nil {
						panics.Add(1)
					}
				}()
				err := idx.IndexFile(context.Background(), f)
				assert.ErrorContains(t, err, "indexer stopped")
			}()
		}
		wg.Wait()
		require.Zero(t, panics.Load(), "iteration %d: IndexFile after Stop panicked", i)
	}
}

func TestIndexer_IndexFileRacingStopNeverPanics(t *testing.T) {
	// The startup scan (staggeredScan) also submits jobs, from a goroutine
	// where a panic cannot be recovered.
	for i := 0; i < 30; i++ {
		dir := t.TempDir()
		emb := &slowEmbedder{delay: time.Millisecond}
		svc, err := NewService(Config{Embedder: emb})
		require.NoError(t, err)
		idx := NewIndexer(svc, IndexerConfig{WorkspaceDir: dir})
		require.NoError(t, idx.Start(context.Background()))

		var wg sync.WaitGroup
		var panics atomic.Int32
		for g := 0; g < 4; g++ {
			rel := fmt.Sprintf("f%d-%d.md", i, g)
			require.NoError(t, os.WriteFile(filepath.Join(dir, rel), []byte("# "+rel+"\nbody text"), 0o644))
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() {
					if r := recover(); r != nil {
						panics.Add(1)
					}
				}()
				for k := 0; k < 5; k++ {
					_ = idx.IndexFile(context.Background(), rel)
				}
			}()
		}
		time.Sleep(time.Duration(i%5) * time.Millisecond)
		idx.Stop()
		wg.Wait()
		svc.Close()
		require.Zero(t, panics.Load(), "iteration %d: IndexFile racing Stop panicked", i)
	}
}

func TestIndexer_StopIsIdempotent(t *testing.T) {
	idx := NewIndexer(nil, IndexerConfig{})
	require.NoError(t, idx.Start(context.Background()))
	idx.Stop()
	assert.NotPanics(t, idx.Stop)
}

func TestIndexer_TimedOutSubmitDoesNotRecordHash(t *testing.T) {
	// Run with -race: the worker finishing after the submitter gave up must
	// not touch idx.hashes (read here under idx.mu by Status).
	// No WorkspaceDir: no startup scan; IndexFile takes the absolute path.
	f := filepath.Join(t.TempDir(), "a.md")
	require.NoError(t, os.WriteFile(f, []byte("# a\nhello world"), 0o644))
	emb := &slowEmbedder{delay: 150 * time.Millisecond}
	svc, err := NewService(Config{Embedder: emb})
	require.NoError(t, err)
	t.Cleanup(func() { svc.Close() })
	idx := NewIndexer(svc, IndexerConfig{})
	require.NoError(t, idx.Start(context.Background()))
	t.Cleanup(idx.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, idx.IndexFile(ctx, f), context.DeadlineExceeded)

	// Hammer the lock-protected reader while the worker completes.
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		_ = idx.Status()
	}
	assert.Equal(t, 0, idx.Status().TrackedFiles,
		"a submit that gave up must not record the hash behind the lock's back")

	// A later, patient submit indexes the file and records its hash.
	require.NoError(t, idx.IndexFile(context.Background(), f))
	assert.Equal(t, 1, idx.Status().TrackedFiles)
}
