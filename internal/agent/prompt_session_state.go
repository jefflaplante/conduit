package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"conduit/internal/sessions"
)

// bd-3ia: the Session State section renders the workspace state file
// (memory/jules-state.json by default) as compact text. It sits in the
// static block, so everything here must be byte-stable between requests:
// the age is rendered in whole days, never hours or minutes.

const (
	// sessionStateMaxChars caps the rendered section, header included.
	sessionStateMaxChars = 2000
	// sessionStateStaleAfter is the age past which the section warns.
	sessionStateStaleAfter = 72 * time.Hour

	sessionStateTruncatedMarker = "…(session state truncated)"
)

// sessionStateFields are the rendered keys, in render order.
var sessionStateFields = []struct{ key, label string }{
	{"working_on", "Working on"},
	{"open_loops", "Open loops"},
	{"resume_hint", "Resume hint"},
}

// buildSessionStateSection loads the state file for the session. A missing
// file, disabled injection or malformed JSON yields an empty section.
func (pb *PromptBuilder) buildSessionStateSection(session *sessions.Session) string {
	if pb.workspaceContext == nil {
		return ""
	}
	raw, ok := pb.workspaceContext.LoadStateFile(workspaceSecurityContext(session))
	if !ok {
		return ""
	}
	return renderSessionState(raw, pb.sectionParams.now(), pb.sectionParams.UserTimezone)
}

// renderSessionState renders the state JSON at now. Unknown keys are ignored.
func renderSessionState(raw string, now time.Time, timezone string) string {
	var state map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &state); err != nil || state == nil {
		return ""
	}

	var body []string
	for _, f := range sessionStateFields {
		body = append(body, renderStateField(f.label, state[f.key])...)
	}
	written, hasWritten := stateString(state["written_at"])
	if len(body) == 0 && !hasWritten {
		return ""
	}

	lines := []string{"## Session State"}
	if hasWritten {
		lines = append(lines, renderStateAge(written, now, timezone)...)
	} else {
		lines = append(lines, "Written: unknown")
	}
	lines = append(lines, body...)
	return truncateSessionState(strings.Join(lines, "\n"), sessionStateMaxChars)
}

// renderStateAge renders written_at with a whole-day relative age and, past
// sessionStateStaleAfter, a staleness warning naming today's daily log.
func renderStateAge(written string, now time.Time, timezone string) []string {
	t, err := time.Parse(time.RFC3339, written)
	if err != nil {
		return []string{"Written: " + written}
	}
	age := now.Sub(t)
	days := int(age / (24 * time.Hour))
	rel := "less than a day ago"
	switch {
	case days == 1:
		rel = "1 day ago"
	case days > 1:
		rel = fmt.Sprintf("%d days ago", days)
	}
	lines := []string{fmt.Sprintf("Written: %s (%s)", written, rel)}
	if age > sessionStateStaleAfter {
		today := now
		if loc, err := time.LoadLocation(timezone); err == nil && timezone != "" {
			today = now.In(loc)
		}
		lines = append(lines, fmt.Sprintf("State is stale (%d days) - verify against memory/%s.md", days, today.Format("2006-01-02")))
	}
	return lines
}

// renderStateField renders one key: strings inline, arrays as a bullet list
// (non-string items as compact JSON), anything else as compact JSON.
func renderStateField(label string, raw json.RawMessage) []string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if s, ok := stateString(raw); ok {
		if s = strings.TrimSpace(s); s == "" {
			return nil
		}
		return []string{label + ": " + s}
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err == nil {
		lines := []string{label + ":"}
		for _, item := range items {
			text, ok := stateString(item)
			if !ok {
				text = compactJSON(item)
			}
			if text = strings.TrimSpace(text); text != "" {
				lines = append(lines, "- "+text)
			}
		}
		if len(lines) == 1 {
			return nil
		}
		return lines
	}
	return []string{label + ": " + compactJSON(raw)}
}

func stateString(raw json.RawMessage) (string, bool) {
	var s string
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

func compactJSON(raw json.RawMessage) string {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return string(raw)
	}
	return b.String()
}

// truncateSessionState cuts s to at most max bytes, preferring a line
// boundary and never splitting a rune, and appends the truncation marker.
func truncateSessionState(s string, max int) string {
	if len(s) <= max {
		return s
	}
	limit := max - len(sessionStateTruncatedMarker) - 1
	cut := limit
	if i := strings.LastIndexByte(s[:limit], '\n'); i > limit/2 {
		cut = i
	}
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimRight(s[:cut], " \n") + "\n" + sessionStateTruncatedMarker
}
