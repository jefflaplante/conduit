package briefing

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerate_Empty(t *testing.T) {
	g := NewGenerator()
	_, err := g.Generate("sess-1", nil)
	assert.Error(t, err, "expected error for empty messages")
}

func TestGenerate_Basic(t *testing.T) {
	g := NewGenerator()

	now := time.Now()
	messages := []Message{
		{ID: "1", Role: "user", Content: "Hello, can you help me?", Timestamp: now},
		{ID: "2", Role: "assistant", Content: "Sure! I decided to use Go for this project.", Timestamp: now.Add(1 * time.Minute)},
		{ID: "3", Role: "user", Content: "Great, please write the code.", Timestamp: now.Add(2 * time.Minute)},
		{ID: "4", Role: "assistant", Content: "Done. I wrote to /tmp/output.go. Next step is to add tests.", Timestamp: now.Add(3 * time.Minute)},
	}

	b, err := g.Generate("test-session", messages)
	require.NoError(t, err)

	assert.Equal(t, "test-session", b.SessionID)
	assert.Equal(t, 4, b.MessageCount)
	assert.Equal(t, 3*time.Minute, b.Duration)
	assert.NotEmpty(t, b.Summary)
	assert.Contains(t, b.Summary, "4 messages")
	assert.NotEmpty(t, b.ID)
}

func TestGenerate_ExtractsDecisions(t *testing.T) {
	g := NewGenerator()

	now := time.Now()
	messages := []Message{
		{ID: "1", Role: "user", Content: "What language?", Timestamp: now},
		{ID: "2", Role: "assistant", Content: "I decided to use Go because of performance.", Timestamp: now.Add(1 * time.Minute)},
	}

	b, err := g.Generate("sess-decisions", messages)
	require.NoError(t, err)

	assert.NotEmpty(t, b.KeyDecisions, "should extract decisions")
	found := false
	for _, d := range b.KeyDecisions {
		if contains(d, "decided to use Go") {
			found = true
		}
	}
	assert.True(t, found, "should find 'decided to use Go' decision")
}

func TestGenerate_ExtractsFilesChanged(t *testing.T) {
	g := NewGenerator()

	now := time.Now()
	messages := []Message{
		{ID: "1", Role: "user", Content: "Write a file", Timestamp: now},
		{ID: "2", Role: "assistant", Content: "I wrote to /home/user/main.go successfully.", Timestamp: now.Add(1 * time.Minute)},
	}

	b, err := g.Generate("sess-files", messages)
	require.NoError(t, err)

	assert.Contains(t, b.FilesChanged, "/home/user/main.go")
}

func TestGenerate_ExtractsToolUsageFromMetadata(t *testing.T) {
	g := NewGenerator()

	now := time.Now()
	messages := []Message{
		{ID: "1", Role: "assistant", Content: "Reading file...", Timestamp: now, Metadata: map[string]string{"tool_name": "Read"}},
		{ID: "2", Role: "assistant", Content: "Writing file...", Timestamp: now.Add(1 * time.Minute), Metadata: map[string]string{"tool_name": "Write"}},
		{ID: "3", Role: "assistant", Content: "Reading again...", Timestamp: now.Add(2 * time.Minute), Metadata: map[string]string{"tool_name": "Read"}},
	}

	b, err := g.Generate("sess-tools", messages)
	require.NoError(t, err)

	assert.NotEmpty(t, b.ToolsUsed)

	readFound := false
	for _, tu := range b.ToolsUsed {
		if tu.Name == "Read" && tu.Count == 2 {
			readFound = true
		}
	}
	assert.True(t, readFound, "should count Read tool usage as 2")
}

func TestGenerate_ExtractsOpenQuestions(t *testing.T) {
	g := NewGenerator()

	now := time.Now()
	messages := []Message{
		{ID: "1", Role: "user", Content: "How does this work?", Timestamp: now},
		{ID: "2", Role: "assistant", Content: "Good question. Should we also add logging?", Timestamp: now.Add(1 * time.Minute)},
	}

	b, err := g.Generate("sess-questions", messages)
	require.NoError(t, err)

	assert.NotEmpty(t, b.OpenQuestions)
}

func TestGenerate_ExtractsNextSteps(t *testing.T) {
	g := NewGenerator()

	now := time.Now()
	messages := []Message{
		{ID: "1", Role: "user", Content: "What's left?", Timestamp: now},
		{ID: "2", Role: "assistant", Content: "We still need to add integration tests and follow up on the deployment.", Timestamp: now.Add(1 * time.Minute)},
	}

	b, err := g.Generate("sess-next", messages)
	require.NoError(t, err)

	assert.NotEmpty(t, b.NextSteps)
}

func TestGenerateFromMessages(t *testing.T) {
	g := NewGenerator()

	now := time.Now()
	messages := []Message{
		{ID: "1", Role: "user", Content: "Hello", Timestamp: now},
		{ID: "2", Role: "assistant", Content: "Hi there!", Timestamp: now.Add(1 * time.Minute)},
	}

	b, err := g.GenerateFromMessages(messages)
	require.NoError(t, err)
	assert.Equal(t, "unknown", b.SessionID)
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()

	b := &Briefing{
		ID:           "briefing-test-20260101-120000",
		SessionID:    "session-abc",
		Timestamp:    time.Now().Truncate(time.Second),
		Summary:      "Test briefing summary",
		KeyDecisions: []string{"Decision A", "Decision B"},
		FilesChanged: []string{"/tmp/file.go"},
		ToolsUsed:    []ToolUsage{{Name: "Read", Count: 3}},
		NextSteps:    []string{"Add tests"},
		Duration:     5 * time.Minute,
		MessageCount: 10,
	}

	err := Save(b, dir)
	require.NoError(t, err)

	// Verify the file exists.
	path := filepath.Join(dir, b.ID+".json")
	_, err = os.Stat(path)
	require.NoError(t, err)

	// Load it back.
	loaded, err := Load(path)
	require.NoError(t, err)

	assert.Equal(t, b.ID, loaded.ID)
	assert.Equal(t, b.SessionID, loaded.SessionID)
	assert.Equal(t, b.Summary, loaded.Summary)
	assert.Equal(t, b.KeyDecisions, loaded.KeyDecisions)
	assert.Equal(t, b.FilesChanged, loaded.FilesChanged)
	assert.Equal(t, b.MessageCount, loaded.MessageCount)
	assert.Len(t, loaded.ToolsUsed, 1)
	assert.Equal(t, "Read", loaded.ToolsUsed[0].Name)
}

func TestListBriefings(t *testing.T) {
	dir := t.TempDir()

	// Save two briefings.
	b1 := &Briefing{
		ID:        "briefing-aaa-20260101-100000",
		SessionID: "s1",
		Timestamp: time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC),
		Summary:   "First briefing",
	}
	b2 := &Briefing{
		ID:        "briefing-bbb-20260101-120000",
		SessionID: "s2",
		Timestamp: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		Summary:   "Second briefing",
	}

	require.NoError(t, Save(b1, dir))
	require.NoError(t, Save(b2, dir))

	summaries, err := ListBriefings(dir)
	require.NoError(t, err)
	assert.Len(t, summaries, 2)

	// Most recent first.
	assert.Equal(t, "briefing-bbb-20260101-120000", summaries[0].ID)
	assert.Equal(t, "briefing-aaa-20260101-100000", summaries[1].ID)
}

func TestListBriefings_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	summaries, err := ListBriefings(dir)
	require.NoError(t, err)
	assert.Empty(t, summaries)
}

func TestListBriefings_NonexistentDir(t *testing.T) {
	summaries, err := ListBriefings("/nonexistent/path/for/testing")
	require.NoError(t, err)
	assert.Nil(t, summaries)
}

func TestLoad_InvalidPath(t *testing.T) {
	_, err := Load("/nonexistent/file.json")
	assert.Error(t, err)
}

func TestLoad_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	require.NoError(t, os.WriteFile(path, []byte("not json"), 0o644))

	_, err := Load(path)
	assert.Error(t, err)
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "abc", truncate("abc", 10))
	assert.Equal(t, "abcdefg...", truncate("abcdefghijklmno", 10))
}

func TestExtractFilePath(t *testing.T) {
	assert.Equal(t, "/tmp/output.go", extractFilePath("/tmp/output.go successfully"))
	assert.Equal(t, "main.go", extractFilePath("main.go is ready"))
	assert.Equal(t, "", extractFilePath(""))
	assert.Equal(t, "", extractFilePath("no path here"))
}

func TestSplitSentences(t *testing.T) {
	result := splitSentences("Hello world. How are you?\nFine.")
	assert.Len(t, result, 3) // "Hello world", "How are you?", "Fine"
}

// helper
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
