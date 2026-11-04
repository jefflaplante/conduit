// Package statehook implements the session-end auto-write of the workspace
// state file (bd-8sib): distill → validate → cap → atomic write → receipt.
//
// The schema is the open-world shape of reference/state-format.md with three
// required fields (working_on, open_loops, resume_hint) plus written_at —
// the 4-field contract the Phase-1 "Session State" prompt section consumes.
// The Python skill writer (skills/state/scripts/state.py) remains a valid
// writer: same schema, same ≥2-required-fields guard, same atomic write.
//
// Everything here is gated behind brain.session_state.enabled (default
// false) at the call sites; the package itself is inert until invoked.
package statehook

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// The 4-field consumption contract (memo §4.3, bd-3ia Phase 1): written_at
// plus the three required content fields. Pinned as exported constants so
// the injection side and this writer cannot drift apart silently.
const (
	FieldWrittenAt  = "written_at"
	FieldWorkingOn  = "working_on"
	FieldOpenLoops  = "open_loops"
	FieldResumeHint = "resume_hint"
)

// RequiredFields are the content fields a write must carry at least two of
// (state.py REQUIRED_FIELDS parity; the empty-clobber guard).
var RequiredFields = []string{FieldWorkingOn, FieldOpenLoops, FieldResumeHint}

// StateFieldNames is the full reference-doc key set, in doc order.
var StateFieldNames = []string{
	"written_at", "written_by", "last_active_pt", "gap_note",
	FieldWorkingOn, FieldOpenLoops, "watch_for", "jeff_state", "tone",
	"key_decisions", FieldResumeHint,
}

// ResumeHintMaxChars bounds resume_hint after the optional-field drop order
// has been exhausted (400 chars ≈ 100 tokens at cpt=4).
const ResumeHintMaxChars = 400

// charsPerToken is the cpt=4 basis inherited from bd-3ia §1's measurement;
// approximate by design — the cap is soft and configurable.
const charsPerToken = 4

// JeffState is the optional jeff_state object.
type JeffState struct {
	Mood  string `json:"mood,omitempty"`
	Focus string `json:"focus,omitempty"`
}

// JulesState is the state-file schema: reference/state-format.md key names,
// optional fields omitempty, unknown fields tolerated on read (the format
// is pass-through — state.py forwards arbitrary envelope keys).
//
// WorkingOnRaw carries the raw value when working_on arrives as a plain
// string (the live file's shape); serialization always writes the array
// form, so both writers converge on one shape.
type JulesState struct {
	WrittenAt    string     `json:"written_at"`
	WrittenBy    string     `json:"written_by,omitempty"`
	LastActivePT string     `json:"last_active_pt,omitempty"`
	GapNote      string     `json:"gap_note,omitempty"`
	WorkingOn    []string   `json:"working_on"`
	WorkingOnRaw string     `json:"-"`
	OpenLoops    []string   `json:"open_loops"`
	WatchFor     []string   `json:"watch_for,omitempty"`
	JeffState    *JeffState `json:"jeff_state,omitempty"`
	Tone         string     `json:"tone,omitempty"`
	KeyDecisions []string   `json:"key_decisions,omitempty"`
	ResumeHint   string     `json:"resume_hint"`

	// Truncated is set (and serialized as "truncated": true) when the cap
	// dropped content. It is metadata about the write, not part of the
	// reference schema's required shape.
	Truncated bool `json:"truncated,omitempty"`
}

// rawState mirrors JulesState with lenient field types (working_on may be a
// string or an array; unknown keys ignored by json.Unmarshal).
type rawState struct {
	WrittenAt    string          `json:"written_at"`
	WrittenBy    string          `json:"written_by"`
	LastActivePT string          `json:"last_active_pt"`
	GapNote      string          `json:"gap_note"`
	WorkingOn    json.RawMessage `json:"working_on"`
	OpenLoops    json.RawMessage `json:"open_loops"`
	WatchFor     []string        `json:"watch_for"`
	JeffState    *JeffState      `json:"jeff_state"`
	Tone         string          `json:"tone"`
	KeyDecisions []string        `json:"key_decisions"`
	ResumeHint   string          `json:"resume_hint"`
}

// ParseJulesState parses on-disk state JSON, tolerating the string form of
// working_on and any unknown keys.
func ParseJulesState(data []byte) (*JulesState, error) {
	var raw rawState
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("state file is not valid JSON: %w", err)
	}
	st := &JulesState{
		WrittenAt:    raw.WrittenAt,
		WrittenBy:    raw.WrittenBy,
		LastActivePT: raw.LastActivePT,
		GapNote:      raw.GapNote,
		WatchFor:     raw.WatchFor,
		JeffState:    raw.JeffState,
		Tone:         raw.Tone,
		KeyDecisions: raw.KeyDecisions,
		ResumeHint:   raw.ResumeHint,
	}
	wo, woRaw, err := stringOrSlice(raw.WorkingOn)
	if err != nil {
		return nil, fmt.Errorf("working_on: %w", err)
	}
	st.WorkingOn, st.WorkingOnRaw = wo, woRaw
	ol, _, err := stringOrSlice(raw.OpenLoops)
	if err != nil {
		return nil, fmt.Errorf("open_loops: %w", err)
	}
	st.OpenLoops = ol
	return st, nil
}

// stringOrSlice decodes a JSON value that may be a string (returned as a
// 1-element slice plus the raw string) or an array of strings.
func stringOrSlice(msg json.RawMessage) ([]string, string, error) {
	if len(msg) == 0 {
		return nil, "", nil
	}
	var s string
	if err := json.Unmarshal(msg, &s); err == nil {
		if strings.TrimSpace(s) == "" {
			return nil, s, nil
		}
		return []string{s}, s, nil
	}
	var list []string
	if err := json.Unmarshal(msg, &list); err != nil {
		return nil, "", fmt.Errorf("must be a string or array of strings")
	}
	return list, "", nil
}

// RequiredFieldCount counts how many of the required content fields carry a
// non-blank value (state.py:85 parity).
func (st *JulesState) RequiredFieldCount() int {
	n := 0
	if nonBlank(st.WorkingOn) || strings.TrimSpace(st.WorkingOnRaw) != "" {
		n++
	}
	if nonBlank(st.OpenLoops) {
		n++
	}
	if strings.TrimSpace(st.ResumeHint) != "" {
		n++
	}
	return n
}

func nonBlank(list []string) bool {
	for _, s := range list {
		if strings.TrimSpace(s) != "" {
			return true
		}
	}
	return false
}

// ApplyCap enforces the soft token cap post-serialization (memo §3.3):
// cap chars = softCapTokens × 4 (cpt=4). Over cap → drop optional fields in
// priority order watch_for → key_decisions → tone → jeff_state; still over →
// truncate resume_hint to ResumeHintMaxChars; in both cases set Truncated.
// Required fields are never dropped. softCapTokens <= 0 keeps content
// untouched (pure function of the state; no I/O, deterministic).
func (st *JulesState) ApplyCap(softCapTokens int) {
	if softCapTokens <= 0 {
		return
	}
	limit := softCapTokens * charsPerToken
	if st.serializedLen() <= limit {
		return
	}
	st.WatchFor = nil
	st.Truncated = true
	if st.serializedLen() <= limit {
		return
	}
	st.KeyDecisions = nil
	if st.serializedLen() <= limit {
		return
	}
	st.Tone = ""
	if st.serializedLen() <= limit {
		return
	}
	st.JeffState = nil
	if st.serializedLen() <= limit {
		return
	}
	if len(st.ResumeHint) > ResumeHintMaxChars {
		st.ResumeHint = st.ResumeHint[:ResumeHintMaxChars]
	}
}

func (st *JulesState) serializedLen() int {
	b, err := json.Marshal(st)
	if err != nil {
		return 0
	}
	return len(b)
}

// WrittenAtTime parses written_at (RFC3339).
func (st *JulesState) WrittenAtTime() (time.Time, error) {
	return time.Parse(time.RFC3339, st.WrittenAt)
}
