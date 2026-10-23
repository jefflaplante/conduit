package topics

import (
	"errors"
	"reflect"
	"testing"
)

// conduit-331g: per-topic prompt profiles.

func TestParseProfile(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    *Profile
		wantErr bool
	}{
		{name: "empty clears", raw: "", want: nil},
		{name: "blank clears", raw: "  \n", want: nil},
		{name: "null clears", raw: "null", want: nil},
		{name: "empty object clears", raw: "{}", want: nil},
		{name: "empty lists clear", raw: `{"skills": [], "skip_context": []}`, want: nil},
		{name: "full",
			raw:  `{"skills": ["gog", "email", "briefing"], "skip_context": ["AGENTS.md", "TOOLS.md"]}`,
			want: &Profile{Skills: []string{"gog", "email", "briefing"}, SkipContext: []string{"AGENTS.md", "TOOLS.md"}}},
		{name: "skills trimmed and deduped",
			raw:  `{"skills": [" gog ", "", "gog", "email"]}`,
			want: &Profile{Skills: []string{"gog", "email"}}},
		{name: "context case-insensitive, canonicalized, deduped",
			raw:  `{"skip_context": ["agents.md", "Tools.MD", "AGENTS.md"]}`,
			want: &Profile{SkipContext: []string{"AGENTS.md", "TOOLS.md"}}},
		{name: "SOUL.md and unknown files ignored",
			raw:  `{"skip_context": ["soul.md", "NOPE.md", "MEMORY.md"]}`,
			want: &Profile{SkipContext: []string{"MEMORY.md"}}},
		{name: "only SOUL.md is no profile", raw: `{"skip_context": ["SOUL.md"]}`, want: nil},
		{name: "malformed JSON", raw: `{"skills": ["gog"`, wantErr: true},
		{name: "wrong type", raw: `{"skills": "gog"}`, wantErr: true},
		{name: "not an object", raw: `["gog"]`, wantErr: true},
		{name: "unknown field (typo)", raw: `{"skill": ["gog"]}`, wantErr: true},
		{name: "trailing data", raw: `{"skills": ["gog"]} {}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseProfile(tt.raw)
			if tt.wantErr {
				if err == nil || !errors.Is(err, ErrInvalidProfile) {
					t.Fatalf("ParseProfile(%q) err = %v, want ErrInvalidProfile", tt.raw, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseProfile(%q): %v", tt.raw, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseProfile(%q) = %+v, want %+v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestProfile_JSONRoundTrip(t *testing.T) {
	p := &Profile{Skills: []string{"gog", "email"}, SkipContext: []string{"AGENTS.md"}}
	back, err := ParseProfile(p.JSON())
	if err != nil || !reflect.DeepEqual(back, p) {
		t.Fatalf("round trip = %+v, %v (json %s)", back, err, p.JSON())
	}
	var nilP *Profile
	if nilP.JSON() != "" {
		t.Fatalf("nil profile JSON = %q, want empty", nilP.JSON())
	}
}

func TestProfile_SessionContext(t *testing.T) {
	var nilP *Profile
	if got := nilP.SkillFilter(); got != "" {
		t.Fatalf("nil SkillFilter = %q", got)
	}
	if got := nilP.ContextSkip(); got != "" {
		t.Fatalf("nil ContextSkip = %q", got)
	}
	p := &Profile{Skills: []string{"gog", "email"}, SkipContext: []string{"AGENTS.md", "TOOLS.md"}}
	if got := p.SkillFilter(); got != "gog,email" {
		t.Fatalf("SkillFilter = %q", got)
	}
	if got := p.ContextSkip(); got != "AGENTS.md,TOOLS.md" {
		t.Fatalf("ContextSkip = %q", got)
	}
}

func TestStore_Profile(t *testing.T) {
	s := newTestStore(t)
	tp, err := s.Create("alpha", "Alpha", "topic_a")
	if err != nil {
		t.Fatal(err)
	}
	if tp.PromptProfile != "" || tp.Profile() != nil {
		t.Fatalf("new topic has a profile: %q", tp.PromptProfile)
	}

	p := &Profile{Skills: []string{"gog"}, SkipContext: []string{"TOOLS.md"}}
	if err := s.SetProfile("alpha", p); err != nil {
		t.Fatalf("SetProfile: %v", err)
	}
	if err := s.SetPointer("telegram:42", "alpha"); err != nil {
		t.Fatal(err)
	}

	byGet, _ := s.Get("alpha")
	bySession, _ := s.GetBySession("topic_a")
	byActive, _ := s.Active("telegram:42")
	list, _ := s.List()
	for name, got := range map[string]*Topic{"Get": byGet, "GetBySession": bySession, "Active": byActive, "List": list[0]} {
		if !reflect.DeepEqual(got.Profile(), p) {
			t.Errorf("%s profile = %+v (raw %q), want %+v", name, got.Profile(), got.PromptProfile, p)
		}
	}

	// Clearing stores NULL: the topic is back to the full prompt.
	if err := s.SetProfile("alpha", nil); err != nil {
		t.Fatal(err)
	}
	var isNull bool
	if err := s.db.QueryRow(`SELECT prompt_profile IS NULL FROM topics WHERE slug = 'alpha'`).Scan(&isNull); err != nil || !isNull {
		t.Fatalf("cleared profile not NULL: %v, %v", isNull, err)
	}
	if got, _ := s.Get("alpha"); got.Profile() != nil {
		t.Fatalf("cleared profile = %+v", got.Profile())
	}

	if err := s.SetProfile("nope", p); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetProfile missing = %v", err)
	}
}

// A malformed value that reached the column some other way (manual edit,
// older writer) must never break reads: the topic falls back to the full
// prompt.
func TestStore_MalformedStoredProfileFallsBack(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create("alpha", "Alpha", "topic_a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE topics SET prompt_profile = '{"skills": [' WHERE slug = 'alpha'`); err != nil {
		t.Fatal(err)
	}
	tp, err := s.Get("alpha")
	if err != nil {
		t.Fatalf("Get with malformed profile: %v", err)
	}
	if tp.Profile() != nil {
		t.Fatalf("malformed profile parsed as %+v", tp.Profile())
	}
	// Read-time is lenient about unknown fields (forward compatibility).
	if _, err := s.db.Exec(`UPDATE topics SET prompt_profile = '{"skills": ["gog"], "future": 1}' WHERE slug = 'alpha'`); err != nil {
		t.Fatal(err)
	}
	tp, _ = s.Get("alpha")
	if !reflect.DeepEqual(tp.Profile(), &Profile{Skills: []string{"gog"}}) {
		t.Fatalf("lenient read = %+v", tp.Profile())
	}
}
