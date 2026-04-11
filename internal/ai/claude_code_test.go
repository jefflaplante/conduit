package ai

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"conduit/internal/config"
	"conduit/internal/sessions"
	"conduit/internal/tools/types"

	_ "modernc.org/sqlite"
)

// --- helpers ---

// writeMockScript writes an executable shell script to tmpDir and returns its path.
// On Windows this would need .bat files, but Conduit targets Linux/macOS.
func writeMockScript(t *testing.T, name, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell mock scripts require unix")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0755); err != nil {
		t.Fatalf("write mock script: %v", err)
	}
	return path
}

// newTestSessionMapper creates a ClaudeCodeSessionMapper backed by an in-memory SQLite DB.
func newTestSessionMapper(t *testing.T) *sessions.ClaudeCodeSessionMapper {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	mapper := sessions.NewClaudeCodeSessionMapper(db)
	if err := mapper.EnsureTable(); err != nil {
		t.Fatalf("ensure table: %v", err)
	}
	return mapper
}

// --- Name() ---

func TestClaudeCodeProvider_Name(t *testing.T) {
	script := writeMockScript(t, "claude", `echo '{}'`)
	p, err := NewClaudeCodeProvider(config.ProviderConfig{
		Name: "my-cc",
		Type: "claude-code",
		ClaudeCode: &config.ClaudeCodeConfig{
			ClaudePath: script,
		},
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := p.Name(); got != "my-cc" {
		t.Errorf("Name() = %q, want %q", got, "my-cc")
	}
}

// --- buildCommand ---

func TestClaudeCodeProvider_BuildCommand_Basic(t *testing.T) {
	p := &ClaudeCodeProvider{
		name:   "test",
		model:  "",
		config: config.ClaudeCodeConfig{ClaudePath: "/usr/bin/claude", TimeoutSeconds: 60},
	}

	cmd, cleanup, err := p.buildCommand(context.Background(), "hello world", "", "", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer cleanup()

	args := cmd.Args[1:] // skip the binary name
	want := []string{"-p", "--output-format", "json", "hello world"}
	if !sliceEqual(args, want) {
		t.Errorf("args = %v, want %v", args, want)
	}
}

func TestClaudeCodeProvider_BuildCommand_Streaming(t *testing.T) {
	p := &ClaudeCodeProvider{
		name:   "test",
		model:  "claude-sonnet-4",
		config: config.ClaudeCodeConfig{ClaudePath: "/usr/bin/claude", TimeoutSeconds: 60},
	}

	cmd, cleanup, err := p.buildCommand(context.Background(), "hi", "", "", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer cleanup()

	args := cmd.Args[1:]
	// Should have stream-json and --model
	assertContainsSequence(t, args, "--output-format", "stream-json")
	assertContainsSequence(t, args, "--model", "claude-sonnet-4")
}

func TestClaudeCodeProvider_BuildCommand_WithSessionResume(t *testing.T) {
	mapper := newTestSessionMapper(t)
	if err := mapper.SaveMapping("conduit-sess-1", "cc-sess-abc"); err != nil {
		t.Fatal(err)
	}

	p := &ClaudeCodeProvider{
		name:          "test",
		config:        config.ClaudeCodeConfig{ClaudePath: "/usr/bin/claude", TimeoutSeconds: 60},
		sessionMapper: mapper,
	}

	cmd, cleanup, err := p.buildCommand(context.Background(), "test", "", "conduit-sess-1", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer cleanup()

	args := cmd.Args[1:]
	assertContainsSequence(t, args, "--resume", "cc-sess-abc")
}

func TestClaudeCodeProvider_BuildCommand_AllOptions(t *testing.T) {
	mapper := newTestSessionMapper(t)
	_ = mapper.SaveMapping("sess-1", "cc-1")

	p := &ClaudeCodeProvider{
		name:  "full",
		model: "claude-opus-4",
		config: config.ClaudeCodeConfig{
			ClaudePath:     "/opt/claude",
			AllowedTools:   []string{"Read", "Write", "Bash"},
			PermissionMode: "bypassPermissions",
			MaxTurns:       5,
			TimeoutSeconds: 120,
			WorkingDir:     "/tmp/work",
		},
		sessionMapper: mapper,
	}

	cmd, cleanup, err := p.buildCommand(context.Background(), "do stuff", "", "sess-1", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer cleanup()

	args := cmd.Args[1:]

	assertContainsSequence(t, args, "--output-format", "stream-json")
	assertContainsSequence(t, args, "--model", "claude-opus-4")
	assertContainsSequence(t, args, "--resume", "cc-1")
	assertContainsSequence(t, args, "--allowedTools", "Read,Write,Bash")
	assertContainsSequence(t, args, "--permission-mode", "bypassPermissions")
	assertContainsSequence(t, args, "--max-turns", "5")

	if cmd.Dir != "/tmp/work" {
		t.Errorf("Dir = %q, want /tmp/work", cmd.Dir)
	}

	// Prompt should be last arg.
	if last := args[len(args)-1]; last != "do stuff" {
		t.Errorf("last arg = %q, want %q", last, "do stuff")
	}
}

func TestClaudeCodeProvider_BuildCommand_NilMapper(t *testing.T) {
	p := &ClaudeCodeProvider{
		name:          "test",
		config:        config.ClaudeCodeConfig{ClaudePath: "/usr/bin/claude", TimeoutSeconds: 60},
		sessionMapper: nil, // nil mapper should not panic
	}

	cmd, cleanup, err := p.buildCommand(context.Background(), "test", "", "some-session", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer cleanup()

	args := cmd.Args[1:]
	// Should NOT contain --resume when mapper is nil.
	for _, arg := range args {
		if arg == "--resume" {
			t.Error("--resume should not appear when sessionMapper is nil")
		}
	}
}

// --- GenerateResponse with mock script ---

func TestClaudeCodeProvider_GenerateResponse(t *testing.T) {
	script := writeMockScript(t, "claude", `echo '{"result":"hello from claude","session_id":"test-sess-123","usage":{"inputTokens":10,"outputTokens":5}}'`)

	p, err := NewClaudeCodeProvider(config.ProviderConfig{
		Name: "test-cc",
		Type: "claude-code",
		ClaudeCode: &config.ClaudeCodeConfig{
			ClaudePath:     script,
			TimeoutSeconds: 30,
		},
	}, nil)
	if err != nil {
		t.Fatalf("NewClaudeCodeProvider: %v", err)
	}

	req := &GenerateRequest{
		Messages: []ChatMessage{
			{Role: "user", Content: "say hello"},
		},
	}

	resp, err := p.GenerateResponse(context.Background(), req)
	if err != nil {
		t.Fatalf("GenerateResponse: %v", err)
	}

	if resp.Content != "hello from claude" {
		t.Errorf("Content = %q, want %q", resp.Content, "hello from claude")
	}
	if resp.Usage.PromptTokens != 10 {
		t.Errorf("PromptTokens = %d, want 10", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 5 {
		t.Errorf("CompletionTokens = %d, want 5", resp.Usage.CompletionTokens)
	}
	if resp.ToolCalls != nil {
		t.Errorf("ToolCalls should be nil, got %v", resp.ToolCalls)
	}
}

func TestClaudeCodeProvider_GenerateResponse_SavesSessionMapping(t *testing.T) {
	script := writeMockScript(t, "claude", `echo '{"result":"ok","session_id":"cc-new-sess","usage":{"inputTokens":1,"outputTokens":1}}'`)

	mapper := newTestSessionMapper(t)

	p, err := NewClaudeCodeProvider(config.ProviderConfig{
		Name: "test-cc",
		Type: "claude-code",
		ClaudeCode: &config.ClaudeCodeConfig{
			ClaudePath:     script,
			TimeoutSeconds: 30,
		},
	}, mapper)
	if err != nil {
		t.Fatal(err)
	}

	// Attach a session key to the context.
	ctx := types.WithRequestContext(context.Background(), "chan-1", "user-1", "conduit-sess-42")

	req := &GenerateRequest{
		Messages: []ChatMessage{{Role: "user", Content: "test"}},
	}

	_, err = p.GenerateResponse(ctx, req)
	if err != nil {
		t.Fatal(err)
	}

	// Verify the mapping was saved.
	ccSess, err := mapper.GetClaudeCodeSession("conduit-sess-42")
	if err != nil {
		t.Fatal(err)
	}
	if ccSess != "cc-new-sess" {
		t.Errorf("saved cc session = %q, want %q", ccSess, "cc-new-sess")
	}
}

// --- GenerateResponseStreaming with mock script ---

func TestClaudeCodeProvider_GenerateResponseStreaming(t *testing.T) {
	// Build a mock script that outputs stream-json events line by line.
	// Uses the real stream-json format: stream_event for deltas, message for final.
	streamOutput := `{"type":"stream_event","event":{"delta":{"type":"text_delta","text":"Hello "}}}
{"type":"stream_event","event":{"delta":{"type":"text_delta","text":"world!"}}}
{"type":"message","session_id":"stream-sess","role":"assistant","content":[{"type":"text","text":"Hello world!"}],"usage":{"input_tokens":15,"output_tokens":8}}`

	script := writeMockScript(t, "claude", fmt.Sprintf("cat <<'STREAMEOF'\n%s\nSTREAMEOF", streamOutput))

	p, err := NewClaudeCodeProvider(config.ProviderConfig{
		Name: "test-stream",
		Type: "claude-code",
		ClaudeCode: &config.ClaudeCodeConfig{
			ClaudePath:     script,
			TimeoutSeconds: 30,
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := &GenerateRequest{
		Messages: []ChatMessage{{Role: "user", Content: "stream test"}},
	}

	var deltas []string
	var doneCalled bool
	onDelta := func(delta string, done bool) {
		if delta != "" {
			deltas = append(deltas, delta)
		}
		if done {
			doneCalled = true
		}
	}

	resp, err := p.GenerateResponseStreaming(context.Background(), req, onDelta)
	if err != nil {
		t.Fatalf("GenerateResponseStreaming: %v", err)
	}

	if resp.Content != "Hello world!" {
		t.Errorf("Content = %q, want %q", resp.Content, "Hello world!")
	}
	if !doneCalled {
		t.Error("done callback was not called")
	}
	if len(deltas) != 2 {
		t.Errorf("got %d deltas, want 2: %v", len(deltas), deltas)
	}
	if resp.Usage.PromptTokens != 15 || resp.Usage.CompletionTokens != 8 {
		t.Errorf("Usage = %+v, want prompt=15 completion=8", resp.Usage)
	}
}

// --- Error handling ---

func TestClaudeCodeProvider_GenerateResponse_ErrorExit(t *testing.T) {
	script := writeMockScript(t, "claude", `echo "authentication error: invalid API key" >&2; exit 1`)

	p, err := NewClaudeCodeProvider(config.ProviderConfig{
		Name: "test-err",
		Type: "claude-code",
		ClaudeCode: &config.ClaudeCodeConfig{
			ClaudePath:     script,
			TimeoutSeconds: 30,
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := &GenerateRequest{
		Messages: []ChatMessage{{Role: "user", Content: "fail"}},
	}

	_, err = p.GenerateResponse(context.Background(), req)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// The error should be classified as authentication.
	category := ClassifyError(err)
	if category != CategoryAuthentication {
		t.Errorf("error category = %d, want CategoryAuthentication (%d)", category, CategoryAuthentication)
	}
}

func TestClaudeCodeProvider_GenerateResponse_RateLimitError(t *testing.T) {
	script := writeMockScript(t, "claude", `echo "rate limit exceeded, 429" >&2; exit 1`)

	p, err := NewClaudeCodeProvider(config.ProviderConfig{
		Name: "test-rl",
		Type: "claude-code",
		ClaudeCode: &config.ClaudeCodeConfig{
			ClaudePath:     script,
			TimeoutSeconds: 30,
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := &GenerateRequest{
		Messages: []ChatMessage{{Role: "user", Content: "fail"}},
	}

	_, err = p.GenerateResponse(context.Background(), req)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	category := ClassifyError(err)
	if category != CategoryRateLimit {
		t.Errorf("error category = %d, want CategoryRateLimit (%d)", category, CategoryRateLimit)
	}
}

func TestClaudeCodeProvider_GenerateResponse_NoUserMessage(t *testing.T) {
	script := writeMockScript(t, "claude", `echo '{}'`)

	p, err := NewClaudeCodeProvider(config.ProviderConfig{
		Name: "test",
		Type: "claude-code",
		ClaudeCode: &config.ClaudeCodeConfig{
			ClaudePath:     script,
			TimeoutSeconds: 30,
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := &GenerateRequest{
		Messages: []ChatMessage{
			{Role: "system", Content: "you are helpful"},
		},
	}

	_, err = p.GenerateResponse(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for missing user message")
	}
	if !strings.Contains(err.Error(), "no user message") {
		t.Errorf("error = %v, expected 'no user message'", err)
	}
}

// --- classifyClaudeCodeError ---

func TestClassifyClaudeCodeError(t *testing.T) {
	tests := []struct {
		name     string
		stderr   string
		exitCode int
		wantCat  AIErrorCategory
	}{
		{"auth", "authentication failed", 1, CategoryAuthentication},
		{"rate limit", "rate_limit: too many requests", 1, CategoryRateLimit},
		{"overloaded", "service overloaded, try later", 1, CategoryRateLimit}, // "overloaded" -> rate limit via ClassifyError
		{"timeout", "operation timed out", 1, CategoryTimeout},
		{"unknown", "something unexpected", 1, CategoryUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := classifyClaudeCodeError(tt.stderr, tt.exitCode, nil)
			got := ClassifyError(err)
			if got != tt.wantCat {
				t.Errorf("ClassifyError(%v) = %d, want %d", err, got, tt.wantCat)
			}
		})
	}
}

// --- Config defaults ---

func TestClaudeCodeOrDefault(t *testing.T) {
	cfg := config.ProviderConfig{
		Name: "cc",
		Type: "claude-code",
	}
	cc := cfg.ClaudeCodeOrDefault()

	if cc.ClaudePath != "claude" {
		t.Errorf("ClaudePath = %q, want 'claude'", cc.ClaudePath)
	}
	if cc.TimeoutSeconds != 300 {
		t.Errorf("TimeoutSeconds = %d, want 300", cc.TimeoutSeconds)
	}
	if cc.MaxTurns != 25 {
		t.Errorf("MaxTurns = %d, want 25", cc.MaxTurns)
	}
}

func TestClaudeCodeOrDefault_ExplicitValues(t *testing.T) {
	cfg := config.ProviderConfig{
		Name: "cc",
		Type: "claude-code",
		ClaudeCode: &config.ClaudeCodeConfig{
			ClaudePath:     "/custom/claude",
			TimeoutSeconds: 60,
			MaxTurns:       3,
		},
	}
	cc := cfg.ClaudeCodeOrDefault()

	if cc.ClaudePath != "/custom/claude" {
		t.Errorf("ClaudePath = %q", cc.ClaudePath)
	}
	if cc.TimeoutSeconds != 60 {
		t.Errorf("TimeoutSeconds = %d", cc.TimeoutSeconds)
	}
	if cc.MaxTurns != 3 {
		t.Errorf("MaxTurns = %d", cc.MaxTurns)
	}
}

// --- Constructor validation ---

func TestNewClaudeCodeProvider_BinaryNotFound(t *testing.T) {
	_, err := NewClaudeCodeProvider(config.ProviderConfig{
		Name: "test",
		Type: "claude-code",
		ClaudeCode: &config.ClaudeCodeConfig{
			ClaudePath: "/nonexistent/claude-binary-xyz",
		},
	}, nil)
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %v, expected 'not found'", err)
	}
}

// --- helpers ---

func sliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- extractSystemContent ---

func TestExtractSystemContent(t *testing.T) {
	tests := []struct {
		name     string
		messages []ChatMessage
		want     string
	}{
		{
			name:     "no system messages",
			messages: []ChatMessage{{Role: "user", Content: "hello"}},
			want:     "",
		},
		{
			name:     "single system message",
			messages: []ChatMessage{{Role: "system", Content: "You are helpful."}},
			want:     "You are helpful.",
		},
		{
			name: "multiple system messages joined",
			messages: []ChatMessage{
				{Role: "system", Content: "Identity section"},
				{Role: "system", Content: "Memory section"},
			},
			want: "Identity section\n\nMemory section",
		},
		{
			name: "mixed roles preserves only system in order",
			messages: []ChatMessage{
				{Role: "system", Content: "First system"},
				{Role: "user", Content: "user msg"},
				{Role: "assistant", Content: "reply"},
				{Role: "system", Content: "Second system"},
			},
			want: "First system\n\nSecond system",
		},
		{
			name: "empty system content skipped",
			messages: []ChatMessage{
				{Role: "system", Content: "Real content"},
				{Role: "system", Content: ""},
				{Role: "system", Content: "More content"},
			},
			want: "Real content\n\nMore content",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractSystemContent(tt.messages)
			if got != tt.want {
				t.Errorf("extractSystemContent() = %q, want %q", got, tt.want)
			}
		})
	}
}

// --- buildCommand system prompt passthrough ---

func TestBuildCommand_SystemPrompt(t *testing.T) {
	provider := &ClaudeCodeProvider{
		name: "test",
		config: config.ClaudeCodeConfig{
			ClaudePath:     "claude",
			TimeoutSeconds: 30,
		},
	}
	ctx := context.Background()

	t.Run("empty system content adds no flag", func(t *testing.T) {
		cmd, cleanup, err := provider.buildCommand(ctx, "hello", "", "", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer cleanup()

		for _, arg := range cmd.Args {
			if arg == "--append-system-prompt-file" {
				t.Error("found --append-system-prompt-file flag with empty system content")
			}
		}
	})

	t.Run("non-empty system content creates temp file", func(t *testing.T) {
		systemContent := "You are Conduit, a helpful AI assistant.\n\nRemember the user's preferences."
		cmd, cleanup, err := provider.buildCommand(ctx, "hello", systemContent, "", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Find the temp file path from args.
		var tempPath string
		for i, arg := range cmd.Args {
			if arg == "--append-system-prompt-file" && i+1 < len(cmd.Args) {
				tempPath = cmd.Args[i+1]
				break
			}
		}
		if tempPath == "" {
			t.Fatal("--append-system-prompt-file flag not found in args")
		}

		// Verify temp file exists and has correct content.
		content, err := os.ReadFile(tempPath)
		if err != nil {
			t.Fatalf("failed to read temp file: %v", err)
		}
		if string(content) != systemContent {
			t.Errorf("temp file content = %q, want %q", string(content), systemContent)
		}

		// Cleanup should remove the file.
		cleanup()
		if _, err := os.Stat(tempPath); !os.IsNotExist(err) {
			t.Error("temp file still exists after cleanup")
		}
	})

	t.Run("prompt is still last argument", func(t *testing.T) {
		cmd, cleanup, err := provider.buildCommand(ctx, "my question", "system stuff", "", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer cleanup()

		last := cmd.Args[len(cmd.Args)-1]
		if last != "my question" {
			t.Errorf("last arg = %q, want %q", last, "my question")
		}
	})
}

// assertContainsSequence checks that args contains key followed by value.
func assertContainsSequence(t *testing.T, args []string, key, value string) {
	t.Helper()
	for i := 0; i < len(args)-1; i++ {
		if args[i] == key && args[i+1] == value {
			return
		}
	}
	t.Errorf("args %v does not contain %q %q sequence", args, key, value)
}
