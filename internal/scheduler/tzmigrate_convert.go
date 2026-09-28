package scheduler

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

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
