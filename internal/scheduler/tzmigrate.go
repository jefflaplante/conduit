package scheduler

// conduit-31jg.60: one-off migration of cron_jobs.json Go jobs from
// expressions written in a server zone (UTC) to the owner's wall-clock zone,
// expressed with the per-job "CRON_TZ=<zone> " prefix that the deployed
// scheduler already understands (conduit-31jg.34). After migration the jobs
// keep their current wall-clock times and no longer drift at DST changes.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
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
}

// TZMigrateOptions configures a migration.
type TZMigrateOptions struct {
	From *time.Location // zone the existing expressions are written in
	To   *time.Location // zone to express them in (CRON_TZ value)
	// Now is the reference instant: the From->To offset in effect at Now
	// defines "today's wall-clock time" for recurring jobs. Zero = time.Now().
	Now time.Time
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

// ConvertScheduleTZ rewrites a Go-job cron expression evaluated in opts.From
// into "CRON_TZ=<To> <expr>" firing at the same wall-clock time in opts.To
// that it fires at today. Returns an errSkip error when no change is needed
// and any other error when the job cannot be converted exactly.
func ConvertScheduleTZ(expr string, opts TZMigrateOptions) (string, []string, error) {
	expr = strings.TrimSpace(expr)
	if p, _ := splitTZPrefix(expr); p != "" {
		return "", nil, errSkip{"already has " + strings.TrimSpace(p) + " prefix"}
	}
	if strings.HasPrefix(expr, "@") {
		switch strings.ToLower(expr) {
		case "@yearly", "@annually":
			expr = "0 0 0 1 1 *"
		case "@monthly":
			expr = "0 0 0 1 * *"
		case "@weekly":
			expr = "0 0 0 * * 0"
		case "@daily", "@midnight":
			expr = "0 0 0 * * *"
		case "@hourly":
			return "", nil, errSkip{"hourly descriptor is zone-independent"}
		default:
			if strings.HasPrefix(expr, "@every") {
				return "", nil, errSkip{"interval schedule is zone-independent"}
			}
			return "", nil, fmt.Errorf("unsupported descriptor %q", expr)
		}
	}
	fields := strings.Fields(expr)
	var sec string
	switch len(fields) {
	case 6:
		sec, fields = fields[0], fields[1:]
	case 5:
	default:
		return "", nil, fmt.Errorf("expected 5 or 6 fields, got %d", len(fields))
	}
	minF, hourF, domF, monF, dowF := fields[0], fields[1], fields[2], fields[3], fields[4]

	if isStar(hourF) {
		return "", nil, errSkip{"hour field is '*': zone-independent"}
	}
	hours, err := expandField(hourF, 0, 23, nil)
	if err != nil {
		return "", nil, fmt.Errorf("hour field: %v", err)
	}
	doms, err := expandField(domF, 1, 31, nil)
	if err != nil {
		return "", nil, fmt.Errorf("day-of-month field: %v", err)
	}
	if _, err := expandField(monF, 1, 12, monthNames); err != nil {
		return "", nil, fmt.Errorf("month field: %v", err)
	}
	dows, err := expandField(dowF, 0, 6, dowNames)
	if err != nil {
		return "", nil, fmt.Errorf("day-of-week field: %v", err)
	}
	oldSched, err := goCronParser.Parse(sixField(sec, fields))
	if err != nil {
		return "", nil, err
	}

	var flags []string
	now := opts.now()
	ref := now
	monthStar := isStar(monF)
	if !monthStar {
		// Date-pinned (e.g. one-shot) jobs: keep the instant they fire at,
		// so take the offset in effect at their next fire, and insist it is
		// the same for every fire in the next year.
		fires := nextN(oldSched, now.In(opts.From), 400*24*time.Hour, 64)
		if len(fires) == 0 {
			return "", nil, errors.New("schedule never fires")
		}
		ref = fires[0]
		base := shiftSeconds(opts.From, opts.To, ref)
		for _, f := range fires[1:] {
			if shiftSeconds(opts.From, opts.To, f) != base {
				return "", nil, fmt.Errorf("month-restricted schedule fires under different UTC offsets (%s vs %s); split it manually", ref.Format("2006-01-02"), f.Format("2006-01-02"))
			}
		}
		flags = append(flags, fmt.Sprintf("date-pinned: offset taken at next fire %s", ref.In(opts.To).Format("2006-01-02 15:04 MST")))
	}
	shift := shiftSeconds(opts.From, opts.To, ref)
	if shift%3600 != 0 {
		return "", nil, fmt.Errorf("zone offset difference %ds is not a whole number of hours", shift)
	}
	shiftH := shift / 3600

	newHours := make([]int, 0, len(hours))
	deltas := map[int]bool{}
	for _, h := range hours {
		p := h + shiftH
		d := floorDiv(p, 24)
		deltas[d] = true
		newHours = append(newHours, p-d*24)
	}
	dayRestricted := !isStar(domF) || !isStar(dowF) || !monthStar
	newDom, newDow := domF, dowF
	if dayRestricted {
		if len(deltas) > 1 {
			return "", nil, fmt.Errorf("hours %s land on different %s days while day fields are restricted; split the job manually", hourF, opts.To)
		}
		var d int
		for k := range deltas {
			d = k
		}
		if d != 0 {
			if !monthStar {
				return "", nil, fmt.Errorf("shift moves the job to the %s day but the month field (%s) is restricted; convert manually", dayWord(d), monF)
			}
			if !isStar(dowF) {
				shifted := make([]int, len(dows))
				for i, v := range dows {
					shifted[i] = ((v+d)%7 + 7) % 7
				}
				newDow = formatSet(shifted)
			}
			if !isStar(domF) {
				shifted := make([]int, len(doms))
				for i, v := range doms {
					nv := v + d
					if v > 28 || nv < 1 || nv > 28 {
						return "", nil, fmt.Errorf("day-of-month %d would cross a month boundary when shifted to the %s day; convert manually", v, dayWord(d))
					}
					shifted[i] = nv
				}
				newDom = formatSet(shifted)
			}
			flags = append(flags, fmt.Sprintf("day fields shifted to the %s day", dayWord(d)))
		}
	}

	newFields := []string{minF, formatSet(newHours), newDom, monF, newDow}
	newExpr := sixField(sec, newFields)
	if sec == "" {
		newExpr = strings.Join(newFields, " ")
	}

	// Verify exactly: under the fixed offsets in effect at ref, the old
	// expression (From) and new expression (To) must fire at identical
	// instants. Guards against any conversion bug producing a wrong schedule.
	newSched, err := goCronParser.Parse(sixField(sec, newFields))
	if err != nil {
		return "", nil, fmt.Errorf("converted expression %q invalid: %v", newExpr, err)
	}
	_, fromOff := ref.In(opts.From).Zone()
	_, toOff := ref.In(opts.To).Zone()
	fixedFrom := time.FixedZone("from", fromOff)
	fixedTo := time.FixedZone("to", toOff)
	a := nextN(oldSched, now.In(fixedFrom), 2*366*24*time.Hour, 2000)
	b := nextN(newSched, now.In(fixedTo), 2*366*24*time.Hour, 2000)
	if !sameInstants(a, b) {
		return "", nil, fmt.Errorf("internal check failed: %q (%s) and %q (%s) disagree; not converting", expr, opts.From, newExpr, opts.To)
	}

	for _, h := range newHours {
		if h < 5 {
			flags = append(flags, fmt.Sprintf("runs in the middle of the night (%02d:%s %s)", h, twoDigit(minF), opts.To))
			break
		}
	}
	return "CRON_TZ=" + opts.To.String() + " " + newExpr, flags, nil
}

func dayWord(d int) string {
	if d < 0 {
		return "previous"
	}
	return "next"
}

func twoDigit(f string) string {
	if n, err := strconv.Atoi(f); err == nil {
		return fmt.Sprintf("%02d", n)
	}
	return f
}

func sixField(sec string, five []string) string {
	if sec == "" {
		sec = "0"
	}
	return sec + " " + strings.Join(five, " ")
}

func shiftSeconds(from, to *time.Location, at time.Time) int {
	_, f := at.In(from).Zone()
	_, t := at.In(to).Zone()
	return t - f
}

func floorDiv(a, b int) int {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// nextN returns up to n fire times of s after t, within horizon.
func nextN(s cron.Schedule, t time.Time, horizon time.Duration, n int) []time.Time {
	end := t.Add(horizon)
	var out []time.Time
	for len(out) < n {
		t = s.Next(t)
		if t.IsZero() || t.After(end) {
			break
		}
		out = append(out, t)
	}
	return out
}

func sameInstants(a, b []time.Time) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !a[i].Equal(b[i]) {
			return false
		}
	}
	return true
}

func isStar(f string) bool { return f == "*" || f == "?" }

var (
	monthNames = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
	dowNames   = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}
)

// expandField expands a robfig/cron field (lists, ranges, steps, names) into
// its sorted set of values.
func expandField(f string, min, max int, names map[string]int) ([]int, error) {
	set := map[int]bool{}
	num := func(s string) (int, error) {
		if v, ok := names[strings.ToLower(s)]; ok {
			return v, nil
		}
		v, err := strconv.Atoi(s)
		if err != nil {
			return 0, fmt.Errorf("bad value %q", s)
		}
		return v, nil
	}
	for _, part := range strings.Split(f, ",") {
		rng, step := part, 1
		if i := strings.Index(part, "/"); i >= 0 {
			rng = part[:i]
			s, err := strconv.Atoi(part[i+1:])
			if err != nil || s <= 0 {
				return nil, fmt.Errorf("bad step in %q", part)
			}
			step = s
		}
		lo, hi := min, max
		switch {
		case isStar(rng):
		case strings.Contains(rng, "-"):
			ab := strings.SplitN(rng, "-", 2)
			var err error
			if lo, err = num(ab[0]); err != nil {
				return nil, err
			}
			if hi, err = num(ab[1]); err != nil {
				return nil, err
			}
		default:
			v, err := num(rng)
			if err != nil {
				return nil, err
			}
			lo, hi = v, v
			if step > 1 { // "a/s" means a..max step s
				hi = max
			}
		}
		if lo < min || hi > max || lo > hi {
			return nil, fmt.Errorf("value out of range in %q (%d-%d)", part, min, max)
		}
		for v := lo; v <= hi; v += step {
			set[v] = true
		}
	}
	out := make([]int, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Ints(out)
	return out, nil
}

// formatSet renders sorted unique values, collapsing runs of 3+ into a-b.
func formatSet(vals []int) string {
	u := map[int]bool{}
	for _, v := range vals {
		u[v] = true
	}
	s := make([]int, 0, len(u))
	for v := range u {
		s = append(s, v)
	}
	sort.Ints(s)
	var parts []string
	for i := 0; i < len(s); {
		j := i
		for j+1 < len(s) && s[j+1] == s[j]+1 {
			j++
		}
		switch {
		case j-i >= 2:
			parts = append(parts, fmt.Sprintf("%d-%d", s[i], s[j]))
		case j == i+1:
			parts = append(parts, strconv.Itoa(s[i]), strconv.Itoa(s[j]))
		default:
			parts = append(parts, strconv.Itoa(s[i]))
		}
		i = j + 1
	}
	return strings.Join(parts, ",")
}

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
		if !j.Enabled {
			r.Flags = append(r.Flags, "disabled")
		}
		if j.OneShot {
			r.Flags = append(r.Flags, "oneshot")
		}
		if j.Type != JobTypeGo {
			r.Action, r.Reason = TZSkip, "system crontab job (runs in the cron daemon's zone; out of scope)"
			if s, err := ParseJobSchedule(j.Schedule, j.Type); err == nil {
				if f := nameClockFlag(j.Name, s, now.In(opts.From), opts.To); f != "" {
					r.Flags = append(r.Flags, f)
				}
			}
			out = append(out, r)
			continue
		}
		newExpr, flags, err := ConvertScheduleTZ(j.Schedule, opts)
		var skip errSkip
		switch {
		case errors.As(err, &skip):
			r.Action, r.Reason = TZSkip, skip.reason
		case err != nil:
			r.Action, r.Reason = TZRefuse, err.Error()
		default:
			r.Action, r.NewSchedule = TZConvert, newExpr
			r.Flags = append(r.Flags, flags...)
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

// scheduleSpans returns, per top-level array element, the byte span of its
// "schedule" string literal ({-1,-1} if absent).
func scheduleSpans(data []byte) ([][2]int, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if t, err := dec.Token(); err != nil || t != json.Delim('[') {
		return nil, errors.New("jobs file is not a JSON array")
	}
	var spans [][2]int
	for dec.More() {
		if t, err := dec.Token(); err != nil || t != json.Delim('{') {
			return nil, fmt.Errorf("job %d is not an object", len(spans))
		}
		sp := [2]int{-1, -1}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, _ := kt.(string)
			before := int(dec.InputOffset())
			var raw json.RawMessage
			if err := dec.Decode(&raw); err != nil {
				return nil, err
			}
			if key != "schedule" {
				continue
			}
			if sp[0] >= 0 {
				return nil, fmt.Errorf("job %d has duplicate schedule keys", len(spans))
			}
			after := int(dec.InputOffset())
			idx := bytes.Index(data[before:after], raw)
			if idx < 0 || len(raw) == 0 || raw[0] != '"' {
				return nil, fmt.Errorf("job %d: schedule is not a string", len(spans))
			}
			sp = [2]int{before + idx, before + idx + len(raw)}
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		spans = append(spans, sp)
	}
	return spans, nil
}

// RewriteSchedules returns data with the schedule of each job index in
// repl replaced, leaving every other byte untouched, and verifies that the
// result differs from data only in those schedule values.
func RewriteSchedules(data []byte, repl map[int]string) ([]byte, error) {
	spans, err := scheduleSpans(data)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	last := 0
	for i, sp := range spans {
		newVal, ok := repl[i]
		if !ok {
			continue
		}
		if sp[0] < 0 {
			return nil, fmt.Errorf("job %d has no schedule", i)
		}
		lit, err := json.Marshal(newVal)
		if err != nil {
			return nil, err
		}
		out.Write(data[last:sp[0]])
		out.Write(lit)
		last = sp[1]
	}
	out.Write(data[last:])
	res := out.Bytes()

	// Semantic check: every job identical except the replaced schedules.
	var before, after []map[string]interface{}
	if err := json.Unmarshal(data, &before); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(res, &after); err != nil {
		return nil, fmt.Errorf("rewritten file is invalid JSON: %w", err)
	}
	if len(before) != len(after) {
		return nil, errors.New("rewrite changed the number of jobs")
	}
	for i := range before {
		if v, ok := repl[i]; ok {
			if after[i]["schedule"] != v {
				return nil, fmt.Errorf("job %d: schedule not rewritten as expected", i)
			}
			after[i]["schedule"] = before[i]["schedule"]
		}
		if !reflect.DeepEqual(before[i], after[i]) {
			return nil, fmt.Errorf("job %d: rewrite changed fields other than schedule", i)
		}
	}
	return res, nil
}

// TZApplyResult reports what ApplyTZMigration did.
type TZApplyResult struct {
	Jobs       []TZJobResult
	Changed    int
	BackupPath string // empty when nothing was written
}

// ApplyTZMigration migrates the jobs file at path in place: it writes a
// timestamped backup next to it, then atomically replaces the file (temp
// file + fsync + rename), preserving its mode and all non-schedule bytes.
// Already-migrated jobs are skipped, so re-running is a no-op.
func ApplyTZMigration(path string, opts TZMigrateOptions) (*TZApplyResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	jobs, err := PlanTZMigration(data, opts)
	if err != nil {
		return nil, err
	}
	res := &TZApplyResult{Jobs: jobs}
	repl := map[int]string{}
	for _, j := range jobs {
		if j.Action == TZConvert {
			repl[j.Index] = j.NewSchedule
		}
	}
	res.Changed = len(repl)
	if len(repl) == 0 {
		return res, nil
	}
	newData, err := RewriteSchedules(data, repl)
	if err != nil {
		return nil, err
	}

	backup := fmt.Sprintf("%s.bak-%s", path, opts.now().UTC().Format("20060102T150405Z"))
	if err := writeFileSync(backup, data, st.Mode().Perm(), true); err != nil {
		return nil, fmt.Errorf("write backup: %w", err)
	}
	res.BackupPath = backup

	// Refuse if something (e.g. the running scheduler's saveJobs) rewrote
	// the file since we read it; the caller can simply re-run.
	if cur, err := os.ReadFile(path); err != nil || !bytes.Equal(cur, data) {
		return nil, fmt.Errorf("%s changed while migrating; nothing written (backup at %s), re-run", path, backup)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".migrate-*")
	if err != nil {
		return nil, err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(newData); err != nil {
		tmp.Close()
		cleanup()
		return nil, err
	}
	if err := tmp.Chmod(st.Mode().Perm()); err != nil {
		tmp.Close()
		cleanup()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return nil, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return nil, err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return res, nil
}

func writeFileSync(name string, data []byte, perm os.FileMode, excl bool) error {
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if excl {
		flags = os.O_WRONLY | os.O_CREATE | os.O_EXCL
	}
	f, err := os.OpenFile(name, flags, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
