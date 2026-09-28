package monitoring

import (
	"sync/atomic"
	"time"
)

// ConfigUpdateOutcome is the terminal outcome of one live config update
// (Gateway tool update_config, conduit-rmho / conduit-2qes). The set is
// fixed so the Prometheus label stays bounded; no key paths or values are
// ever recorded.
type ConfigUpdateOutcome string

const (
	// ConfigUpdatePlanned: a valid plan with changes was built and is
	// awaiting approval (not terminal; counted separately).
	ConfigUpdatePlanned ConfigUpdateOutcome = "planned"
	// ConfigUpdateAppliedLive: saved, and every changed key applied live.
	ConfigUpdateAppliedLive ConfigUpdateOutcome = "applied_live"
	// ConfigUpdateAppliedRestartRequired: saved; at least one changed key
	// only takes effect after a restart.
	ConfigUpdateAppliedRestartRequired ConfigUpdateOutcome = "applied_restart_required"
	// ConfigUpdateUnchanged: every requested key already had its value.
	ConfigUpdateUnchanged ConfigUpdateOutcome = "unchanged"
	// ConfigUpdateRejectedInvalid: the patch failed parsing, the secret
	// check or validation; nothing changed.
	ConfigUpdateRejectedInvalid ConfigUpdateOutcome = "rejected_invalid"
	// ConfigUpdateRejectedConflict: the file changed on disk between read
	// and write; nothing changed.
	ConfigUpdateRejectedConflict ConfigUpdateOutcome = "rejected_conflict"
	// ConfigUpdateFailed: reading, building (providers, call log) or
	// persisting failed; nothing changed.
	ConfigUpdateFailed ConfigUpdateOutcome = "failed"
	// ConfigUpdateApprovalDenied: the owner denied the approval prompt.
	ConfigUpdateApprovalDenied ConfigUpdateOutcome = "approval_denied"
	// ConfigUpdateApprovalExpired: the approval prompt expired unanswered.
	ConfigUpdateApprovalExpired ConfigUpdateOutcome = "approval_expired"
)

// ConfigUpdateOutcomes lists every outcome in a stable order (for
// exposition).
var ConfigUpdateOutcomes = []ConfigUpdateOutcome{
	ConfigUpdatePlanned,
	ConfigUpdateAppliedLive,
	ConfigUpdateAppliedRestartRequired,
	ConfigUpdateUnchanged,
	ConfigUpdateRejectedInvalid,
	ConfigUpdateRejectedConflict,
	ConfigUpdateFailed,
	ConfigUpdateApprovalDenied,
	ConfigUpdateApprovalExpired,
}

// ConfigUpdateMetrics counts live config updates by outcome. The zero value
// is ready to use and it is safe for concurrent use (lock-free).
type ConfigUpdateMetrics struct {
	planned, appliedLive, appliedRestart, unchanged atomic.Int64
	rejectedInvalid, rejectedConflict, failed       atomic.Int64
	approvalDenied, approvalExpired                 atomic.Int64
	providersRebuilt                                atomic.Int64
	lastAppliedUnixNano                             atomic.Int64
}

func (m *ConfigUpdateMetrics) counter(o ConfigUpdateOutcome) *atomic.Int64 {
	switch o {
	case ConfigUpdatePlanned:
		return &m.planned
	case ConfigUpdateAppliedLive:
		return &m.appliedLive
	case ConfigUpdateAppliedRestartRequired:
		return &m.appliedRestart
	case ConfigUpdateUnchanged:
		return &m.unchanged
	case ConfigUpdateRejectedInvalid:
		return &m.rejectedInvalid
	case ConfigUpdateRejectedConflict:
		return &m.rejectedConflict
	case ConfigUpdateFailed:
		return &m.failed
	case ConfigUpdateApprovalDenied:
		return &m.approvalDenied
	case ConfigUpdateApprovalExpired:
		return &m.approvalExpired
	}
	return nil
}

// Record counts one outcome. Unknown outcomes are ignored.
func (m *ConfigUpdateMetrics) Record(o ConfigUpdateOutcome) {
	if c := m.counter(o); c != nil {
		c.Add(1)
	}
}

// RecordApplied counts a successful apply: its outcome (applied_live or
// applied_restart_required), the number of provider instances it rebuilt,
// and the apply time.
func (m *ConfigUpdateMetrics) RecordApplied(o ConfigUpdateOutcome, providersRebuilt int, at time.Time) {
	m.Record(o)
	if providersRebuilt > 0 {
		m.providersRebuilt.Add(int64(providersRebuilt))
	}
	m.lastAppliedUnixNano.Store(at.UnixNano())
}

// ConfigUpdateSnapshot is a point-in-time copy of ConfigUpdateMetrics.
type ConfigUpdateSnapshot struct {
	Planned                int64 `json:"planned_total"`
	AppliedLive            int64 `json:"applied_live_total"`
	AppliedRestartRequired int64 `json:"applied_restart_required_total"`
	Unchanged              int64 `json:"unchanged_total"`
	RejectedInvalid        int64 `json:"rejected_invalid_total"`
	RejectedConflict       int64 `json:"rejected_conflict_total"`
	Failed                 int64 `json:"failed_total"`
	ApprovalDenied         int64 `json:"approval_denied_total"`
	ApprovalExpired        int64 `json:"approval_expired_total"`
	// ProvidersRebuilt counts provider instances rebuilt by live updates.
	ProvidersRebuilt int64 `json:"providers_rebuilt_total"`
	// LastApplied is the time of the last successful apply; nil if none.
	LastApplied *time.Time `json:"last_applied,omitempty"`
}

// Snapshot returns the current counters.
func (m *ConfigUpdateMetrics) Snapshot() ConfigUpdateSnapshot {
	s := ConfigUpdateSnapshot{
		Planned:                m.planned.Load(),
		AppliedLive:            m.appliedLive.Load(),
		AppliedRestartRequired: m.appliedRestart.Load(),
		Unchanged:              m.unchanged.Load(),
		RejectedInvalid:        m.rejectedInvalid.Load(),
		RejectedConflict:       m.rejectedConflict.Load(),
		Failed:                 m.failed.Load(),
		ApprovalDenied:         m.approvalDenied.Load(),
		ApprovalExpired:        m.approvalExpired.Load(),
		ProvidersRebuilt:       m.providersRebuilt.Load(),
	}
	if ns := m.lastAppliedUnixNano.Load(); ns != 0 {
		t := time.Unix(0, ns).UTC()
		s.LastApplied = &t
	}
	return s
}

// Count returns the counter for outcome o in the snapshot.
func (s ConfigUpdateSnapshot) Count(o ConfigUpdateOutcome) int64 {
	switch o {
	case ConfigUpdatePlanned:
		return s.Planned
	case ConfigUpdateAppliedLive:
		return s.AppliedLive
	case ConfigUpdateAppliedRestartRequired:
		return s.AppliedRestartRequired
	case ConfigUpdateUnchanged:
		return s.Unchanged
	case ConfigUpdateRejectedInvalid:
		return s.RejectedInvalid
	case ConfigUpdateRejectedConflict:
		return s.RejectedConflict
	case ConfigUpdateFailed:
		return s.Failed
	case ConfigUpdateApprovalDenied:
		return s.ApprovalDenied
	case ConfigUpdateApprovalExpired:
		return s.ApprovalExpired
	}
	return 0
}

// ToMap renders the snapshot for tool responses (Gateway status).
func (s ConfigUpdateSnapshot) ToMap() map[string]interface{} {
	m := make(map[string]interface{}, len(ConfigUpdateOutcomes)+2)
	for _, o := range ConfigUpdateOutcomes {
		m[string(o)+"_total"] = s.Count(o)
	}
	m["providers_rebuilt_total"] = s.ProvidersRebuilt
	if s.LastApplied != nil {
		m["last_applied"] = s.LastApplied.Format(time.RFC3339)
	}
	return m
}
