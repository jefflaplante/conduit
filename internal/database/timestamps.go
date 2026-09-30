package database

import (
	"regexp"
	"strings"
	"time"
)

// StoredTimeLayout is the canonical on-disk format for gateway.db timestamp
// text columns (sessions.updated_at since conduit-31jg.24; messages.timestamp
// and sessions.created_at since conduit-a636): UTC with fixed-width
// nanoseconds, so text ORDER BY and < / > comparisons are chronological, and
// modernc parses it back into a time.Time (as UTC) on scan.
const StoredTimeLayout = "2006-01-02 15:04:05.000000000"

// canonicalTimeGlob matches a value already in StoredTimeLayout.
const canonicalTimeGlob = `[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9] [0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]`

// CanonicalTimeGlobSQL returns an SQL expression that is true when column
// holds a value already in StoredTimeLayout.
func CanonicalTimeGlobSQL(column string) string {
	return column + ` GLOB '` + canonicalTimeGlob + `'`
}

// FormatStoredTime renders t in StoredTimeLayout. Bind this string instead of
// a time.Time: without a write format the driver stores t.String() (the
// value's zone plus a monotonic "m=+..." reading), which does not sort
// chronologically.
func FormatStoredTime(t time.Time) string {
	return t.UTC().Format(StoredTimeLayout)
}

// goTimeStringRE matches Go's time.Time.String() output once any monotonic
// "m=±..." reading is removed: date, time, numeric offset, then the zone
// name. The name is only a label ("UTC", "CEST", "+0530", "-03", "LMT"); the
// numeric offset alone fixes the instant, so the name is not parsed.
var goTimeStringRE = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}(?:\.\d{1,9})?) ([+-]\d{4})(?: \S+)?$`)

// storedTimeLayouts are the other formats gateway.db text timestamps may
// hold. Zone-less forms are UTC: that is what SQLite's CURRENT_TIMESTAMP
// writes and how the driver reads them back.
var storedTimeLayouts = []string{
	"2006-01-02 15:04:05.999999999", // canonical, CURRENT_TIMESTAMP
	"2006-01-02 15:04:05.999999999Z07:00",
	time.RFC3339Nano, // also accepts RFC 3339 without fractional seconds
	"2006-01-02 15:04:05.999999999-0700",
	"2006-01-02T15:04:05.999999999-0700",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04",
	"2006-01-02T15:04",
	"2006-01-02",
}

// ParseStoredTime parses a timestamp as stored in gateway.db text columns:
// StoredTimeLayout; Go time.Time.String() output from the driver's default
// time binding, with or without a trailing monotonic "m=+..." reading and
// with any zone name; RFC 3339 variants; the driver's "_time_format=sqlite"
// form; and SQLite CURRENT_TIMESTAMP. Zone-less forms are UTC.
func ParseStoredTime(v string) (time.Time, bool) {
	v = strings.TrimSpace(v)
	if i := strings.Index(v, " m="); i > 0 {
		v = strings.TrimSpace(v[:i])
	}
	if m := goTimeStringRE.FindStringSubmatch(v); m != nil {
		if t, err := time.Parse("2006-01-02 15:04:05.999999999 -0700", m[1]+" "+m[2]); err == nil {
			return t, true
		}
	}
	for _, layout := range storedTimeLayouts {
		if t, err := time.Parse(layout, v); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// canonicalStoredTime returns v rewritten in StoredTimeLayout, or false when
// v does not parse or cannot be written in the fixed-width layout without
// changing its instant (years outside 0000-9999).
func canonicalStoredTime(v string) (string, bool) {
	t, ok := ParseStoredTime(v)
	if !ok {
		return "", false
	}
	if y := t.UTC().Year(); y < 0 || y > 9999 {
		return "", false
	}
	out := FormatStoredTime(t)
	back, err := time.Parse(StoredTimeLayout, out)
	if err != nil || !back.Equal(t) {
		return "", false
	}
	return out, true
}
