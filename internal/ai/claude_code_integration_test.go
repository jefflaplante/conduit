package ai

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"conduit/internal/config"
	"conduit/internal/sessions"
	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// --- Integration test helpers ---

// writeMockClaude writes an executable shell script simulating the claude CLI.
func writeMockClaude(t *testing.T, name, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell mock scripts require unix")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0755)
	require.NoError(t, err)
	return path
}

// newIntegrationSessionMapper creates a ClaudeCodeSessionMapper backed by an on-disk
// SQLite DB in a temp directory (closer to production than in-memory).
func newIntegrationSessionMapper(t *testing.T) *sessions.ClaudeCodeSessionMapper {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "integration_test.db")
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	mapper := sessions.NewClaudeCodeSessionMapper(db)
	require.NoError(t, mapper.EnsureTable())
	return mapper
}

// mockStreamOutput returns a valid stream-json string for testing.
func mockStreamOutput(text, sessionID string, inputTokens, outputTokens int) string {
	// Build individual word deltas from the text.
	words := strings.Fields(text)
	var lines []string
	for i, w := range words {
		if i > 0 {
			w = " " + w
		}
		lines = append(lines, fmt.Sprintf(
			`{"type":"stream_event","event":{"delta":{"type":"text_delta","text":"%s"}}}`, w))
	}
	// Final message event with session_id and usage.
	lines = append(lines, fmt.Sprintf(
		`{"type":"message","session_id":"%s","role":"assistant","content":[{"type":"text","text":"%s"}],"usage":{"input_tokens":%d,"output_tokens":%d}}`,
		sessionID, text, inputTokens, outputTokens))
	return strings.Join(lines, "\n") + "\n"
}

// mockJSONOutput returns a valid JSON output string for testing.
func mockJSONOutput(result, sessionID string, inputTokens, outputTokens int) string {
	return fmt.Sprintf(
		`{"result":"%s","session_id":"%s","usage":{"inputTokens":%d,"outputTokens":%d}}`,
		result, sessionID, inputTokens, outputTokens)
}

// --- Integration Tests ---

// TestIntegration_FullProviderFlowWithSessionMapping verifies the full request
// lifecycle: first request creates a new session mapping, second request uses
// --resume with the saved CC session ID.
func TestIntegration_FullProviderFlowWithSessionMapping(t *testing.T) {
	// The mock script writes received arguments to a file so we can inspect them,
	// then outputs a JSON response with a session_id.
	argsFile := filepath.Join(t.TempDir(), "args.log")
	script := writeMockClaude(t, "claude", fmt.Sprintf(`
# Append all arguments to the log file
echo "$@" >> %s
# Produce a JSON result with a session_id
echo '{"result":"response","session_id":"cc-session-new-abc","usage":{"inputTokens":10,"outputTokens":5}}'
`, argsFile))

	mapper := newIntegrationSessionMapper(t)

	p, err := NewClaudeCodeProvider(config.ProviderConfig{
		Name: "integration-test",
		Type: "claude-code",
		ClaudeCode: &config.ClaudeCodeConfig{
			ClaudePath:     script,
			TimeoutSeconds: 30,
		},
	}, mapper)
	require.NoError(t, err)

	ctx := types.WithRequestContext(context.Background(), "ch-1", "user-1", "conduit-session-1")
	req := &GenerateRequest{
		Messages: []ChatMessage{{Role: "user", Content: "first message"}},
	}

	// --- First request: no --resume expected ---
	resp, err := p.GenerateResponse(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, "response", resp.Content)
	assert.Equal(t, 10, resp.Usage.PromptTokens)
	assert.Equal(t, 5, resp.Usage.CompletionTokens)

	// Verify session mapping was saved.
	ccSess, err := mapper.GetClaudeCodeSession("conduit-session-1")
	require.NoError(t, err)
	assert.Equal(t, "cc-session-new-abc", ccSess)

	// Read the args log to verify no --resume on first call.
	argsData, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	firstCallArgs := strings.TrimSpace(strings.Split(string(argsData), "\n")[0])
	assert.NotContains(t, firstCallArgs, "--resume",
		"first call should not use --resume")

	// --- Second request: --resume expected ---
	req2 := &GenerateRequest{
		Messages: []ChatMessage{{Role: "user", Content: "follow-up message"}},
	}
	resp2, err := p.GenerateResponse(ctx, req2)
	require.NoError(t, err)
	assert.Equal(t, "response", resp2.Content)

	// Read args log again.
	argsData, err = os.ReadFile(argsFile)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(argsData)), "\n")
	require.Len(t, lines, 2, "should have logged two invocations")

	secondCallArgs := lines[1]
	assert.Contains(t, secondCallArgs, "--resume")
	assert.Contains(t, secondCallArgs, "cc-session-new-abc",
		"second call should resume the saved CC session")
}

// TestIntegration_ProviderWithAllCLIFlags verifies the exact command-line
// arguments for a fully-configured provider.
func TestIntegration_ProviderWithAllCLIFlags(t *testing.T) {
	// Script captures all args and exits.
	argsFile := filepath.Join(t.TempDir(), "args.log")
	script := writeMockClaude(t, "claude", fmt.Sprintf(`
echo "$@" > %s
echo '{"result":"ok","session_id":"s1","usage":{"inputTokens":1,"outputTokens":1}}'
`, argsFile))

	mapper := newIntegrationSessionMapper(t)
	require.NoError(t, mapper.SaveMapping("sess-x", "cc-existing"))

	workDir := t.TempDir()

	p, err := NewClaudeCodeProvider(config.ProviderConfig{
		Name:  "full-flags",
		Type:  "claude-code",
		Model: "claude-opus-4",
		ClaudeCode: &config.ClaudeCodeConfig{
			ClaudePath:     script,
			AllowedTools:   []string{"Read", "Write", "Bash"},
			PermissionMode: "bypassPermissions",
			MaxTurns:       10,
			TimeoutSeconds: 120,
			WorkingDir:     workDir,
		},
	}, mapper)
	require.NoError(t, err)

	ctx := types.WithRequestContext(context.Background(), "ch", "usr", "sess-x")
	req := &GenerateRequest{
		Messages: []ChatMessage{{Role: "user", Content: "do things"}},
	}
	_, err = p.GenerateResponse(ctx, req)
	require.NoError(t, err)

	argsData, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	args := string(argsData)

	// Verify all expected flags appear.
	assert.Contains(t, args, "--output-format json")
	assert.Contains(t, args, "--model claude-opus-4")
	assert.Contains(t, args, "--resume cc-existing")
	assert.Contains(t, args, "--allowedTools Read,Write,Bash")
	assert.Contains(t, args, "--permission-mode bypassPermissions")
	assert.Contains(t, args, "--max-turns 10")
	// The user prompt should be the last argument.
	assert.True(t, strings.HasSuffix(strings.TrimSpace(args), "do things"),
		"user prompt should be last arg")
}

// TestIntegration_ProviderErrorRecovery verifies that a provider remains functional
// after encountering an error. The first invocation fails, the second succeeds.
func TestIntegration_ProviderErrorRecovery(t *testing.T) {
	// The script checks an invocation counter file. First call fails, second succeeds.
	counterFile := filepath.Join(t.TempDir(), "counter")
	require.NoError(t, os.WriteFile(counterFile, []byte("0"), 0644))

	script := writeMockClaude(t, "claude", fmt.Sprintf(`
count=$(cat %s)
next=$((count + 1))
echo "$next" > %s
if [ "$count" = "0" ]; then
  echo "rate limit exceeded, 429" >&2
  exit 1
fi
echo '{"result":"recovered","session_id":"s-ok","usage":{"inputTokens":5,"outputTokens":3}}'
`, counterFile, counterFile))

	p, err := NewClaudeCodeProvider(config.ProviderConfig{
		Name: "error-recovery",
		Type: "claude-code",
		ClaudeCode: &config.ClaudeCodeConfig{
			ClaudePath:     script,
			TimeoutSeconds: 30,
		},
	}, nil)
	require.NoError(t, err)

	req := &GenerateRequest{
		Messages: []ChatMessage{{Role: "user", Content: "test"}},
	}

	// First call should fail with rate limit error.
	_, err = p.GenerateResponse(context.Background(), req)
	require.Error(t, err)
	category := ClassifyError(err)
	assert.Equal(t, CategoryRateLimit, category,
		"first call should be classified as rate limit error")

	// Second call should succeed (provider is still functional).
	resp, err := p.GenerateResponse(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "recovered", resp.Content)
	assert.Equal(t, 5, resp.Usage.PromptTokens)
	assert.Equal(t, 3, resp.Usage.CompletionTokens)
}

// TestIntegration_StreamingEndToEnd verifies that stream-json output is parsed
// correctly, all deltas arrive in order, and the done signal fires at the end.
func TestIntegration_StreamingEndToEnd(t *testing.T) {
	streamData := mockStreamOutput("Hello beautiful world", "stream-sess-1", 20, 12)
	script := writeMockClaude(t, "claude", fmt.Sprintf("cat <<'STREAMEOF'\n%sSTREAMEOF", streamData))

	mapper := newIntegrationSessionMapper(t)

	p, err := NewClaudeCodeProvider(config.ProviderConfig{
		Name: "stream-e2e",
		Type: "claude-code",
		ClaudeCode: &config.ClaudeCodeConfig{
			ClaudePath:     script,
			TimeoutSeconds: 30,
		},
	}, mapper)
	require.NoError(t, err)

	ctx := types.WithRequestContext(context.Background(), "ch", "user", "conduit-stream-sess")
	req := &GenerateRequest{
		Messages: []ChatMessage{{Role: "user", Content: "stream test"}},
	}

	var mu sync.Mutex
	var deltas []string
	var doneCount int

	onDelta := func(delta string, done bool) {
		mu.Lock()
		defer mu.Unlock()
		if done {
			doneCount++
		} else if delta != "" {
			deltas = append(deltas, delta)
		}
	}

	resp, err := p.GenerateResponseStreaming(ctx, req, onDelta)
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()

	// Verify content accumulated correctly.
	assert.Equal(t, "Hello beautiful world", resp.Content)

	// Verify all deltas arrived in order.
	assert.Len(t, deltas, 3, "should have 3 word deltas")
	assert.Equal(t, "Hello", deltas[0])
	assert.Equal(t, " beautiful", deltas[1])
	assert.Equal(t, " world", deltas[2])

	// Verify done signal was called exactly once.
	assert.Equal(t, 1, doneCount, "done callback should fire exactly once")

	// Verify usage.
	assert.Equal(t, 20, resp.Usage.PromptTokens)
	assert.Equal(t, 12, resp.Usage.CompletionTokens)
	assert.Equal(t, 32, resp.Usage.TotalTokens)

	// Verify session mapping was saved from the stream.
	ccSess, err := mapper.GetClaudeCodeSession("conduit-stream-sess")
	require.NoError(t, err)
	assert.Equal(t, "stream-sess-1", ccSess)
}

// TestIntegration_StreamingWithMultipleEventTypes verifies that the streaming
// provider correctly handles a mix of event types (text_delta, tool_use, retry).
func TestIntegration_StreamingWithMultipleEventTypes(t *testing.T) {
	streamData := strings.Join([]string{
		`{"type":"stream_event","event":{"delta":{"type":"text_delta","text":"Checking "}}}`,
		`{"type":"message","content":[{"type":"tool_use","name":"Read","input":{"file":"test.go"}}]}`,
		`{"type":"system/api_retry","attempt":1,"max_retries":3,"retry_delay_ms":500}`,
		`{"type":"stream_event","event":{"delta":{"type":"text_delta","text":"files. "}}}`,
		`{"type":"stream_event","event":{"delta":{"type":"text_delta","text":"Done."}}}`,
		`{"type":"message","session_id":"mixed-sess","role":"assistant","content":[{"type":"text","text":"Checking files. Done."}],"usage":{"input_tokens":30,"output_tokens":15}}`,
		"",
	}, "\n")

	script := writeMockClaude(t, "claude", fmt.Sprintf("cat <<'STREAMEOF'\n%sSTREAMEOF", streamData))

	p, err := NewClaudeCodeProvider(config.ProviderConfig{
		Name: "stream-mixed",
		Type: "claude-code",
		ClaudeCode: &config.ClaudeCodeConfig{
			ClaudePath:     script,
			TimeoutSeconds: 30,
		},
	}, nil)
	require.NoError(t, err)

	req := &GenerateRequest{
		Messages: []ChatMessage{{Role: "user", Content: "mixed events"}},
	}

	var deltas []string
	resp, err := p.GenerateResponseStreaming(context.Background(), req, func(delta string, done bool) {
		if !done && delta != "" {
			deltas = append(deltas, delta)
		}
	})
	require.NoError(t, err)

	// Only text_delta events contribute to content.
	assert.Equal(t, "Checking files. Done.", resp.Content)
	assert.Equal(t, []string{"Checking ", "files. ", "Done."}, deltas)
	assert.Equal(t, 30, resp.Usage.PromptTokens)
	assert.Equal(t, 15, resp.Usage.CompletionTokens)
}

// TestIntegration_ProviderTimeout verifies that the provider's configured
// timeout terminates a long-running process.
func TestIntegration_ProviderTimeout(t *testing.T) {
	// Script uses exec to replace the shell process (avoiding orphan child).
	// The `exec` ensures SIGKILL from exec.CommandContext kills the actual process.
	script := writeMockClaude(t, "claude", `exec sleep 300`)

	p, err := NewClaudeCodeProvider(config.ProviderConfig{
		Name: "timeout-test",
		Type: "claude-code",
		ClaudeCode: &config.ClaudeCodeConfig{
			ClaudePath:     script,
			TimeoutSeconds: 2, // Very short timeout.
		},
	}, nil)
	require.NoError(t, err)

	req := &GenerateRequest{
		Messages: []ChatMessage{{Role: "user", Content: "should timeout"}},
	}

	start := time.Now()
	_, err = p.GenerateResponse(context.Background(), req)
	elapsed := time.Since(start)

	require.Error(t, err, "should return error on timeout")
	// Should complete within a reasonable time after the 2s timeout.
	assert.Less(t, elapsed, 10*time.Second,
		"should return after timeout, not wait for the full sleep")
}

// TestIntegration_ProviderEmptyResponse verifies handling of an empty response
// from the claude CLI (e.g., process exits immediately with empty stdout).
func TestIntegration_ProviderEmptyResponse(t *testing.T) {
	script := writeMockClaude(t, "claude", `echo ""`)

	p, err := NewClaudeCodeProvider(config.ProviderConfig{
		Name: "empty-resp",
		Type: "claude-code",
		ClaudeCode: &config.ClaudeCodeConfig{
			ClaudePath:     script,
			TimeoutSeconds: 30,
		},
	}, nil)
	require.NoError(t, err)

	req := &GenerateRequest{
		Messages: []ChatMessage{{Role: "user", Content: "test"}},
	}

	resp, err := p.GenerateResponse(context.Background(), req)
	require.NoError(t, err)
	assert.Empty(t, resp.Content, "empty response should return empty content")
}

// TestIntegration_SessionMapperMultipleSessions verifies that multiple different
// conduit sessions can each have their own CC session mapping.
func TestIntegration_SessionMapperMultipleSessions(t *testing.T) {
	// The mock script echoes back a session_id based on the prompt content.
	// We use the prompt to control which CC session ID gets returned.
	script := writeMockClaude(t, "claude", `
# Get the last argument (POSIX-compatible)
for prompt; do :; done
case "$prompt" in
  *"session-a"*) echo '{"result":"a","session_id":"cc-aaa","usage":{"inputTokens":1,"outputTokens":1}}' ;;
  *"session-b"*) echo '{"result":"b","session_id":"cc-bbb","usage":{"inputTokens":1,"outputTokens":1}}' ;;
  *"session-c"*) echo '{"result":"c","session_id":"cc-ccc","usage":{"inputTokens":1,"outputTokens":1}}' ;;
  *) echo '{"result":"unknown","session_id":"cc-xxx","usage":{"inputTokens":1,"outputTokens":1}}' ;;
esac
`)

	mapper := newIntegrationSessionMapper(t)

	p, err := NewClaudeCodeProvider(config.ProviderConfig{
		Name: "multi-session",
		Type: "claude-code",
		ClaudeCode: &config.ClaudeCodeConfig{
			ClaudePath:     script,
			TimeoutSeconds: 30,
		},
	}, mapper)
	require.NoError(t, err)

	sessions := []struct {
		conduitID string
		prompt    string
		wantCCID  string
	}{
		{"conduit-1", "session-a", "cc-aaa"},
		{"conduit-2", "session-b", "cc-bbb"},
		{"conduit-3", "session-c", "cc-ccc"},
	}

	for _, s := range sessions {
		ctx := types.WithRequestContext(context.Background(), "ch", "usr", s.conduitID)
		req := &GenerateRequest{
			Messages: []ChatMessage{{Role: "user", Content: s.prompt}},
		}
		_, err := p.GenerateResponse(ctx, req)
		require.NoError(t, err)
	}

	// Verify each conduit session has its own CC session mapping.
	for _, s := range sessions {
		ccSess, err := mapper.GetClaudeCodeSession(s.conduitID)
		require.NoError(t, err)
		assert.Equal(t, s.wantCCID, ccSess,
			"conduit session %s should map to %s", s.conduitID, s.wantCCID)
	}
}

// TestIntegration_ErrorClassification verifies that different error scenarios
// from the claude CLI get classified into the correct error categories.
func TestIntegration_ErrorClassification(t *testing.T) {
	tests := []struct {
		name     string
		stderr   string
		wantCat  AIErrorCategory
	}{
		{
			name:    "authentication_error",
			stderr:  `echo "unauthorized: invalid API key, 401" >&2; exit 1`,
			wantCat: CategoryAuthentication,
		},
		{
			name:    "rate_limit_error",
			stderr:  `echo "too many requests: rate_limit exceeded" >&2; exit 1`,
			wantCat: CategoryRateLimit,
		},
		{
			name:    "service_unavailable",
			stderr:  `echo "service unavailable 503" >&2; exit 1`,
			wantCat: CategoryServiceUnavailable,
		},
		{
			name:    "timeout_error",
			stderr:  `echo "operation timed out" >&2; exit 1`,
			wantCat: CategoryTimeout,
		},
		{
			name:    "generic_error",
			stderr:  `echo "something unexpected happened" >&2; exit 1`,
			wantCat: CategoryUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			script := writeMockClaude(t, "claude", tt.stderr)

			p, err := NewClaudeCodeProvider(config.ProviderConfig{
				Name: "err-test",
				Type: "claude-code",
				ClaudeCode: &config.ClaudeCodeConfig{
					ClaudePath:     script,
					TimeoutSeconds: 30,
				},
			}, nil)
			require.NoError(t, err)

			req := &GenerateRequest{
				Messages: []ChatMessage{{Role: "user", Content: "trigger error"}},
			}

			_, err = p.GenerateResponse(context.Background(), req)
			require.Error(t, err)
			assert.Equal(t, tt.wantCat, ClassifyError(err),
				"error %q should classify as %d", err, tt.wantCat)
		})
	}
}

// --- helpers ---

// backdateSessionDB modifies the last_used_at of a session mapping to be
// the given duration in the past (for testing cleanup).
func backdateSessionDB(t *testing.T, db *sql.DB, conduitSessionID string, age time.Duration) {
	t.Helper()
	hours := int(age.Hours())
	_, err := db.Exec(fmt.Sprintf(`
		UPDATE claude_code_sessions
		SET last_used_at = datetime('now', '-%d hours')
		WHERE conduit_session_id = ?
	`, hours), conduitSessionID)
	require.NoError(t, err)
}

// newIntegrationSessionMapperWithDB returns both the mapper and underlying DB
// so tests can manipulate data directly.
func newIntegrationSessionMapperWithDB(t *testing.T) (*sessions.ClaudeCodeSessionMapper, *sql.DB) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "integration_test.db")
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	mapper := sessions.NewClaudeCodeSessionMapper(db)
	require.NoError(t, mapper.EnsureTable())
	return mapper, db
}

// TestIntegration_SessionMapperCleanupWithDB verifies that CleanupOld removes
// stale session mappings (backdated via direct SQL) but preserves recent ones.
func TestIntegration_SessionMapperCleanupWithDB(t *testing.T) {
	mapper, db := newIntegrationSessionMapperWithDB(t)

	// Create several mappings.
	require.NoError(t, mapper.SaveMapping("session-old-1", "cc-old-1"))
	require.NoError(t, mapper.SaveMapping("session-old-2", "cc-old-2"))
	require.NoError(t, mapper.SaveMapping("session-recent", "cc-recent"))
	require.NoError(t, mapper.SaveMapping("session-newest", "cc-newest"))

	// Backdate old sessions using direct SQL.
	backdateSessionDB(t, db, "session-old-1", 72*time.Hour)
	backdateSessionDB(t, db, "session-old-2", 48*time.Hour)

	// Cleanup sessions older than 24 hours.
	deleted, err := mapper.CleanupOld(24 * time.Hour)
	require.NoError(t, err)
	assert.Equal(t, int64(2), deleted, "should delete exactly 2 old sessions")

	// Old sessions should be gone.
	got, _ := mapper.GetClaudeCodeSession("session-old-1")
	assert.Empty(t, got)
	got, _ = mapper.GetClaudeCodeSession("session-old-2")
	assert.Empty(t, got)

	// Recent sessions should still exist.
	got, _ = mapper.GetClaudeCodeSession("session-recent")
	assert.Equal(t, "cc-recent", got)
	got, _ = mapper.GetClaudeCodeSession("session-newest")
	assert.Equal(t, "cc-newest", got)
}

// TestIntegration_SessionMappingPreservesCreatedAt verifies that repeated
// requests in the same session update last_used_at without resetting created_at.
func TestIntegration_SessionMappingPreservesCreatedAt(t *testing.T) {
	mapper, db := newIntegrationSessionMapperWithDB(t)

	// Mock script always returns the same session_id.
	script := writeMockClaude(t, "claude", `
echo '{"result":"ok","session_id":"cc-stable","usage":{"inputTokens":1,"outputTokens":1}}'
`)

	p, err := NewClaudeCodeProvider(config.ProviderConfig{
		Name: "test-update-last-used",
		Type: "claude-code",
		ClaudeCode: &config.ClaudeCodeConfig{
			ClaudePath:     script,
			TimeoutSeconds: 10,
		},
	}, mapper)
	require.NoError(t, err)

	ctx := types.WithRequestContext(context.Background(), "ch-1", "user-1", "conduit-session-ts")
	req := &GenerateRequest{Messages: []ChatMessage{{Role: "user", Content: "hello"}}}

	// First request — creates the mapping.
	_, err = p.GenerateResponse(ctx, req)
	require.NoError(t, err)

	// Read created_at from the DB.
	var createdAt1 string
	err = db.QueryRow(`SELECT created_at FROM claude_code_sessions WHERE conduit_session_id = ?`,
		"conduit-session-ts").Scan(&createdAt1)
	require.NoError(t, err)
	assert.NotEmpty(t, createdAt1)

	// Backdate created_at so we can detect if it gets reset.
	_, err = db.Exec(`UPDATE claude_code_sessions SET created_at = datetime('now', '-1 hour') WHERE conduit_session_id = ?`,
		"conduit-session-ts")
	require.NoError(t, err)

	var backdatedCreatedAt string
	db.QueryRow(`SELECT created_at FROM claude_code_sessions WHERE conduit_session_id = ?`,
		"conduit-session-ts").Scan(&backdatedCreatedAt)

	// Second request — should UpdateLastUsed, not SaveMapping.
	_, err = p.GenerateResponse(ctx, req)
	require.NoError(t, err)

	var createdAt2, lastUsed2 string
	err = db.QueryRow(`SELECT created_at, last_used_at FROM claude_code_sessions WHERE conduit_session_id = ?`,
		"conduit-session-ts").Scan(&createdAt2, &lastUsed2)
	require.NoError(t, err)

	assert.Equal(t, backdatedCreatedAt, createdAt2,
		"created_at should be preserved (not reset) on second call")
	assert.NotEqual(t, backdatedCreatedAt, lastUsed2,
		"last_used_at should be more recent than the backdated created_at")
}

// TestIntegration_StaleSessionRecovery verifies that when --resume targets an
// expired session, the provider deletes the mapping and retries successfully.
func TestIntegration_StaleSessionRecovery(t *testing.T) {
	counterFile := filepath.Join(t.TempDir(), "call_count")
	argsFile := filepath.Join(t.TempDir(), "args.log")

	// First invocation: write "session not found" to stderr and exit 1.
	// Second invocation: succeed.
	script := writeMockClaude(t, "claude", fmt.Sprintf(`
COUNTER=%s
ARGSLOG=%s
echo "$@" >> "$ARGSLOG"
if [ ! -f "$COUNTER" ]; then
  echo "1" > "$COUNTER"
  echo "Error: session not found" >&2
  exit 1
fi
echo '{"result":"recovered","session_id":"cc-new-session","usage":{"inputTokens":10,"outputTokens":5}}'
`, counterFile, argsFile))

	mapper := newIntegrationSessionMapper(t)
	// Pre-seed a stale session mapping.
	require.NoError(t, mapper.SaveMapping("conduit-stale", "cc-expired-session"))

	p, err := NewClaudeCodeProvider(config.ProviderConfig{
		Name: "stale-recovery-test",
		Type: "claude-code",
		ClaudeCode: &config.ClaudeCodeConfig{
			ClaudePath:     script,
			TimeoutSeconds: 10,
		},
	}, mapper)
	require.NoError(t, err)

	ctx := types.WithRequestContext(context.Background(), "ch-1", "user-1", "conduit-stale")
	req := &GenerateRequest{Messages: []ChatMessage{{Role: "user", Content: "hello"}}}

	resp, err := p.GenerateResponse(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, "recovered", resp.Content)

	// Verify the retry used no --resume flag.
	argsData, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	argLines := strings.Split(strings.TrimSpace(string(argsData)), "\n")
	require.Len(t, argLines, 2, "should have exactly 2 invocations (fail + retry)")

	assert.Contains(t, argLines[0], "--resume",
		"first call should have used --resume")
	assert.NotContains(t, argLines[1], "--resume",
		"retry call should NOT have used --resume")

	// Verify the new session mapping was saved.
	ccSess, err := mapper.GetClaudeCodeSession("conduit-stale")
	require.NoError(t, err)
	assert.Equal(t, "cc-new-session", ccSess)
}
