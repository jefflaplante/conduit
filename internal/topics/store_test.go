package topics

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"conduit/internal/database"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", database.BuildDSN(t.TempDir()+"/topics.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.ConfigureDatabase(db); err != nil {
		t.Fatalf("configure: %v", err)
	}
	return NewStore(db)
}

func TestNormalizeSlug(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "conduit-feature", want: "conduit-feature"},
		{in: "Conduit Feature", want: "conduit-feature"},
		{in: "  --Solar__Panels!!  ", want: "solar-panels"},
		{in: "a/b.c", want: "a-b-c"},
		{in: strings.Repeat("x", 40), want: strings.Repeat("x", 32)},
		{in: strings.Repeat("ab-", 11) + "c", want: "ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab"},
		{in: "", wantErr: true},
		{in: "!!!", wantErr: true},
		{in: "home", wantErr: true},
		{in: "HOME", wantErr: true},
		{in: "park", wantErr: true},
		{in: "close", wantErr: true},
		{in: "rename", wantErr: true},
		{in: "persona", wantErr: true},
		{in: "stitch", wantErr: true},
		{in: "list", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := NormalizeSlug(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("NormalizeSlug(%q) = %q, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeSlug(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeSlug(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// H1: CreateOwned records the owning chat; every read path returns it, and
// Create makes an unscoped owner topic.
func TestStore_CreateOwned(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.CreateOwned("alpha", "Alpha", "topic_a", "tui_bot", "bot"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("beta", "Beta", "topic_b"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPointer("tui_bot:bot", "alpha"); err != nil {
		t.Fatal(err)
	}
	get, _ := s.Get("alpha")
	bySess, _ := s.GetBySession("topic_a")
	active, _ := s.Active("tui_bot:bot")
	list, _ := s.List()
	var listed *Topic
	for _, tp := range list {
		if tp.Slug == "alpha" {
			listed = tp
		}
	}
	for name, tp := range map[string]*Topic{"Get": get, "GetBySession": bySess, "Active": active, "List": listed} {
		if tp == nil || tp.OwnerChannel != "tui_bot" || tp.OwnerUser != "bot" || !tp.Scoped() {
			t.Errorf("%s = %+v, want owner tui_bot/bot", name, tp)
		}
	}
	if b, _ := s.Get("beta"); b == nil || b.Scoped() || b.OwnerChannel != "" || b.OwnerUser != "" {
		t.Errorf("owner topic = %+v, want unscoped", b)
	}
	if err := s.Rename("alpha", "gamma", "Gamma"); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.Get("gamma"); g == nil || g.OwnerChannel != "tui_bot" || g.OwnerUser != "bot" {
		t.Errorf("rename dropped the owner: %+v", g)
	}
}

func TestStore_CreateGetList(t *testing.T) {
	s := newTestStore(t)

	a, err := s.Create("alpha", "Alpha", "topic_aaa")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if a.ID == 0 || a.Status != StatusActive || a.SessionKey != "topic_aaa" || a.Name != "Alpha" {
		t.Fatalf("created topic = %+v", a)
	}
	if _, err := s.Create("alpha", "Again", "topic_bbb"); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate create err = %v, want ErrExists", err)
	}
	if _, err := s.Create("beta", "Beta", "topic_bbb"); err != nil {
		t.Fatal(err)
	}

	got, err := s.Get("alpha")
	if err != nil || got.Name != "Alpha" {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if _, err := s.Get("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get missing err = %v, want ErrNotFound", err)
	}
	bySess, err := s.GetBySession("topic_bbb")
	if err != nil || bySess.Slug != "beta" {
		t.Fatalf("GetBySession = %+v, %v", bySess, err)
	}
	if _, err := s.GetBySession("topic_zzz"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetBySession missing err = %v, want ErrNotFound", err)
	}

	list, err := s.List()
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %v, %v", list, err)
	}
}

func TestStore_Updates(t *testing.T) {
	tests := []struct {
		name  string
		apply func(s *Store) error
		check func(t *testing.T, tp *Topic)
	}{
		{"status parked", func(s *Store) error { return s.SetStatus("alpha", StatusParked) },
			func(t *testing.T, tp *Topic) {
				if tp.Status != StatusParked {
					t.Fatalf("status = %s", tp.Status)
				}
			}},
		{"summary", func(s *Store) error { return s.SetSummary("alpha", "Did things.") },
			func(t *testing.T, tp *Topic) {
				if tp.Summary != "Did things." {
					t.Fatalf("summary = %q", tp.Summary)
				}
			}},
		{"persona", func(s *Store) error { return s.SetPersona("alpha", "Be terse.") },
			func(t *testing.T, tp *Topic) {
				if tp.Persona != "Be terse." {
					t.Fatalf("persona = %q", tp.Persona)
				}
			}},
		{"persona cleared", func(s *Store) error {
			if err := s.SetPersona("alpha", "x"); err != nil {
				return err
			}
			return s.SetPersona("alpha", "")
		}, func(t *testing.T, tp *Topic) {
			if tp.Persona != "" {
				t.Fatalf("persona = %q", tp.Persona)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			if _, err := s.Create("alpha", "Alpha", "topic_a"); err != nil {
				t.Fatal(err)
			}
			if err := tt.apply(s); err != nil {
				t.Fatalf("apply: %v", err)
			}
			tp, err := s.Get("alpha")
			if err != nil {
				t.Fatal(err)
			}
			tt.check(t, tp)
		})
	}

	t.Run("missing slug", func(t *testing.T) {
		s := newTestStore(t)
		for name, err := range map[string]error{
			"status":  s.SetStatus("nope", StatusParked),
			"summary": s.SetSummary("nope", "x"),
			"persona": s.SetPersona("nope", "x"),
		} {
			if !errors.Is(err, ErrNotFound) {
				t.Errorf("%s on missing slug err = %v, want ErrNotFound", name, err)
			}
		}
	})
}

func TestStore_Pointers(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create("alpha", "Alpha", "topic_a"); err != nil {
		t.Fatal(err)
	}

	// Absent pointer = home.
	if slug, err := s.Pointer("telegram:42"); err != nil || slug != "" {
		t.Fatalf("absent pointer = %q, %v", slug, err)
	}
	if tp, err := s.Active("telegram:42"); err != nil || tp != nil {
		t.Fatalf("Active with no pointer = %+v, %v", tp, err)
	}

	if err := s.SetPointer("telegram:42", "alpha"); err != nil {
		t.Fatal(err)
	}
	if slug, _ := s.Pointer("telegram:42"); slug != "alpha" {
		t.Fatalf("pointer = %q, want alpha", slug)
	}
	tp, err := s.Active("telegram:42")
	if err != nil || tp == nil || tp.Slug != "alpha" {
		t.Fatalf("Active = %+v, %v", tp, err)
	}
	// Another chat is independent.
	if slug, _ := s.Pointer("telegram:7"); slug != "" {
		t.Fatalf("other chat pointer = %q", slug)
	}

	// Home clears it.
	if err := s.SetPointer("telegram:42", ""); err != nil {
		t.Fatal(err)
	}
	if slug, _ := s.Pointer("telegram:42"); slug != "" {
		t.Fatalf("pointer after home = %q", slug)
	}

	// ClearPointersTo moves every chat pointing at a slug home.
	_ = s.SetPointer("telegram:42", "alpha")
	_ = s.SetPointer("telegram:7", "alpha")
	n, err := s.ClearPointersTo("alpha")
	if err != nil || n != 2 {
		t.Fatalf("ClearPointersTo = %d, %v", n, err)
	}
	if slug, _ := s.Pointer("telegram:7"); slug != "" {
		t.Fatalf("pointer after clear = %q", slug)
	}

	// A pointer to a slug that no longer exists resolves to "missing", not an error.
	if err := s.SetPointer("telegram:42", "ghost"); err != nil {
		t.Fatal(err)
	}
	tp, err = s.Active("telegram:42")
	if !errors.Is(err, ErrNotFound) || tp != nil {
		t.Fatalf("Active on dangling pointer = %+v, %v; want ErrNotFound", tp, err)
	}
}

func TestStore_Rename(t *testing.T) {
	tests := []struct {
		name     string
		newSlug  string
		newName  string
		wantErr  error
		wantSlug string
	}{
		{name: "name only", newSlug: "alpha", newName: "Alpha Two", wantSlug: "alpha"},
		{name: "slug and name", newSlug: "gamma", newName: "Gamma", wantSlug: "gamma"},
		{name: "collision", newSlug: "beta", newName: "Beta", wantErr: ErrExists},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			_, _ = s.Create("alpha", "Alpha", "topic_a")
			_, _ = s.Create("beta", "Beta", "topic_b")
			_ = s.SetPointer("telegram:42", "alpha")

			err := s.Rename("alpha", tt.newSlug, tt.newName)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("rename err = %v, want %v", err, tt.wantErr)
				}
				// Nothing changed.
				if slug, _ := s.Pointer("telegram:42"); slug != "alpha" {
					t.Fatalf("pointer changed on failed rename: %q", slug)
				}
				return
			}
			if err != nil {
				t.Fatalf("rename: %v", err)
			}
			got, err := s.Get(tt.wantSlug)
			if err != nil || got.Name != tt.newName || got.SessionKey != "topic_a" {
				t.Fatalf("renamed = %+v, %v", got, err)
			}
			if slug, _ := s.Pointer("telegram:42"); slug != tt.wantSlug {
				t.Fatalf("pointer = %q, want %q (cascade)", slug, tt.wantSlug)
			}
			if tt.wantSlug != "alpha" {
				if _, err := s.Get("alpha"); !errors.Is(err, ErrNotFound) {
					t.Fatalf("old slug still resolves: %v", err)
				}
			}
		})
	}

	t.Run("missing", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.Rename("nope", "x", "X"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
}
