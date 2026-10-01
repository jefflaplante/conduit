package maintenance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"conduit/internal/config"
	"conduit/internal/database"
	"conduit/internal/sessions"
)

// Session retention (conduit-2cxu).
//
// Only automated sessions are pruned: a session is deleted when its key
// starts with one of the prunable prefixes (default cron_, heartbeat_,
// subagent_, test_) AND its last activity is older than the cutoff. Last
// activity is the later of sessions.updated_at and the newest
// messages.timestamp of the session. Every other key (telegram_, tui_ and
// any unknown prefix) is kept, whatever its age. A session whose timestamps
// cannot be parsed is kept.
//
// Timestamps are compared in Go, not SQL. Older gateway.db rows held the
// driver's default time.Time.String() rendering in messages.timestamp and
// sessions.created_at ("2026-02-13 01:03:25.453024355 +0000 UTC m=+2227.79"),
// which carries the process's zone and does not sort chronologically as
// text. Since conduit-a636 new rows are canonical UTC text and migration 10
// rewrote the old ones, so SQL comparisons against sessions.FormatUpdatedAt
// work; parsing in Go still copes with any value the migration had to leave
// as it was (and a bound time.Time cutoff would still compare wrongly).
//
// Plan and Execute share the selection: a dry run reports exactly the
// sessions Execute would delete. Execute re-checks each session inside its
// batch transaction (updated_at and message count unchanged since the plan)
// so a session that became active in the meantime is skipped, not deleted.

const (
	// DefaultRetentionDays is the default age after which automated sessions
	// are pruned.
	DefaultRetentionDays = 30
	// DefaultPruneBatchSize is the default number of sessions deleted per
	// transaction.
	DefaultPruneBatchSize = 500
)

// PrefixCount aggregates sessions and messages for one session-key prefix.
type PrefixCount struct {
	Prefix        string    `json:"prefix"`
	Sessions      int       `json:"sessions"`
	EmptySessions int       `json:"empty_sessions"`
	Messages      int       `json:"messages"`
	Oldest        time.Time `json:"oldest,omitzero"`
	Newest        time.Time `json:"newest,omitzero"`
}

func (c *PrefixCount) addTime(t time.Time) {
	if t.IsZero() {
		return
	}
	if c.Oldest.IsZero() || t.Before(c.Oldest) {
		c.Oldest = t
	}
	if c.Newest.IsZero() || t.After(c.Newest) {
		c.Newest = t
	}
}

// PruneReport describes a retention run: what is (dry run) or was deleted,
// and what old data is kept because it is not prunable.
type PruneReport struct {
	DryRun           bool      `json:"dry_run"`
	Cutoff           time.Time `json:"cutoff"`
	RetentionDays    int       `json:"retention_days"`
	PrunablePrefixes []string  `json:"prunable_prefixes"`

	// TotalSessions / TotalMessages are the row counts at plan time.
	TotalSessions int `json:"total_sessions"`
	TotalMessages int `json:"total_messages"`
	// Inventory is every session and message, per key prefix.
	Inventory []PrefixCount `json:"inventory"`

	// Prune is what the plan selects, per prunable prefix (Oldest/Newest are
	// last-activity times).
	Prune         []PrefixCount `json:"prune"`
	PruneSessions int           `json:"prune_sessions"`
	PruneMessages int           `json:"prune_messages"`

	// KeptProtected counts non-prunable sessions whose last activity is older
	// than the cutoff, and messages older than the cutoff in non-prunable
	// sessions, per prefix: old data deliberately kept.
	KeptProtected         []PrefixCount `json:"kept_protected"`
	KeptProtectedSessions int           `json:"kept_protected_sessions"`
	KeptProtectedMessages int           `json:"kept_protected_messages"`
	// KeptRecent counts prunable-prefix sessions still inside the retention
	// window; KeptUnparseable those kept because a timestamp did not parse.
	KeptRecent      int `json:"kept_recent"`
	KeptUnparseable int `json:"kept_unparseable"`

	// Execution results (zero for a dry run).
	BackupPath string `json:"backup_path,omitempty"`
	// BackupRotation is the rotation after the backup (conduit-16f0); in a
	// dry run, the rotation the run would do after writing its backup.
	BackupRotation      *BackupRotation `json:"backup_rotation,omitempty"`
	SessionsDeleted     int             `json:"sessions_deleted"`
	MessagesDeleted     int             `json:"messages_deleted"`
	SummariesDeleted    int             `json:"summaries_deleted,omitempty"`
	MappingsDeleted     int             `json:"claude_code_mappings_deleted,omitempty"`
	SearchIndexDeleted  int             `json:"search_index_deleted,omitempty"`
	SkippedChanged      int             `json:"skipped_changed"`
	Batches             int             `json:"batches"`
	ExecuteDuration     time.Duration   `json:"execute_duration,omitempty"`
	SearchIndexWarnings []string        `json:"search_index_warnings,omitempty"`

	candidates []pruneCandidate
}

// pruneCandidate is a session selected for deletion, with the values the
// batch transaction re-checks before deleting it.
type pruneCandidate struct {
	key       string
	updatedAt string // raw CAST(updated_at AS TEXT)
	messages  int
}

// PrunedKeysWithMessages returns the keys of selected sessions that had
// messages (the only ones with rows in a messages FTS mirror).
func (r *PruneReport) PrunedKeysWithMessages() []string {
	var keys []string
	for _, c := range r.candidates {
		if c.messages > 0 {
			keys = append(keys, c.key)
		}
	}
	return keys
}

// RetentionPolicy selects what session cleanup prunes.
type RetentionPolicy struct {
	RetentionDays    int
	PrunablePrefixes []string // normalised, see config.NormalizePrunablePrefixes
}

// prefixLabel groups a session key for reporting: the key up to and
// including its first "_", or "(other)".
func prefixLabel(key string) string {
	if i := strings.IndexByte(key, '_'); i > 0 {
		return key[:i+1]
	}
	return "(other)"
}

// matchPrefix returns the prunable prefix key starts with, or "". Protected
// prefixes never match, whatever the policy says (defence in depth: the
// policy is validated by config.NormalizePrunablePrefixes).
func matchPrefix(key string, prefixes []string) string {
	lk := strings.ToLower(key)
	for _, prot := range config.ProtectedSessionPrefixes {
		if strings.HasPrefix(lk, prot) {
			return ""
		}
	}
	for _, p := range prefixes {
		if strings.HasPrefix(key, p) {
			return p
		}
	}
	return ""
}

type sessionScan struct {
	updatedRaw   string
	updatedOK    bool
	lastActivity time.Time
	messages     int
	oldMessages  int // messages older than the cutoff
	badMessageTS bool
}

// PlanPrune selects the sessions a retention run deletes. It only reads.
func PlanPrune(ctx context.Context, db *sql.DB, policy RetentionPolicy, now time.Time) (*PruneReport, error) {
	if policy.RetentionDays < 1 {
		return nil, fmt.Errorf("retention must be at least 1 day (got %d)", policy.RetentionDays)
	}
	prefixes, err := config.NormalizePrunablePrefixes(policy.PrunablePrefixes)
	if err != nil {
		return nil, err
	}
	cutoff := now.UTC().AddDate(0, 0, -policy.RetentionDays)
	rep := &PruneReport{
		DryRun:           true,
		Cutoff:           cutoff,
		RetentionDays:    policy.RetentionDays,
		PrunablePrefixes: prefixes,
	}

	// Two plain reads, not a transaction: the DSN's _txlock=immediate would
	// make any transaction take the write lock and stall a running gateway.
	// A row that changes between (or after) the reads is caught by
	// ExecutePrune's per-session re-check.
	scans := make(map[string]*sessionScan)
	var order []string
	rows, err := db.QueryContext(ctx, `SELECT key, CAST(updated_at AS TEXT) FROM sessions`)
	if err != nil {
		return nil, fmt.Errorf("scan sessions: %w", err)
	}
	for rows.Next() {
		var key string
		var upd sql.NullString
		if err := rows.Scan(&key, &upd); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan sessions: %w", err)
		}
		s := &sessionScan{updatedRaw: upd.String}
		if upd.Valid {
			if t, ok := sessions.ParseStoredTime(upd.String); ok {
				s.updatedOK, s.lastActivity = true, t.UTC()
			}
		}
		scans[key] = s
		order = append(order, key)
	}
	if err := closeRows(rows); err != nil {
		return nil, fmt.Errorf("scan sessions: %w", err)
	}

	orphans := 0
	rows, err = db.QueryContext(ctx, `SELECT session_key, CAST(timestamp AS TEXT) FROM messages`)
	if err != nil {
		return nil, fmt.Errorf("scan messages: %w", err)
	}
	for rows.Next() {
		var key string
		var ts sql.NullString
		if err := rows.Scan(&key, &ts); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan messages: %w", err)
		}
		rep.TotalMessages++
		s := scans[key]
		if s == nil {
			orphans++ // FK-less legacy row; never touched
			continue
		}
		s.messages++
		t, ok := time.Time{}, false
		if ts.Valid {
			t, ok = sessions.ParseStoredTime(ts.String)
		}
		if !ok {
			s.badMessageTS = true
			continue
		}
		t = t.UTC()
		if t.After(s.lastActivity) {
			s.lastActivity = t
		}
		if t.Before(cutoff) {
			s.oldMessages++
		}
	}
	if err := closeRows(rows); err != nil {
		return nil, fmt.Errorf("scan messages: %w", err)
	}

	inv := map[string]*PrefixCount{}
	prune := map[string]*PrefixCount{}
	kept := map[string]*PrefixCount{}
	get := func(m map[string]*PrefixCount, p string) *PrefixCount {
		c := m[p]
		if c == nil {
			c = &PrefixCount{Prefix: p}
			m[p] = c
		}
		return c
	}

	rep.TotalSessions = len(order)
	for _, key := range order {
		s := scans[key]
		label := prefixLabel(key)
		ic := get(inv, label)
		ic.Sessions++
		ic.Messages += s.messages
		if s.messages == 0 {
			ic.EmptySessions++
		}
		ic.addTime(s.lastActivity)

		p := matchPrefix(key, prefixes)
		old := s.updatedOK && s.lastActivity.Before(cutoff)
		switch {
		case p == "":
			if old || s.oldMessages > 0 {
				kc := get(kept, label)
				if old {
					kc.Sessions++
					if s.messages == 0 {
						kc.EmptySessions++
					}
					kc.addTime(s.lastActivity)
					rep.KeptProtectedSessions++
				}
				kc.Messages += s.oldMessages
				rep.KeptProtectedMessages += s.oldMessages
			}
		case !s.updatedOK || s.badMessageTS:
			rep.KeptUnparseable++
		case !old:
			rep.KeptRecent++
		default:
			pc := get(prune, p)
			pc.Sessions++
			pc.Messages += s.messages
			if s.messages == 0 {
				pc.EmptySessions++
			}
			pc.addTime(s.lastActivity)
			rep.PruneSessions++
			rep.PruneMessages += s.messages
			rep.candidates = append(rep.candidates, pruneCandidate{key: key, updatedAt: s.updatedRaw, messages: s.messages})
		}
	}
	if orphans > 0 {
		get(inv, "(orphan messages)").Messages += orphans
	}

	rep.Inventory = sortedCounts(inv)
	rep.Prune = sortedCounts(prune)
	rep.KeptProtected = sortedCounts(kept)
	return rep, nil
}

func sortedCounts(m map[string]*PrefixCount) []PrefixCount {
	out := make([]PrefixCount, 0, len(m))
	for _, c := range m {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Prefix < out[j].Prefix })
	return out
}

func closeRows(rows *sql.Rows) error {
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	return rows.Close()
}

// ExecutePrune deletes the sessions rep selected, batchSize sessions per
// short IMMEDIATE transaction (so a running gateway is blocked for one batch
// at a time, and SQLITE_BUSY is retried). For each session it re-checks that
// updated_at and the message count are unchanged since the plan, then
// deletes its messages (the messages_fts triggers keep gateway.db's FTS
// index in sync), any session_summaries / claude_code_sessions rows keyed by
// it, and the session row. rep is updated with the results.
func ExecutePrune(ctx context.Context, db *sql.DB, rep *PruneReport, batchSize int) error {
	if batchSize <= 0 {
		batchSize = DefaultPruneBatchSize
	}
	rep.DryRun = false
	start := time.Now()
	defer func() { rep.ExecuteDuration = time.Since(start) }()

	tables, err := sessions.DetectDependentTables(ctx, db)
	if err != nil {
		return err
	}

	for i := 0; i < len(rep.candidates); i += batchSize {
		if err := ctx.Err(); err != nil {
			return err
		}
		batch := rep.candidates[i:min(i+batchSize, len(rep.candidates))]
		var b batchResult
		err := database.RetryOnBusy(5, func() error {
			var err error
			b, err = pruneBatch(ctx, db, batch, rep.PrunablePrefixes, tables)
			return err
		})
		if err != nil {
			return fmt.Errorf("prune batch %d: %w", rep.Batches+1, err)
		}
		rep.Batches++
		rep.SessionsDeleted += b.sessions
		rep.MessagesDeleted += b.messages
		rep.SummariesDeleted += b.summaries
		rep.MappingsDeleted += b.mappings
		rep.SkippedChanged += b.skipped
	}
	return nil
}

type batchResult struct {
	sessions, messages, summaries, mappings, skipped int
}

func pruneBatch(ctx context.Context, db *sql.DB, batch []pruneCandidate, prefixes []string, tables sessions.DependentTables) (batchResult, error) {
	var r batchResult
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return r, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	check, err := tx.PrepareContext(ctx, `SELECT CAST(updated_at AS TEXT),
		(SELECT COUNT(*) FROM messages WHERE session_key = s.key) FROM sessions s WHERE key = ?`)
	if err != nil {
		return r, err
	}
	defer check.Close()

	for _, c := range batch {
		if matchPrefix(c.key, prefixes) == "" {
			return r, fmt.Errorf("refusing to delete non-prunable session %q", c.key)
		}
		var upd sql.NullString
		var n int
		err := check.QueryRowContext(ctx, c.key).Scan(&upd, &n)
		if errors.Is(err, sql.ErrNoRows) {
			r.skipped++ // already gone
			continue
		}
		if err != nil {
			return r, err
		}
		if upd.String != c.updatedAt || n != c.messages {
			r.skipped++ // active since the plan
			continue
		}
		// conduit-385r: the same deletion the scheduler's end-of-run
		// cleanup uses (messages, summaries, Claude Code mapping, row).
		d, err := sessions.DeleteSessionTx(ctx, tx, c.key, tables)
		if err != nil {
			return r, err
		}
		r.messages += d.Messages
		r.summaries += d.Summaries
		r.mappings += d.Mappings
		r.sessions += d.Sessions
	}
	return r, tx.Commit()
}

func rowsAffected(res sql.Result) int {
	n, err := res.RowsAffected()
	if err != nil {
		return 0
	}
	return int(n)
}

func tableExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("check table %s: %w", name, err)
	}
	return n > 0, nil
}

// PruneSearchIndex removes the given sessions' rows from a search.db
// messages_fts mirror (internal/searchdb MessageSyncer). The mirror is only
// appended to by the gateway and fully rebuilt at startup when its row count
// differs from gateway.db's, so without this pruned automated messages would
// stay searchable until the next restart. Returns rows removed. A search.db
// without messages_fts is left alone.
func PruneSearchIndex(ctx context.Context, searchDB *sql.DB, keys []string, batchSize int) (int, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	ok, err := tableExists(ctx, searchDB, "messages_fts")
	if err != nil || !ok {
		return 0, err
	}
	if batchSize <= 0 {
		batchSize = DefaultPruneBatchSize
	}
	total := 0
	for i := 0; i < len(keys); i += batchSize {
		part := keys[i:min(i+batchSize, len(keys))]
		args := make([]any, len(part))
		for j, k := range part {
			args[j] = k
		}
		q := `DELETE FROM messages_fts WHERE session_key IN (?` + strings.Repeat(",?", len(part)-1) + `)`
		err := database.RetryOnBusy(5, func() error {
			res, err := searchDB.ExecContext(ctx, q, args...)
			if err != nil {
				return err
			}
			total += rowsAffected(res)
			return nil
		})
		if err != nil {
			return total, fmt.Errorf("prune search index: %w", err)
		}
	}
	return total, nil
}
