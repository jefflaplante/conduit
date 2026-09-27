package brain

import (
	"math"
	"strconv"
	"time"
)

// LTM salience model (conduit-31jg.53).
//
// brain_ltm.salience stores only the BASE salience:
//
//	base = access_score*accessWeight + tier_score(LTM=0.8)*tierWeight (+ REM boost/decay)
//
// Recency is never stored. It is computed whenever a row is scored:
//
//	effective = clamp01(base + recencyWeight * 1/(1 + hours_since_access*recencyDecayRate))
//
// Recall, Get, List, the Situation Awareness sort, cluster expansion, graph
// export, spreading activation and capacity eviction all use the effective
// value, so a fact that hasn't been touched for days no longer carries the
// "just accessed" recency score it had at write time.
//
// Before conduit-31jg.53 every upsert/Get baked recency = 1.0 into the column
// (salience = access + recencyWeight + tier). Migration 9 subtracts that fixed
// recencyWeight contribution once. REM's absolute thresholds (prune < 0.1,
// integration >= 0.5 / > 0.7, the 1.0 boost cap, the 0.0 decay floor) were
// calibrated against those stored values, so REM compares against the PEAK
// salience instead:
//
//	peak = base + recencyWeight   (== effective at the moment of access)
//
// which equals the pre-migration stored value exactly, so REM decisions on
// existing rows do not shift.

// RecencyWeight is the configured recency weight: the constant that separates
// stored base salience from peak salience.
func (b *Brain) RecencyWeight() float64 { return b.recencyWeight }

// sqlFloat renders a config float as a SQL literal. Values come from typed
// config, never user input, so inlining them is safe and keeps the dozen
// queries that score salience free of positional-parameter bookkeeping.
func sqlFloat(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "0.0"
	}
	s := strconv.FormatFloat(f, 'f', -1, 64)
	for _, c := range s {
		if c == '.' {
			return s
		}
	}
	return s + ".0"
}

// EffectiveSalienceSQL returns a SQL expression (over brain_ltm columns
// salience and accessed_at) for query-time effective salience.
func (b *Brain) EffectiveSalienceSQL() string {
	return "MIN(1.0, MAX(0.0, salience + " + sqlFloat(b.recencyWeight) +
		" / (1.0 + MAX(0.0, (julianday('now') - julianday(accessed_at)) * 24.0) * " +
		sqlFloat(b.recencyDecayRate) + ")))"
}

// PeakSalienceSQL returns a SQL expression for the salience a row has at the
// moment it is accessed (base + recencyWeight). REM phases compare it against
// their historical absolute thresholds.
func (b *Brain) PeakSalienceSQL() string {
	return "(salience + " + sqlFloat(b.recencyWeight) + ")"
}

// ltmBaseSalienceSQL is the stored base salience for a row whose access count
// is accessCountExpr: access term + LTM tier term, no recency.
func (b *Brain) ltmBaseSalienceSQL(accessCountExpr string) string {
	n := b.accessCountCap
	if n <= 0 {
		n = 1
	}
	return "(MIN(CAST(" + accessCountExpr + " AS REAL) / " + sqlFloat(float64(n)) + ", 1.0) * " +
		sqlFloat(b.accessWeight) + " + 0.8 * " + sqlFloat(b.tierWeight) + ")"
}

// ltmBaseSalience is ltmBaseSalienceSQL in Go.
func (b *Brain) ltmBaseSalience(accessCount int) float64 {
	n := b.accessCountCap
	if n <= 0 {
		n = 1
	}
	return math.Min(float64(accessCount)/float64(n), 1.0)*b.accessWeight + 0.8*b.tierWeight
}

// effectiveSalience is EffectiveSalienceSQL in Go.
func (b *Brain) effectiveSalience(base float64, accessedAt, now time.Time) float64 {
	hours := now.Sub(accessedAt).Hours()
	if hours < 0 {
		hours = 0
	}
	v := base + b.recencyWeight/(1.0+hours*b.recencyDecayRate)
	return math.Min(1.0, math.Max(0.0, v))
}
