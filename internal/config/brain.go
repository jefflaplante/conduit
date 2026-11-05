package config

import (
	"fmt"
	"path/filepath"
	"strings"
)

// DeriveBrainDBPath returns a brain DB path derived from the gateway DB path.
// For example, "gateway.db" becomes "gateway.brain.db".
func DeriveBrainDBPath(gatewayDBPath string) string {
	ext := filepath.Ext(gatewayDBPath)
	base := strings.TrimSuffix(gatewayDBPath, ext)
	if ext == "" {
		ext = ".db"
	}
	return base + ".brain" + ext
}

// BrainConfig holds configuration for the tiered memory (Brain) subsystem.
type BrainConfig struct {
	Enabled              bool    `json:"enabled"`
	Path                 string  `json:"path,omitempty"`                    // Path to brain DB file (derived from gateway DB if empty)
	MaxLTMEntries        int     `json:"max_ltm_entries,omitempty"`         // Maximum long-term memory entries (default 10000)
	WMGracePeriodSeconds int     `json:"wm_grace_period_seconds,omitempty"` // Seconds to keep WM after session end (default 300)
	AutoFlushSeconds     int     `json:"auto_flush_seconds,omitempty"`      // Auto-flush interval in seconds (default 600)
	ConsolidateThreshold float64 `json:"consolidate_threshold,omitempty"`   // Salience threshold for auto-promote (default 0.6)
	EvictThreshold       float64 `json:"evict_threshold,omitempty"`         // Salience threshold for eviction (default 0.1)
	AutoPromote          bool    `json:"auto_promote,omitempty"`            // Auto-promote high-salience WM keys on consolidation

	// Salience formula weights (must sum to 1.0)
	AccessWeight  float64 `json:"access_weight,omitempty"`  // default 0.4
	RecencyWeight float64 `json:"recency_weight,omitempty"` // default 0.4
	TierWeight    float64 `json:"tier_weight,omitempty"`    // default 0.2

	// Recency decay rate: 1/(1 + hours * decay_rate). Higher = faster decay
	RecencyDecayRate float64 `json:"recency_decay_rate,omitempty"` // default 1.0

	// Access count normalization cap
	AccessCountCap int `json:"access_count_cap,omitempty"` // default 100

	// REM Sleep configuration
	REMEnabled           bool    `json:"rem_enabled,omitempty"`
	REMSchedule          string  `json:"rem_schedule,omitempty"`
	REMIntegrationDay    int     `json:"rem_integration_day,omitempty"`
	REMPruneAgeDays      int     `json:"rem_prune_age_days,omitempty"`
	REMSalienceDecayRate float64 `json:"rem_salience_decay_rate,omitempty"`
	REMGroomWithLLM      bool    `json:"rem_groom_with_llm,omitempty"`
	REMLogPath           string  `json:"rem_log_path,omitempty"`

	// Warmth-floor injection: recall appends up to WarmthInjectLimit high-warmth
	// LTM entries that didn't match the query keywords, at the tail of results.
	WarmthInjectFloor float64 `json:"warmth_inject_floor,omitempty"` // min warmth to qualify (default 0.7)
	WarmthInjectLimit int     `json:"warmth_inject_limit,omitempty"` // max injected per recall (default 2; 0 disables)

	// LTMEvictionGraceSeconds: a freshly written or accessed LTM row is immune
	// from capacity eviction for this long. 0/omitted = default (3600);
	// negative = no window beyond the write's own second. conduit-31jg.53
	LTMEvictionGraceSeconds int `json:"ltm_eviction_grace_seconds,omitempty"`
	// MaxWMEntriesPerUser caps each user's working-memory bucket (lowest-value
	// entries are evicted, hot ones promoted to LTM first). 0/omitted =
	// default (1000); negative = unbounded. conduit-31jg.53
	MaxWMEntriesPerUser int `json:"max_wm_entries_per_user,omitempty"`

	// DashboardEnabled toggles the /dashboard/brain memory-graph dashboard
	// and its backing /api/brain/graph endpoint. Off by default.
	DashboardEnabled bool `json:"dashboard_enabled,omitempty"`

	// SessionState configures the session-end auto-write of the Jules state
	// file (bd-8sib). Default OFF; parsed, defaulted and validated but
	// otherwise unused until the flag flips (the bd-3ia Phase-0 pattern).
	SessionState SessionStateConfig `json:"session_state,omitempty"`
}

// DefaultSessionStateSoftCapTokens is the default soft cap, in ~tokens
// (cpt=4 basis), for a hook-written state file.
const DefaultSessionStateSoftCapTokens = 500

// MinSessionStateSoftCapTokens and MaxSessionStateSoftCapTokens bound
// brain.session_state.soft_cap_tokens.
const (
	MinSessionStateSoftCapTokens = 100
	MaxSessionStateSoftCapTokens = 2000
)

// SessionStateConfig is the brain.session_state block (bd-8sib): the
// session-end hook that auto-writes the workspace state file
// (memory/jules-state.json by default — DefaultWorkspaceStateFile).
type SessionStateConfig struct {
	// Enabled is the master switch. Default false.
	Enabled bool `json:"enabled,omitempty"`

	// IdleDistill gates Path B (idle-sweeper distillation of sessions that
	// ended without a farewell). Path A rides the farewell reflection turn
	// whenever Enabled is on. Default false.
	IdleDistill bool `json:"idle_distill,omitempty"`

	// SoftCapTokens is the write-time soft cap (~tokens, cpt=4). The writer
	// drops optional fields in priority order before ever touching required
	// ones. 0 = DefaultSessionStateSoftCapTokens; validated 100..2000.
	SoftCapTokens int `json:"soft_cap_tokens,omitempty"`

	// File optionally overrides the state-file path (relative to the
	// workspace context dir). Empty = workspace.files.state_file, else
	// DefaultWorkspaceStateFile — one source of truth for the path.
	// When set: relative, no "..", must end in .json.
	File string `json:"file,omitempty"`
}

// SoftCapTokensEffective returns the configured cap or the default.
func (s SessionStateConfig) SoftCapTokensEffective() int {
	if s.SoftCapTokens == 0 {
		return DefaultSessionStateSoftCapTokens
	}
	return s.SoftCapTokens
}

// FileEffective returns the hook's state-file path: session_state.file,
// else the workspace default. Same validation rules as
// workspace.files.state_file.
func (s SessionStateConfig) FileEffective() string {
	if s.File != "" {
		return s.File
	}
	return DefaultWorkspaceStateFile
}

// FileEffectiveFor returns session_state.file, else the workspace
// files config's state file (which itself defaults to
// DefaultWorkspaceStateFile).
func (s SessionStateConfig) FileEffectiveFor(files WorkspaceFilesConfig) string {
	if s.File != "" {
		return s.File
	}
	return files.StateFilePath()
}

// DefaultBrainConfig returns sensible defaults for the brain subsystem.
func DefaultBrainConfig() BrainConfig {
	return BrainConfig{
		Enabled:              false,
		MaxLTMEntries:        10000,
		WMGracePeriodSeconds: 300,
		AutoFlushSeconds:     600,
		ConsolidateThreshold: 0.6,
		EvictThreshold:       0.1,
		AutoPromote:          true,
		AccessWeight:         0.4,
		RecencyWeight:        0.4,
		TierWeight:           0.2,
		RecencyDecayRate:     1.0,
		AccessCountCap:       100,
		REMEnabled:           true,
		REMSchedule:          "0 2 * * *",
		REMIntegrationDay:    0,
		REMPruneAgeDays:      30,
		REMSalienceDecayRate: 0.1,
		REMGroomWithLLM:      true,
		REMLogPath:           "memory/rem-log",
		WarmthInjectFloor:    0.7,
		WarmthInjectLimit:    2,

		LTMEvictionGraceSeconds: 3600,
		MaxWMEntriesPerUser:     1000,
	}
}

// ApplyDefaults fills in zero-valued fields with sensible defaults.
// Called before Validate to handle omitempty JSON fields that weren't specified.
func (b *BrainConfig) ApplyDefaults() {
	defaults := DefaultBrainConfig()
	if b.MaxLTMEntries == 0 {
		b.MaxLTMEntries = defaults.MaxLTMEntries
	}
	if b.WMGracePeriodSeconds == 0 {
		b.WMGracePeriodSeconds = defaults.WMGracePeriodSeconds
	}
	if b.AutoFlushSeconds == 0 {
		b.AutoFlushSeconds = defaults.AutoFlushSeconds
	}
	if b.ConsolidateThreshold == 0 {
		b.ConsolidateThreshold = defaults.ConsolidateThreshold
	}
	if b.EvictThreshold == 0 {
		b.EvictThreshold = defaults.EvictThreshold
	}
	if b.AccessWeight == 0 && b.RecencyWeight == 0 && b.TierWeight == 0 {
		b.AccessWeight = defaults.AccessWeight
		b.RecencyWeight = defaults.RecencyWeight
		b.TierWeight = defaults.TierWeight
	}
	if b.RecencyDecayRate == 0 {
		b.RecencyDecayRate = defaults.RecencyDecayRate
	}
	if b.AccessCountCap == 0 {
		b.AccessCountCap = defaults.AccessCountCap
	}
	if b.REMSchedule == "" {
		b.REMSchedule = defaults.REMSchedule
	}
	if b.REMIntegrationDay == 0 {
		b.REMIntegrationDay = defaults.REMIntegrationDay
	}
	if b.REMPruneAgeDays == 0 {
		b.REMPruneAgeDays = defaults.REMPruneAgeDays
	}
	if b.REMSalienceDecayRate == 0 {
		b.REMSalienceDecayRate = defaults.REMSalienceDecayRate
	}
	if b.REMLogPath == "" {
		b.REMLogPath = defaults.REMLogPath
	}
	if b.LTMEvictionGraceSeconds == 0 {
		b.LTMEvictionGraceSeconds = defaults.LTMEvictionGraceSeconds
	}
	if b.MaxWMEntriesPerUser == 0 {
		b.MaxWMEntriesPerUser = defaults.MaxWMEntriesPerUser
	}
}

// Validate checks the brain configuration for errors.
func (b *BrainConfig) Validate() error {
	if !b.Enabled {
		return nil
	}
	b.ApplyDefaults()
	if b.MaxLTMEntries < 0 {
		return fmt.Errorf("max_ltm_entries must be non-negative")
	}
	if b.ConsolidateThreshold < 0 || b.ConsolidateThreshold > 1 {
		return fmt.Errorf("consolidate_threshold must be between 0 and 1")
	}
	if b.EvictThreshold < 0 || b.EvictThreshold > 1 {
		return fmt.Errorf("evict_threshold must be between 0 and 1")
	}
	if b.AccessWeight > 0 || b.RecencyWeight > 0 || b.TierWeight > 0 {
		sum := b.AccessWeight + b.RecencyWeight + b.TierWeight
		if sum < 0.99 || sum > 1.01 {
			return fmt.Errorf("brain salience weights must sum to 1.0 (got %.2f)", sum)
		}
	}
	if b.RecencyDecayRate < 0 {
		return fmt.Errorf("brain recency_decay_rate must be non-negative")
	}
	if b.AccessCountCap < 1 {
		return fmt.Errorf("brain access_count_cap must be >= 1")
	}
	return nil
}
