package approval

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Defaults for Config zero values.
const (
	DefaultTTL           = 5 * time.Minute
	DefaultExecTimeout   = 2 * time.Minute
	DefaultMaxBadReplies = 3
	// MaxTTL caps a per-action TTL (Action.TTL). An approval code is a
	// bearer credential for one frozen action; it should not outlive the
	// conversation it was asked in (conduit-enf0).
	MaxTTL     = time.Hour
	codeLength = 6
	// codeAlphabet omits look-alikes (0/O, 1/I/L) so codes are easy to type.
	codeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
)

// ErrNonInteractive is returned (wrapped in *NonInteractiveError) when an
// approval is requested from a turn with no live human to ask.
var ErrNonInteractive = errors.New("human approval required, but this turn has no live human channel")

// ErrCannotPrompt is returned when the originating channel cannot deliver
// the approval prompt. The action is denied (fail closed).
var ErrCannotPrompt = errors.New("human approval required, but the originating channel cannot be prompted")

// NonInteractiveError carries the non-interactive source (heartbeat, cron,
// subagent, wake, unknown) for the model-facing error and the audit log.
type NonInteractiveError struct{ Source string }

func (e *NonInteractiveError) Error() string {
	src := e.Source
	if src == "" {
		src = "unknown"
	}
	return fmt.Sprintf("%s (origin: %s)", ErrNonInteractive.Error(), src)
}

func (e *NonInteractiveError) Unwrap() error { return ErrNonInteractive }

// Decision is the terminal state of an approval.
type Decision string

const (
	DecisionApproved Decision = "approved"
	DecisionDenied   Decision = "denied"
	DecisionExpired  Decision = "expired"
)

// Field is one labelled value shown to the human in the prompt. Fields are
// never logged; use Action.Audit for loggable metadata.
type Field struct {
	Name  string
	Value string
}

// Action describes the risky operation awaiting approval.
type Action struct {
	// Kind is a stable machine label, e.g. "email.send_as_owner".
	Kind string
	// Title is a one-line human description shown in the prompt.
	Title string
	// Fields are shown to the human (may include content previews).
	Fields []Field
	// Audit is logged on every event. Never put secrets or bodies here.
	Audit map[string]string
	// Fingerprint binds the approval to the exact parameters; see
	// Fingerprint(). ExecuteFuncs should re-verify it before acting.
	Fingerprint string
	// TTL optionally overrides the Manager's TTL for this action (e.g. the
	// SSH tool's security.approval_timeout). Zero or negative uses
	// Config.TTL; values above MaxTTL are capped (conduit-enf0).
	TTL time.Duration
	// Purpose is the agent's stated reason for the action, shown labelled as
	// the agent's words (conduit-25lt.2). Model-supplied: untrusted.
	Purpose string
}

// ttlFor returns the lifetime of a pending approval for action.
func (m *Manager) ttlFor(action Action) time.Duration {
	switch {
	case action.TTL <= 0:
		return m.cfg.TTL
	case action.TTL > MaxTTL:
		return MaxTTL
	default:
		return action.TTL
	}
}

// Ticket identifies one pending approval.
type Ticket struct {
	ID          string
	Code        string
	Kind        string
	Fingerprint string
	SessionKey  string
	ChannelID   string
	UserID      string
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

// Requester is the slice of *Manager a caller needs to gate an action
// (conduit-c8ct, conduit-w3l7). Tools receive it via types.ToolServices so
// they never import the gateway.
type Requester interface {
	Request(ctx context.Context, action Action, exec ExecuteFunc) (*Ticket, error)
}

var _ Requester = (*Manager)(nil)

// ExecuteFunc performs the approved action. It runs at most once, only after
// the bound human approves, with a fresh context bounded by ExecTimeout.
type ExecuteFunc func(ctx context.Context, t Ticket) (result string, err error)

// Resolution reports how an approval ended (for OnResolved hooks).
type Resolution struct {
	Ticket   Ticket
	Action   Action
	Decision Decision
	Result   string // ExecuteFunc result on approve
	Err      error  // ExecuteFunc error on approve
}

// Inbound is a human message offered to HandleReply by a channel inbound path.
type Inbound struct {
	ChannelID  string
	UserID     string
	SessionKey string
	Text       string
	// Notify replies to this human (used for "no such code" feedback).
	Notify Notifier
}

// Config tunes a Manager. Zero values pick the defaults.
type Config struct {
	TTL           time.Duration
	ExecTimeout   time.Duration
	MaxBadReplies int
	Logger        *slog.Logger
	// Now is injectable for tests.
	Now func() time.Time
	// OnResolved, if set, is called once per terminal decision (after the
	// action ran, for approvals). Used by the gateway to note the outcome
	// in session history so the model learns what happened.
	OnResolved func(Resolution)
}

type pending struct {
	ticket Ticket
	action Action
	origin Origin
	exec   ExecuteFunc
	timer  *time.Timer
}

// Manager tracks pending approvals. It is safe for concurrent use.
type Manager struct {
	cfg    Config
	log    *slog.Logger
	now    func() time.Time
	base   context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	pending map[string]*pending // by code
	bad     map[string]int      // wrong-code replies per session
	closed  bool

	wg sync.WaitGroup
}

// NewManager creates a Manager.
func NewManager(cfg Config) *Manager {
	if cfg.TTL <= 0 {
		cfg.TTL = DefaultTTL
	}
	if cfg.ExecTimeout <= 0 {
		cfg.ExecTimeout = DefaultExecTimeout
	}
	if cfg.MaxBadReplies <= 0 {
		cfg.MaxBadReplies = DefaultMaxBadReplies
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	base, cancel := context.WithCancel(context.Background())
	return &Manager{
		cfg:     cfg,
		log:     logger.With("component", "approval"),
		now:     now,
		base:    base,
		cancel:  cancel,
		pending: make(map[string]*pending),
		bad:     make(map[string]int),
	}
}

// TTL returns the configured default approval lifetime (see Action.TTL).
func (m *Manager) TTL() time.Duration { return m.cfg.TTL }

// Request registers a pending approval for action and prompts the human on
// the originating channel. It never blocks waiting for the answer: exec runs
// later, when the human approves (conduit-31jg.43). Non-interactive or
// unknown origins, and channels that cannot prompt, fail closed.
func (m *Manager) Request(ctx context.Context, action Action, exec ExecuteFunc) (*Ticket, error) {
	origin, ok := OriginFrom(ctx)
	if !ok || !origin.Interactive {
		src := origin.Source
		if !ok {
			src = "unknown"
		}
		m.audit("approval.refused_noninteractive", nil, action, "source", src)
		return nil, &NonInteractiveError{Source: src}
	}
	if origin.Notify == nil || origin.SessionKey == "" || origin.UserID == "" {
		m.audit("approval.refused_cannot_prompt", nil, action, "source", origin.Source)
		return nil, ErrCannotPrompt
	}
	if action.Fingerprint == "" || exec == nil {
		return nil, errors.New("approval: action fingerprint and execute func are required")
	}

	ttl := m.ttlFor(action)
	now := m.now()
	t := Ticket{
		ID:          "apr_" + randomHex(8),
		Kind:        action.Kind,
		Fingerprint: action.Fingerprint,
		SessionKey:  origin.SessionKey,
		ChannelID:   origin.ChannelID,
		UserID:      origin.UserID,
		CreatedAt:   now,
		ExpiresAt:   now.Add(ttl),
	}
	p := &pending{action: action, origin: origin, exec: exec}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errors.New("approval: manager closed")
	}
	for {
		t.Code = randomCode()
		if _, dup := m.pending[t.Code]; !dup {
			break
		}
	}
	p.ticket = t
	m.pending[t.Code] = p
	code, id := t.Code, t.ID
	p.timer = time.AfterFunc(ttl, func() { m.expire(code, id) })
	m.mu.Unlock()

	m.audit("approval.requested", &t, action, "source", origin.Source)

	if err := origin.Notify(ctx, promptNotice(t, action, ttl, origin.RequestText)); err != nil {
		m.remove(code, id)
		m.audit("approval.prompt_failed", &t, action, "error", err.Error())
		return nil, fmt.Errorf("%w: %v", ErrCannotPrompt, err)
	}
	out := t
	return &out, nil
}

// replyRe matches "YES ABC123" / "no abc123" (optionally slash-prefixed, as
// Telegram users often type commands). Bare "yes" never matches: a code is
// required so a stray yes cannot approve the wrong thing.
var replyRe = regexp.MustCompile(`(?i)^\s*/?(yes|y|approve|no|n|deny|reject|cancel)[\s:_-]+([a-z0-9]{6})\s*[.!]?\s*$`)

// ParseReply extracts (approve, code) from an approval reply. ok is false for
// any text that is not an approval reply.
func ParseReply(text string) (approve bool, code string, ok bool) {
	m := replyRe.FindStringSubmatch(text)
	if m == nil {
		return false, "", false
	}
	// Codes always mix letters and digits, so ordinary replies such as
	// "no thanks" / "yes please" pass through to the model untouched.
	if !isCodeShaped(m[2]) {
		return false, "", false
	}
	verb := strings.ToLower(m[1])
	approve = verb == "yes" || verb == "y" || verb == "approve"
	return approve, strings.ToUpper(m[2]), true
}

func isCodeShaped(s string) bool {
	var letter, digit bool
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digit = true
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			letter = true
		}
	}
	return letter && digit
}

// HandleReply must be called by every human inbound path BEFORE the message
// is stored or queued for the model (i.e. before the per-session turn lock).
// It returns true when the message was an approval reply and has been fully
// consumed; the caller must then drop it. It never blocks on the model: an
// approved action runs in its own goroutine.
func (m *Manager) HandleReply(ctx context.Context, in Inbound) bool {
	if m == nil {
		return false
	}
	approve, code, ok := ParseReply(in.Text)
	if !ok {
		return false
	}

	m.mu.Lock()
	p := m.pending[code]
	bound := p != nil &&
		p.ticket.SessionKey == in.SessionKey &&
		p.ticket.UserID == in.UserID &&
		p.ticket.ChannelID == in.ChannelID
	if !bound {
		// Wrong/unknown code, or a code bound to another session/user.
		m.bad[in.SessionKey]++
		var revoked []*pending
		if m.bad[in.SessionKey] >= m.cfg.MaxBadReplies {
			for c, q := range m.pending {
				if q.ticket.SessionKey == in.SessionKey {
					q.timer.Stop()
					delete(m.pending, c)
					revoked = append(revoked, q)
				}
			}
			delete(m.bad, in.SessionKey)
		}
		m.mu.Unlock()

		m.log.Warn("approval reply rejected", "event", "approval.reply_rejected",
			"code", code, "session_key", in.SessionKey, "channel_id", in.ChannelID,
			"user_id", in.UserID, "reason", "no pending approval bound to this code/session")
		msg := fmt.Sprintf("No pending approval matches code %s here (it may have expired or already been used). Nothing was approved.", code)
		if len(revoked) > 0 {
			msg += " Too many unmatched codes: all pending approvals in this session were cancelled."
		}
		m.notify(ctx, in.Notify, Notice{Text: msg})
		for _, q := range revoked {
			m.audit("approval.denied", &q.ticket, q.action, "reason", "too_many_bad_replies")
			m.resolved(Resolution{Ticket: q.ticket, Action: q.action, Decision: DecisionDenied})
		}
		return true
	}

	// Single use: remove before acting so a second reply can never re-run it.
	delete(m.pending, code)
	p.timer.Stop()
	delete(m.bad, in.SessionKey)
	expired := !m.now().Before(p.ticket.ExpiresAt)
	if approve && !expired {
		m.wg.Add(1) // under mu so Close cannot race Wait with this Add
	}
	m.mu.Unlock()

	switch {
	case expired:
		m.audit("approval.timeout", &p.ticket, p.action, "reason", "reply_after_expiry")
		m.notify(ctx, p.origin.Notify, Notice{Text: fmt.Sprintf("Approval %s had expired. Nothing was done; ask the agent again if you still want this.", code)})
		m.resolved(Resolution{Ticket: p.ticket, Action: p.action, Decision: DecisionExpired})
	case !approve:
		m.audit("approval.denied", &p.ticket, p.action, "reason", "human_denied")
		m.notify(ctx, p.origin.Notify, Notice{Text: fmt.Sprintf("Denied (%s): %s. Nothing was done.", code, p.action.Title)})
		m.resolved(Resolution{Ticket: p.ticket, Action: p.action, Decision: DecisionDenied})
	default:
		m.audit("approval.approved", &p.ticket, p.action)
		go m.run(p)
	}
	return true
}

func (m *Manager) run(p *pending) {
	defer m.wg.Done()
	ctx, cancel := context.WithTimeout(m.base, m.cfg.ExecTimeout)
	defer cancel()

	var (
		result string
		err    error
	)
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("approved action panicked: %v", r)
			}
		}()
		result, err = p.exec(ctx, p.ticket)
	}()

	var text string
	if err != nil {
		m.audit("approval.execute_failed", &p.ticket, p.action, "error", err.Error())
		text = fmt.Sprintf("Approved (%s), but the action failed: %v", p.ticket.Code, err)
	} else {
		m.audit("approval.executed", &p.ticket, p.action)
		text = fmt.Sprintf("Approved (%s) and done: %s", p.ticket.Code, p.action.Title)
		if result != "" {
			text += "\n" + truncate(result, 500)
		}
	}
	m.notify(ctx, p.origin.Notify, Notice{Text: text})
	m.resolved(Resolution{Ticket: p.ticket, Action: p.action, Decision: DecisionApproved, Result: result, Err: err})
}

func (m *Manager) expire(code, id string) {
	m.mu.Lock()
	p := m.pending[code]
	if p == nil || p.ticket.ID != id {
		m.mu.Unlock()
		return
	}
	delete(m.pending, code)
	m.mu.Unlock()

	m.audit("approval.timeout", &p.ticket, p.action, "reason", "ttl_elapsed")
	m.notify(m.base, p.origin.Notify, Notice{Text: fmt.Sprintf("Approval %s expired without an answer. Nothing was done.", code)})
	m.resolved(Resolution{Ticket: p.ticket, Action: p.action, Decision: DecisionExpired})
}

func (m *Manager) remove(code, id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p := m.pending[code]; p != nil && p.ticket.ID == id {
		p.timer.Stop()
		delete(m.pending, code)
	}
}

// Pending returns the pending tickets for a session (for status/tests).
func (m *Manager) Pending(sessionKey string) []Ticket {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Ticket
	for _, p := range m.pending {
		if p.ticket.SessionKey == sessionKey {
			out = append(out, p.ticket)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Wait blocks until all in-flight approved actions have finished.
func (m *Manager) Wait() { m.wg.Wait() }

// Close cancels pending approvals (without running them) and waits for
// in-flight approved actions.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	for c, p := range m.pending {
		p.timer.Stop()
		delete(m.pending, c)
	}
	m.mu.Unlock()
	m.wg.Wait()
	m.cancel()
}

func (m *Manager) notify(ctx context.Context, n Notifier, notice Notice) {
	if n == nil {
		return
	}
	if err := n(ctx, notice); err != nil {
		m.log.Warn("approval notice delivery failed", "error", err)
	}
}

func (m *Manager) resolved(r Resolution) {
	if m.cfg.OnResolved != nil {
		m.cfg.OnResolved(r)
	}
}

// audit logs one approval event. Only ticket metadata and Action.Audit are
// logged, never Action.Fields (which may hold bodies).
func (m *Manager) audit(event string, t *Ticket, a Action, extra ...any) {
	attrs := []any{"event", event, "kind", a.Kind}
	if t != nil {
		attrs = append(attrs,
			"approval_id", t.ID,
			"session_key", t.SessionKey,
			"channel_id", t.ChannelID,
			"user_id", t.UserID,
			"expires_at", t.ExpiresAt.Format(time.RFC3339))
	}
	if len(a.Fingerprint) >= 12 {
		attrs = append(attrs, "fingerprint", a.Fingerprint[:12])
	}
	keys := make([]string, 0, len(a.Audit))
	for k := range a.Audit {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		attrs = append(attrs, k, a.Audit[k])
	}
	attrs = append(attrs, extra...)
	m.log.Info("approval audit", attrs...)
}

// promptRequestRunes and promptPurposeRunes bound the two context lines.
const (
	promptRequestRunes = 200
	promptPurposeRunes = 300
)

func promptNotice(t Ticket, a Action, ttl time.Duration, requestText string) Notice {
	var b strings.Builder
	fmt.Fprintf(&b, "APPROVAL NEEDED [%s]\n%s\n", t.Code, a.Title)
	// conduit-25lt.2: what the human asked (trusted, from the turn) next to
	// what the agent says it is doing (untrusted), before the details.
	if r := strings.TrimSpace(requestText); r != "" {
		writeField(&b, Field{Name: "You asked", Value: clipRunes(oneLine(r), promptRequestRunes)})
	}
	if p := strings.TrimSpace(a.Purpose); p != "" {
		writeField(&b, Field{Name: "Agent's stated purpose", Value: clipRunes(oneLine(p), promptPurposeRunes)})
	}
	for _, f := range a.Fields {
		writeField(&b, f)
	}
	fmt.Fprintf(&b, "\n\nReply \"YES %s\" to approve or \"NO %s\" to cancel. Expires in %s (%s).",
		t.Code, t.Code, ttl.Round(time.Second), t.ExpiresAt.Format("15:04:05"))
	b.WriteString("\nOnly you can approve this; the agent cannot.")
	return Notice{
		Text: b.String(),
		Choices: []Choice{
			{Label: "Approve", Reply: "YES " + t.Code},
			{Label: "Deny", Reply: "NO " + t.Code},
		},
	}
}

// writeField renders one prompt field. Single-line values stay on the label
// line; multi-line values (commands, bodies) go on their own indented lines
// so the human can read exactly what will run.
func writeField(b *strings.Builder, f Field) {
	v := strings.TrimRight(f.Value, "\n")
	if !strings.Contains(v, "\n") {
		fmt.Fprintf(b, "\n%s: %s", f.Name, v)
		return
	}
	fmt.Fprintf(b, "\n%s:", f.Name)
	for _, line := range strings.Split(v, "\n") {
		b.WriteString("\n    ")
		b.WriteString(line)
	}
}

// Fingerprint returns a stable SHA-256 over kind and the exact parameters.
// Keys are sorted and every component is length-prefixed so distinct
// parameter sets can never collide by concatenation.
func Fingerprint(kind string, params map[string]string) string {
	h := sha256.New()
	writePart := func(s string) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		h.Write(n[:])
		h.Write([]byte(s))
	}
	writePart(kind)
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		writePart(k)
		writePart(params[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// randomCode returns a code containing at least one letter and one digit
// (see isCodeShaped).
func randomCode() string {
	for {
		buf := make([]byte, codeLength)
		if _, err := rand.Read(buf); err != nil {
			panic("approval: crypto/rand failed: " + err.Error())
		}
		out := make([]byte, codeLength)
		for i, c := range buf {
			// len(codeAlphabet) is 31; modulo bias is negligible for a
			// single-use, session-bound, 5-minute code.
			out[i] = codeAlphabet[int(c)%len(codeAlphabet)]
		}
		if s := string(out); isCodeShaped(s) {
			return s
		}
	}
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic("approval: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(buf)
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "..."
}

// oneLine collapses whitespace runs (newlines included) to single spaces.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// clipRunes shortens s to n runes, marking the cut.
func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
