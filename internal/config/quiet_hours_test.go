package config

import (
	"testing"
	"time"
)

// conduit-31jg.33: quiet hours must be evaluated on the wall clock of the
// configured timezone, including on DST transition days, with an inclusive
// start and exclusive end.

func laQuietCfg(start, end string) AgentHeartbeatConfig {
	return AgentHeartbeatConfig{
		Timezone:     "America/Los_Angeles",
		QuietEnabled: true,
		QuietHours:   QuietHoursConfig{StartTime: start, EndTime: end},
	}
}

func mustLA(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatalf("load LA: %v", err)
	}
	return loc
}

func TestIsQuietTime_BoundaryInclusivity(t *testing.T) {
	cfg := laQuietCfg("22:00", "06:00")
	loc := mustLA(t)
	cases := []struct {
		h, m, s int
		want    bool
	}{
		{21, 59, 59, false},
		{22, 0, 0, true}, // start is inclusive
		{22, 0, 1, true},
		{5, 59, 59, true},
		{6, 0, 0, false}, // end is exclusive
		{12, 0, 0, false},
	}
	for _, c := range cases {
		ts := time.Date(2026, 6, 10, c.h, c.m, c.s, 0, loc)
		if got := cfg.IsQuietTime(ts); got != c.want {
			t.Errorf("%s: IsQuietTime=%v want %v", ts.Format("15:04:05"), got, c.want)
		}
	}
}

func TestIsQuietTime_SameDayWindow(t *testing.T) {
	cfg := laQuietCfg("13:00", "14:30")
	loc := mustLA(t)
	for _, c := range []struct {
		h, m int
		want bool
	}{{12, 59, false}, {13, 0, true}, {14, 29, true}, {14, 30, false}} {
		ts := time.Date(2026, 6, 10, c.h, c.m, 0, 0, loc)
		if got := cfg.IsQuietTime(ts); got != c.want {
			t.Errorf("%02d:%02d: got %v want %v", c.h, c.m, got, c.want)
		}
	}
}

func TestIsQuietTime_EqualStartEndIsNeverQuiet(t *testing.T) {
	cfg := laQuietCfg("08:00", "08:00")
	loc := mustLA(t)
	for h := 0; h < 24; h++ {
		if cfg.IsQuietTime(time.Date(2026, 6, 10, h, 0, 0, 0, loc)) {
			t.Fatalf("hour %d reported quiet for empty window", h)
		}
	}
}

// Spring forward: 2026-03-08 02:00 PST -> 03:00 PDT. The old startOfDay.Add
// implementation placed 22:00 at 23:00 PDT and 06:00 at 07:00 PDT.
func TestIsQuietTime_DSTSpringForward_LosAngeles(t *testing.T) {
	cfg := laQuietCfg("22:00", "06:00")
	loc := mustLA(t)
	cases := []struct {
		ts   time.Time
		want bool
	}{
		{time.Date(2026, 3, 8, 22, 30, 0, 0, loc), true}, // PDT evening, after start
		{time.Date(2026, 3, 8, 21, 59, 0, 0, loc), false},
		{time.Date(2026, 3, 8, 6, 30, 0, 0, loc), false}, // PDT morning, after end
		{time.Date(2026, 3, 8, 5, 59, 0, 0, loc), true},
		{time.Date(2026, 3, 8, 3, 15, 0, 0, loc), true}, // just after the gap
	}
	for _, c := range cases {
		if got := cfg.IsQuietTime(c.ts); got != c.want {
			t.Errorf("%s: got %v want %v", c.ts.Format(time.RFC3339), got, c.want)
		}
	}
}

// Fall back: 2026-11-01 02:00 PDT -> 01:00 PST. The old implementation placed
// 22:00 at 21:00 PST and 06:00 at 05:00 PST.
func TestIsQuietTime_DSTFallBack_LosAngeles(t *testing.T) {
	cfg := laQuietCfg("22:00", "06:00")
	loc := mustLA(t)
	cases := []struct {
		ts   time.Time
		want bool
	}{
		{time.Date(2026, 11, 1, 21, 30, 0, 0, loc), false},
		{time.Date(2026, 11, 1, 22, 0, 0, 0, loc), true},
		{time.Date(2026, 11, 1, 5, 30, 0, 0, loc), true},
		{time.Date(2026, 11, 1, 6, 0, 0, 0, loc), false},
	}
	for _, c := range cases {
		if got := cfg.IsQuietTime(c.ts); got != c.want {
			t.Errorf("%s: got %v want %v", c.ts.Format(time.RFC3339), got, c.want)
		}
	}
	// Both passes through the repeated 01:xx hour are quiet.
	first := time.Date(2026, 11, 1, 8, 30, 0, 0, time.UTC)  // 01:30 PDT
	second := time.Date(2026, 11, 1, 9, 30, 0, 0, time.UTC) // 01:30 PST
	if !cfg.IsQuietTime(first) || !cfg.IsQuietTime(second) {
		t.Errorf("repeated 01:30 hour must be quiet on both passes")
	}
}

// Evaluated from a UTC clock (container/systemd without TZ), the configured
// zone must still drive the decision.
func TestIsQuietTime_UsesConfiguredZoneNotServerZone(t *testing.T) {
	cfg := laQuietCfg("20:00", "06:00")
	// 2026-09-26 04:00 UTC == 2026-09-25 21:00 PDT -> quiet.
	if !cfg.IsQuietTime(time.Date(2026, 9, 26, 4, 0, 0, 0, time.UTC)) {
		t.Error("21:00 PDT should be quiet")
	}
	// 2026-09-26 23:00 UTC == 16:00 PDT -> not quiet (old hardcoded 22-08 UTC logic said quiet).
	if cfg.IsQuietTime(time.Date(2026, 9, 26, 23, 0, 0, 0, time.UTC)) {
		t.Error("16:00 PDT should not be quiet")
	}
}

func TestNextQuietEnd(t *testing.T) {
	cfg := laQuietCfg("22:00", "06:00")
	loc := mustLA(t)
	cases := []struct {
		now, want time.Time
	}{
		// Evening: ends tomorrow morning.
		{time.Date(2026, 6, 10, 23, 0, 0, 0, loc), time.Date(2026, 6, 11, 6, 0, 0, 0, loc)},
		// After midnight: ends this morning.
		{time.Date(2026, 6, 11, 2, 0, 0, 0, loc), time.Date(2026, 6, 11, 6, 0, 0, 0, loc)},
		// Across spring-forward night: must be 06:00 PDT, not 07:00.
		{time.Date(2026, 3, 7, 23, 0, 0, 0, loc), time.Date(2026, 3, 8, 6, 0, 0, 0, loc)},
		// Across fall-back night: must be 06:00 PST, not 05:00.
		{time.Date(2026, 10, 31, 23, 0, 0, 0, loc), time.Date(2026, 11, 1, 6, 0, 0, 0, loc)},
	}
	for _, c := range cases {
		got := cfg.NextQuietEnd(c.now)
		if !got.Equal(c.want) {
			t.Errorf("NextQuietEnd(%s)=%s want %s", c.now.Format(time.RFC3339), got.Format(time.RFC3339), c.want.Format(time.RFC3339))
		}
		if got.In(loc).Hour() != 6 {
			t.Errorf("NextQuietEnd wall clock hour=%d want 6", got.In(loc).Hour())
		}
	}
}

func TestNextQuietStart(t *testing.T) {
	cfg := laQuietCfg("22:00", "06:00")
	loc := mustLA(t)
	got := cfg.NextQuietStart(time.Date(2026, 3, 8, 12, 0, 0, 0, loc))
	want := time.Date(2026, 3, 8, 22, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Errorf("got %s want %s", got, want)
	}
	// Exactly at start: the next start is tomorrow.
	got = cfg.NextQuietStart(want)
	if !got.Equal(time.Date(2026, 3, 9, 22, 0, 0, 0, loc)) {
		t.Errorf("at-start: got %s", got)
	}
}
