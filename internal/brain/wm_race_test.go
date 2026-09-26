package brain

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestWMConcurrentRecallFlushConsolidateStore exercises the WM entry-pointer
// paths concurrently. Run with -race: before conduit-31jg.32, Get/Recall/List
// handed out live *Entry pointers (and Recall sorted them outside the lock)
// while autoFlush/Consolidate/Store mutated them under b.mu.
func TestWMConcurrentRecallFlushConsolidateStore(t *testing.T) {
	b := newTestBrain(t, WithSpreadingEnabled(false))
	ctx := testCtx("race-user")
	for i := 0; i < 20; i++ {
		if err := b.Store(ctx, fmt.Sprintf("race.key%d", i), "race value", TierWorking, "tool"); err != nil {
			t.Fatal(err)
		}
	}

	const iters = 200
	var wg sync.WaitGroup
	run := func(f func(i int)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				f(i)
			}
		}()
	}

	// Readers that inspect the returned entries (reading fields the writers mutate).
	run(func(i int) {
		res, err := b.Recall(ctx, "race value", 50)
		if err != nil {
			t.Error(err)
			return
		}
		for _, e := range res {
			_ = e.Salience + float64(e.AccessCount)
			_ = e.AccessedAt
		}
	})
	run(func(i int) {
		e, err := b.Get(ctx, fmt.Sprintf("race.key%d", i%20))
		if err != nil {
			t.Error(err)
			return
		}
		if e != nil {
			_ = e.Salience + float64(e.AccessCount)
		}
	})
	run(func(i int) {
		res, _ := b.List(ctx, "race.", "")
		for _, e := range res {
			_ = e.Salience + float64(e.AccessCount)
		}
	})
	// Writers.
	run(func(i int) { b.autoFlush() })
	run(func(i int) {
		if _, err := b.Consolidate(ctx, false); err != nil {
			t.Error(err)
		}
	})
	run(func(i int) {
		_ = b.Store(ctx, fmt.Sprintf("race.key%d", i%20), fmt.Sprintf("race value %d", i), TierWorking, "tool")
	})
	wg.Wait()
}

// TestWMReturnedEntriesAreSnapshots proves callers cannot mutate WM through
// entries returned by Get/Recall/List. conduit-31jg.32
func TestWMReturnedEntriesAreSnapshots(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("u")
	if err := b.Store(ctx, "snap.key", "v1", TierWorking, "tool"); err != nil {
		t.Fatal(err)
	}
	e, err := b.Get(ctx, "snap.key")
	if err != nil || e == nil {
		t.Fatalf("get: %v %v", e, err)
	}
	e.Value = "mutated-by-get"
	e.AccessedAt = time.Time{}

	res, err := b.Recall(ctx, "snap", 10)
	if err != nil || len(res) == 0 {
		t.Fatalf("recall: %v %v", res, err)
	}
	res[0].Value = "mutated-by-recall"

	list, _ := b.List(ctx, "snap.", "")
	if len(list) == 0 {
		t.Fatal("list returned nothing")
	}
	list[0].Value = "mutated-by-list"

	for _, got := range b.WorkingMemoryEntries(ctx) {
		if got.Key == "snap.key" && got.Value != "v1" {
			t.Fatalf("WM entry mutated through returned pointer: %q", got.Value)
		}
	}
}
