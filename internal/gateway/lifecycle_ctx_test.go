package gateway

import (
	"context"
	"sync"
	"testing"
)

// conduit-31jg.73: Start's write of the lifecycle context must not race with
// WS/wake goroutines reading it (run with -race).
func TestLifecycleCtx_ConcurrentSetAndRead(t *testing.T) {
	g := &Gateway{}
	if g.lifecycleCtx() == nil {
		t.Fatal("lifecycleCtx before Start must not be nil")
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			g.setLifecycleCtx(context.Background())
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			_ = g.lifecycleCtx()
		}
	}()
	wg.Wait()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g.setLifecycleCtx(ctx)
	if g.lifecycleCtx() != ctx {
		t.Fatal("lifecycleCtx did not return the stored context")
	}
}
