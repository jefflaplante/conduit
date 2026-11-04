package statehook

import (
	"fmt"
	"strings"
)

// ExtractStateJSON pulls the state JSON object out of a model reply
// (Path A): prefer the first ```json-fenced block whose content is a JSON
// object; fall back to the first balanced top-level {...} object in the
// text. Lenient parse (string working_on, unknown keys). Returns an error
// when no object is found — the caller logs and moves on; a failed state
// write never blocks the delivered reply.
func ExtractStateJSON(reply string) (*JulesState, error) {
	for _, cand := range candidateJSONBlocks(reply) {
		if st, err := ParseJulesState([]byte(cand)); err == nil {
			return st, nil
		}
	}
	return nil, fmt.Errorf("no JSON state object found in reply (%d chars)", len(reply))
}

// candidateJSONBlocks returns JSON-object candidates, best first: fenced
// blocks (json/json5/plain fences, in order of appearance), then balanced
// {...} spans from the raw text.
func candidateJSONBlocks(reply string) []string {
	var out []string
	reply = strings.TrimSpace(reply)

	// Strip code fences in order; a fence with a JSON-looking object inside
	// is a candidate. Non-object fences ("no json here") fail parse later.
	for _, fence := range splitFences(reply) {
		body := strings.TrimSpace(fence.body)
		if strings.HasPrefix(body, "{") && strings.HasSuffix(body, "}") {
			out = append(out, body)
		}
	}

	// Balanced top-level objects in the raw text (bare JSON, no fence).
	out = append(out, balancedObjects(reply)...)
	return dedupe(out)
}

type fence struct {
	info string // info string (json, json5, "" for plain)
	body string
}

// splitFences walks ```-fenced blocks. Tolerates an unclosed final fence.
func splitFences(s string) []fence {
	var out []fence
	lines := strings.Split(s, "\n")
	var cur *fence
	for _, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if cur == nil {
			if strings.HasPrefix(trimmed, "```") {
				cur = &fence{info: strings.TrimPrefix(trimmed, "```")}
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") {
			out = append(out, *cur)
			cur = nil
			continue
		}
		cur.body += ln + "\n"
	}
	if cur != nil { // unclosed fence: take what's there
		out = append(out, *cur)
	}
	return out
}

// balancedObjects scans for {...} spans whose braces balance, starting only
// at a line-leading or whitespace-preceded '{'.
func balancedObjects(s string) []string {
	var out []string
	depth := 0
	start := -1
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			if depth > 0 {
				depth--
				if depth == 0 && start >= 0 {
					out = append(out, s[start:i+1])
				}
			}
		}
	}
	return out
}

func dedupe(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// DistillPrompt is the Path A instruction appended to the farewell
// reflection turn (rides the reply the model was already generating — zero
// marginal LLM calls).
func DistillPrompt() string {
	var b strings.Builder
	b.WriteString("SESSION STATE HANDOFF — after your reflection, ALSO emit the session-state file content. ")
	b.WriteString("Append a fenced ```json block with EXACTLY these keys (omit optional ones you cannot fill honestly):\n")
	b.WriteString(`{
  "working_on":   ["1-3 specific items you were mid-stream on"],
  "open_loops":   ["unfinished items the next session must pick up"],
  "watch_for":    ["background awareness items"],
  "jeff_state":   {"mood": "...", "focus": "..."},
  "tone":         "one line on the session's emotional context",
  "key_decisions": ["decisions made this session"],
  "resume_hint":  "the single most useful sentence for the next session"
}` + "\n")
	b.WriteString("Required: working_on, open_loops, resume_hint (at least 2 must be non-empty). ")
	b.WriteString("Be specific and compressed — this is a briefing, not a log. Then finish your goodbye normally.")
	return b.String()
}

// DistillStatePrompt is the Path B standalone system prompt for the idle
// sweeper's one-shot distillation call.
func DistillStatePrompt(sessionKey string) string {
	return "You distill session handoff state for session " + sessionKey + ". " +
		"Reply with ONLY a fenced ```json block and nothing else:\n" +
		`{
  "working_on":   ["..."],
  "open_loops":   ["..."],
  "watch_for":    ["..."],
  "jeff_state":   {"mood": "...", "focus": "..."},
  "tone":         "...",
  "key_decisions": ["..."],
  "resume_hint":  "..."
}` + "\n" +
		"Required: working_on, open_loops, resume_hint. Specific, compressed, honest — no fabrication."
}

// BuildTranscriptTail returns the last n transcript lines ("role: content").
func BuildTranscriptTail(lines []string, n int) []string {
	if n <= 0 || len(lines) <= n {
		return lines
	}
	return lines[len(lines)-n:]
}
