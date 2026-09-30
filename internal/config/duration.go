package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// Duration is a time.Duration that reads from JSON as either a Go duration
// string ("30s", "5m", "1h30m") or an integer count of nanoseconds, the
// form older configs used. It is written back as a string (conduit-2lr5).
type Duration time.Duration

// Duration returns d as a time.Duration.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// String formats d like time.Duration.
func (d Duration) String() string { return time.Duration(d).String() }

// MarshalJSON writes d as a duration string, e.g. "5m0s".
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON accepts a duration string or integer nanoseconds. null
// leaves d unchanged, like encoding/json does for other types.
func (d *Duration) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if bytes.Equal(b, []byte("null")) {
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		v, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("invalid duration %q (use e.g. \"30s\", \"5m\", \"1h\"): %w", s, err)
		}
		*d = Duration(v)
		return nil
	}
	if len(b) > 0 && (b[0] == '-' || (b[0] >= '0' && b[0] <= '9')) {
		n, err := strconv.ParseInt(string(b), 10, 64)
		if err != nil {
			return fmt.Errorf("invalid duration %s: numbers are integer nanoseconds; prefer a string such as \"30s\"", b)
		}
		*d = Duration(n)
		return nil
	}
	return fmt.Errorf("invalid duration %s: want a string such as \"30s\" or integer nanoseconds", b)
}
