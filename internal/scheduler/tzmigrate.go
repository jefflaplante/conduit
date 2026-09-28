package scheduler

// conduit-31jg.60: one-off migration of cron_jobs.json Go jobs from
// expressions written in a server zone (UTC) to the owner's wall-clock zone,
// expressed with the per-job "CRON_TZ=<zone> " prefix that the deployed
// scheduler already understands (conduit-31jg.34). After migration the jobs
// keep their current wall-clock times and no longer drift at DST changes.

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// goCronParser matches the parser cron.New(cron.WithSeconds()) uses.
var goCronParser = cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// ParseJobSchedule parses a job's schedule exactly as the scheduler does:
// 6-field (seconds first) for Go jobs, standard 5-field for system jobs.
// A 5-field Go expression gets a "0" seconds field like normalizeSchedule.
func ParseJobSchedule(schedule string, jobType JobType) (cron.Schedule, error) {
	norm, err := normalizeSchedule(schedule, jobType)
	if err != nil {
		if strings.HasPrefix(strings.TrimSpace(schedule), "@") {
			return goCronParser.Parse(strings.TrimSpace(schedule))
		}
		return nil, err
	}
	if jobType == JobTypeGo {
		return goCronParser.Parse(norm)
	}
	return cron.ParseStandard(norm)
}

// TZAction is the outcome of migrating one job.
type TZAction string

const (
	TZConvert TZAction = "convert" // schedule rewritten
	TZSkip    TZAction = "skip"    // nothing to do (system, interval, already zoned)
	TZRefuse  TZAction = "refuse"  // cannot convert exactly; left unchanged
)

// TZJobResult describes the migration of one job.
type TZJobResult struct {
	Index       int
	ID          string
	Name        string
	Type        JobType
	Enabled     bool
	OneShot     bool
	OldSchedule string
	NewSchedule string // equal to OldSchedule unless Action == TZConvert
	Action      TZAction
	Reason      string   // why skipped/refused
	Flags       []string // things the owner should double-check
	Command     string   // system jobs: crontab command (conduit-31jg.74)
}

// TZMigrateOptions configures a migration.
type TZMigrateOptions struct {
	From *time.Location // zone the existing expressions are written in
	To   *time.Location // zone to express them in (CRON_TZ value)
	// Now is the reference instant: the From->To offset in effect at Now
	// defines "today's wall-clock time" for recurring jobs. Zero = time.Now().
	Now time.Time
	// IncludeSystem also converts system (crontab) jobs (conduit-31jg.74).
	// The host's vixie cron ignores CRON_TZ, so the scheduler writes such a
	// schedule as a zone-guarded crontab line (crontab_tz.go); From must be
	// the cron daemon's zone. Jobs the guard cannot express are refused.
	IncludeSystem bool
}

func (o TZMigrateOptions) now() time.Time {
	if o.Now.IsZero() {
		return time.Now()
	}
	return o.Now
}

// errSkip marks a schedule that needs no conversion.
type errSkip struct{ reason string }

func (e errSkip) Error() string { return e.reason }

var clockInName = regexp.MustCompile(`(?i)\b(\d{1,2})(?::(\d{2}))?\s*(am|pm)\b`)

// nameClockFlag flags a job whose name states a clock time that differs
// from the hour it fires at in loc.
func nameClockFlag(name string, s cron.Schedule, now time.Time, loc *time.Location) string {
	m := clockInName.FindStringSubmatch(name)
	if m == nil || s == nil {
		return ""
	}
	h, _ := strconv.Atoi(m[1])
	if strings.EqualFold(m[3], "pm") && h != 12 {
		h += 12
	} else if strings.EqualFold(m[3], "am") && h == 12 {
		h = 0
	}
	next := s.Next(now)
	if next.IsZero() {
		return ""
	}
	if got := next.In(loc).Hour(); got != h {
		return fmt.Sprintf("name says %q but it fires at %s", m[0], next.In(loc).Format("15:04 MST"))
	}
	return ""
}

// systemHourInterval reports whether a 5-field schedule's hour field is a
// "*/N" step (conduit-31jg.74).
func systemHourInterval(schedule string) bool {
	_, rest := splitTZPrefix(schedule)
	f := strings.Fields(rest)
	return len(f) == 5 && strings.HasPrefix(f[1], "*/")
}

// PlanTZMigration computes the per-job migration for a cron_jobs.json body.
func PlanTZMigration(data []byte, opts TZMigrateOptions) ([]TZJobResult, error) {
	if opts.From == nil || opts.To == nil {
		return nil, errors.New("from and to locations are required")
	}
	var jobs []*Job
	if err := json.Unmarshal(data, &jobs); err != nil {
		return nil, fmt.Errorf("parse jobs file: %w", err)
	}
	now := opts.now()
	out := make([]TZJobResult, 0, len(jobs))
	for i, j := range jobs {
		r := TZJobResult{
			Index: i, ID: j.ID, Name: j.Name, Type: j.Type, Enabled: j.Enabled, OneShot: j.OneShot,
			OldSchedule: j.Schedule, NewSchedule: j.Schedule,
		}
		if j.Type == JobTypeSystem {
			r.Command = j.Command
		}
		if !j.Enabled {
			r.Flags = append(r.Flags, "disabled")
		}
		if j.OneShot {
			r.Flags = append(r.Flags, "oneshot")
		}
		if j.Type != JobTypeGo && !(opts.IncludeSystem && j.Type == JobTypeSystem) {
			r.Action, r.Reason = TZSkip, "system crontab job (runs in the cron daemon's zone; pass --include-system to convert)"
			if s, err := ParseJobSchedule(j.Schedule, j.Type); err == nil {
				if f := nameClockFlag(j.Name, s, now.In(opts.From), opts.To); f != "" {
					r.Flags = append(r.Flags, f)
				}
			}
			out = append(out, r)
			continue
		}
		newExpr, flags, err := ConvertScheduleTZ(j.Schedule, opts)
		if j.Type == JobTypeSystem && err == nil && systemHourInterval(j.Schedule) {
			// conduit-31jg.74: "5 */2 * * *" is an interval, not a wall-clock
			// time; converting it would fire every UTC hour behind a guard.
			err = errSkip{"hour field is a '*/N' interval: zone-independent in intent"}
		}
		var skip errSkip
		switch {
		case errors.As(err, &skip):
			r.Action, r.Reason = TZSkip, skip.reason
		case err != nil:
			r.Action, r.Reason = TZRefuse, err.Error()
		default:
			r.Action, r.NewSchedule = TZConvert, newExpr
			r.Flags = append(r.Flags, flags...)
			if j.Type == JobTypeSystem {
				// conduit-31jg.74: must be expressible as a guarded crontab line.
				if rr, err := renderCrontabSchedule(newExpr, opts.From, now); err != nil {
					r.Action, r.NewSchedule, r.Reason = TZRefuse, j.Schedule, err.Error()
				} else if len(rr.GuardHours) > 0 {
					r.Flags = append(r.Flags, "crontab: "+rr.Fields+" + "+rr.Zone+" hour guard")
				}
			}
		}
		if s, err := ParseJobSchedule(r.NewSchedule, j.Type); err == nil {
			if f := nameClockFlag(j.Name, s, now.In(opts.From), opts.To); f != "" {
				r.Flags = append(r.Flags, f)
			}
		}
		out = append(out, r)
	}
	return out, nil
}
