package agent

import (
	"context"
	"strings"
	"testing"

	"conduit/internal/sessions"
	"conduit/internal/workspace"
)

// bd-3ia phase 2: the Memory Map pointer block tells every session type what
// on-demand memory exists, now that MEMORY.md / TOOLS.md / reference docs are
// (or soon will be) no longer injected wholesale.

const memoryMapTestHeader = "## Memory Map"

func memoryMapOf(t *testing.T, pb *PromptBuilder, session *sessions.Session) string {
	t.Helper()
	return pb.buildWorkspaceContextSection(context.Background(), session)
}

func memoryMapLines(got string) []string {
	start := strings.Index(got, memoryMapTestHeader)
	if start < 0 {
		return nil
	}
	rest := got[start:]
	if end := strings.Index(rest[len(memoryMapTestHeader):], "\n## "); end >= 0 {
		rest = rest[:len(memoryMapTestHeader)+end]
	}
	return strings.Split(rest, "\n")
}

func TestMemoryMap_PresentByDefault_MainSession(t *testing.T) {
	dir := writeStateWorkspace(t, map[string]string{"SOUL.md": "soul", "MEMORY.md": "long term facts"})
	pb := stateBuilder(t, dir, workspace.DefaultOptions())
	got := memoryMapOf(t, pb, nil)

	if !strings.Contains(got, memoryMapTestHeader) {
		t.Fatalf("Memory Map missing from Project Context:\n%s", got)
	}
	for _, want := range []string{"TOOLS.md", "reference/README.md", "memory/YYYY-MM-DD.md", "MemorySearch"} {
		if !strings.Contains(got, want) {
			t.Errorf("Memory Map should mention %q:\n%s", want, got)
		}
	}
}

// MEMORY.md content is in the prompt (main session, memory_md_inject default
// on) -> the pointer to it is redundant and must be omitted.
func TestMemoryMap_MemoryMDLineOmittedWhenInjected(t *testing.T) {
	dir := writeStateWorkspace(t, map[string]string{"SOUL.md": "soul", "MEMORY.md": "long term facts"})
	pb := stateBuilder(t, dir, workspace.DefaultOptions())
	got := memoryMapOf(t, pb, nil)

	if !strings.Contains(got, "## MEMORY.md\nlong term facts") {
		t.Fatalf("precondition: MEMORY.md should be injected by default:\n%s", got)
	}
	if lines := memoryMapLines(got); len(lines) == 0 {
		t.Fatalf("Memory Map missing:\n%s", got)
	}
	for _, line := range memoryMapLines(got) {
		if strings.Contains(line, "MEMORY.md") {
			t.Errorf("MEMORY.md pointer line should be omitted when the file is injected, got %q", line)
		}
	}
}

// The MEMORY.md line is present whenever the file's content is NOT in the
// prompt: flag off, file missing, session skips it, non-main session.
func TestMemoryMap_MemoryMDLinePresentWhenNotInjected(t *testing.T) {
	opts := workspace.DefaultOptions()
	skipping := &sessions.Session{
		Key:     "telegram_1234567890_abcdef",
		Context: map[string]string{"model": "claude-sonnet-4-6", sessions.ContextSkip: "MEMORY.MD"},
	}

	cases := []struct {
		name    string
		files   map[string]string
		opts    workspace.Options
		session *sessions.Session
	}{
		{"flag off", map[string]string{"SOUL.md": "soul", "MEMORY.md": "facts"}, workspace.Options{MemoryMDInject: false, MemoryMap: true}, nil},
		{"file missing", map[string]string{"SOUL.md": "soul"}, opts, nil},
		{"session skips MEMORY.md", map[string]string{"SOUL.md": "soul", "MEMORY.md": "facts"}, opts, skipping},
		{"shared chat (non-main)", map[string]string{"SOUL.md": "soul", "MEMORY.md": "facts"}, opts,
			&sessions.Session{Key: "telegram_-1001234567890_abcdef", ChannelID: "telegram", UserID: "-1001234567890"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeStateWorkspace(t, tc.files)
			pb := stateBuilder(t, dir, tc.opts)
			got := memoryMapOf(t, pb, tc.session)

			if strings.Contains(got, "long term") || strings.Contains(got, "facts") {
				t.Fatalf("precondition: MEMORY.md content should not be injected:\n%s", got)
			}
			lines := memoryMapLines(got)
			if len(lines) == 0 {
				t.Fatalf("Memory Map missing:\n%s", got)
			}
			found := false
			for _, line := range lines {
				if strings.HasPrefix(line, "- MEMORY.md:") {
					found = true
				}
			}
			if !found {
				t.Errorf("MEMORY.md pointer line missing when the file is not injected:\n%s", got)
			}
		})
	}
}

// Heartbeat/cron sessions fumble on-demand reads, so they always get the map.
//
// Note on the MEMORY.md line: a heartbeat_/cron_ key prefix does NOT change
// the security session type (workspaceSecurityContext still reports "main"),
// so these sessions do receive MEMORY.md content today and the pointer line
// is therefore omitted — same rule as every other session: content present
// => pointer dropped. The block itself is present regardless.
func TestMemoryMap_PresentInHeartbeatAndCronSessions(t *testing.T) {
	dir := writeStateWorkspace(t, map[string]string{"SOUL.md": "soul", "MEMORY.md": "facts", "HEARTBEAT.md": "hb"})

	for _, key := range []string{"heartbeat_x", "cron_job_a"} {
		s := &sessions.Session{Key: key, ChannelID: "cron", UserID: "cron", Context: map[string]string{"model": "claude-sonnet-4-6"}}

		// memory_md_inject on (default): content present, pointer omitted.
		got := memoryMapOf(t, stateBuilder(t, dir, workspace.DefaultOptions()), s)
		if !strings.Contains(got, memoryMapTestHeader) {
			t.Errorf("%s session: Memory Map missing:\n%s", key, got)
		}
		if !strings.Contains(got, "## HEARTBEAT.md\nhb") {
			t.Errorf("%s session: precondition, HEARTBEAT.md should be injected:\n%s", key, got)
		}
		if !strings.Contains(got, "## MEMORY.md\nfacts") {
			t.Errorf("%s session: precondition, MEMORY.md should be injected while memory_md_inject is on:\n%s", key, got)
		}
		if strings.Contains(got, "\n- MEMORY.md:") {
			t.Errorf("%s session: pointer line should be omitted while MEMORY.md content is injected:\n%s", key, got)
		}

		// memory_md_inject off (the post-soak live setting): content gone,
		// the pointer takes over.
		opts := workspace.DefaultOptions()
		opts.MemoryMDInject = false
		got = memoryMapOf(t, stateBuilder(t, dir, opts), s)
		if strings.Contains(got, "facts") {
			t.Errorf("%s session: MEMORY.md content should be gone with memory_md_inject=false:\n%s", key, got)
		}
		if !strings.Contains(got, "\n- MEMORY.md:") {
			t.Errorf("%s session: MEMORY.md pointer line missing once content is not injected:\n%s", key, got)
		}
	}
}

func TestMemoryMap_DisabledOmitsBlock(t *testing.T) {
	dir := writeStateWorkspace(t, map[string]string{"SOUL.md": "soul", "MEMORY.md": "long term facts"})
	opts := workspace.DefaultOptions()
	opts.MemoryMap = false
	got := memoryMapOf(t, stateBuilder(t, dir, opts), nil)

	if strings.Contains(got, "Memory Map") {
		t.Errorf("memory_map=false should omit the block:\n%s", got)
	}
	if !strings.Contains(got, "## SOUL.md\nsoul") {
		t.Errorf("other core files must still be injected:\n%s", got)
	}
	if !strings.Contains(got, "## MEMORY.md\nlong term facts") {
		t.Errorf("memory_map must not affect MEMORY.md injection:\n%s", got)
	}
}

// The block is part of the cache-carrying static system block: repeated
// builds in one session must be byte-identical.
func TestMemoryMap_StaticAndStable(t *testing.T) {
	dir := writeStateWorkspace(t, map[string]string{
		"SOUL.md":                 "soul",
		"MEMORY.md":               "long term facts",
		"memory/jules-state.json": validState,
	})
	pb := stateBuilder(t, dir, workspace.DefaultOptions())

	first := memoryMapOf(t, pb, nil)
	for i := 0; i < 30; i++ {
		if got := memoryMapOf(t, pb, nil); got != first {
			t.Fatalf("Project Context differs on build %d", i+1)
		}
	}

	blocks, err := pb.Build(context.Background(), sessionWithModel("claude-sonnet-4-6"), false)
	if err != nil {
		t.Fatal(err)
	}
	static, dynamic := blocks[0].Text, blocks[len(blocks)-1].Text
	if !strings.Contains(static, memoryMapTestHeader) {
		t.Fatalf("Memory Map missing from static block:\n%s", static)
	}
	if strings.Contains(dynamic, memoryMapTestHeader) {
		t.Error("Memory Map leaked into the dynamic block")
	}
	if strings.Index(static, "## Session State") > strings.Index(static, memoryMapTestHeader) {
		t.Error("Memory Map should sit after the Session State section")
	}
}

// Compact by design (~400 chars) — the block replaces multi-KB file dumps.
func TestMemoryMap_Compact(t *testing.T) {
	dir := writeStateWorkspace(t, map[string]string{"SOUL.md": "soul", "MEMORY.md": "facts"})
	got := memoryMapOf(t, stateBuilder(t, dir, workspace.DefaultOptions()), nil)

	start := strings.Index(got, memoryMapTestHeader)
	if start < 0 {
		t.Fatalf("Memory Map missing:\n%s", got)
	}
	if n := len(got) - start; n > 600 {
		t.Errorf("Memory Map is %d chars, want a compact block (<= 600)", n)
	}
}

// It is a curated static list: never enumerates what is on disk.
func TestMemoryMap_DoesNotEnumerateDisk(t *testing.T) {
	dir := writeStateWorkspace(t, map[string]string{
		"SOUL.md":                  "soul",
		"reference/secret-doc.md":  "ref",
		"memory/2026-09-27.md":     "day",
		"memory/unlisted-thing.md": "thing",
	})
	got := memoryMapOf(t, stateBuilder(t, dir, workspace.DefaultOptions()), nil)

	lines := memoryMapLines(got)
	if len(lines) == 0 {
		t.Fatalf("Memory Map missing:\n%s", got)
	}
	for _, line := range lines {
		if strings.Contains(line, "secret-doc") || strings.Contains(line, "2026-09-27") || strings.Contains(line, "unlisted-thing") {
			t.Errorf("Memory Map must not list on-disk files, got %q", line)
		}
	}
}
