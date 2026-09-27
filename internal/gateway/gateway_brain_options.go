package gateway

import (
	"time"

	"conduit/internal/brain"
	"conduit/internal/config"
)

// brainCapacityOptions maps the Brain capacity knobs (LTM eviction grace
// window, per-user working-memory cap) from config onto brain options.
// conduit-31jg.53
//
//   - ltm_eviction_grace_seconds: 0 = brain default (1h); negative = no grace
//     beyond the write's own second.
//   - max_wm_entries_per_user: 0 = brain default (1000); negative = unbounded.
func brainCapacityOptions(bc config.BrainConfig) []brain.Option {
	var opts []brain.Option
	switch {
	case bc.LTMEvictionGraceSeconds > 0:
		opts = append(opts, brain.WithLTMEvictionGrace(time.Duration(bc.LTMEvictionGraceSeconds)*time.Second))
	case bc.LTMEvictionGraceSeconds < 0:
		opts = append(opts, brain.WithLTMEvictionGrace(0))
	}
	switch {
	case bc.MaxWMEntriesPerUser > 0:
		opts = append(opts, brain.WithMaxWMEntriesPerUser(bc.MaxWMEntriesPerUser))
	case bc.MaxWMEntriesPerUser < 0:
		opts = append(opts, brain.WithMaxWMEntriesPerUser(0))
	}
	return opts
}
