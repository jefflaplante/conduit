package statehook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestState builds a fully-populated state with synthetic values only
// (this repo is public — no real session content, ever).
func newTestState() *JulesState {
	return &JulesState{
		WorkingOn: []string{"Acme cart redesign — checkout flow at 80%"},
		OpenLoops: []string{"Acme invoice export still blocked on API key", "Deploy Acme staging after review"},
		ResumeHint: "Pick up the Acme checkout tests; the payment mock needs a " +
			"fake card fixture before the E2E suite can pass.",
	}
}

// TestJulesStateGoldenKeys pins the serialized key set to the contract in
// reference/state-format.md (the Phase-1 injection schema): written_at plus
// the three required fields survive a marshal round-trip in that exact set
// when only required content is present; every optional key is spelled as
// the reference doc spells it.
func TestJulesStateGoldenKeys(t *testing.T) {
	st := newTestState()
	st.WrittenAt = "2026-11-04T05:00:00-08:00"
	st.WrittenBy = "gateway-session-hook"
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal round-trip: %v", err)
	}
	for _, k := range []string{"written_at", "written_by", "working_on", "open_loops", "resume_hint"} {
		if _, ok := m[k]; !ok {
			t.Errorf("serialized state missing key %q (have %v)", k, keysOf(m))
		}
	}

	// Full shape: every optional field serializes under the reference doc's key.
	full := &JulesState{
		WrittenAt:    "2026-11-04T05:00:00-08:00",
		WrittenBy:    "gateway-session-hook",
		LastActivePT: "2026-11-04 05:00 PT",
		GapNote:      "~0h ago",
		WorkingOn:    []string{"Acme sitemap"},
		OpenLoops:    []string{"Acme deploy"},
		WatchFor:     []string{"Acme renewal"},
		JeffState:    &JeffState{Mood: "focused", Focus: "Acme"},
		Tone:         "steady",
		KeyDecisions: []string{"chose JSON over YAML"},
		ResumeHint:   "continue",
	}
	rawFull, err := json.Marshal(full)
	if err != nil {
		t.Fatalf("marshal full: %v", err)
	}
	var mf map[string]any
	_ = json.Unmarshal(rawFull, &mf)
	for _, k := range StateFieldNames {
		if _, ok := mf[k]; !ok {
			t.Errorf("full state missing key %q (have %v)", k, keysOf(mf))
		}
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sorted := out
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j] < sorted[i] {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	return out
}

// TestJulesStateContractConsts pins the 4-field consumption contract as
// exported constants (memo §4.3): written_at + the 3 required fields.
func TestJulesStateContractConsts(t *testing.T) {
	if FieldWrittenAt != "written_at" || FieldWorkingOn != "working_on" ||
		FieldOpenLoops != "open_loops" || FieldResumeHint != "resume_hint" {
		t.Fatalf("contract consts drifted: %q %q %q %q",
			FieldWrittenAt, FieldWorkingOn, FieldOpenLoops, FieldResumeHint)
	}
	if len(RequiredFields) != 3 {
		t.Fatalf("RequiredFields must hold exactly the 3 required names, got %v", RequiredFields)
	}
}

// TestJulesStateParsesFixture parses a fixture shaped like the real on-disk
// state file (real content stays out of this public repo): string OR array
// working_on, missing optional keys, extra unknown keys tolerated.
func TestJulesStateParsesFixture(t *testing.T) {
	const fixture = `{
  "working_on": "single-string working_on drift (the live file does this)",
  "open_loops": ["Acme invoice export", "Acme staging deploy"],
  "resume_hint": "Continue the Acme checkout tests.",
  "written_by": "test-runner",
  "written_at": "2026-10-31T19:23:49-07:00",
  "some_future_key": {"nested": true}
}`
	st, err := ParseJulesState([]byte(fixture))
	if err != nil {
		t.Fatalf("ParseJulesState: %v", err)
	}
	if len(st.OpenLoops) != 2 || st.OpenLoops[0] != "Acme invoice export" {
		t.Errorf("open_loops mismatch: %v", st.OpenLoops)
	}
	if st.WorkingOnRaw == "" || len(st.WorkingOn) != 1 || st.WorkingOn[0] != "single-string working_on drift (the live file does this)" {
		t.Errorf("string working_on must normalize to a 1-element slice: raw=%q slice=%v", st.WorkingOnRaw, st.WorkingOn)
	}
	if st.WrittenAt == "" || st.WrittenBy != "test-runner" {
		t.Errorf("envelope fields mismatch: %q %q", st.WrittenAt, st.WrittenBy)
	}
}

func TestParseJulesStateInvalid(t *testing.T) {
	if _, err := ParseJulesState([]byte(`{"working_on": "x"`)); err == nil {
		t.Error("expected error for malformed JSON")
	}
	if _, err := ParseJulesState([]byte(`[1,2,3]`)); err == nil {
		t.Error("expected error for non-object JSON")
	}
}

func TestCountRequired(t *testing.T) {
	st := newTestState()
	if got := st.RequiredFieldCount(); got != 3 {
		t.Errorf("full state RequiredFieldCount = %d, want 3", got)
	}
	empty := &JulesState{}
	if got := empty.RequiredFieldCount(); got != 0 {
		t.Errorf("empty state RequiredFieldCount = %d, want 0", got)
	}
}

func TestApplyCapDefaults(t *testing.T) {
	st := &JulesState{WorkingOn: []string{"Acme"}}
	st.ApplyCap(0) // cap 0 = defaults; required fields must survive untouched
	if st.Truncated {
		t.Error("default cap must not mark state truncated")
	}
	if len(st.WorkingOn) != 1 {
		t.Error("default cap must not drop content")
	}
}

func TestApplyCapDropOrder(t *testing.T) {
	// soft cap = tokens*4 chars (cpt=4 basis). Build a state whose optional
	// fields each blow past a 1-token cap: watch_for drops first, then
	// key_decisions, then tone, then jeff_state; required always survive.
	st := &JulesState{
		WatchFor:     []string{strings.Repeat("w", 800)},
		KeyDecisions: []string{strings.Repeat("d", 800)},
		Tone:         strings.Repeat("t", 800),
		JeffState:    &JeffState{Mood: strings.Repeat("m", 400), Focus: strings.Repeat("f", 400)},
		WorkingOn:    []string{"Acme checkout"},
		OpenLoops:    []string{"Acme deploy"},
		ResumeHint:   "continue Acme",
	}
	st.ApplyCap(1) // 1 token = 4 chars — everything optional must go
	if st.WatchFor != nil || st.KeyDecisions != nil || st.Tone != "" || st.JeffState != nil {
		t.Errorf("cap did not drop all optional fields: %+v", st)
	}
	if len(st.WorkingOn) != 1 || len(st.OpenLoops) != 1 || st.ResumeHint != "continue Acme" {
		t.Error("cap must never drop required fields")
	}
	if !st.Truncated {
		t.Error("expected Truncated=true after dropping optional fields")
	}
}

func TestApplyCapResumeHintTruncation(t *testing.T) {
	st := &JulesState{
		WorkingOn:  []string{strings.Repeat("r", 3000)}, // required, huge
		OpenLoops:  []string{strings.Repeat("r", 3000)},
		ResumeHint: strings.Repeat("h", 2000),
	}
	st.ApplyCap(100) // 400 chars — required fields alone already exceed
	if !st.Truncated {
		t.Error("expected Truncated=true when resume_hint is truncated")
	}
	if len(st.ResumeHint) > ResumeHintMaxChars {
		t.Errorf("resume_hint not truncated: %d chars", len(st.ResumeHint))
	}
	if len(st.WorkingOn[0]) != 3000 {
		t.Error("resume_hint truncation must not touch other required fields")
	}
}

func TestWriteAtomicAndVerify(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jules-state.json")
	st := newTestState()
	st.WrittenBy = "test"

	w := NewWriter(path)
	n, err := w.Write(st)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n == 0 {
		t.Error("Write returned 0 bytes")
	}

	// Re-read from disk: parses, required fields intact, no temp files left.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	back, err := ParseJulesState(raw)
	if err != nil {
		t.Fatalf("re-read parse: %v", err)
	}
	if back.RequiredFieldCount() != 3 {
		t.Errorf("re-read state lost required fields: %+v", back)
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), TempFilePrefix) {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

// TestWriteRefusesFewerThanTwoRequired is the state.py:86-90 empty-clobber
// guard, restated in Go: fewer than 2 of the 3 required fields → refusal,
// and the existing on-disk file must be untouched.
func TestWriteRefusesFewerThanTwoRequired(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jules-state.json")
	existing := newTestState()
	existing.WrittenAt = time.Now().Format(time.RFC3339)
	prev, _ := json.MarshalIndent(existing, "", "  ")
	if err := os.WriteFile(path, prev, 0o600); err != nil {
		t.Fatal(err)
	}

	w := NewWriter(path)
	cases := map[string]*JulesState{
		"no required fields":  {},
		"one required field":  {WorkingOn: []string{"Acme"}},
		"only resume_hint":    {ResumeHint: "continue"},
		"empty required vals": {WorkingOn: []string{"  "}, OpenLoops: []string{""}},
	}
	for name, st := range cases {
		if _, err := w.Write(st); err == nil {
			t.Errorf("%s: expected refusal, got success", name)
		} else if !strings.Contains(err.Error(), "refusing") {
			t.Errorf("%s: error should say 'refusing', got: %v", name, err)
		}
	}

	now, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(now) != string(prev) {
		t.Error("refused write mutated the on-disk file")
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), TempFilePrefix) {
			t.Errorf("temp file left behind after refusal: %s", e.Name())
		}
	}
}

func TestWriteStampsWrittenAt(t *testing.T) {
	dir := t.TempDir()
	w := NewWriter(filepath.Join(dir, "jules-state.json"))
	st := newTestState()
	st.WrittenAt = "" // hook always stamps freshness itself (state.py:91)
	if _, err := w.Write(st); err != nil {
		t.Fatalf("Write: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "jules-state.json"))
	back, _ := ParseJulesState(raw)
	if back.WrittenAt == "" {
		t.Error("Write did not stamp written_at")
	}
	if _, err := time.Parse(time.RFC3339, back.WrittenAt); err != nil {
		t.Errorf("written_at %q not RFC3339: %v", back.WrittenAt, err)
	}
}

func TestExtractStateJSON(t *testing.T) {
	st := newTestState()
	st.GapNote = "extract me"
	body, _ := json.Marshal(st)

	cases := map[string]string{
		"fenced":        "Sure!\n\n```json\n" + string(body) + "\n```\nDone.",
		"bare":          "prefix text " + string(body) + " suffix",
		"json5-fence":   "```json5\n" + string(body) + "\n```",
		"plain-fence":   "```\n" + string(body) + "\n```",
		"surrounded":    "State saved:\n" + string(body),
		"trailing js":   "```json\n" + string(body) + "\n```\n(Note: values are examples.)",
		"multi-fence":   "```\nno json here\n```\n```json\n" + string(body) + "\n```",
		"double-fenced": "```json\n" + string(body) + "\n```\nsome trailing prose\n```json\n{\"working_on\":[\"second\"]}\n```",
	}
	for name, reply := range cases {
		got, err := ExtractStateJSON(reply)
		if err != nil {
			t.Errorf("%s: ExtractStateJSON: %v", name, err)
			continue
		}
		if got.GapNote != "extract me" || len(got.OpenLoops) != 2 {
			t.Errorf("%s: extraction mismatch: %+v", name, got)
		}
	}

	if _, err := ExtractStateJSON("no json at all, just a goodbye"); err == nil {
		t.Error("expected error when reply contains no JSON object")
	}
	if _, err := ExtractStateJSON("```json\n[1,2,3]\n```"); err == nil {
		t.Error("expected error when fenced content is not an object")
	}
}

func TestExtractStateJSONLenientWorkingOn(t *testing.T) {
	// working_on may arrive as a bare string (the live file's shape).
	got, err := ExtractStateJSON("```json\n{\"working_on\":\"single string\",\"open_loops\":[\"a\"],\"resume_hint\":\"r\"}\n```")
	if err != nil {
		t.Fatalf("ExtractStateJSON: %v", err)
	}
	if len(got.WorkingOn) != 1 || got.WorkingOn[0] != "single string" {
		t.Errorf("string working_on not normalized: %+v", got.WorkingOn)
	}
}

func TestDistillPrompt(t *testing.T) {
	p := DistillPrompt()
	for _, sub := range []string{
		"working_on", "open_loops", "resume_hint", "```json",
	} {
		if !strings.Contains(p, sub) {
			t.Errorf("DistillPrompt missing %q", sub)
		}
	}
}

func TestDistillStatePrompt(t *testing.T) {
	p := DistillStatePrompt("session-key-1")
	for _, sub := range []string{"working_on", "open_loops", "resume_hint", "```json"} {
		if !strings.Contains(p, sub) {
			t.Errorf("DistillStatePrompt missing %q", sub)
		}
	}
}

func TestBuildDistillMessages(t *testing.T) {
	msgs := BuildDistillMessages([]string{
		"user: finish the Acme deploy",
		"assistant: deployed to staging",
		"user: goodbye",
	})
	if len(msgs) != 2 {
		t.Fatalf("BuildDistillMessages: got %d messages, want 2 (system+user)", len(msgs))
	}
	if msgs[0].Role != "system" || msgs[1].Role != "user" {
		t.Errorf("roles: %+v", msgs)
	}
	if !strings.Contains(msgs[1].Content, "Acme deploy") {
		t.Error("transcript tail missing from distill user message")
	}
}

func TestIsValidSessionKey(t *testing.T) {
	cases := map[string]bool{
		"telegram_111222333_telegram":       true, // interactive main session (synthetic)
		"sess-abc":                          true,
		"":                                  false,
		"cron_run_1730690400":               false, // scheduler session
		"heartbeat_1234567890_heartbeat_ab": false, // heartbeat session
		"subagent_1730690400123456789":      false, // sub-agent session
		"subagent_x":                        false,
		"Cron_1730":                         false, // case-insensitive prefix
		"Heartbeat_9":                       false,
		"SubAgent_1":                        false,
		"telegram_cron_discussion":          true,  // prefix must match at start
		"cron":                              false, // bare prefix is a scheduler key
		"subagent":                          false,
		"heartbeat":                         false,
	}
	for key, want := range cases {
		if got := IsValidSessionKey(key); got != want {
			t.Errorf("IsValidSessionKey(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestBuildTranscriptTail(t *testing.T) {
	msgs := BuildTranscriptTail([]string{
		"user: hello",
		"assistant: hi",
		"user: do the Acme thing",
		"assistant: done",
		"user: thanks, bye",
	}, 2)
	if len(msgs) != 2 {
		t.Fatalf("BuildTranscriptTail(2) = %v", len(msgs))
	}
	if msgs[0] != "assistant: done" || msgs[1] != "user: thanks, bye" {
		t.Errorf("tail not last-N: %v", msgs)
	}
}
