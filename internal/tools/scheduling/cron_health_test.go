package scheduling

import (
	"context"
	"strings"
	"testing"

	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// conduit-2six: failing jobs are visible in Cron list/status output.

func TestCronToolList_ShowsFailureStreak(t *testing.T) {
	mockGw := &fullMockGatewayService{mockGatewayService: mockGatewayService{jobs: []types.SchedulerJob{
		{ID: "ok", Name: "Healthy", Schedule: "0 8 * * *", Type: "go", Enabled: true},
		{ID: "wildlife", Name: "Wildlife", Schedule: "0 9 * * *", Type: "go", Enabled: true,
			ConsecutiveFailures: 3, LastError: "AI execution failed: " + strings.Repeat("x", 300)},
	}}}
	tool := NewCronTool(&types.ToolServices{Gateway: mockGw})

	result, err := tool.Execute(context.Background(), map[string]interface{}{"action": "list"})
	require.NoError(t, err)
	require.True(t, result.Success)
	assert.Contains(t, result.Content, "FAILING: 3 consecutive failed run(s) - last error: AI execution failed: ")
	assert.Equal(t, 1, strings.Count(result.Content, "FAILING"))
	assert.NotContains(t, result.Content, strings.Repeat("x", 200), "last error should be truncated")
}

func TestFormatFailingJobs(t *testing.T) {
	assert.Equal(t, "", formatFailingJobs(nil))
	assert.Equal(t, "Failing Jobs: none\n", formatFailingJobs(map[string]int{}))
	assert.Equal(t, "Failing Jobs: a (1 consecutive), b (4 consecutive)\n", formatFailingJobs(map[string]int{"b": 4, "a": 1}))
}
