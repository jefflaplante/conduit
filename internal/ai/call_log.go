package ai

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"conduit/internal/config"
	"conduit/internal/datadir"
	"conduit/internal/logging"
	"conduit/internal/redact"
	"conduit/internal/sessions"
)

// conduit-2lzv: persistent structured LLM call log.
//
// One JSON line per provider call, written at the single point every call
// passes through (Router.meterCall): each callWithRecovery attempt, every
// guarded call (tool-loop depths, EmptyGuard retry/failover, length
// auto-continue, their timeout retries and handoffs) and side calls. An
// attempt whose concurrency-slot wait was abandoned (conduit-3j08) makes no
// provider call and is not metered, but still gets one line, written by
// acquireProviderSlotObserved with error_class queue_timeout/queue_cancel.
//
// METADATA ONLY: a record carries routing, token, cost, latency and error
// classification fields. It never carries prompt text, response text or tool
// arguments — CallRecord has no field that could hold them — and the error
// message is scrubbed with internal/redact and truncated.
//
// Writes are asynchronous: Record does a non-blocking send to a buffered
// channel drained by one writer goroutine. A full buffer drops the record
// and counts it; a provider call is never blocked or failed by the log.

// Default call-log settings.
const (
	DefaultCallLogFile      = "llm-calls.jsonl"
	DefaultCallLogMaxSizeMB = 20
	DefaultCallLogMaxFiles  = 5
	DefaultCallLogBuffer    = 1024

	// callLogErrMsgMax bounds the redacted error message (runes).
	callLogErrMsgMax = 240
	// callLogCloseBudget caps Close's flush wait inside the stop budget.
	callLogCloseBudget = 2 * time.Second
	// callLogReopenBackoff spaces reopen attempts after an open failure.
	callLogReopenBackoff = 30 * time.Second
)

// Error classes (bead conduit-2lzv's list plus quota and context_length,
// which the router handles distinctly).
const (
	ErrClassNone          = "none"
	ErrClassTimeout       = "timeout"
	ErrClassRateLimit     = "rate_limit"
	ErrClassQuota         = "quota"
	ErrClassAuth          = "auth"
	ErrClassServer        = "server"
	ErrClassContextCancel = "context_cancel"
	ErrClassContextLength = "context_length"
	ErrClassOther         = "other"

	// Throttle waits abandoned before any provider call (conduit-3j08):
	// the attempt's deadline expired (queue_timeout) or its ctx was
	// canceled — /stop, sub-agent cancel, shutdown (queue_cancel).
	ErrClassQueueTimeout = "queue_timeout"
	ErrClassQueueCancel  = "queue_cancel"
)

// CallRecord is one line of the call log. Every field is metadata; there is
// deliberately no field for prompt/response text or tool arguments.
type CallRecord struct {
	TS           string `json:"ts"` // RFC3339 (ms), call completion time
	SessionKey   string `json:"session_key,omitempty"`
	SessionLabel string `json:"session_label,omitempty"`
	AgentKind    string `json:"agent_kind,omitempty"` // main | subagent
	Channel      string `json:"channel,omitempty"`    // channel kind (telegram, tui, cron, ...)
	TurnID       string `json:"turn_id,omitempty"`    // one per router turn entry
	RequestID    string `json:"request_id,omitempty"` // logging request ID, when present

	Provider     string `json:"provider"`
	Model        string `json:"model"`
	Phase        string `json:"phase"`   // depth0..N, continue, empty-guard, side-call[:label]
	Attempt      int    `json:"attempt"` // 1..n within one logical call (recovery retries/handoffs)
	HandedOff    bool   `json:"handed_off"`
	FallbackFrom string `json:"fallback_from,omitempty"` // turn's primary model when this call ran elsewhere
	Streaming    bool   `json:"streaming"`

	StopReason   string `json:"stop_reason,omitempty"`
	FinishReason string `json:"finish_reason,omitempty"`
	ToolCalls    int    `json:"tool_calls,omitempty"` // count only — never tool arguments
	Empty        bool   `json:"empty,omitempty"`      // raw-empty response (no content, no tool calls)

	PromptTokens        int     `json:"prompt_tokens"`
	CompletionTokens    int     `json:"completion_tokens"`
	TotalTokens         int     `json:"total_tokens"`
	CacheCreationTokens int     `json:"cache_creation_tokens"`
	CacheReadTokens     int     `json:"cache_read_tokens"`
	CostUSD             float64 `json:"cost_usd"`
	Priced              *bool   `json:"priced,omitempty"`        // nil on error
	LatencyMs           int64   `json:"latency_ms"`              // provider call only; excludes queue_wait_ms (0 for queue_* records)
	QueueWaitMs         int64   `json:"queue_wait_ms,omitempty"` // time waiting for a concurrency slot (conduit-38cz)
	TTFTMs              *int64  `json:"ttft_ms,omitempty"`       // streaming calls that emitted text
	ErrorClass          string  `json:"error_class"`
	HTTPStatus          int     `json:"http_status,omitempty"`
	Error               string  `json:"error,omitempty"` // redacted + truncated
}

// CallLogOptions configures a CallLog.
type CallLogOptions struct {
	Path         string
	MaxSizeBytes int64 // rotate before exceeding; <= 0 = default
	MaxFiles     int   // total files kept incl. the active one; <= 0 = default
	BufferSize   int   // record buffer; <= 0 = default
}

// CallLogStats are the log's counters.
type CallLogStats struct {
	Written     uint64 `json:"written"`
	Dropped     uint64 `json:"dropped"`
	WriteErrors uint64 `json:"write_errors"`
}

// CallLog is the asynchronous, size-rotated JSONL writer.
type CallLog struct {
	opts CallLogOptions

	mu     sync.RWMutex // guards closed vs. sends on ch
	closed bool
	ch     chan CallRecord
	done   chan struct{}

	written, dropped, writeErrors atomic.Uint64

	// writer-goroutine state
	f          *os.File
	size       int64
	nextOpenAt time.Time
}

// NewCallLog starts a call log writing to opts.Path. The file is opened
// lazily on the first record (0600, parent dir 0700).
func NewCallLog(opts CallLogOptions) *CallLog {
	if opts.MaxSizeBytes <= 0 {
		opts.MaxSizeBytes = DefaultCallLogMaxSizeMB << 20
	}
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = DefaultCallLogMaxFiles
	}
	if opts.BufferSize <= 0 {
		opts.BufferSize = DefaultCallLogBuffer
	}
	l := &CallLog{
		opts: opts,
		ch:   make(chan CallRecord, opts.BufferSize),
		done: make(chan struct{}),
	}
	go l.run()
	return l
}

// OpenCallLog builds the call log from ai.call_log config. It returns nil,
// nil when the log is disabled. dataDir is config data_dir (resolved via
// internal/datadir: CONDUIT_DATA_DIR, then data_dir, then ~/.conduit).
func OpenCallLog(cfg config.CallLogConfig, dataDir string) (*CallLog, error) {
	if !cfg.CallLogEnabled() {
		return nil, nil
	}
	path := cfg.Path
	if path == "" {
		dd, err := datadir.New(dataDir)
		if err != nil {
			return nil, fmt.Errorf("call log: resolve data dir: %w", err)
		}
		path = filepath.Join(dd.Root(), "logs", DefaultCallLogFile)
	}
	return NewCallLog(CallLogOptions{
		Path:         path,
		MaxSizeBytes: int64(cfg.MaxSizeMB) << 20,
		MaxFiles:     cfg.MaxFiles,
	}), nil
}

// Path returns the active log file path.
func (l *CallLog) Path() string { return l.opts.Path }

// Record queues rec without blocking. It returns false when the record was
// dropped (buffer full or log closed).
func (l *CallLog) Record(rec CallRecord) bool {
	if l == nil {
		return false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.closed {
		l.dropped.Add(1)
		return false
	}
	select {
	case l.ch <- rec:
		return true
	default:
		if n := l.dropped.Add(1); n == 1 || n%100 == 0 {
			log.Printf("[CallLog] buffer full — dropped %d record(s) so far (conduit-2lzv)", n)
		}
		return false
	}
}

// Stats returns the log's counters.
func (l *CallLog) Stats() CallLogStats {
	if l == nil {
		return CallLogStats{}
	}
	return CallLogStats{Written: l.written.Load(), Dropped: l.dropped.Load(), WriteErrors: l.writeErrors.Load()}
}

// Close stops accepting records, flushes the queued ones and closes the
// file. It waits at most until ctx is done or callLogCloseBudget elapses;
// later calls are no-ops.
func (l *CallLog) Close(ctx context.Context) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	if !l.closed {
		l.closed = true
		close(l.ch)
	}
	l.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	t := time.NewTimer(callLogCloseBudget)
	defer t.Stop()
	select {
	case <-l.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("call log: close: %w", ctx.Err())
	case <-t.C:
		return errors.New("call log: close: flush budget exceeded")
	}
}

func (l *CallLog) run() {
	defer close(l.done)
	for rec := range l.ch {
		l.write(rec)
	}
	if l.f != nil {
		_ = l.f.Sync()
		_ = l.f.Close()
		l.f = nil
	}
}

func (l *CallLog) write(rec CallRecord) {
	line, err := json.Marshal(rec)
	if err != nil {
		l.writeErrors.Add(1)
		return
	}
	line = append(line, '\n')
	if l.f != nil && l.size > 0 && l.size+int64(len(line)) > l.opts.MaxSizeBytes {
		l.rotate()
	}
	if l.f == nil && !l.open() {
		l.writeErrors.Add(1)
		return
	}
	// One Write per line on an O_APPEND file: a crash leaves at most one
	// partial trailing line.
	n, err := l.f.Write(line)
	l.size += int64(n)
	if err != nil {
		l.writeErrors.Add(1)
		log.Printf("[CallLog] write %s: %v (conduit-2lzv)", l.opts.Path, err)
		_ = l.f.Close()
		l.f = nil
		return
	}
	l.written.Add(1)
}

func (l *CallLog) open() bool {
	if now := time.Now(); now.Before(l.nextOpenAt) {
		return false
	}
	fail := func(err error) bool {
		l.nextOpenAt = time.Now().Add(callLogReopenBackoff)
		log.Printf("[CallLog] open %s: %v — records dropped until it opens (conduit-2lzv)", l.opts.Path, err)
		return false
	}
	if err := os.MkdirAll(filepath.Dir(l.opts.Path), 0o700); err != nil {
		return fail(err)
	}
	f, err := os.OpenFile(l.opts.Path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fail(err)
	}
	_ = f.Chmod(0o600) // a pre-existing file keeps its mode otherwise
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fail(err)
	}
	l.f, l.size = f, st.Size()
	return true
}

// rotate shifts path -> path.1 -> ... -> path.(MaxFiles-1), dropping the
// oldest. With MaxFiles == 1 the active file is simply replaced.
func (l *CallLog) rotate() {
	_ = l.f.Close()
	l.f, l.size = nil, 0
	p := l.opts.Path
	if l.opts.MaxFiles <= 1 {
		_ = os.Remove(p)
		return
	}
	_ = os.Remove(fmt.Sprintf("%s.%d", p, l.opts.MaxFiles-1))
	for i := l.opts.MaxFiles - 2; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", p, i), fmt.Sprintf("%s.%d", p, i+1))
	}
	if err := os.Rename(p, p+".1"); err != nil && !os.IsNotExist(err) {
		log.Printf("[CallLog] rotate %s: %v (conduit-2lzv)", p, err)
	}
}

// ---------------------------------------------------------------------------
// Router wiring
// ---------------------------------------------------------------------------

// SetCallLog attaches the call log (nil detaches it).
func (r *Router) SetCallLog(l *CallLog) { r.callLog.Store(l) }

// CallLog returns the attached call log, or nil.
func (r *Router) CallLog() *CallLog {
	if r == nil {
		return nil
	}
	return r.callLog.Load()
}

// CloseCallLog flushes and closes the attached call log (nil-safe).
func (r *Router) CloseCallLog(ctx context.Context) error {
	return r.CallLog().Close(ctx)
}

// callTurn is the per-turn scope set on ctx at each router entry.
type callTurn struct {
	sessionKey, sessionLabel, agentKind, channel, turnID string

	depth atomic.Int32 // guarded tool-loop calls so far

	mu                            sync.Mutex
	primaryProvider, primaryModel string // first non-side call of the turn
}

type callTurnKey struct{}

// withCallTurn attaches a fresh turn scope for session to ctx (no-op when
// the call log is off).
func (r *Router) withCallTurn(ctx context.Context, session *sessions.Session) context.Context {
	if ctx == nil || r.CallLog() == nil {
		return ctx
	}
	t := &callTurn{turnID: newTurnID(), agentKind: "main"}
	if session != nil {
		t.sessionKey = session.Key
		t.sessionLabel = session.Context["label"]
		t.channel = channelKind(session.ChannelID)
		if session.UserID == "subagent" || strings.HasPrefix(session.Key, "subagent_") {
			t.agentKind = "subagent"
		}
	}
	ctx = context.WithValue(ctx, callPhaseKey{}, "") // an outer turn's phase marker must not leak in
	return context.WithValue(ctx, callTurnKey{}, t)
}

func callTurnFrom(ctx context.Context) *callTurn {
	if ctx == nil {
		return nil
	}
	t, _ := ctx.Value(callTurnKey{}).(*callTurn)
	return t
}

// callState is one logical call: its phase label and attempt counter.
type callState struct {
	phase    string
	side     bool
	attempts atomic.Int32
}

type callStateKey struct{}

func withCallState(ctx context.Context, phase string, side bool) context.Context {
	if ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, callStateKey{}, &callState{phase: phase, side: side})
}

// beginRecoveryCall scopes callWithRecovery's attempts as the turn's first
// logical call (depth0).
func beginRecoveryCall(ctx context.Context) context.Context {
	return withCallState(ctx, "depth0", false)
}

// beginGuardCall scopes one contextGuardProvider.GenerateResponse: its
// phase is empty-guard, continue, or the next tool-loop depth.
func beginGuardCall(ctx context.Context) context.Context {
	if ctx == nil {
		return ctx
	}
	var phase string
	switch {
	case isEmptyGuardCall(ctx):
		phase = "empty-guard"
	case callPhaseMarker(ctx) != "":
		phase = callPhaseMarker(ctx)
	default:
		if t := callTurnFrom(ctx); t != nil {
			phase = fmt.Sprintf("depth%d", t.depth.Add(1))
		} else {
			phase = "tool-loop"
		}
	}
	return withCallState(ctx, phase, false)
}

// beginSideCall scopes a GenerateSideCall ("side-call" or
// "side-call:<label>" when the caller set WithSideCallLabel).
func beginSideCall(ctx context.Context) context.Context {
	phase := "side-call"
	if ctx != nil {
		if l, _ := ctx.Value(sideCallLabelKey{}).(string); l != "" {
			phase += ":" + l
		}
	}
	return withCallState(ctx, phase, true)
}

type callPhaseKey struct{}

// withCallPhase marks ctx for a guarded call with an explicit phase label
// (the length auto-continue uses "continue").
func withCallPhase(ctx context.Context, phase string) context.Context {
	if ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, callPhaseKey{}, phase)
}

func callPhaseMarker(ctx context.Context) string {
	p, _ := ctx.Value(callPhaseKey{}).(string)
	return p
}

type sideCallLabelKey struct{}

// WithSideCallLabel labels the side calls made under ctx in the call log
// (e.g. "vision" → phase "side-call:vision").
func WithSideCallLabel(ctx context.Context, label string) context.Context {
	return context.WithValue(ctx, sideCallLabelKey{}, label)
}

// callObs carries per-attempt observations only the call site sees: whether
// it streamed, when the first text delta arrived, and how long it waited
// for a provider concurrency slot (conduit-38cz).
type callObs struct {
	streaming  bool
	start      time.Time
	firstDelta atomic.Int64  // unix nanos, 0 = none yet
	queueWait  time.Duration // set by acquireProviderSlotObserved
}

// stream marks the attempt as streaming and wraps cb to time the first
// delta. A nil cb stays nil.
func (o *callObs) stream(cb StreamCallback) StreamCallback {
	o.streaming = true
	o.start = time.Now()
	if cb == nil {
		return nil
	}
	return func(delta string, done bool) {
		if delta != "" && o.firstDelta.Load() == 0 {
			o.firstDelta.CompareAndSwap(0, time.Now().UnixNano())
		}
		cb(delta, done)
	}
}

func (o *callObs) ttftMs() *int64 {
	if o == nil || !o.streaming {
		return nil
	}
	fd := o.firstDelta.Load()
	if fd == 0 {
		return nil
	}
	ms := time.Duration(fd - o.start.UnixNano()).Milliseconds()
	return &ms
}

// logCall emits one record for a metered provider call. model is already
// resolved ("" → provider default) and resp.Usage already carries the cost.
func (r *Router) logCall(ctx context.Context, providerName, model string, resp *GenerateResponse, err error, latencyMs int64, obs *callObs) {
	cl := r.CallLog()
	if cl == nil {
		return
	}
	rec := CallRecord{
		TS:         time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		Provider:   providerName,
		Model:      model,
		Phase:      "unscoped",
		Attempt:    1,
		LatencyMs:  latencyMs,
		ErrorClass: classifyCallError(err),
	}
	var side bool
	if ctx != nil {
		if st, ok := ctx.Value(callStateKey{}).(*callState); ok {
			rec.Phase, side = st.phase, st.side
			rec.Attempt = int(st.attempts.Add(1))
		}
		rec.RequestID = logging.RequestIDFromContext(ctx)
	}
	if t := callTurnFrom(ctx); t != nil {
		rec.SessionKey, rec.SessionLabel, rec.AgentKind = t.sessionKey, t.sessionLabel, t.agentKind
		rec.Channel, rec.TurnID = t.channel, t.turnID
		if !side {
			t.mu.Lock()
			if t.primaryProvider == "" && t.primaryModel == "" {
				t.primaryProvider, t.primaryModel = providerName, model
			} else if t.primaryProvider != providerName || !strings.EqualFold(t.primaryModel, model) {
				rec.HandedOff, rec.FallbackFrom = true, t.primaryModel
			}
			t.mu.Unlock()
		}
	}
	if obs != nil {
		rec.Streaming = obs.streaming
		rec.TTFTMs = obs.ttftMs()
		rec.QueueWaitMs = obs.queueWait.Milliseconds()
	}
	if err != nil {
		rec.HTTPStatus = providerStatusCode(err)
		rec.Error = redactCallError(err)
	} else if resp != nil {
		u := resp.Usage
		rec.StopReason, rec.FinishReason = resp.StopReason, resp.FinishReason
		rec.ToolCalls = len(resp.ToolCalls)
		rec.Empty = IsEmptyModelResponse(resp)
		rec.PromptTokens, rec.CompletionTokens, rec.TotalTokens = u.PromptTokens, u.CompletionTokens, u.TotalTokens
		if rec.TotalTokens == 0 {
			rec.TotalTokens = u.PromptTokens + u.CompletionTokens
		}
		rec.CacheCreationTokens, rec.CacheReadTokens = u.CacheCreationInputTokens, u.CacheReadInputTokens
		rec.CostUSD = u.CostUSD
		priced := u.PricedCalls > 0
		rec.Priced = &priced
	}
	cl.Record(rec)
}

// classifyCallError maps a provider error to a call-log error class.
func classifyCallError(err error) string {
	switch {
	case err == nil:
		return ErrClassNone
	}
	var we *ThrottleWaitError
	if errors.As(err, &we) { // conduit-3j08: no provider call was made
		if errors.Is(we.Err, context.Canceled) {
			return ErrClassQueueCancel
		}
		return ErrClassQueueTimeout
	}
	switch {
	case errors.Is(err, context.Canceled):
		return ErrClassContextCancel
	case IsTransientTimeoutError(err):
		return ErrClassTimeout
	case IsQuotaError(err):
		return ErrClassQuota
	}
	switch code := providerStatusCode(err); {
	case code == 429:
		return ErrClassRateLimit
	case code == 401 || code == 403:
		return ErrClassAuth
	case code == 529:
		return ErrClassRateLimit // Anthropic overloaded
	case code >= 500:
		return ErrClassServer
	}
	if IsRetryableOverloadError(err) {
		return ErrClassRateLimit
	}
	switch ClassifyError(err) {
	case CategoryTimeout:
		return ErrClassTimeout
	case CategoryRateLimit:
		return ErrClassRateLimit
	case CategoryServiceUnavailable:
		return ErrClassServer
	case CategoryAuthentication:
		return ErrClassAuth
	case CategoryContextExceeded:
		return ErrClassContextLength
	}
	return ErrClassOther
}

// redactCallError returns err's message scrubbed of credentials, flattened
// to one line and truncated to callLogErrMsgMax runes.
func redactCallError(err error) string {
	msg := redact.String(err.Error())
	msg = strings.Join(strings.Fields(msg), " ")
	if utf8.RuneCountInString(msg) > callLogErrMsgMax {
		r := []rune(msg)
		msg = string(r[:callLogErrMsgMax]) + "…"
	}
	return msg
}

// channelKind reduces a session channel ID to its kind: the leading segment
// before '_', ':', '-', '/' or '.' ("subagent_123" → "subagent").
func channelKind(channelID string) string {
	if i := strings.IndexAny(channelID, "_:-/."); i > 0 {
		channelID = channelID[:i]
	}
	return strings.ToLower(channelID)
}

func newTurnID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
