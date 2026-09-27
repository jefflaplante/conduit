package brain

import "time"

// MaxWMEntriesPerUser returns the per-user working-memory cap (<= 0 means
// unbounded). conduit-31jg.53
func (b *Brain) MaxWMEntriesPerUser() int { return b.maxWMEntriesPerUser }

// LTMEvictionGrace returns how long a freshly written/accessed LTM row is
// immune from capacity eviction. conduit-31jg.53
func (b *Brain) LTMEvictionGrace() time.Duration { return b.ltmEvictionGrace }
