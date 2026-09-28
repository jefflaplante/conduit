package tools

import (
	"context"
	"fmt"
	"time"

	"conduit/internal/tools/types"
)

// RegistrySelfTestResult aggregates self-test results for all tools.
type RegistrySelfTestResult struct {
	Results       map[string]*types.SelfTestResult `json:"results"`
	TotalTools    int                              `json:"total_tools"`
	TestedTools   int                              `json:"tested_tools"`
	HealthyTools  int                              `json:"healthy_tools"`
	DegradedTools int                              `json:"degraded_tools"`
	FailedTools   int                              `json:"failed_tools"`
	TestedAt      time.Time                        `json:"tested_at"`
	TestDuration  time.Duration                    `json:"test_duration"`
}

// Summary returns a human-readable summary.
func (r *RegistrySelfTestResult) Summary() string {
	return fmt.Sprintf("%d tools tested: %d healthy, %d degraded, %d failed (%d did not implement self-test)",
		r.TestedTools, r.HealthyTools, r.DegradedTools, r.FailedTools, r.TotalTools-r.TestedTools)
}

// IsHealthy returns true if all tested tools are OK.
func (r *RegistrySelfTestResult) IsHealthy() bool {
	return r.FailedTools == 0 && r.DegradedTools == 0
}

// SelfTestTool runs a self-test on a specific tool by name.
// Returns nil if the tool doesn't exist or isn't enabled.
func (r *Registry) SelfTestTool(ctx context.Context, name string, opts *types.SelfTestOptions) *types.SelfTestResult {
	r.mu.RLock()
	tool, exists := r.tools[name]
	enabled := r.isToolEnabledLocked(name)
	r.mu.RUnlock()
	if !exists || !enabled {
		return nil
	}

	tester, ok := tool.(types.SelfTester)
	if !ok {
		return &types.SelfTestResult{
			Status:   types.SelfTestStatusOK,
			Message:  fmt.Sprintf("Tool '%s' does not implement self-test (assumed functional)", name),
			TestedAt: time.Now(),
		}
	}

	if opts == nil {
		opts = types.DefaultSelfTestOptions()
	}

	toolCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	return tester.SelfTest(toolCtx, opts)
}

// SelfTestAll runs self-tests on all enabled tools that implement SelfTester.
func (r *Registry) SelfTestAll(ctx context.Context, opts *types.SelfTestOptions) *RegistrySelfTestResult {
	if opts == nil {
		opts = types.DefaultSelfTestOptions()
	}

	result := &RegistrySelfTestResult{
		Results:  make(map[string]*types.SelfTestResult),
		TestedAt: time.Now(),
	}

	availableTools := r.GetAvailableTools()
	result.TotalTools = len(availableTools)

	for name, tool := range availableTools {
		if tester, ok := tool.(types.SelfTester); ok {
			toolCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			testResult := tester.SelfTest(toolCtx, opts)
			cancel()

			result.Results[name] = testResult
			result.TestedTools++

			switch testResult.Status {
			case types.SelfTestStatusOK:
				result.HealthyTools++
			case types.SelfTestStatusDegraded:
				result.DegradedTools++
			case types.SelfTestStatusFailed:
				result.FailedTools++
			}
		}
	}

	result.TestDuration = time.Since(result.TestedAt)
	return result
}
