//go:build unix

package tools

import (
	"context"
	"testing"
	"time"

	"conduit/internal/ai"
	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// conduit-31jg.88: once the gateway drains, a tool call's timeout is capped
// to the remaining drain budget (minus a margin) and its result tells the
// model to wrap up.

func drainingCtx(deadline time.Time) context.Context {
	return types.WithDrainDeadline(context.Background(), func() (time.Time, bool) { return deadline, true })
}

func TestDrainCappedTimeout(t *testing.T) {
	// Not draining: unchanged.
	d, draining := drainCappedTimeout(context.Background(), 10*time.Minute)
	assert.False(t, draining)
	assert.Equal(t, 10*time.Minute, d)

	notYet := types.WithDrainDeadline(context.Background(), func() (time.Time, bool) { return time.Time{}, false })
	_, draining = drainCappedTimeout(notYet, time.Minute)
	assert.False(t, draining)

	// 20s left: capped to <= 20s - margin.
	d, draining = drainCappedTimeout(drainingCtx(time.Now().Add(20*time.Second)), 10*time.Minute)
	assert.True(t, draining)
	assert.LessOrEqual(t, d, 20*time.Second-drainToolMargin)
	assert.Greater(t, d, 15*time.Second)

	// A shorter own timeout is kept.
	d, _ = drainCappedTimeout(drainingCtx(time.Now().Add(20*time.Second)), 2*time.Second)
	assert.Equal(t, 2*time.Second, d)

	// Less than margin+floor left: floor, but never past the deadline.
	d, _ = drainCappedTimeout(drainingCtx(time.Now().Add(2*time.Second)), time.Minute)
	assert.Equal(t, drainToolFloor, d)
	d, _ = drainCappedTimeout(drainingCtx(time.Now().Add(500*time.Millisecond)), time.Minute)
	assert.LessOrEqual(t, d, 500*time.Millisecond)
	assert.Greater(t, d, time.Duration(0))

	// Deadline already passed: not refused, a short floor.
	d, _ = drainCappedTimeout(drainingCtx(time.Now().Add(-time.Second)), time.Minute)
	assert.Equal(t, drainToolFloor, d)
}

// The prod case: `sleep 150` with a long per-call timeout started mid-drain.
func TestExecutionEngine_DrainCapsBashAndAddsHint(t *testing.T) {
	reg := newBashRegistryForTest(t)
	engine := NewExecutionEngine(reg, 4, time.Minute, 5)

	budget := drainToolMargin + 1500*time.Millisecond
	ctx := drainingCtx(time.Now().Add(budget))
	start := time.Now()
	results, err := engine.ExecuteToolCalls(ctx, []ai.ToolCall{
		{ID: "1", Name: "Bash", Args: map[string]interface{}{"command": "sleep 150", "timeout": float64(600000)}},
	})
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Less(t, elapsed, budget, "call must end within the remaining drain budget")

	res := results[0].Result
	require.NotNil(t, res)
	assert.False(t, res.Success)
	assert.Contains(t, res.Content, "gateway is restarting; wrap up")
}

func TestExecutionEngine_NoHintWhenNotDraining(t *testing.T) {
	reg := newBashRegistryForTest(t)
	engine := NewExecutionEngine(reg, 4, time.Minute, 5)
	results, err := engine.ExecuteToolCalls(context.Background(), []ai.ToolCall{
		{ID: "1", Name: "Bash", Args: map[string]interface{}{"command": "echo hi"}},
	})
	require.NoError(t, err)
	require.NotNil(t, results[0].Result)
	assert.NotContains(t, results[0].Result.Content, "gateway is restarting")

	// Draining: a successful call keeps its output and gains the hint.
	results, err = engine.ExecuteToolCalls(drainingCtx(time.Now().Add(20*time.Second)), []ai.ToolCall{
		{ID: "1", Name: "Bash", Args: map[string]interface{}{"command": "echo hi"}},
	})
	require.NoError(t, err)
	res := results[0].Result
	require.NotNil(t, res)
	assert.True(t, res.Success)
	assert.Contains(t, res.Content, "hi")
	assert.Contains(t, res.Content, "gateway is restarting; wrap up")
}
