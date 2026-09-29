package config

import (
	"fmt"
	"strings"
)

// MaintenanceConfig configures the `conduit maintenance` CLI (conduit-2cxu).
// Nothing in the gateway process reads it: maintenance runs only when the CLI
// is invoked (e.g. from a system timer). Every field is optional; zero values
// mean the built-in defaults.
type MaintenanceConfig struct {
	// RetentionDays is how long automated sessions are kept after their last
	// activity. 0 means the default (30).
	RetentionDays int `json:"retention_days,omitempty"`

	// PrunablePrefixes lists the session-key prefixes that session cleanup
	// may delete. Default-deny: sessions whose key does not start with one of
	// these are never deleted. A trailing "_" is implied ("cron" matches
	// "cron_..." but not "cronjob_..."). Empty means
	// DefaultPrunableSessionPrefixes. Prefixes that could match Telegram or
	// TUI sessions are rejected.
	PrunablePrefixes []string `json:"prunable_prefixes,omitempty"`

	// BatchSize is the number of sessions deleted per transaction, keeping
	// each write-lock hold short while the gateway is running. 0 means the
	// default (500).
	BatchSize int `json:"batch_size,omitempty"`

	// BackupDir is where the pre-cleanup VACUUM INTO backup is written.
	// Empty means next to the database file.
	BackupDir string `json:"backup_dir,omitempty" cfg:"path"`
}

// DefaultPrunableSessionPrefixes are the session-key prefixes of automated
// sessions, as the gateway builds them:
//
//	cron_<job>_<nanos>_cron_<uuid8>          (gateway/scheduler_ops.go)
//	heartbeat_<nanos>_heartbeat_<uuid8>      (heartbeat/executor.go)
//	subagent_<nanos>_subagent_<uuid8>        (gateway/subagents.go)
//	test_<user>_<uuid8>                      (gateway/http_helpers.go, /api/test/message)
var DefaultPrunableSessionPrefixes = []string{"cron_", "heartbeat_", "subagent_", "test_"}

// ProtectedSessionPrefixes are session-key prefixes of human conversations.
// No prunable prefix may overlap them.
var ProtectedSessionPrefixes = []string{"telegram_", "tui_"}

// NormalizePrunablePrefixes validates and canonicalises a prunable-prefix
// list: each entry is trimmed and gets a trailing "_" (so matching is on
// the exact key segment), duplicates are dropped, and any entry that could
// match a protected (Telegram/TUI) session key is an error. An empty list
// returns DefaultPrunableSessionPrefixes.
func NormalizePrunablePrefixes(in []string) ([]string, error) {
	if len(in) == 0 {
		return append([]string(nil), DefaultPrunableSessionPrefixes...), nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		p := strings.TrimSpace(raw)
		if p == "" || p == "_" {
			return nil, fmt.Errorf("maintenance.prunable_prefixes: empty prefix is not allowed")
		}
		for _, r := range p {
			ok := r == '_' || r == '-' || r == '.' || r == ':' ||
				(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
			if !ok {
				return nil, fmt.Errorf("maintenance.prunable_prefixes: %q contains %q; prefixes are literal (no wildcards)", raw, r)
			}
		}
		if !strings.HasSuffix(p, "_") {
			p += "_"
		}
		lp := strings.ToLower(p)
		for _, prot := range ProtectedSessionPrefixes {
			if strings.HasPrefix(lp, prot) || strings.HasPrefix(prot, lp) {
				return nil, fmt.Errorf("maintenance.prunable_prefixes: %q would match protected %q sessions; Telegram and TUI sessions are never pruned", raw, strings.TrimSuffix(prot, "_"))
			}
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, nil
}

// validateMaintenance checks the optional maintenance section (conduit-2cxu).
func validateMaintenance(me *multiError, m MaintenanceConfig) {
	if m.RetentionDays < 0 {
		me.add("maintenance.retention_days must be >= 0 (got %d)", m.RetentionDays)
	}
	if m.BatchSize < 0 {
		me.add("maintenance.batch_size must be >= 0 (got %d)", m.BatchSize)
	}
	if _, err := NormalizePrunablePrefixes(m.PrunablePrefixes); err != nil {
		me.add("%v", err)
	}
}
