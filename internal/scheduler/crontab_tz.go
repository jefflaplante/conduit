package scheduler

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// conduit-31jg.74: CRON_TZ for system (crontab) jobs.
//
// This host runs Debian/Ubuntu vixie cron (package cron 3.0pl1), which does
// NOT honour CRON_TZ: crontab(5) says every entry fires in the daemon's zone
// (Etc/UTC here) and suggests checking the time inside the command instead.
// So a system job scheduled "CRON_TZ=America/Los_Angeles 10 8 * * 1-5" is
// written to the crontab as ONE line that fires at every daemon-zone hour the
// target wall-clock hour can map to across the zone's offsets (PDT and PST:
// 15 and 16 UTC), guarded by a shell check of the current hour in the target
// zone:
//
//	10 15,16 * * 1-5 case "$(TZ=America/Los_Angeles date +\%H)" in 08) ;; *) exit 0 ;; esac; <command> # CONDUIT-MANAGED CONDUIT-JOB-ID:<id>
//
// Exactly one of the candidate hours passes the guard on any given day, so
// the job fires at 08:10 Pacific year-round. The cron daemon stays the
// executor (same user, environment, PATH and output handling as today) and
// jobs keep running while the gateway is down or restarting. ('%' must be
// escaped in crontab commands.)
//
// Limits (rejected with an error rather than approximated): the zone must
// differ from the daemon zone by whole hours, and if a mapped hour crosses
// midnight the day-of-month, month and day-of-week fields must be '*'.
// Like any local-time cron, a wall-clock hour skipped by spring-forward
// (02:xx Pacific) does not fire that day and one repeated by fall-back
// (01:xx) fires twice.

// crontabDaemonLocation is the zone the system cron daemon evaluates
// crontab lines in. The daemon runs on the gateway host, so time.Local.
var crontabDaemonLocation = func() *time.Location { return time.Local }

// crontabRendering is the crontab form of a system-job schedule.
type crontabRendering struct {
	Fields     string // 5 crontab fields in the daemon zone
	Zone       string // CRON_TZ zone ("" if none)
	GuardHours []int  // allowed wall-clock hours in Zone (nil = no guard)
}

// Guard returns the shell prefix that makes the command a no-op outside the
// target wall-clock hours ("" when no guard is needed).
func (r crontabRendering) Guard() string {
	if len(r.GuardHours) == 0 {
		return ""
	}
	hs := make([]string, len(r.GuardHours))
	for i, h := range r.GuardHours {
		hs[i] = fmt.Sprintf("%02d", h)
	}
	return fmt.Sprintf(`case "$(TZ=%s date +\%%H)" in %s) ;; *) exit 0 ;; esac; `, r.Zone, strings.Join(hs, "|"))
}

// zoneShiftsHours returns the distinct (daemon offset - zone offset) values,
// in whole hours, in effect between now and ~400 days ahead.
func zoneShiftsHours(zone, daemon *time.Location, now time.Time) ([]int, error) {
	seen := map[int]bool{}
	for h := 0; h <= 400*24; h++ {
		t := now.Add(time.Duration(h) * time.Hour)
		_, zo := t.In(zone).Zone()
		_, do := t.In(daemon).Zone()
		d := do - zo
		if d%3600 != 0 {
			return nil, fmt.Errorf("%s differs from the cron daemon zone %s by %ds, not a whole number of hours", zone, daemon, d)
		}
		seen[d/3600] = true
	}
	out := make([]int, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Ints(out)
	return out, nil
}

// renderCrontabSchedule converts a normalized 5-field system schedule, with
// an optional CRON_TZ=/TZ= prefix, into its crontab form for daemon.
func renderCrontabSchedule(schedule string, daemon *time.Location, now time.Time) (crontabRendering, error) {
	prefix, rest := splitTZPrefix(schedule)
	if prefix == "" {
		return crontabRendering{Fields: rest}, nil
	}
	zoneName := strings.TrimSpace(prefix[strings.Index(prefix, "=")+1:])
	zone, err := time.LoadLocation(zoneName)
	if err != nil {
		return crontabRendering{}, fmt.Errorf("unknown zone %q: %v", zoneName, err)
	}
	fields := strings.Fields(rest)
	if len(fields) != 5 {
		return crontabRendering{}, fmt.Errorf("system job schedule must have 5 fields after %s, got %d", strings.TrimSpace(prefix), len(fields))
	}
	minF, hourF, domF, monF, dowF := fields[0], fields[1], fields[2], fields[3], fields[4]
	shifts, err := zoneShiftsHours(zone, daemon, now)
	if err != nil {
		return crontabRendering{}, err
	}
	if isStar(hourF) {
		// Every hour: whole-hour offsets don't move minute or day fields.
		return crontabRendering{Fields: rest, Zone: zoneName}, nil
	}
	hours, err := expandField(hourF, 0, 23, nil)
	if err != nil {
		return crontabRendering{}, fmt.Errorf("hour field: %v", err)
	}
	dayRestricted := !isStar(domF) || !isStar(monF) || !isStar(dowF)
	daemonHours := make([]int, 0, len(hours)*len(shifts))
	for _, h := range hours {
		for _, s := range shifts {
			u := h + s
			d := floorDiv(u, 24)
			if d != 0 && dayRestricted {
				return crontabRendering{}, fmt.Errorf("%02d:00 %s falls on a different day in the cron daemon zone %s while day/month fields are restricted; cannot express with vixie cron (use a Go job or a daemon-zone schedule)", h, zoneName, daemon)
			}
			daemonHours = append(daemonHours, u-d*24)
		}
	}
	if len(shifts) == 1 && shifts[0] == 0 {
		return crontabRendering{Fields: rest, Zone: zoneName}, nil // same zone
	}
	return crontabRendering{
		Fields:     strings.Join([]string{minF, formatSet(daemonHours), domF, monF, dowF}, " "),
		Zone:       zoneName,
		GuardHours: hours,
	}, nil
}

// crontabLine renders the managed crontab line for a system job, applying
// any CRON_TZ prefix as described above.
func (s *Scheduler) crontabLine(job *Job) (string, error) {
	return CrontabLine(job, s.crontagMarker)
}

// CrontabLine renders the managed crontab line for a system job (marker is
// usually CrontabMarker).
func CrontabLine(job *Job, marker string) (string, error) {
	return crontabLineIn(job, marker, crontabDaemonLocation(), time.Now())
}

// crontabLineIn is CrontabLine with an explicit daemon zone and reference
// time (migration planning and tests).
func crontabLineIn(job *Job, marker string, daemon *time.Location, now time.Time) (string, error) {
	r, err := renderCrontabSchedule(job.Schedule, daemon, now)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s %s%s %s "+CrontabJobIDFormat, r.Fields, r.Guard(), job.Command, marker, job.ID), nil
}

// lineHasJobID reports whether a crontab line carries the managed marker for
// exactly jobID (not merely a prefix of a longer ID).
func lineHasJobID(line, jobID string) bool {
	marker := fmt.Sprintf(CrontabJobIDFormat, jobID)
	for rest := line; ; {
		i := strings.Index(rest, marker)
		if i < 0 {
			return false
		}
		end := i + len(marker)
		if end == len(rest) || rest[end] == ' ' || rest[end] == '\t' || rest[end] == '\r' {
			return true
		}
		rest = rest[end:]
	}
}

// Crontab change actions reported by PlanCrontabTZUpdate.
const (
	CrontabRewrite      = "rewrite"       // managed line replaced
	CrontabInstall      = "install"       // missing line added (installMissing)
	CrontabNotInstalled = "not-installed" // enabled job has no managed line; left alone
	CrontabDisabled     = "disabled"      // disabled job without a line; nothing to do
)

// CrontabChange describes the crontab effect of one converted system job.
type CrontabChange struct {
	ID, Name string
	Action   string
	Old, New string // full crontab lines ("" when absent)
	Note     string
}

// PlanCrontabTZUpdate re-renders the managed crontab lines of the system jobs
// converted by a TZ migration (Action == TZConvert) and returns the new
// crontab (all other lines untouched, order preserved) plus per-job changes.
// The scheduler never re-syncs system jobs from cron_jobs.json on reload, so
// the migration has to rewrite the crontab itself.
func PlanCrontabTZUpdate(current []string, jobs []TZJobResult, daemon *time.Location, now time.Time, installMissing bool) ([]string, []CrontabChange, error) {
	out := append([]string(nil), current...)
	var changes []CrontabChange
	for _, j := range jobs {
		if j.Type != JobTypeSystem || j.Action != TZConvert {
			continue
		}
		newLine, err := crontabLineIn(&Job{ID: j.ID, Schedule: j.NewSchedule, Command: j.Command}, CrontabMarker, daemon, now)
		if err != nil {
			return nil, nil, fmt.Errorf("job %s: %w", j.ID, err)
		}
		expectOld, _ := crontabLineIn(&Job{ID: j.ID, Schedule: j.OldSchedule, Command: j.Command}, CrontabMarker, daemon, now)
		ch := CrontabChange{ID: j.ID, Name: j.Name, New: newLine}
		var notes []string
		idx := -1
		kept := make([]string, 0, len(out))
		for _, line := range out {
			if lineHasJobID(line, j.ID) {
				if idx >= 0 {
					notes = append(notes, "duplicate managed line removed")
					continue
				}
				idx = len(kept)
				ch.Old = line
			}
			kept = append(kept, line)
		}
		out = kept
		switch {
		case idx >= 0:
			out[idx] = newLine
			ch.Action = CrontabRewrite
			if strings.TrimSpace(ch.Old) != expectOld {
				notes = append(notes, "existing line differed from cron_jobs.json; re-rendered from cron_jobs.json")
			}
		case !j.Enabled:
			ch.Action, ch.New = CrontabDisabled, ""
		case installMissing:
			out = append(out, newLine)
			ch.Action = CrontabInstall
		default:
			ch.Action = CrontabNotInstalled
			notes = append(notes, "enabled but not in the crontab; --install-missing adds it (remove any hand-written duplicate first)")
		}
		ch.Note = strings.Join(notes, "; ")
		changes = append(changes, ch)
	}
	return out, changes, nil
}

// NextCrontabFires simulates what the cron daemon will actually do with a
// system-job schedule once rendered (daemon-zone fields + zone guard) and
// returns its next n fire times after t. Used for dry-run previews and to
// prove the rendering matches the CRON_TZ intent.
func NextCrontabFires(schedule string, daemon *time.Location, after time.Time, n int) ([]time.Time, error) {
	r, err := renderCrontabSchedule(schedule, daemon, after)
	if err != nil {
		return nil, err
	}
	sched, err := cron.ParseStandard(r.Fields)
	if err != nil {
		return nil, err
	}
	var zone *time.Location
	if len(r.GuardHours) > 0 {
		if zone, err = time.LoadLocation(r.Zone); err != nil {
			return nil, err
		}
	}
	allowed := map[int]bool{}
	for _, h := range r.GuardHours {
		allowed[h] = true
	}
	var out []time.Time
	t := after.In(daemon)
	for guard := 0; len(out) < n && guard < 100000; guard++ {
		t = sched.Next(t)
		if t.IsZero() {
			break
		}
		if zone == nil || allowed[t.In(zone).Hour()] {
			out = append(out, t)
		}
	}
	return out, nil
}
