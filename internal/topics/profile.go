package topics

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Prompt profiles (conduit-331g): a topic may narrow the system prompt its
// session runs with — a skill allowlist and a list of workspace context
// files to leave out. Stored as JSON in topics.prompt_profile; NULL means
// the full prompt, exactly as before profiles existed.

// ErrInvalidProfile is returned when a profile is not valid JSON of the
// expected shape.
var ErrInvalidProfile = errors.New("invalid topic prompt profile")

// PinnedContextFile is always loaded: it carries identity and safety rules,
// so a profile can never skip it.
const PinnedContextFile = "SOUL.md"

// SkippableContextFiles are the workspace context files a profile may skip,
// in their canonical spelling.
var SkippableContextFiles = []string{"USER.md", "AGENTS.md", "TOOLS.md", "IDENTITY.md", "BOOTSTRAP.md", "HEARTBEAT.md", "MEMORY.md"}

// Profile is a topic's prompt profile.
type Profile struct {
	// Skills is the skill allowlist; empty means every skill.
	Skills []string `json:"skills,omitempty"`
	// SkipContext lists context files (canonical names) to leave out.
	SkipContext []string `json:"skip_context,omitempty"`
}

// ParseProfile validates and normalizes a profile as given by a user or
// model: skill names are trimmed and deduped, context file names are
// matched case-insensitively against SkippableContextFiles and
// canonicalized (SOUL.md and unknown names are dropped). Unknown fields are
// rejected so a typo ("skill") is not silently ignored. A blank, null or
// empty profile returns (nil, nil): no profile.
func ParseProfile(raw string) (*Profile, error) {
	return parseProfile(raw, true)
}

// Profile returns the topic's stored profile, or nil for none. It never
// fails: a stored value that does not parse means the full prompt.
func (t *Topic) Profile() *Profile {
	p, err := parseProfile(t.PromptProfile, false)
	if err != nil {
		return nil
	}
	return p
}

func parseProfile(raw string, strict bool) (*Profile, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return nil, nil
	}
	var in Profile
	dec := json.NewDecoder(strings.NewReader(raw))
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(&in); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidProfile, err)
	}
	if dec.Decode(&json.RawMessage{}) != io.EOF {
		return nil, fmt.Errorf("%w: trailing data after the JSON object", ErrInvalidProfile)
	}

	var out Profile
	seen := map[string]bool{}
	for _, s := range in.Skills {
		if s = strings.TrimSpace(s); s != "" && !seen[s] {
			seen[s] = true
			out.Skills = append(out.Skills, s)
		}
	}
	seen = map[string]bool{}
	for _, f := range in.SkipContext {
		if c := canonicalContextFile(f); c != "" && !seen[c] {
			seen[c] = true
			out.SkipContext = append(out.SkipContext, c)
		}
	}
	if len(out.Skills) == 0 && len(out.SkipContext) == 0 {
		return nil, nil
	}
	return &out, nil
}

// canonicalContextFile maps name to its SkippableContextFiles spelling, or
// "" when it is not skippable (unknown, or the pinned SOUL.md).
func canonicalContextFile(name string) string {
	name = strings.TrimSpace(name)
	for _, f := range SkippableContextFiles {
		if strings.EqualFold(name, f) {
			return f
		}
	}
	return ""
}

// JSON encodes the profile for storage; a nil profile encodes as "".
func (p *Profile) JSON() string {
	if p == nil {
		return ""
	}
	b, _ := json.Marshal(p)
	return string(b)
}

// SkillFilter renders the allowlist as the comma-separated value of the
// session context's skill filter; "" for none.
func (p *Profile) SkillFilter() string {
	if p == nil {
		return ""
	}
	return strings.Join(p.Skills, ",")
}

// ContextSkip renders the skip list as the comma-separated value of the
// session context's context-skip key; "" for none.
func (p *Profile) ContextSkip() string {
	if p == nil {
		return ""
	}
	return strings.Join(p.SkipContext, ",")
}
