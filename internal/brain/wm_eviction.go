package brain

import (
	"log"
	"math"
	"sort"
	"time"
)

// DefaultMaxWMEntriesPerUser is the default per-user working-memory cap.
// conduit-31jg.29
const DefaultMaxWMEntriesPerUser = 1000

// wmEvictMinIdle is how long a WM entry must go untouched before it can be
// evicted by score (autoFlush / Consolidate). conduit-31jg.29
const wmEvictMinIdle = time.Hour

// wmRef is a snapshot of a WM entry plus the bucket it lives in.
type wmRef struct {
	userID string
	entry  Entry
}

// wmEvictScore is the part of a WM entry's salience that actually varies
// between WM entries: access*accessWeight + recency*recencyWeight.
//
// conduit-31jg.29: eviction used to compare full salience against
// evictThreshold, but full salience includes the constant tier term
// (0.5*tierWeight = 0.1 by default), so with the default threshold of 0.1
// `salience < evictThreshold` was unreachable and WM only drained via TTL or
// promotion. The tier term carries no information when comparing WM entries
// with each other, so it is excluded here. With defaults, an entry touched
// once is evictable after ~3.2h idle; one touched 10 times after ~5.7h;
// 25+ touches keep it indefinitely (it will have been promoted as hot).
func (b *Brain) wmEvictScore(e *Entry, now time.Time) float64 {
	accessScore := 0.0
	if b.accessCountCap > 0 {
		accessScore = math.Min(float64(e.AccessCount)/float64(b.accessCountCap), 1.0)
	}
	hoursSince := now.Sub(e.AccessedAt).Hours()
	if hoursSince < 0 {
		hoursSince = 0
	}
	recencyScore := 1.0 / (1.0 + hoursSince*b.recencyDecayRate)
	return accessScore*b.accessWeight + recencyScore*b.recencyWeight
}

// wmEvictable reports whether a WM entry is cold enough to leave WM.
func (b *Brain) wmEvictable(e *Entry, now time.Time) bool {
	return now.Sub(e.AccessedAt) > wmEvictMinIdle && b.wmEvictScore(e, now) < b.evictThreshold
}

// wmIsHot mirrors REM's heat-promotion rule: an entry accessed at least
// heatPromotionThreshold times is worth keeping in LTM regardless of salience.
func (b *Brain) wmIsHot(e *Entry) bool {
	return b.heatPromotionThreshold > 0 && e.AccessCount >= b.heatPromotionThreshold
}

// enforceWMCapLocked trims userID's WM bucket to maxWMEntriesPerUser, never
// choosing a key in protect. Victims are the lowest wmEvictScore (oldest
// AccessedAt breaks ties). Hot victims are returned for promotion to LTM when
// autoPromote is on; everything else is dropped. Caller holds b.mu.
// conduit-31jg.29
func (b *Brain) enforceWMCapLocked(userID string, protect map[string]bool, now time.Time) []Entry {
	if b.maxWMEntriesPerUser <= 0 {
		return nil
	}
	wm := b.working[userID]
	excess := len(wm) - b.maxWMEntriesPerUser
	if excess <= 0 {
		return nil
	}
	type cand struct {
		e     *Entry
		score float64
	}
	cands := make([]cand, 0, len(wm))
	for k, e := range wm {
		if protect[k] {
			continue
		}
		cands = append(cands, cand{e, b.wmEvictScore(e, now)})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score < cands[j].score
		}
		return cands[i].e.AccessedAt.Before(cands[j].e.AccessedAt)
	})
	if excess > len(cands) {
		excess = len(cands)
	}
	var rescue []Entry
	for _, c := range cands[:excess] {
		if b.autoPromote && b.wmIsHot(c.e) {
			rescue = append(rescue, *c.e)
		}
		delete(wm, c.e.Key)
	}
	log.Printf("Brain: WM cap (%d) reached for user %q — evicted %d entries (%d promoted to LTM)",
		b.maxWMEntriesPerUser, userID, excess, len(rescue))
	return rescue
}

// promoteEvicted writes cap-evicted hot WM entries to LTM. Must be called
// without b.mu held. Best-effort: failures are logged.
func (b *Brain) promoteEvicted(entries []Entry) {
	for _, e := range entries {
		if err := b.storeLTM(e.Key, e.Value, e.Source, time.Now(), e.ExpiresAt); err != nil {
			log.Printf("Brain: failed to promote cap-evicted WM key %q: %v", e.Key, err)
		}
	}
}

// promoteThenDrop promotes each snapshot to LTM and, only on success, removes
// the WM entry if it is unchanged since the snapshot (a concurrent write keeps
// it in WM). Must be called without b.mu held.
func (b *Brain) promoteThenDrop(refs []wmRef) {
	for _, r := range refs {
		if err := b.storeLTM(r.entry.Key, r.entry.Value, r.entry.Source, time.Now(), r.entry.ExpiresAt); err != nil {
			log.Printf("Brain: failed to promote idle hot WM key %q (kept in WM): %v", r.entry.Key, err)
			continue
		}
		b.mu.Lock()
		if wm, ok := b.working[r.userID]; ok {
			if live, ok := wm[r.entry.Key]; ok && live.Value == r.entry.Value && live.AccessedAt.Equal(r.entry.AccessedAt) {
				delete(wm, r.entry.Key)
				if len(wm) == 0 {
					delete(b.working, r.userID)
				}
			}
		}
		b.mu.Unlock()
	}
}
