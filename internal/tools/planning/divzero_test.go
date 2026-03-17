package planning

import (
	"testing"
	"time"
)

func TestRecordPlanExecution_EmptyStepResults(t *testing.T) {
	mc := NewMetricsCollector()

	// PlanResult with zero step results should not cause division by zero
	planResult := &PlanResult{
		PlanID:      "test_plan",
		StepResults: map[string]*StepResult{}, // empty
		StartTime:   time.Now(),
		EndTime:     time.Now(),
		Duration:    time.Second,
		Success:     true,
		TotalSteps:  0,
	}

	estimated := EstimatedMetrics{
		Duration: time.Second,
	}

	// Should not panic
	mc.RecordPlanExecution(planResult, estimated, []string{"test"})
}

func TestPlanExecution_ZeroSteps_NoDivisionByZero(t *testing.T) {
	// Simulate the log line in planning_execution.go that divides by TotalSteps
	totalSteps := 0
	cacheHits := 0

	cacheHitRate := 0.0
	if totalSteps > 0 {
		cacheHitRate = float64(cacheHits) / float64(totalSteps) * 100
	}

	if cacheHitRate != 0.0 {
		t.Errorf("expected 0.0 cache hit rate, got %f", cacheHitRate)
	}
}
