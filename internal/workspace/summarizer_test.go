package workspace

import (
	"context"
	"testing"
	"time"
)

// mockSummaryExecutor implements SummaryAIExecutor for testing
type mockSummaryExecutor struct {
	summaryContent string
	err            error
	callCount      int
}

func (m *mockSummaryExecutor) Summarize(ctx context.Context, content, fileType string, targetRatio float64, preserveKeys []string) (string, error) {
	m.callCount++
	if m.err != nil {
		return "", m.err
	}
	if m.summaryContent != "" {
		return m.summaryContent, nil
	}
	// Default: return truncated content
	targetLen := int(float64(len(content)) * targetRatio)
	if targetLen < len(content) {
		return content[:targetLen], nil
	}
	return content, nil
}

func TestSummaryManager_IsEnabled(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
		want    bool
	}{
		{"enabled", true, true},
		{"disabled", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sm := NewSummaryManager("/tmp", nil, SummaryConfig{Enabled: tt.enabled})
			if got := sm.IsEnabled(); got != tt.want {
				t.Errorf("IsEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSummaryManager_GetSummarizedContext_Disabled(t *testing.T) {
	sm := NewSummaryManager("/tmp", nil, SummaryConfig{Enabled: false})

	files := map[string]string{
		"SOUL.md": "Original content that should not be modified",
	}

	result, err := sm.GetSummarizedContext(context.Background(), files)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// When disabled, should return original files unchanged
	if result["SOUL.md"] != files["SOUL.md"] {
		t.Errorf("expected original content, got %q", result["SOUL.md"])
	}
}

func TestSummaryManager_GetSummarizedContext_SmallFiles(t *testing.T) {
	executor := &mockSummaryExecutor{}
	sm := NewSummaryManager("/tmp", executor, SummaryConfig{
		Enabled:     true,
		TargetRatio: 0.25,
	})

	// File under 500 bytes should not be summarized
	files := map[string]string{
		"SOUL.md": "Short content",
	}

	result, err := sm.GetSummarizedContext(context.Background(), files)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Small files should be returned unchanged
	if result["SOUL.md"] != files["SOUL.md"] {
		t.Errorf("expected original content, got %q", result["SOUL.md"])
	}

	// Executor should not have been called
	if executor.callCount != 0 {
		t.Errorf("executor should not be called for small files, got %d calls", executor.callCount)
	}
}

func TestSummaryManager_GetSummarizedContext_Summarizes(t *testing.T) {
	executor := &mockSummaryExecutor{
		summaryContent: "Compressed personality definition",
	}
	// Use temp directory for isolated test
	tempDir := t.TempDir()
	sm := NewSummaryManager(tempDir, executor, SummaryConfig{
		Enabled:       true,
		TargetRatio:   0.25,
		CacheTTLHours: 1,
		CacheDir:      "test-cache-summarizes",
	})

	// Large file should be summarized
	largeContent := "You are a helpful AI assistant with a friendly personality. " +
		"You enjoy helping users with their questions and tasks. " +
		"Your tone is warm, approachable, and professional. " +
		"You communicate clearly and concisely. " +
		"You are patient and thorough in your responses. " +
		"You adapt your communication style to the user's needs. " +
		"You provide accurate and helpful information. " +
		"You maintain a positive and supportive demeanor. " +
		"You are respectful and courteous in all interactions. " +
		"You help users accomplish their goals effectively."

	files := map[string]string{
		"SOUL.md": largeContent,
	}

	result, err := sm.GetSummarizedContext(context.Background(), files)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should get summarized content
	if result["SOUL.md"] != "Compressed personality definition" {
		t.Errorf("expected summarized content, got %q", result["SOUL.md"])
	}

	// Executor should have been called
	if executor.callCount != 1 {
		t.Errorf("executor should be called once, got %d calls", executor.callCount)
	}
}

func TestSummaryManager_CacheHit(t *testing.T) {
	executor := &mockSummaryExecutor{
		summaryContent: "Cached summary",
	}
	// Use temp directory for isolated test
	tempDir := t.TempDir()
	sm := NewSummaryManager(tempDir, executor, SummaryConfig{
		Enabled:       true,
		TargetRatio:   0.25,
		CacheTTLHours: 1,
		CacheDir:      "test-cache-hit",
	})

	// Content must be >= 500 bytes to trigger summarization
	largeContent := "You are a helpful AI assistant with a friendly personality. " +
		"You enjoy helping users with their questions and tasks. " +
		"Your tone is warm, approachable, and professional. " +
		"You communicate clearly and concisely. " +
		"You are patient and thorough in your responses. " +
		"You adapt your communication style to the user's needs. " +
		"You provide accurate and helpful information. " +
		"You maintain a positive and supportive demeanor. " +
		"You are respectful and courteous in all interactions. " +
		"You help users accomplish their goals effectively. " +
		"This is additional content to ensure the file is over 500 bytes."

	files := map[string]string{
		"SOUL.md": largeContent,
	}

	// First call - should call executor
	_, err := sm.GetSummarizedContext(context.Background(), files)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if executor.callCount != 1 {
		t.Errorf("first call should invoke executor, got %d calls", executor.callCount)
	}

	// Second call with same content - should use cache
	_, err = sm.GetSummarizedContext(context.Background(), files)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if executor.callCount != 1 {
		t.Errorf("second call should use cache, got %d calls", executor.callCount)
	}

	// Wait for async cache write to complete before test cleanup
	time.Sleep(10 * time.Millisecond)
}

func TestSummaryManager_CacheInvalidation(t *testing.T) {
	executor := &mockSummaryExecutor{
		summaryContent: "Summary",
	}
	// Use temp directory for isolated test
	tempDir := t.TempDir()
	sm := NewSummaryManager(tempDir, executor, SummaryConfig{
		Enabled:       true,
		TargetRatio:   0.25,
		CacheTTLHours: 1,
		CacheDir:      "test-cache-invalidation",
	})

	// Content must be >= 500 bytes to trigger summarization
	// Make sure content1 and content2 are distinct with different hashes
	content1 := "CONTENT_VERSION_ONE: You are a helpful AI assistant with a friendly personality. " +
		"You enjoy helping users with their questions and tasks. " +
		"Your tone is warm, approachable, and professional. " +
		"You communicate clearly and concisely. " +
		"You are patient and thorough in your responses. " +
		"You adapt your communication style to the user's needs. " +
		"You provide accurate and helpful information. " +
		"You maintain a positive and supportive demeanor. " +
		"You are respectful and courteous in all interactions. " +
		"You help users accomplish their goals effectively."

	content2 := "CONTENT_VERSION_TWO: You are a technical AI assistant specialized in coding. " +
		"You help developers write clean, efficient code. " +
		"Your tone is precise and technical. " +
		"You communicate with clarity and accuracy. " +
		"You provide detailed explanations when needed. " +
		"You understand multiple programming languages like Go, Python, and JavaScript. " +
		"You follow best practices for software development and testing. " +
		"You can debug complex issues effectively and efficiently. " +
		"You write clear documentation and comprehensive comments. " +
		"You help with code reviews, optimization, and architectural decisions."

	// Verify content is large enough
	if len(content1) < 500 || len(content2) < 500 {
		t.Fatalf("test content too short: content1=%d, content2=%d", len(content1), len(content2))
	}

	// Verify hashes are different
	if ComputeHash(content1) == ComputeHash(content2) {
		t.Fatal("content1 and content2 should have different hashes")
	}

	// First content
	files := map[string]string{"SOUL.md": content1}
	_, _ = sm.GetSummarizedContext(context.Background(), files)
	if executor.callCount != 1 {
		t.Errorf("first content should invoke executor, got %d calls", executor.callCount)
	}

	// Changed content - should call executor again (hash mismatch)
	files = map[string]string{"SOUL.md": content2}
	_, _ = sm.GetSummarizedContext(context.Background(), files)
	if executor.callCount != 2 {
		t.Errorf("changed content should invoke executor again, got %d calls", executor.callCount)
	}
}

func TestComputeHash(t *testing.T) {
	hash1 := ComputeHash("content one")
	hash2 := ComputeHash("content two")
	hash1Again := ComputeHash("content one")

	if hash1 == hash2 {
		t.Error("different content should have different hashes")
	}
	if hash1 != hash1Again {
		t.Error("same content should have same hash")
	}
	if len(hash1) != 64 {
		t.Errorf("SHA256 hash should be 64 hex chars, got %d", len(hash1))
	}
}

func TestDetectFileType(t *testing.T) {
	tests := []struct {
		filename string
		want     string
	}{
		{"SOUL.md", "SOUL"},
		{"USER.md", "USER"},
		{"AGENTS.md", "AGENTS"},
		{"TOOLS.md", "TOOLS"},
		{"IDENTITY.md", "IDENTITY"},
		{"HEARTBEAT.md", "HEARTBEAT"},
		{"MEMORY.md", "MEMORY"},
		{"CUSTOM.md", "CONTEXT"},
		{"path/to/SOUL.md", "SOUL"},
		{"memory/2024-01-01.md", "CONTEXT"},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			got := DetectFileType(tt.filename)
			if got != tt.want {
				t.Errorf("DetectFileType(%q) = %q, want %q", tt.filename, got, tt.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	content := "Line one\n\nLine two\n\nLine three\n\nLine four"

	// Truncate to roughly half
	truncated := Truncate(content, 25)

	// Should include truncation notice
	if len(truncated) > 30+50 { // some wiggle room for the notice
		t.Errorf("truncated content too long: %d", len(truncated))
	}

	// Should end with truncation notice
	if !containsSubstring(truncated, "truncated") {
		t.Errorf("truncated content should mention truncation: %q", truncated)
	}
}

func TestTruncate_ShortContent(t *testing.T) {
	content := "Short"
	truncated := Truncate(content, 100)

	// Content shorter than target should be unchanged
	if truncated != content {
		t.Errorf("short content should be unchanged, got %q", truncated)
	}
}

func TestSummaryConfig_GetTargetRatio(t *testing.T) {
	config := SummaryConfig{
		TargetRatio: 0.25,
		FileConfigs: map[string]SummaryFileConfig{
			"SOUL.md": {Ratio: 0.40},
		},
	}

	// File with override
	if got := config.GetTargetRatio("SOUL.md"); got != 0.40 {
		t.Errorf("SOUL.md ratio = %v, want 0.40", got)
	}

	// File without override
	if got := config.GetTargetRatio("USER.md"); got != 0.25 {
		t.Errorf("USER.md ratio = %v, want 0.25", got)
	}
}

func TestSummaryConfig_GetPreserveKeys(t *testing.T) {
	config := SummaryConfig{
		FileConfigs: map[string]SummaryFileConfig{
			"SOUL.md": {PreserveKeys: []string{"personality", "tone"}},
		},
	}

	// File with keys
	keys := config.GetPreserveKeys("SOUL.md")
	if len(keys) != 2 {
		t.Errorf("SOUL.md keys = %v, want 2 keys", keys)
	}

	// File without keys
	keys = config.GetPreserveKeys("OTHER.md")
	if keys != nil {
		t.Errorf("OTHER.md keys = %v, want nil", keys)
	}
}

func TestSummaryConfig_GetCacheTTL(t *testing.T) {
	tests := []struct {
		name  string
		hours int
		want  time.Duration
	}{
		{"custom", 24, 24 * time.Hour},
		{"default", 0, 168 * time.Hour},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := SummaryConfig{CacheTTLHours: tt.hours}
			if got := config.GetCacheTTL(); got != tt.want {
				t.Errorf("GetCacheTTL() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShouldSummarize(t *testing.T) {
	tests := []struct {
		name          string
		contextWindow int
		threshold     int
		want          bool
	}{
		{"small context", 8000, 128000, true},
		{"large context", 200000, 128000, false},
		{"at threshold", 128000, 128000, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShouldSummarize(tt.contextWindow, tt.threshold); got != tt.want {
				t.Errorf("ShouldSummarize(%d, %d) = %v, want %v",
					tt.contextWindow, tt.threshold, got, tt.want)
			}
		})
	}
}

func TestSummaryManager_Stats(t *testing.T) {
	sm := NewSummaryManager("/tmp", nil, SummaryConfig{
		Enabled:     true,
		Model:       "test-model",
		TargetRatio: 0.25,
	})

	stats := sm.Stats()

	if stats["enabled"] != true {
		t.Error("stats should show enabled")
	}
	if stats["model"] != "test-model" {
		t.Error("stats should show model")
	}
	if stats["target_ratio"] != 0.25 {
		t.Error("stats should show target ratio")
	}
}

func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s[1:], substr) || s[:len(substr)] == substr)
}
