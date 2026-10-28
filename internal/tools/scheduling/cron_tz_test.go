package scheduling

import (
	"context"
	"strings"
	"testing"
	"time"

	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bd-vo77: per-job timezone param on the Cron tool.
func TestCronToolScheduleTimezone(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	require.NoError(t, err)

	tests := []struct {
		name         string
		args         map[string]interface{}
		wantErr      []string // substrings of result.Error; nil = success
		wantSchedule string   // exact final schedule ("" = don't check)
		wantPrefix   *bool    // whether the schedule carries CRON_TZ=
		wantTZ       string
		wantWall     *[2]int // hour, minute of nextRun in wantTZ
		wantContent  []string
	}{
		{
			name:         "go job with timezone gets CRON_TZ prefix",
			args:         map[string]interface{}{"schedule": "40 10 * * *", "timezone": "America/Los_Angeles"},
			wantSchedule: "CRON_TZ=America/Los_Angeles 40 10 * * *",
			wantTZ:       "America/Los_Angeles",
			wantWall:     &[2]int{10, 40},
			wantContent:  []string{"Next run:", "(America/Los_Angeles)"},
		},
		{
			name:         "no timezone leaves schedule unchanged",
			args:         map[string]interface{}{"schedule": "40 10 * * *"},
			wantSchedule: "40 10 * * *",
			wantTZ:       "",
		},
		{
			name:    "invalid timezone fails",
			args:    map[string]interface{}{"schedule": "40 10 * * *", "timezone": "Not/AZone"},
			wantErr: []string{"Not/AZone", "IANA"},
		},
		{
			name:         "matching explicit prefix is not duplicated",
			args:         map[string]interface{}{"schedule": "CRON_TZ=America/Los_Angeles 0 9 * * *", "timezone": "America/Los_Angeles"},
			wantSchedule: "CRON_TZ=America/Los_Angeles 0 9 * * *",
			wantTZ:       "America/Los_Angeles",
			wantWall:     &[2]int{9, 0},
		},
		{
			name:    "conflicting explicit prefix fails",
			args:    map[string]interface{}{"schedule": "CRON_TZ=America/New_York 0 9 * * *", "timezone": "America/Los_Angeles"},
			wantErr: []string{"conflict", "America/New_York", "America/Los_Angeles"},
		},
		{
			name:    "delayMinutes rejects timezone",
			args:    map[string]interface{}{"delayMinutes": 5, "timezone": "America/Los_Angeles"},
			wantErr: []string{"delayMinutes", "timezone"},
		},
		{
			name:         "system job with timezone",
			args:         map[string]interface{}{"schedule": "0 9 * * 1-5", "timezone": "America/Los_Angeles", "jobType": "system"},
			wantSchedule: "CRON_TZ=America/Los_Angeles 0 9 * * 1-5",
			wantTZ:       "America/Los_Angeles",
			wantWall:     &[2]int{9, 0},
		},
		{
			name:         "oneshot reminder shows zone wall-clock",
			args:         map[string]interface{}{"schedule": "40 10 24 10 *", "timezone": "America/Los_Angeles", "oneshot": true},
			wantSchedule: "CRON_TZ=America/Los_Angeles 40 10 24 10 *",
			wantTZ:       "America/Los_Angeles",
			wantWall:     &[2]int{10, 40},
			wantContent:  []string{"Reminder set for", "10:40 PDT"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gw := &fullMockGatewayService{}
			tool := NewCronTool(&types.ToolServices{Gateway: gw})
			// 2026-10-24 05:00 PDT: before the 10:40 PDT fire on the same day.
			tool.now = func() time.Time { return time.Date(2026, 10, 24, 5, 0, 0, 0, la) }

			args := map[string]interface{}{"action": "schedule", "command": "test"}
			for k, v := range tc.args {
				args[k] = v
			}
			result, err := tool.Execute(context.Background(), args)
			require.NoError(t, err)

			if tc.wantErr != nil {
				assert.False(t, result.Success)
				for _, s := range tc.wantErr {
					assert.Contains(t, result.Error, s)
				}
				assert.Empty(t, gw.jobs, "no job may be created on error")
				return
			}

			require.True(t, result.Success, result.Error)
			require.Len(t, gw.jobs, 1)
			job := gw.jobs[0]
			if tc.wantSchedule != "" {
				assert.Equal(t, tc.wantSchedule, job.Schedule)
			}
			if tc.wantPrefix != nil {
				assert.Equal(t, *tc.wantPrefix, strings.Contains(job.Schedule, "CRON_TZ="))
			}
			assert.LessOrEqual(t, strings.Count(job.Schedule, "TZ="), 1, "at most one zone prefix")

			assert.Equal(t, tc.wantTZ, result.Data["timezone"])

			nextRun, ok := result.Data["nextRun"].(string)
			require.True(t, ok, "nextRun must be present")
			next, err := time.Parse(time.RFC3339, nextRun)
			require.NoError(t, err)

			nextLocal, ok := result.Data["nextRunLocal"].(string)
			require.True(t, ok, "nextRunLocal must be present")
			nl, err := time.Parse(time.RFC3339, nextLocal)
			require.NoError(t, err)
			assert.True(t, next.Equal(nl), "nextRun and nextRunLocal must be the same instant")

			if tc.wantWall != nil {
				loc, err := time.LoadLocation(tc.wantTZ)
				require.NoError(t, err)
				w := next.In(loc)
				assert.Equal(t, tc.wantWall[0], w.Hour())
				assert.Equal(t, tc.wantWall[1], w.Minute())
			}
			for _, s := range tc.wantContent {
				assert.Contains(t, result.Content, s)
			}
			if tc.wantTZ == "" {
				assert.NotContains(t, result.Content, "Next run")
			}
		})
	}
}

// bd-vo77: nextRun is computed in the job's zone from the injected clock.
func TestCronToolScheduleTimezoneNextRunInstant(t *testing.T) {
	gw := &fullMockGatewayService{}
	tool := NewCronTool(&types.ToolServices{Gateway: gw})
	// 12:00 UTC = 05:00 PDT, so the next 10:40 PDT fire is 17:40 UTC the same day.
	tool.now = func() time.Time { return time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC) }

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":   "schedule",
		"command":  "test",
		"schedule": "40 10 * * *",
		"timezone": "America/Los_Angeles",
	})
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)

	next, err := time.Parse(time.RFC3339, result.Data["nextRun"].(string))
	require.NoError(t, err)
	assert.True(t, next.Equal(time.Date(2026, 10, 24, 17, 40, 0, 0, time.UTC)), "got %s", next)
	assert.Equal(t, "2026-10-24T10:40:00-07:00", result.Data["nextRun"])
}

func TestCronToolListJobsTimezone(t *testing.T) {
	gw := &fullMockGatewayService{mockGatewayService: mockGatewayService{jobs: []types.SchedulerJob{
		{ID: "a", Name: "Job A", Schedule: "CRON_TZ=America/Los_Angeles 0 9 * * *", Enabled: true},
		{ID: "b", Name: "Job B", Schedule: "0 8 * * *", Enabled: true},
	}}}
	tool := NewCronTool(&types.ToolServices{Gateway: gw})

	result, err := tool.Execute(context.Background(), map[string]interface{}{"action": "list"})
	require.NoError(t, err)
	require.True(t, result.Success)

	assert.Contains(t, result.Content, "   tz: America/Los_Angeles\n")
	assert.Contains(t, result.Content, "   0 9 * * *\n")
	assert.NotContains(t, result.Content, "CRON_TZ=")
	assert.Contains(t, result.Content, "   0 8 * * *\n")
	assert.Equal(t, 1, strings.Count(result.Content, "tz:"))

	jobs := result.Data["jobs"].([]*types.SchedulerJob)
	assert.Equal(t, "CRON_TZ=America/Los_Angeles 0 9 * * *", jobs[0].Schedule, "raw Schedule unchanged in Data")
}

func TestCronToolParametersTimezone(t *testing.T) {
	tool := NewCronTool(&types.ToolServices{})
	props := tool.Parameters()["properties"].(map[string]interface{})
	tz, ok := props["timezone"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "string", tz["type"])
	assert.Contains(t, tz["description"], "IANA")
	assert.Contains(t, tool.Description(), "timezone=")
}

func boolPtr(b bool) *bool { return &b }
