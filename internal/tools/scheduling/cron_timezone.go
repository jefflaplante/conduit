package scheduling

import (
	"fmt"
	"strings"
	"time"

	"conduit/internal/scheduler"

	"github.com/robfig/cron/v3"
)

// clock returns the tool's notion of "now" (injectable for tests).
func (t *CronTool) clock() time.Time {
	if t.now != nil {
		return t.now()
	}
	return time.Now()
}

// splitScheduleZone returns the zone named by a leading CRON_TZ=/TZ= prefix
// (empty if none) and the remaining cron expression.
func splitScheduleZone(schedule string) (zone, expr string) {
	prefix, expr := scheduler.SplitTZPrefix(schedule)
	if prefix == "" {
		return "", expr
	}
	_, zone, _ = strings.Cut(strings.TrimSpace(prefix), "=")
	return zone, expr
}

// applyTimezone pins schedule to the IANA zone tz with a CRON_TZ= prefix
// (bd-vo77). It returns the final schedule and the effective zone ("" when the
// schedule runs in server local time). An explicit prefix already on schedule
// is kept when tz is empty, and must agree with tz otherwise.
func applyTimezone(schedule, tz string) (string, string, error) {
	existing, expr := splitScheduleZone(schedule)
	if tz == "" {
		return schedule, existing, nil
	}

	loc, err := time.LoadLocation(tz)
	if err != nil {
		return "", "", fmt.Errorf("invalid timezone %q: %v (use an IANA name like America/Los_Angeles)", tz, err)
	}
	if existing != "" {
		exLoc, err := time.LoadLocation(existing)
		if err != nil || exLoc.String() != loc.String() {
			return "", "", fmt.Errorf("timezone conflict: schedule prefix names %q but timezone parameter is %q; pass only one", existing, loc.String())
		}
	}
	return "CRON_TZ=" + loc.String() + " " + expr, loc.String(), nil
}

// nextRunTime returns the next fire time of schedule after now, parsed the
// way the scheduler will run it (see normalizeSchedule in
// internal/scheduler/scheduler_run.go): go jobs accept 5 or 6 fields, system
// jobs are 5-field crontab with any seconds field dropped. The result is in
// the schedule's zone (time.Local without a prefix).
func nextRunTime(schedule, jobType string, now time.Time) (time.Time, error) {
	prefix, expr := scheduler.SplitTZPrefix(schedule)
	fields := strings.Fields(expr)
	if len(fields) != 5 && len(fields) != 6 {
		return time.Time{}, fmt.Errorf("invalid cron expression: expected 5 or 6 fields, got %d", len(fields))
	}

	var parser cron.Parser
	if jobType == "system" {
		if len(fields) == 6 {
			expr = strings.Join(fields[1:], " ")
		}
		parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	} else {
		if len(fields) == 5 {
			expr = "0 " + expr
		}
		parser = cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	}

	sched, err := parser.Parse(prefix + expr)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid cron expression: %v", err)
	}
	next := sched.Next(now)
	if next.IsZero() {
		return time.Time{}, fmt.Errorf("schedule %q never fires", schedule)
	}
	// robfig returns Next in now's location; render it in the job's zone.
	loc := time.Local
	if spec, ok := sched.(*cron.SpecSchedule); ok && spec.Location != nil {
		loc = spec.Location
	}
	return next.In(loc), nil
}
