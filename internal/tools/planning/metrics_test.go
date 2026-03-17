package planning

import (
	"sync"
	"testing"
	"time"
)

func TestMetricsCollector_SetEnabledConcurrent(t *testing.T) {
	mc := NewMetricsCollector()

	var wg sync.WaitGroup

	// Concurrently toggle enabled state
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			mc.SetEnabled(id%2 == 0)
		}(i)
	}

	// Concurrently record executions (which check enabled state)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			mc.RecordExecution(
				"test_tool",
				time.Millisecond*10,
				true,  // success
				false, // cacheHit
				0,     // retries
				false, // fallbackUsed
				0.01,  // cost
				nil,   // err
			)
		}(i)
	}

	// Concurrently read metrics
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = mc.GetToolMetrics("test_tool")
			_ = mc.GetAllToolMetrics()
			_ = mc.GetPerformanceSummary()
		}()
	}

	wg.Wait()

	// Verify collector is in a consistent state
	metrics := mc.GetToolMetrics("test_tool")
	if metrics == nil {
		// It's possible all RecordExecution calls ran while enabled was false,
		// but at least some should have recorded if enabled was true at start.
		// The main purpose is to verify no data races, not exact counts.
		t.Log("metrics is nil - all RecordExecution calls may have run while disabled")
	}
}
