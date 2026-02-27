package types

import (
	"context"
	"sync"
	"testing"
)

func TestWithRequestContext(t *testing.T) {
	ctx := context.Background()
	ctx = WithRequestContext(ctx, "chan1", "user1", "sess1")

	if got := RequestChannelID(ctx); got != "chan1" {
		t.Errorf("RequestChannelID = %q, want %q", got, "chan1")
	}
	if got := RequestUserID(ctx); got != "user1" {
		t.Errorf("RequestUserID = %q, want %q", got, "user1")
	}
	if got := RequestSessionKey(ctx); got != "sess1" {
		t.Errorf("RequestSessionKey = %q, want %q", got, "sess1")
	}
}

func TestRequestContext_EmptyOnMissingValues(t *testing.T) {
	ctx := context.Background()

	if got := RequestChannelID(ctx); got != "" {
		t.Errorf("RequestChannelID on bare context = %q, want %q", got, "")
	}
	if got := RequestUserID(ctx); got != "" {
		t.Errorf("RequestUserID on bare context = %q, want %q", got, "")
	}
	if got := RequestSessionKey(ctx); got != "" {
		t.Errorf("RequestSessionKey on bare context = %q, want %q", got, "")
	}
}

func TestRequestContext_ConcurrentSafety(t *testing.T) {
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(2)

		// Goroutine A
		go func() {
			defer wg.Done()
			ctx := WithRequestContext(context.Background(), "chanA", "userA", "sessA")
			if got := RequestChannelID(ctx); got != "chanA" {
				t.Errorf("goroutine A: RequestChannelID = %q, want %q", got, "chanA")
			}
			if got := RequestUserID(ctx); got != "userA" {
				t.Errorf("goroutine A: RequestUserID = %q, want %q", got, "userA")
			}
			if got := RequestSessionKey(ctx); got != "sessA" {
				t.Errorf("goroutine A: RequestSessionKey = %q, want %q", got, "sessA")
			}
		}()

		// Goroutine B
		go func() {
			defer wg.Done()
			ctx := WithRequestContext(context.Background(), "chanB", "userB", "sessB")
			if got := RequestChannelID(ctx); got != "chanB" {
				t.Errorf("goroutine B: RequestChannelID = %q, want %q", got, "chanB")
			}
			if got := RequestUserID(ctx); got != "userB" {
				t.Errorf("goroutine B: RequestUserID = %q, want %q", got, "userB")
			}
			if got := RequestSessionKey(ctx); got != "sessB" {
				t.Errorf("goroutine B: RequestSessionKey = %q, want %q", got, "sessB")
			}
		}()
	}

	wg.Wait()
}
