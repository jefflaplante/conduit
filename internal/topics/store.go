// Package topics stores the topic switcher's state (conduit-43vk): named
// topics, each owning its own session, and the per-chat pointer that selects
// which topic a chat's messages go to. Home is not a topic: a missing or
// empty pointer means the chat's default session.
//
// The tables live in the gateway DB (migration 11); the store only needs the
// *sql.DB that the sessions store already opened.
package topics

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"conduit/internal/database"
)

// Status is a topic's lifecycle state.
type Status string

const (
	StatusActive   Status = "active"
	StatusParked   Status = "parked"
	StatusArchived Status = "archived"
)

// MaxSlugLen bounds a normalized slug.
const MaxSlugLen = 32

var (
	// ErrNotFound is returned when no topic has the slug (or session key).
	ErrNotFound = errors.New("topic not found")
	// ErrExists is returned when a slug is already taken.
	ErrExists = errors.New("topic slug already exists")
	// ErrInvalidSlug is returned when a slug normalizes to nothing or to a
	// reserved word.
	ErrInvalidSlug = errors.New("invalid topic slug")
)

// reserved slugs collide with /topic subcommands.
var reserved = map[string]bool{
	"home": true, "list": true, "park": true, "close": true,
	"rename": true, "persona": true, "stitch": true, "help": true,
}

// Topic is one row of the topics table.
type Topic struct {
	ID         int64
	Slug       string
	Name       string
	Status     Status
	Persona    string
	Summary    string
	SessionKey string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// NormalizeSlug lowercases s, turns every run of characters outside
// [a-z0-9] into a single hyphen, trims hyphens and caps the length at
// MaxSlugLen. Empty results and subcommand names are rejected.
func NormalizeSlug(s string) (string, error) {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	slug := b.String()
	if len(slug) > MaxSlugLen {
		slug = slug[:MaxSlugLen]
	}
	slug = strings.Trim(slug, "-")
	if slug == "" {
		return "", fmt.Errorf("%w: %q", ErrInvalidSlug, s)
	}
	if reserved[slug] {
		return "", fmt.Errorf("%w: %q is reserved", ErrInvalidSlug, slug)
	}
	return slug, nil
}

// Store reads and writes topics and topic pointers.
type Store struct {
	db *sql.DB
	// mu serializes writes so multi-statement updates (rename cascade,
	// pointer moves) never interleave. Reads take no lock.
	mu sync.Mutex
}

// NewStore returns a store over the gateway DB (migrations already run).
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

const topicColumns = `id, slug, name, status, COALESCE(persona, ''), COALESCE(summary, ''), session_key, created_at, updated_at`

type rowScanner interface{ Scan(dest ...any) error }

func scanTopic(row rowScanner) (*Topic, error) {
	var t Topic
	var status string
	var created, updated sql.NullString
	if err := row.Scan(&t.ID, &t.Slug, &t.Name, &status, &t.Persona, &t.Summary, &t.SessionKey, &created, &updated); err != nil {
		return nil, err
	}
	t.Status = Status(status)
	t.CreatedAt, _ = database.ParseStoredTime(created.String)
	t.UpdatedAt, _ = database.ParseStoredTime(updated.String)
	return &t, nil
}

func now() string { return database.FormatStoredTime(time.Now()) }

// Create inserts an active topic. A taken slug returns ErrExists.
func (s *Store) Create(slug, name, sessionKey string) (*Topic, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := now()
	err := database.RetryOnBusy(5, func() error {
		_, err := s.db.Exec(`INSERT INTO topics (slug, name, status, session_key, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)`, slug, name, string(StatusActive), sessionKey, ts, ts)
		return err
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%w: %s", ErrExists, slug)
		}
		return nil, fmt.Errorf("create topic %s: %w", slug, err)
	}
	return s.Get(slug)
}

// Get returns the topic with slug, or ErrNotFound.
func (s *Store) Get(slug string) (*Topic, error) {
	t, err := scanTopic(s.db.QueryRow(`SELECT `+topicColumns+` FROM topics WHERE slug = ?`, slug))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, slug)
	}
	return t, err
}

// GetBySession returns the topic owning sessionKey, or ErrNotFound.
func (s *Store) GetBySession(sessionKey string) (*Topic, error) {
	t, err := scanTopic(s.db.QueryRow(`SELECT `+topicColumns+` FROM topics WHERE session_key = ? LIMIT 1`, sessionKey))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: session %s", ErrNotFound, sessionKey)
	}
	return t, err
}

// List returns every topic, most recently updated first.
func (s *Store) List() ([]*Topic, error) {
	rows, err := s.db.Query(`SELECT ` + topicColumns + ` FROM topics ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Topic
	for rows.Next() {
		t, err := scanTopic(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// update sets one column on slug's row and bumps updated_at.
func (s *Store) update(slug, column, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	err := database.RetryOnBusy(5, func() error {
		res, err := s.db.Exec(`UPDATE topics SET `+column+` = ?, updated_at = ? WHERE slug = ?`, value, now(), slug)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return fmt.Errorf("update topic %s: %w", slug, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, slug)
	}
	return nil
}

// SetStatus changes a topic's status.
func (s *Store) SetStatus(slug string, status Status) error {
	return s.update(slug, "status", string(status))
}

// SetSummary stores a topic's auto-generated summary.
func (s *Store) SetSummary(slug, summary string) error { return s.update(slug, "summary", summary) }

// SetPersona sets (or, with "", clears) a topic's persona fragment.
func (s *Store) SetPersona(slug, persona string) error { return s.update(slug, "persona", persona) }

// Rename changes oldSlug's slug and display name and moves every chat
// pointer to the new slug, in one transaction. The old slug stops resolving.
func (s *Store) Rename(oldSlug, newSlug, newName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.Exec(`UPDATE topics SET slug = ?, name = ?, updated_at = ? WHERE slug = ?`, newSlug, newName, now(), oldSlug)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: %s", ErrExists, newSlug)
		}
		return fmt.Errorf("rename topic %s: %w", oldSlug, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, oldSlug)
	}
	if newSlug != oldSlug {
		if _, err := tx.Exec(`UPDATE topic_pointers SET topic_slug = ? WHERE topic_slug = ?`, newSlug, oldSlug); err != nil {
			return fmt.Errorf("rename topic pointers %s: %w", oldSlug, err)
		}
	}
	return tx.Commit()
}

// Pointer returns the slug chatID points at, "" for home.
func (s *Store) Pointer(chatID string) (string, error) {
	var slug sql.NullString
	err := s.db.QueryRow(`SELECT topic_slug FROM topic_pointers WHERE chat_id = ?`, chatID).Scan(&slug)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return slug.String, err
}

// Active returns the topic chatID points at in one indexed lookup: (nil,
// nil) for home, ErrNotFound when the pointer names a slug that no longer
// exists. The caller decides what a non-active status means.
func (s *Store) Active(chatID string) (*Topic, error) {
	row := s.db.QueryRow(`SELECT p.topic_slug, t.id, t.slug, t.name, t.status, COALESCE(t.persona, ''),
			COALESCE(t.summary, ''), t.session_key, t.created_at, t.updated_at
		FROM topic_pointers p LEFT JOIN topics t ON t.slug = p.topic_slug
		WHERE p.chat_id = ?`, chatID)
	var ptr sql.NullString
	var id sql.NullInt64
	var slug, name, status, persona, summary, key, created, updated sql.NullString
	err := row.Scan(&ptr, &id, &slug, &name, &status, &persona, &summary, &key, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && ptr.String == "") {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !id.Valid {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, ptr.String)
	}
	t := &Topic{
		ID: id.Int64, Slug: slug.String, Name: name.String, Status: Status(status.String),
		Persona: persona.String, Summary: summary.String, SessionKey: key.String,
	}
	t.CreatedAt, _ = database.ParseStoredTime(created.String)
	t.UpdatedAt, _ = database.ParseStoredTime(updated.String)
	return t, nil
}

// SetPointer points chatID at slug; "" sends the chat home (the row is
// removed, so home is "absent", exactly as before topics existed).
func (s *Store) SetPointer(chatID, slug string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return database.RetryOnBusy(5, func() error {
		if slug == "" {
			_, err := s.db.Exec(`DELETE FROM topic_pointers WHERE chat_id = ?`, chatID)
			return err
		}
		_, err := s.db.Exec(`INSERT INTO topic_pointers (chat_id, topic_slug) VALUES (?, ?)
			ON CONFLICT(chat_id) DO UPDATE SET topic_slug = excluded.topic_slug`, chatID, slug)
		return err
	})
}

// ClearPointersTo sends every chat pointing at slug home and returns how
// many moved.
func (s *Store) ClearPointersTo(slug string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	err := database.RetryOnBusy(5, func() error {
		res, err := s.db.Exec(`DELETE FROM topic_pointers WHERE topic_slug = ?`, slug)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return n, err
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
