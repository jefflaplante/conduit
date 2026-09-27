package gateway

import (
	"path/filepath"
	"testing"
	"time"

	"conduit/internal/brain"
	"conduit/internal/config"
)

// conduit-31jg.53: the LTM eviction grace window and per-user WM cap are
// configurable and reach the Brain.
func TestBrainCapacityOptions(t *testing.T) {
	cases := []struct {
		name      string
		grace     int
		wmCap     int
		wantGrace time.Duration
		wantCap   int
	}{
		{"defaults (omitted)", 0, 0, brain.DefaultLTMEvictionGrace, brain.DefaultMaxWMEntriesPerUser},
		{"explicit", 120, 50, 2 * time.Minute, 50},
		{"disabled", -1, -1, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := append([]brain.Option{brain.WithAutoFlushInterval(0)},
				brainCapacityOptions(config.BrainConfig{LTMEvictionGraceSeconds: tc.grace, MaxWMEntriesPerUser: tc.wmCap})...)
			b, err := brain.New(filepath.Join(t.TempDir(), "b.db"), opts...)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			if got := b.LTMEvictionGrace(); got != tc.wantGrace {
				t.Errorf("grace = %v, want %v", got, tc.wantGrace)
			}
			if got := b.MaxWMEntriesPerUser(); got != tc.wantCap {
				t.Errorf("wm cap = %d, want %d", got, tc.wantCap)
			}
		})
	}
}
