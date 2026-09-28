package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"conduit/internal/config"
	"conduit/internal/sessions"
)

// conduit-2lzv: persistent metadata-only LLM call log.

const (
	sentinelPrompt  = "SENTINEL_PROMPT_q7Zx"
	sentinelResp    = "SENTINEL_RESPONSE_k2Wm"
	sentinelToolArg = "SENTINEL_TOOLARG_p9Lr"
	// Telegram-token-shaped secret embedded in a provider error.
	sentinelTGSecret = "AAHsentinelTelegramSecret0123456789xyz"
)

// attachCallLog gives r a call log in a temp dir and returns a reader that
// closes the log (flushing it) and returns the raw bytes and the records.
func attachCallLog(t *testing.T, r *Router) func() ([]byte, []CallRecord) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "logs", DefaultCallLogFile)
	cl := NewCallLog(CallLogOptions{Path: path})
	r.SetCallLog(cl)
	t.Cleanup(func() { _ = cl.Close(context.Background()) })
	return func() ([]byte, []CallRecord) {
		t.Helper()
		if err := r.CloseCallLog(context.Background()); err != nil {
			t.Fatalf("close call log: %v", err)
		}
		raw, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return raw, parseCallLog(t, raw)
	}
}

func parseCallLog(t *testing.T, raw []byte) []CallRecord {
	t.Helper()
	var recs []CallRecord
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		var rec CallRecord
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("corrupt line %q: %v", sc.Text(), err)
		}
		recs = append(recs, rec)
	}
	return recs
}

func phases(recs []CallRecord) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = fmt.Sprintf("%s#%d", r.Phase, r.Attempt)
	}
	return out
}

func assertPhases(t *testing.T, recs []CallRecord, want ...string) {
	t.Helper()
	got := phases(recs)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("records = %v, want %v", got, want)
	}
}

func assertNoContent(t *testing.T, raw []byte) {
	t.Helper()
	for _, s := range []string{sentinelPrompt, sentinelResp, sentinelToolArg, sentinelTGSecret} {
		if bytes.Contains(raw, []byte(s)) {
			t.Errorf("call log contains %q:\n%s", s, raw)
		}
	}
}

// callLogRouter is a single-provider router (z-ai, default glm-5.3, priced)
// with a tool loop of `rounds` extra provider calls.
func callLogRouter(t *testing.T, rounds int) (*Router, *MockProvider, *sessions.Session) {
	t.Helper()
	cfg := config.AIConfig{DefaultProvider: "z-ai", PricingOverrides: map[string]config.PricingOverride{
		"glm-5.3": {InputPerMToken: 1.4, OutputPerMToken: 4.4},
	}}
	r, err := NewRouterWithExecution(cfg, nil, &sumToolLoopEngine{rounds: rounds})
	if err != nil {
		t.Fatal(err)
	}
	mock := NewMockProvider("z-ai")
	r.RegisterProvider("z-ai", mock)
	r.providerMeta["z-ai"] = ProviderMeta{Name: "z-ai", Type: "openai", DefaultModel: "glm-5.3"}
	sess := &sessions.Session{Key: "telegram_42", ChannelID: "telegram", UserID: "u1", Context: map[string]string{"label": "main-chat"}}
	return r, mock, sess
}

func sentinelToolCalls() []ToolCall {
	return []ToolCall{{ID: "t1", Name: "Bash", Args: map[string]interface{}{"command": sentinelToolArg}}}
}

func TestCallLog_ToolLoopGuardRetry_OneRecordPerCall(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("streaming=%v", streaming), func(t *testing.T) {
			r, mock, sess := callLogRouter(t, 2)
			read := attachCallLog(t, r)
			u := Usage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110, CacheCreationInputTokens: 7, CacheReadInputTokens: 30}
			mock.SetResponses([]MockResponse{
				{Content: "", Usage: u}, // raw-empty → EmptyGuard retry
				{Content: sentinelResp + " a", ToolCalls: sentinelToolCalls(), Usage: u},
				{Content: sentinelResp + " b", ToolCalls: sentinelToolCalls(), Usage: u},
				{Content: sentinelResp + " final", Usage: u},
			})
			var err error
			if streaming {
				_, err = r.GenerateResponseStreaming(context.Background(), sess, sentinelPrompt, "", "", func(string, bool) {})
			} else {
				_, err = r.GenerateResponseWithTools(context.Background(), sess, sentinelPrompt, "", "")
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, recs := read()
			assertNoContent(t, raw)
			if mock.GetCallCount() != 4 {
				t.Fatalf("provider calls = %d", mock.GetCallCount())
			}
			assertPhases(t, recs, "depth0#1", "empty-guard#1", "depth1#1", "depth2#1")
			for i, rec := range recs {
				if rec.SessionKey != "telegram_42" || rec.SessionLabel != "main-chat" || rec.AgentKind != "main" || rec.Channel != "telegram" {
					t.Errorf("rec %d session fields = %+v", i, rec)
				}
				if rec.TurnID == "" || rec.TurnID != recs[0].TurnID {
					t.Errorf("rec %d turn_id = %q, want shared %q", i, rec.TurnID, recs[0].TurnID)
				}
				if rec.Provider != "z-ai" || rec.Model != "glm-5.3" || rec.HandedOff || rec.FallbackFrom != "" {
					t.Errorf("rec %d route = %+v", i, rec)
				}
				if rec.ErrorClass != ErrClassNone || rec.PromptTokens != 100 || rec.CompletionTokens != 10 ||
					rec.TotalTokens != 110 || rec.CacheCreationTokens != 7 || rec.CacheReadTokens != 30 {
					t.Errorf("rec %d tokens/class = %+v", i, rec)
				}
				if rec.Priced == nil || !*rec.Priced || rec.CostUSD <= 0 {
					t.Errorf("rec %d cost = %v priced=%v", i, rec.CostUSD, rec.Priced)
				}
				wantStream := streaming && i == 0
				if rec.Streaming != wantStream {
					t.Errorf("rec %d streaming = %v, want %v", i, rec.Streaming, wantStream)
				}
			}
			if !recs[0].Empty || recs[1].ToolCalls != 1 {
				t.Errorf("empty/tool_calls flags: %+v / %+v", recs[0], recs[1])
			}
		})
	}
}

func TestCallLog_StreamingTTFT(t *testing.T) {
	r, mock, sess := callLogRouter(t, 0)
	read := attachCallLog(t, r)
	mock.SetResponses([]MockResponse{{Content: sentinelResp, Usage: Usage{PromptTokens: 5, CompletionTokens: 1}}})
	if _, err := r.GenerateResponseStreaming(context.Background(), sess, sentinelPrompt, "", "", func(string, bool) {}); err != nil {
		t.Fatal(err)
	}
	raw, recs := read()
	assertNoContent(t, raw)
	assertPhases(t, recs, "depth0#1")
	if !recs[0].Streaming || recs[0].TTFTMs == nil || *recs[0].TTFTMs < 0 {
		t.Fatalf("streaming/ttft = %v/%v", recs[0].Streaming, recs[0].TTFTMs)
	}
	if recs[0].TotalTokens != 6 {
		t.Errorf("total_tokens = %d, want prompt+completion", recs[0].TotalTokens)
	}
}

func TestCallLog_AutoContinue(t *testing.T) {
	r, mock, sess := callLogRouter(t, 0)
	read := attachCallLog(t, r)
	mock.SetResponses([]MockResponse{
		{Content: sentinelResp + " part1", FinishReason: "length"},
		{Content: sentinelResp + " part2", FinishReason: "stop"},
	})
	if _, err := r.GenerateResponseWithTools(context.Background(), sess, sentinelPrompt, "", ""); err != nil {
		t.Fatal(err)
	}
	raw, recs := read()
	assertNoContent(t, raw)
	assertPhases(t, recs, "depth0#1", "continue#1")
	if recs[0].FinishReason != "length" || recs[1].FinishReason != "stop" {
		t.Errorf("finish reasons = %q, %q", recs[0].FinishReason, recs[1].FinishReason)
	}
}

func TestCallLog_TimeoutRetryHandoff(t *testing.T) {
	for name, run := range handoffPaths(t) {
		t.Run(name, func(t *testing.T) {
			router, primary, fallback := newFallbackTestRouter(t)
			read := attachCallLog(t, router)
			primary.SetResponses([]MockResponse{{Error: errFallbackTimeout}, {Error: errFallbackTimeout}})
			fallback.SetResponses([]MockResponse{{Content: sentinelResp, Usage: Usage{PromptTokens: 10, CompletionTokens: 2}}})
			if _, err := run(router, context.Background()); err != nil {
				t.Fatal(err)
			}
			raw, recs := read()
			assertNoContent(t, raw)
			assertPhases(t, recs, "depth0#1", "depth0#2", "depth0#3")
			for i := 0; i < 2; i++ {
				if recs[i].Provider != "primary" || recs[i].ErrorClass != ErrClassTimeout || recs[i].Error == "" || recs[i].HandedOff {
					t.Errorf("primary attempt %d = %+v", i+1, recs[i])
				}
				if recs[i].Priced != nil {
					t.Errorf("error record carries priced flag")
				}
			}
			fb := recs[2]
			if fb.Provider != "fallbackprov" || fb.Model != handoffFBModel || !fb.HandedOff ||
				fb.FallbackFrom != "claude-haiku-4-5-20251001" || fb.ErrorClass != ErrClassNone {
				t.Errorf("handoff record = %+v", fb)
			}
		})
	}
}

// A guarded tool-loop call that times out twice and hands off: one logical
// call (depth1) with attempts 1..3.
func TestCallLog_ToolLoopTimeoutHandoff(t *testing.T) {
	router, primary, fallback := newFallbackTestRouter(t)
	router.executionEngine = &sumToolLoopEngine{rounds: 1}
	read := attachCallLog(t, router)
	primary.SetResponses([]MockResponse{
		{ToolCalls: sentinelToolCalls()},
		{Error: errFallbackTimeout},
		{Error: errFallbackTimeout},
	})
	fallback.SetResponses([]MockResponse{{Content: sentinelResp}})
	if _, err := router.GenerateResponseWithToolsAndProgress(context.Background(), newFallbackSession(t), sentinelPrompt, "primary", "claude-haiku-4-5-20251001", nil); err != nil {
		t.Fatal(err)
	}
	raw, recs := read()
	assertNoContent(t, raw)
	assertPhases(t, recs, "depth0#1", "depth1#1", "depth1#2", "depth1#3")
	if !recs[3].HandedOff || recs[3].Provider != "fallbackprov" || recs[3].FallbackFrom != "claude-haiku-4-5-20251001" {
		t.Errorf("handoff record = %+v", recs[3])
	}
}

func TestCallLog_ErrorRedactedAndClassified(t *testing.T) {
	r, mock, sess := callLogRouter(t, 0)
	read := attachCallLog(t, r)
	tokErr := fmt.Errorf("post https://api.telegram.org/bot123456789:%s/getUpdates: %w", sentinelTGSecret, context.DeadlineExceeded)
	mock.SetResponses([]MockResponse{{Error: tokErr}, {Error: tokErr}})
	if _, err := r.GenerateResponseWithTools(context.Background(), sess, sentinelPrompt, "", ""); err == nil {
		t.Fatal("expected error")
	}
	raw, recs := read()
	assertNoContent(t, raw)
	assertPhases(t, recs, "depth0#1", "depth0#2") // original + bd-13p retry, no fallback configured
	for _, rec := range recs {
		if rec.ErrorClass != ErrClassTimeout || !strings.Contains(rec.Error, "<REDACTED>") {
			t.Errorf("error record = %+v", rec)
		}
	}
}

// sideCallEngine makes a side call from inside the tool loop (as the Image
// tool does) and then one more provider call.
type sideCallEngine struct{ r *Router }

func (e *sideCallEngine) HandleToolCallFlow(ctx context.Context, provider Provider, req *GenerateRequest, resp *GenerateResponse) (ConversationResponse, error) {
	if _, err := e.r.GenerateSideCall(WithSideCallLabel(ctx, "vision"), "vis", &GenerateRequest{Model: "vis-model",
		Messages: []ChatMessage{{Role: "user", Content: sentinelPrompt}}}); err != nil {
		return nil, err
	}
	r, err := provider.GenerateResponse(ctx, req)
	if err != nil {
		return nil, err
	}
	return &SimpleConversationResponse{Content: r.Content, Usage: &r.Usage, Steps: 2}, nil
}

func TestCallLog_SideCall(t *testing.T) {
	r, mock, sess := callLogRouter(t, 0)
	r.executionEngine = &sideCallEngine{r: r}
	vis := NewMockProvider("vis")
	vis.SetResponses([]MockResponse{{Content: sentinelResp}})
	r.RegisterProvider("vis", vis)
	read := attachCallLog(t, r)
	mock.SetResponses([]MockResponse{{ToolCalls: sentinelToolCalls()}, {Content: sentinelResp}})
	if _, err := r.GenerateResponseWithTools(context.Background(), sess, sentinelPrompt, "", ""); err != nil {
		t.Fatal(err)
	}
	// Outside any turn.
	vis.SetResponses([]MockResponse{{Content: sentinelResp}})
	if _, err := r.GenerateSideCall(context.Background(), "vis", &GenerateRequest{Model: "vis-model"}); err != nil {
		t.Fatal(err)
	}
	raw, recs := read()
	assertNoContent(t, raw)
	assertPhases(t, recs, "depth0#1", "side-call:vision#1", "depth1#1", "side-call#1")
	if recs[1].SessionKey != "telegram_42" || recs[1].TurnID != recs[0].TurnID || recs[1].HandedOff || recs[1].Provider != "vis" {
		t.Errorf("in-turn side call = %+v", recs[1])
	}
	if recs[2].HandedOff {
		t.Errorf("side call must not become the turn's primary route: %+v", recs[2])
	}
	if recs[3].SessionKey != "" || recs[3].TurnID != "" {
		t.Errorf("out-of-turn side call carries turn fields: %+v", recs[3])
	}
}

func TestCallLog_SubagentKind(t *testing.T) {
	r, mock, _ := callLogRouter(t, 0)
	read := attachCallLog(t, r)
	mock.SetResponses([]MockResponse{{Content: "x"}})
	sess := &sessions.Session{Key: "subagent_1", UserID: "subagent", ChannelID: "subagent_1"}
	if _, err := r.GenerateResponseWithTools(context.Background(), sess, "hi", "", ""); err != nil {
		t.Fatal(err)
	}
	_, recs := read()
	if len(recs) != 1 || recs[0].AgentKind != "subagent" || recs[0].Channel != "subagent" {
		t.Fatalf("records = %+v", recs)
	}
}

func TestCallLog_DisabledRouterLogsNothing(t *testing.T) {
	r, mock, sess := callLogRouter(t, 0)
	mock.SetResponses([]MockResponse{{Content: "x"}})
	if _, err := r.GenerateResponseWithTools(context.Background(), sess, "hi", "", ""); err != nil {
		t.Fatal(err)
	}
	if r.CallLog() != nil || r.CloseCallLog(context.Background()) != nil {
		t.Fatal("no call log expected")
	}
}

// ---------------------------------------------------------------------------
// Writer
// ---------------------------------------------------------------------------

func testRecord(i int) CallRecord {
	return CallRecord{TS: time.Now().UTC().Format(time.RFC3339), Provider: "p", Model: "m", Phase: "depth0", Attempt: 1, LatencyMs: int64(i), ErrorClass: ErrClassNone}
}

func TestCallLogWriter_CloseFlushesAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "calls.jsonl")
	l := NewCallLog(CallLogOptions{Path: path, BufferSize: 1000})
	for i := 0; i < 500; i++ {
		if !l.Record(testRecord(i)) {
			t.Fatalf("record %d dropped", i)
		}
	}
	if err := l.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(parseCallLog(t, raw)); n != 500 {
		t.Fatalf("lines = %d, want 500", n)
	}
	if s := l.Stats(); s.Written != 500 || s.Dropped != 0 || s.WriteErrors != 0 {
		t.Fatalf("stats = %+v", s)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", st.Mode().Perm())
	}
	if l.Record(testRecord(0)) {
		t.Error("Record after Close accepted")
	}
	if err := l.Close(context.Background()); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestCallLogWriter_Rotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "calls.jsonl")
	line, _ := json.Marshal(testRecord(0))
	maxSize := int64(len(line)+1)*3 + 10 // ~3 lines per file
	l := NewCallLog(CallLogOptions{Path: path, MaxSizeBytes: maxSize, MaxFiles: 3})
	for i := 0; i < 40; i++ {
		l.Record(testRecord(i))
		time.Sleep(time.Millisecond) // let the writer keep up (buffer is large anyway)
	}
	if err := l.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, path + ".1", path + ".2"} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if st.Size() > maxSize {
			t.Errorf("%s size %d > max %d", p, st.Size(), maxSize)
		}
		raw, _ := os.ReadFile(p)
		if len(parseCallLog(t, raw)) == 0 {
			t.Errorf("%s empty", p)
		}
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Errorf("%s.3 kept beyond max_files=3", path)
	}
	// The newest record is in the active file.
	raw, _ := os.ReadFile(path)
	recs := parseCallLog(t, raw)
	if recs[len(recs)-1].LatencyMs != 39 {
		t.Errorf("last active record = %+v", recs[len(recs)-1])
	}
}

func TestCallLogWriter_FullBufferDropsWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calls.jsonl")
	// Writer not started yet: the buffer (2) fills and stays full.
	l := &CallLog{opts: CallLogOptions{Path: path, MaxSizeBytes: 1 << 20, MaxFiles: 2, BufferSize: 2},
		ch: make(chan CallRecord, 2), done: make(chan struct{})}
	start := time.Now()
	accepted := 0
	for i := 0; i < 50; i++ {
		if l.Record(testRecord(i)) {
			accepted++
		}
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Record blocked for %v", d)
	}
	if accepted != 2 || l.Stats().Dropped != 48 {
		t.Fatalf("accepted=%d dropped=%d, want 2/48", accepted, l.Stats().Dropped)
	}
	go l.run()
	if err := l.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if n := len(parseCallLog(t, raw)); n != 2 {
		t.Fatalf("written lines = %d, want 2", n)
	}
}

func TestCallLogWriter_ConcurrentRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calls.jsonl")
	l := NewCallLog(CallLogOptions{Path: path, BufferSize: 64, MaxSizeBytes: 64 << 10, MaxFiles: 50})
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				rec := testRecord(i)
				rec.SessionKey = fmt.Sprintf("s%d", g)
				l.Record(rec)
			}
		}(g)
	}
	wg.Wait()
	if err := l.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(path + "*")
	total := 0
	for _, f := range files {
		raw, _ := os.ReadFile(f)
		total += len(parseCallLog(t, raw)) // fails on any interleaved/corrupt line
	}
	s := l.Stats()
	if uint64(total) != s.Written || s.Written+s.Dropped != 1600 {
		t.Fatalf("lines=%d stats=%+v", total, s)
	}
}

func TestCallLogWriter_OpenFailureNeverPanics(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	l := NewCallLog(CallLogOptions{Path: filepath.Join(blocker, "sub", "calls.jsonl")})
	l.Record(testRecord(1))
	l.Record(testRecord(2))
	if err := l.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := l.Stats(); s.Written != 0 || s.WriteErrors != 2 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestOpenCallLog_ConfigDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_DATA_DIR", dir)
	l, err := OpenCallLog(config.CallLogConfig{}, "")
	if err != nil || l == nil {
		t.Fatalf("default: %v %v", l, err)
	}
	defer l.Close(context.Background())
	if want := filepath.Join(dir, "logs", DefaultCallLogFile); l.Path() != want {
		t.Errorf("path = %q, want %q", l.Path(), want)
	}
	if l.opts.MaxSizeBytes != DefaultCallLogMaxSizeMB<<20 || l.opts.MaxFiles != DefaultCallLogMaxFiles {
		t.Errorf("opts = %+v", l.opts)
	}
	off := false
	if l2, err := OpenCallLog(config.CallLogConfig{Enabled: &off}, ""); l2 != nil || err != nil {
		t.Errorf("disabled: %v %v", l2, err)
	}
	custom, _ := OpenCallLog(config.CallLogConfig{Path: filepath.Join(dir, "x.jsonl"), MaxSizeMB: 1, MaxFiles: 2}, "")
	defer custom.Close(context.Background())
	if custom.Path() != filepath.Join(dir, "x.jsonl") || custom.opts.MaxSizeBytes != 1<<20 || custom.opts.MaxFiles != 2 {
		t.Errorf("custom opts = %+v", custom.opts)
	}
}

func TestClassifyCallError(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{nil, ErrClassNone},
		{context.Canceled, ErrClassContextCancel},
		{fmt.Errorf("x: %w", context.DeadlineExceeded), ErrClassTimeout},
		{errors.New("API error: 429 - {\"error\":{\"type\":\"rate_limit_error\",\"message\":\"slow down\"}}"), ErrClassRateLimit},
		{errors.New("API error: 401 - {\"error\":{\"type\":\"authentication_error\",\"message\":\"bad key\"}}"), ErrClassAuth},
		{errors.New("API error: 500 - {\"error\":{\"type\":\"api_error\",\"message\":\"boom\"}}"), ErrClassServer},
		{errors.New("API error: 529 - {\"error\":{\"type\":\"overloaded_error\",\"message\":\"busy\"}}"), ErrClassRateLimit},
		{errors.New("prompt is too long: 250000 tokens > 200000 maximum"), ErrClassContextLength},
		{errors.New("weird failure"), ErrClassOther},
	}
	for _, c := range cases {
		if got := classifyCallError(c.err); got != c.want {
			t.Errorf("classifyCallError(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

func TestRedactCallError_TruncatesAndFlattens(t *testing.T) {
	msg := redactCallError(errors.New("line1\nline2 bot123456:" + sentinelTGSecret + " " + strings.Repeat("x", 500)))
	if strings.Contains(msg, "\n") || strings.Contains(msg, sentinelTGSecret) || len([]rune(msg)) > callLogErrMsgMax+1 {
		t.Fatalf("redacted = %q", msg)
	}
}
