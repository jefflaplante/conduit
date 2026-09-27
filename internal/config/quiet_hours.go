package config

import "time"

// conduit-31jg.33: this file is the single quiet-hours implementation. All
// callers (heartbeat GatewayIntegration) go through these
// methods so the decision always uses the configured timezone and wall-clock
// arithmetic. The previous IsQuietTime used startOfDay.Add(duration), which is
// off by an hour on DST transition days, and excluded the start boundary.

// quietBounds returns the quiet window as minutes past local midnight.
// ok is false when either bound is unparseable.
func (a AgentHeartbeatConfig) quietBounds() (start, end int, ok bool) {
	s, err := time.Parse("15:04", a.QuietHours.StartTime)
	if err != nil {
		return 0, 0, false
	}
	e, err := time.Parse("15:04", a.QuietHours.EndTime)
	if err != nil {
		return 0, 0, false
	}
	return s.Hour()*60 + s.Minute(), e.Hour()*60 + e.Minute(), true
}

// IsQuietTime reports whether t falls inside quiet hours, evaluated on the
// wall clock of the configured timezone. The window is [start, end): the
// start minute is quiet, the end minute is not. A window whose end precedes
// its start spans midnight; start == end is an empty window.
func (a AgentHeartbeatConfig) IsQuietTime(t time.Time) bool {
	if !a.QuietEnabled {
		return false
	}
	start, end, ok := a.quietBounds()
	if !ok || start == end {
		return false
	}
	lt := t.In(a.GetLocation())
	m := lt.Hour()*60 + lt.Minute()
	if start < end {
		return m >= start && m < end
	}
	return m >= start || m < end
}

// NextQuietEnd returns the first instant strictly after t at which the
// end-of-quiet wall-clock time occurs in the configured timezone.
func (a AgentHeartbeatConfig) NextQuietEnd(t time.Time) time.Time {
	return a.nextWallClock(t, a.QuietHours.EndTime, 8, 0)
}

// NextQuietStart returns the first instant strictly after t at which quiet
// hours begin in the configured timezone.
func (a AgentHeartbeatConfig) NextQuietStart(t time.Time) time.Time {
	return a.nextWallClock(t, a.QuietHours.StartTime, 23, 0)
}

// nextWallClock builds candidates with time.Date (never Add) so the result
// lands on the requested wall-clock time even across DST changes.
func (a AgentHeartbeatConfig) nextWallClock(t time.Time, hhmm string, defH, defM int) time.Time {
	h, m := defH, defM
	if p, err := time.Parse("15:04", hhmm); err == nil {
		h, m = p.Hour(), p.Minute()
	}
	loc := a.GetLocation()
	y, mo, d := t.In(loc).Date()
	for i := 0; i < 2; i++ {
		if c := time.Date(y, mo, d+i, h, m, 0, 0, loc); c.After(t) {
			return c
		}
	}
	return time.Date(y, mo, d+2, h, m, 0, 0, loc)
}
