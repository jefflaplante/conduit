package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"conduit/internal/sessions"
	"conduit/internal/workspace"
)

// bd-3ia: the Session State section renders memory/jules-state.json.

var stateNow = time.Date(2026, 9, 28, 14, 30, 0, 0, time.UTC)

func writeStateWorkspace(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func stateBuilder(t *testing.T, dir string, opts workspace.Options) *PromptBuilder {
	t.Helper()
	pb := newTestPromptBuilder()
	pb.workspaceContext = workspace.NewWorkspaceContextWithOptions(dir, opts)
	pb.SetClock(func() time.Time { return stateNow })
	return pb
}

func sessionStateOf(t *testing.T, pb *PromptBuilder, session *sessions.Session) string {
	t.Helper()
	return pb.buildSessionStateSection(session)
}

const validState = `{
  "written_at": "2026-09-27T10:00:00Z",
  "working_on": "Refactor the Acme widget pipeline",
  "open_loops": ["Reply to Rex about Job A", {"id": "projects.alpha", "note": "review"}],
  "resume_hint": "Start with the failing test in widget_test.go",
  "ignored_field": "not rendered"
}`

func TestSessionState_ValidRender(t *testing.T) {
	dir := writeStateWorkspace(t, map[string]string{"memory/jules-state.json": validState})
	got := sessionStateOf(t, stateBuilder(t, dir, workspace.DefaultOptions()), nil)

	for _, want := range []string{
		"## Session State",
		"Written: 2026-09-27T10:00:00Z (1 day ago)",
		"Working on: Refactor the Acme widget pipeline",
		"Open loops:\n- Reply to Rex about Job A\n- {\"id\":\"projects.alpha\",\"note\":\"review\"}",
		"Resume hint: Start with the failing test in widget_test.go",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "stale") {
		t.Errorf("fresh state must not carry a staleness warning:\n%s", got)
	}
	if strings.Contains(got, "ignored_field") {
		t.Errorf("unknown fields must not be rendered:\n%s", got)
	}
	wi, ri := strings.Index(got, "Written:"), strings.Index(got, "Working on:")
	oi, hi := strings.Index(got, "Open loops:"), strings.Index(got, "Resume hint:")
	if !(wi < ri && ri < oi && oi < hi) {
		t.Errorf("fields out of order (written, working_on, open_loops, resume_hint):\n%s", got)
	}
}

func TestSessionState_FreshUnderADay(t *testing.T) {
	dir := writeStateWorkspace(t, map[string]string{"memory/jules-state.json": `{"written_at":"2026-09-28T13:00:00Z","working_on":"x"}`})
	got := sessionStateOf(t, stateBuilder(t, dir, workspace.DefaultOptions()), nil)
	if !strings.Contains(got, "(less than a day ago)") {
		t.Errorf("want sub-day age, got:\n%s", got)
	}
}

func TestSessionState_StalenessWarning(t *testing.T) {
	dir := writeStateWorkspace(t, map[string]string{"memory/jules-state.json": `{"written_at":"2026-09-23T09:00:00Z","working_on":"x"}`})
	got := sessionStateOf(t, stateBuilder(t, dir, workspace.DefaultOptions()), nil)
	want := "State is stale (5 days) - verify against memory/2026-09-28.md"
	if !strings.Contains(got, want) {
		t.Errorf("missing %q in:\n%s", want, got)
	}

	// Exactly 72h is not yet stale.
	dir = writeStateWorkspace(t, map[string]string{"memory/jules-state.json": `{"written_at":"2026-09-25T14:30:00Z","working_on":"x"}`})
	if got := sessionStateOf(t, stateBuilder(t, dir, workspace.DefaultOptions()), nil); strings.Contains(got, "stale") {
		t.Errorf("72h old state should not be stale yet:\n%s", got)
	}
}

func TestSessionState_MalformedJSONEmpty(t *testing.T) {
	for _, bad := range []string{`{"working_on": "x"`, `not json`, `["a","b"]`, `"string"`, ``, `{}`} {
		dir := writeStateWorkspace(t, map[string]string{"memory/jules-state.json": bad})
		if got := sessionStateOf(t, stateBuilder(t, dir, workspace.DefaultOptions()), nil); got != "" {
			t.Errorf("state %q should render empty, got:\n%s", bad, got)
		}
	}
}

func TestSessionState_MissingFileEmpty(t *testing.T) {
	dir := writeStateWorkspace(t, map[string]string{"SOUL.md": "soul"})
	if got := sessionStateOf(t, stateBuilder(t, dir, workspace.DefaultOptions()), nil); got != "" {
		t.Errorf("missing state file should render empty, got:\n%s", got)
	}
	pb := newTestPromptBuilder() // no workspace context at all
	if got := pb.buildSessionStateSection(nil); got != "" {
		t.Errorf("no workspace should render empty, got: %q", got)
	}
}

func TestSessionState_DisabledEmpty(t *testing.T) {
	dir := writeStateWorkspace(t, map[string]string{"memory/jules-state.json": validState})
	opts := workspace.DefaultOptions()
	opts.StateInject = false
	if got := sessionStateOf(t, stateBuilder(t, dir, opts), nil); got != "" {
		t.Errorf("state_inject=false should render empty, got:\n%s", got)
	}
}

func TestSessionState_TruncationCap(t *testing.T) {
	loops := make([]string, 0, 200)
	for i := 0; i < 200; i++ {
		loops = append(loops, `"loop item with some padding text é"`)
	}
	state := `{"written_at":"2026-09-28T10:00:00Z","working_on":"` + strings.Repeat("wörk ", 600) +
		`","open_loops":[` + strings.Join(loops, ",") + `],"resume_hint":"tail"}`
	dir := writeStateWorkspace(t, map[string]string{"memory/jules-state.json": state})
	got := sessionStateOf(t, stateBuilder(t, dir, workspace.DefaultOptions()), nil)

	if len(got) > sessionStateMaxChars {
		t.Errorf("section is %d chars, cap is %d", len(got), sessionStateMaxChars)
	}
	if !utf8.ValidString(got) {
		t.Error("truncation split a multi-byte rune")
	}
	if !strings.HasPrefix(got, "## Session State") || !strings.Contains(got, "Written: 2026-09-28T10:00:00Z") {
		t.Errorf("header lost in truncation:\n%s", got)
	}
	if !strings.HasSuffix(got, sessionStateTruncatedMarker) {
		t.Errorf("truncated section should end with the marker, got tail %q", got[len(got)-60:])
	}
}

func TestSessionState_InStaticBlockAfterRuntimeBeforeProjectContext(t *testing.T) {
	dir := writeStateWorkspace(t, map[string]string{
		"SOUL.md":                 "soul text",
		"memory/jules-state.json": validState,
	})
	pb := stateBuilder(t, dir, workspace.DefaultOptions())
	blocks, err := pb.Build(context.Background(), sessionWithModel("claude-sonnet-4-6"), false)
	if err != nil {
		t.Fatal(err)
	}
	static := blocks[0].Text
	si := strings.Index(static, "## Session State")
	ri := strings.Index(static, "## Runtime")
	pi := strings.Index(static, "# Project Context")
	if si < 0 || ri < 0 || pi < 0 || !(ri < si && si < pi) {
		t.Fatalf("want Runtime < Session State < Project Context in static block (got %d, %d, %d)", ri, si, pi)
	}
	if strings.Count(static, "Refactor the Acme widget pipeline") != 1 {
		t.Errorf("state file content should appear once (Project Context must not re-render it)")
	}
	if strings.Contains(blocks[len(blocks)-1].Text, "## Session State") {
		t.Errorf("Session State leaked into the dynamic block")
	}

	var section *promptSection
	all := pb.buildSectionListWithParams(context.Background(), nil, false, false, pb.sectionParams)
	for i := range all {
		if all[i].name == "Session State" {
			section = &all[i]
		}
	}
	if section == nil || section.priority != 1 || section.dynamic {
		t.Fatalf("Session State must be a static P1 section, got %+v", section)
	}
}

func TestMemoryMDInject_Gate(t *testing.T) {
	dir := writeStateWorkspace(t, map[string]string{"SOUL.md": "soul", "MEMORY.md": "long term facts"})
	on := stateBuilder(t, dir, workspace.DefaultOptions())
	if got := on.buildWorkspaceContextSection(context.Background(), nil); !strings.Contains(got, "## MEMORY.md\nlong term facts") {
		t.Errorf("MEMORY.md should be injected by default:\n%s", got)
	}

	opts := workspace.DefaultOptions()
	opts.MemoryMDInject = false
	off := stateBuilder(t, dir, opts)
	got := off.buildWorkspaceContextSection(context.Background(), nil)
	if strings.Contains(got, "long term facts") {
		t.Errorf("memory_md_inject=false should drop MEMORY.md:\n%s", got)
	}
	if !strings.Contains(got, "## SOUL.md\nsoul") {
		t.Errorf("other core files must still be injected:\n%s", got)
	}
}
