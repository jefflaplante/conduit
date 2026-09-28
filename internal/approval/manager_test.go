package approval

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeChannel records notices sent to the human.
type fakeChannel struct {
	mu      sync.Mutex
	notices []Notice
	fail    error
}

func (f *fakeChannel) notify(_ context.Context, n Notice) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	f.notices = append(f.notices, n)
	return nil
}

func (f *fakeChannel) all() []Notice {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Notice(nil), f.notices...)
}

func (f *fakeChannel) last() Notice {
	n := f.all()
	if len(n) == 0 {
		return Notice{}
	}
	return n[len(n)-1]
}

var codeRe = regexp.MustCompile(`\[([A-Z0-9]{6})\]`)

func codeFrom(t *testing.T, n Notice) string {
	t.Helper()
	m := codeRe.FindStringSubmatch(n.Text)
	if m == nil {
		t.Fatalf("no code in prompt: %q", n.Text)
	}
	return m[1]
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newTestManager(t *testing.T, cfg Config) *Manager {
	t.Helper()
	if cfg.Logger == nil {
		cfg.Logger = quietLogger()
	}
	m := NewManager(cfg)
	t.Cleanup(m.Close)
	return m
}

const (
	sess = "telegram_42_sess"
	chID = "telegram"
	user = "42"
)

func interactiveCtx(ch *fakeChannel) context.Context {
	return WithInteractiveOrigin(context.Background(), Origin{
		Source: chID, ChannelID: chID, UserID: user, SessionKey: sess, Notify: ch.notify,
	})
}

func inbound(ch *fakeChannel, text string) Inbound {
	return Inbound{ChannelID: chID, UserID: user, SessionKey: sess, Text: text, Notify: ch.notify}
}

func testAction(to string) Action {
	return Action{
		Kind:        "test.send",
		Title:       "send to " + to,
		Fields:      []Field{{Name: "To", Value: to}, {Name: "Body", Value: "SECRET BODY"}},
		Audit:       map[string]string{"to": to},
		Fingerprint: Fingerprint("test.send", map[string]string{"to": to}),
	}
}

// counter is an ExecuteFunc recording which fingerprints ran.
type counter struct {
	mu   sync.Mutex
	runs []string
	done chan struct{}
}

func newCounter() *counter { return &counter{done: make(chan struct{}, 16)} }

func (c *counter) exec(tag string) ExecuteFunc {
	return func(ctx context.Context, tk Ticket) (string, error) {
		c.mu.Lock()
		c.runs = append(c.runs, tag)
		c.mu.Unlock()
		c.done <- struct{}{}
		return "ok " + tag, nil
	}
}

func (c *counter) get() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.runs...)
}

func TestRequest_UnknownOriginFailsClosed(t *testing.T) {
	m := newTestManager(t, Config{})
	_, err := m.Request(context.Background(), testAction("a@x"), newCounter().exec("a"))
	var ni *NonInteractiveError
	if !errors.As(err, &ni) || ni.Source != "unknown" || !errors.Is(err, ErrNonInteractive) {
		t.Fatalf("want NonInteractiveError(unknown), got %v", err)
	}
}

func TestRequest_NonInteractiveSourcesFail(t *testing.T) {
	ch := &fakeChannel{}
	m := newTestManager(t, Config{})
	for _, src := range []string{"heartbeat", "cron", "subagent", "wake:inter_session"} {
		// Even when an interactive origin was inherited, WithNonInteractive wins.
		ctx := WithNonInteractive(interactiveCtx(ch), src)
		_, err := m.Request(ctx, testAction("a@x"), newCounter().exec("a"))
		var ni *NonInteractiveError
		if !errors.As(err, &ni) || ni.Source != src {
			t.Fatalf("%s: want NonInteractiveError, got %v", src, err)
		}
	}
	if len(ch.all()) != 0 {
		t.Fatalf("no prompt may be sent for non-interactive origins")
	}
	if len(m.Pending(sess)) != 0 {
		t.Fatalf("nothing may be pending")
	}
}

func TestRequest_CannotPromptFailsClosed(t *testing.T) {
	m := newTestManager(t, Config{})
	// Interactive but no notifier.
	ctx := WithInteractiveOrigin(context.Background(), Origin{ChannelID: chID, UserID: user, SessionKey: sess})
	if _, err := m.Request(ctx, testAction("a@x"), newCounter().exec("a")); !errors.Is(err, ErrCannotPrompt) {
		t.Fatalf("want ErrCannotPrompt, got %v", err)
	}
	// Notifier errors.
	ch := &fakeChannel{fail: errors.New("adapter down")}
	if _, err := m.Request(interactiveCtx(ch), testAction("a@x"), newCounter().exec("a")); !errors.Is(err, ErrCannotPrompt) {
		t.Fatalf("want ErrCannotPrompt, got %v", err)
	}
	if len(m.Pending(sess)) != 0 {
		t.Fatalf("failed prompt must not leave a pending approval")
	}
}

func TestRequest_PromptsAndApproveRunsOnce(t *testing.T) {
	ch := &fakeChannel{}
	var resolved []Resolution
	var rmu sync.Mutex
	m := newTestManager(t, Config{OnResolved: func(r Resolution) { rmu.Lock(); resolved = append(resolved, r); rmu.Unlock() }})
	c := newCounter()

	tk, err := m.Request(interactiveCtx(ch), testAction("a@x"), c.exec("A"))
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	prompt := ch.last()
	code := codeFrom(t, prompt)
	if code != tk.Code || len(prompt.Choices) != 2 || prompt.Choices[0].Reply != "YES "+code {
		t.Fatalf("bad prompt: %+v", prompt)
	}
	if !strings.Contains(prompt.Text, "a@x") {
		t.Fatalf("prompt should show the recipient: %q", prompt.Text)
	}
	if got := m.Pending(sess); len(got) != 1 {
		t.Fatalf("want 1 pending, got %d", len(got))
	}

	if !m.HandleReply(context.Background(), inbound(ch, "yes "+strings.ToLower(code))) {
		t.Fatal("reply not consumed")
	}
	m.Wait()
	if got := c.get(); len(got) != 1 || got[0] != "A" {
		t.Fatalf("want exactly A once, got %v", got)
	}

	// Reuse: same code again must not run anything.
	if !m.HandleReply(context.Background(), inbound(ch, "YES "+code)) {
		t.Fatal("reused code reply should still be consumed (and rejected)")
	}
	m.Wait()
	if got := c.get(); len(got) != 1 {
		t.Fatalf("reuse ran the action again: %v", got)
	}
	if !strings.Contains(ch.last().Text, "No pending approval") {
		t.Fatalf("reuse should be rejected, got %q", ch.last().Text)
	}
	rmu.Lock()
	defer rmu.Unlock()
	if len(resolved) != 1 || resolved[0].Decision != DecisionApproved || resolved[0].Result != "ok A" {
		t.Fatalf("OnResolved: %+v", resolved)
	}
}

func TestHandleReply_NoDenies(t *testing.T) {
	ch := &fakeChannel{}
	m := newTestManager(t, Config{})
	c := newCounter()
	tk, _ := m.Request(interactiveCtx(ch), testAction("a@x"), c.exec("A"))
	if !m.HandleReply(context.Background(), inbound(ch, "NO "+tk.Code)) {
		t.Fatal("NO not consumed")
	}
	// A later YES for the same code must not resurrect it.
	m.HandleReply(context.Background(), inbound(ch, "YES "+tk.Code))
	m.Wait()
	if len(c.get()) != 0 {
		t.Fatalf("denied action ran: %v", c.get())
	}
	if len(m.Pending(sess)) != 0 {
		t.Fatal("denied approval still pending")
	}
}

func TestHandleReply_WrongCodeDoesNotApprove(t *testing.T) {
	ch := &fakeChannel{}
	m := newTestManager(t, Config{MaxBadReplies: 3})
	c := newCounter()
	tk, _ := m.Request(interactiveCtx(ch), testAction("a@x"), c.exec("A"))

	wrong := "ZZZZZ9"
	if wrong == tk.Code {
		wrong = "YYYYY8"
	}
	if !m.HandleReply(context.Background(), inbound(ch, "YES "+wrong)) {
		t.Fatal("wrong-code reply should be consumed")
	}
	m.Wait()
	if len(c.get()) != 0 {
		t.Fatal("wrong code approved the action")
	}
	if len(m.Pending(sess)) != 1 {
		t.Fatal("one wrong code should not cancel the pending approval")
	}
	// Two more wrong codes => everything pending in the session is revoked.
	m.HandleReply(context.Background(), inbound(ch, "YES "+wrong))
	m.HandleReply(context.Background(), inbound(ch, "YES "+wrong))
	if len(m.Pending(sess)) != 0 {
		t.Fatal("too many wrong codes should revoke pending approvals")
	}
	m.HandleReply(context.Background(), inbound(ch, "YES "+tk.Code))
	m.Wait()
	if len(c.get()) != 0 {
		t.Fatal("revoked approval ran")
	}
}

func TestHandleReply_BoundToSessionUserChannel(t *testing.T) {
	ch := &fakeChannel{}
	m := newTestManager(t, Config{MaxBadReplies: 100})
	c := newCounter()
	tk, _ := m.Request(interactiveCtx(ch), testAction("a@x"), c.exec("A"))

	others := []Inbound{
		{ChannelID: chID, UserID: "999", SessionKey: sess, Text: "YES " + tk.Code},       // other user
		{ChannelID: chID, UserID: user, SessionKey: "other", Text: "YES " + tk.Code},     // other session
		{ChannelID: "websocket", UserID: user, SessionKey: sess, Text: "YES " + tk.Code}, // other channel
	}
	for _, in := range others {
		if !m.HandleReply(context.Background(), in) {
			t.Fatalf("reply should be consumed: %+v", in)
		}
	}
	m.Wait()
	if len(c.get()) != 0 {
		t.Fatal("approval granted from the wrong session/user/channel")
	}
	if len(m.Pending(sess)) != 1 {
		t.Fatal("pending approval should survive foreign replies")
	}
}

func TestHandleReply_ExpiredDenied(t *testing.T) {
	ch := &fakeChannel{}
	clk := &clock{now: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)}
	var resolved atomic.Value
	m := newTestManager(t, Config{TTL: time.Hour, Now: clk.Now, OnResolved: func(r Resolution) { resolved.Store(r.Decision) }})
	c := newCounter()
	tk, _ := m.Request(interactiveCtx(ch), testAction("a@x"), c.exec("A"))

	clk.Add(time.Hour + time.Second)
	if !m.HandleReply(context.Background(), inbound(ch, "YES "+tk.Code)) {
		t.Fatal("expired reply not consumed")
	}
	m.Wait()
	if len(c.get()) != 0 {
		t.Fatal("expired approval ran")
	}
	if d, _ := resolved.Load().(Decision); d != DecisionExpired {
		t.Fatalf("want expired resolution, got %v", d)
	}
}

func TestTTLTimerExpires(t *testing.T) {
	ch := &fakeChannel{}
	expired := make(chan Resolution, 1)
	m := newTestManager(t, Config{TTL: 30 * time.Millisecond, OnResolved: func(r Resolution) { expired <- r }})
	c := newCounter()
	tk, _ := m.Request(interactiveCtx(ch), testAction("a@x"), c.exec("A"))
	select {
	case r := <-expired:
		if r.Decision != DecisionExpired {
			t.Fatalf("want expired, got %v", r.Decision)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timer never expired the approval")
	}
	m.HandleReply(context.Background(), inbound(ch, "YES "+tk.Code))
	m.Wait()
	if len(c.get()) != 0 {
		t.Fatal("timed-out approval ran")
	}
	if !strings.Contains(ch.all()[1].Text, "expired") {
		t.Fatalf("human should be told it expired: %+v", ch.all())
	}
}

// conduit-enf0: Action.TTL overrides the manager TTL (capped at MaxTTL);
// zero keeps the manager default.
func TestActionTTLOverride(t *testing.T) {
	clk := &clock{now: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)}
	m := newTestManager(t, Config{TTL: 5 * time.Minute, Now: clk.Now})
	for _, tc := range []struct {
		ttl, want time.Duration
	}{
		{0, 5 * time.Minute},
		{-time.Second, 5 * time.Minute},
		{90 * time.Second, 90 * time.Second},
		{30 * time.Minute, 30 * time.Minute},
		{48 * time.Hour, MaxTTL},
	} {
		ch := &fakeChannel{}
		a := testAction("a@x")
		a.TTL = tc.ttl
		tk, err := m.Request(interactiveCtx(ch), a, newCounter().exec("A"))
		if err != nil {
			t.Fatal(err)
		}
		if got := tk.ExpiresAt.Sub(clk.Now()); got != tc.want {
			t.Errorf("TTL %v: expires in %v, want %v", tc.ttl, got, tc.want)
		}
		if want := "Expires in " + tc.want.String(); !strings.Contains(ch.last().Text, want) {
			t.Errorf("TTL %v: prompt %q lacks %q", tc.ttl, ch.last().Text, want)
		}
	}
}

func TestActionTTLExpiresEarly(t *testing.T) {
	ch := &fakeChannel{}
	expired := make(chan Resolution, 1)
	m := newTestManager(t, Config{OnResolved: func(r Resolution) { expired <- r }}) // default 5m
	a := testAction("a@x")
	a.TTL = 30 * time.Millisecond
	c := newCounter()
	tk, _ := m.Request(interactiveCtx(ch), a, c.exec("A"))
	select {
	case r := <-expired:
		if r.Decision != DecisionExpired {
			t.Fatalf("want expired, got %v", r.Decision)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("per-action TTL did not expire the approval")
	}
	m.HandleReply(context.Background(), inbound(ch, "YES "+tk.Code))
	m.Wait()
	if len(c.get()) != 0 {
		t.Fatal("expired approval ran")
	}
}

func TestApprovalForAParamsDoesNotAuthorizeB(t *testing.T) {
	ch := &fakeChannel{}
	m := newTestManager(t, Config{})
	c := newCounter()
	a, _ := m.Request(interactiveCtx(ch), testAction("a@x"), c.exec("A"))
	b, _ := m.Request(interactiveCtx(ch), testAction("b@evil"), c.exec("B"))
	if a.Code == b.Code || a.Fingerprint == b.Fingerprint {
		t.Fatal("distinct actions must get distinct codes and fingerprints")
	}
	m.HandleReply(context.Background(), inbound(ch, "YES "+a.Code))
	m.Wait()
	if got := c.get(); len(got) != 1 || got[0] != "A" {
		t.Fatalf("approving A must run only A, got %v", got)
	}
	if p := m.Pending(sess); len(p) != 1 || p[0].Code != b.Code {
		t.Fatalf("B must remain pending, got %+v", p)
	}
}

func TestConcurrentApprovesRunOnce(t *testing.T) {
	ch := &fakeChannel{}
	m := newTestManager(t, Config{MaxBadReplies: 1000})
	c := newCounter()
	tk, _ := m.Request(interactiveCtx(ch), testAction("a@x"), c.exec("A"))
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); m.HandleReply(context.Background(), inbound(ch, "YES "+tk.Code)) }()
	}
	wg.Wait()
	m.Wait()
	if got := c.get(); len(got) != 1 {
		t.Fatalf("want exactly one run, got %d", len(got))
	}
}

func TestExecuteFailureReported(t *testing.T) {
	ch := &fakeChannel{}
	m := newTestManager(t, Config{})
	tk, _ := m.Request(interactiveCtx(ch), testAction("a@x"), func(ctx context.Context, _ Ticket) (string, error) {
		return "", errors.New("smtp exploded")
	})
	m.HandleReply(context.Background(), inbound(ch, "YES "+tk.Code))
	m.Wait()
	if !strings.Contains(ch.last().Text, "failed: smtp exploded") {
		t.Fatalf("failure not reported: %q", ch.last().Text)
	}
}

func TestParseReply(t *testing.T) {
	cases := []struct {
		in      string
		ok      bool
		approve bool
		code    string
	}{
		{"YES ABC234", true, true, "ABC234"},
		{"  yes abc234 ", true, true, "ABC234"},
		{"/yes ABC234", true, true, "ABC234"},
		{"approve ABC234!", true, true, "ABC234"},
		{"NO ABC234", true, false, "ABC234"},
		{"deny abc234", true, false, "ABC234"},
		{"yes", false, false, ""},
		{"no thanks", false, false, ""},  // ordinary reply, not a code
		{"yes please", false, false, ""}, // ordinary reply, not a code
		{"no 123456", false, false, ""},  // codes always contain a letter
		{"yes ABCDEF", false, false, ""}, // ...and a digit
		{"no", false, false, ""},
		{"yes please send it ABC234", false, false, ""},
		{"YES ABC234 and also delete everything", false, false, ""},
		{"hello", false, false, ""},
	}
	for _, tc := range cases {
		approve, code, ok := ParseReply(tc.in)
		if ok != tc.ok || approve != tc.approve || code != tc.code {
			t.Errorf("ParseReply(%q) = (%v,%q,%v), want (%v,%q,%v)", tc.in, approve, code, ok, tc.approve, tc.code, tc.ok)
		}
	}
}

func TestHandleReply_IgnoresNonReplies(t *testing.T) {
	m := newTestManager(t, Config{})
	if m.HandleReply(context.Background(), Inbound{SessionKey: sess, Text: "yes"}) {
		t.Fatal("bare yes must pass through to the model")
	}
	if m.HandleReply(context.Background(), Inbound{SessionKey: sess, Text: "no thanks"}) {
		t.Fatal("'no thanks' must pass through to the model")
	}
	for i := 0; i < 200; i++ {
		if c := randomCode(); !isCodeShaped(c) || len(c) != codeLength {
			t.Fatalf("bad generated code %q", c)
		}
	}
	var nilMgr *Manager
	if nilMgr.HandleReply(context.Background(), Inbound{Text: "YES ABC234"}) {
		t.Fatal("nil manager must not consume")
	}
}

func TestFingerprintNoConcatCollision(t *testing.T) {
	a := Fingerprint("k", map[string]string{"a": "bc"})
	b := Fingerprint("k", map[string]string{"ab": "c"})
	c := Fingerprint("k2", map[string]string{"a": "bc"})
	if a == b || a == c {
		t.Fatal("fingerprint collision")
	}
	if a != Fingerprint("k", map[string]string{"a": "bc"}) {
		t.Fatal("fingerprint not stable")
	}
}

func TestAuditNeverLogsFields(t *testing.T) {
	var buf strings.Builder
	var mu sync.Mutex
	w := writerFunc(func(p []byte) (int, error) { mu.Lock(); defer mu.Unlock(); return buf.Write(p) })
	ch := &fakeChannel{}
	m := newTestManager(t, Config{Logger: slog.New(slog.NewTextHandler(w, nil))})
	tk, _ := m.Request(interactiveCtx(ch), testAction("a@x"), newCounter().exec("A"))
	m.HandleReply(context.Background(), inbound(ch, "NO "+tk.Code))
	mu.Lock()
	defer mu.Unlock()
	out := buf.String()
	if strings.Contains(out, "SECRET BODY") {
		t.Fatal("audit log leaked a prompt field")
	}
	for _, ev := range []string{"approval.requested", "approval.denied"} {
		if !strings.Contains(out, ev) {
			t.Fatalf("audit log missing %s: %s", ev, out)
		}
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// Multi-line values (commands, bodies) render on their own indented lines
// so the human reads exactly what will run (conduit-c8ct, conduit-w3l7).
func TestPromptNotice_MultiLineFields(t *testing.T) {
	n := promptNotice(Ticket{Code: "ABC234", ExpiresAt: time.Now()}, Action{
		Title: "Run SSH command on web-1",
		Fields: []Field{
			{Name: "Host", Value: "web-1"},
			{Name: "Command", Value: "systemctl stop app\nrm -rf /srv/app/cache\n"},
		},
	}, time.Minute)
	want := "\nHost: web-1\nCommand:\n    systemctl stop app\n    rm -rf /srv/app/cache\n\nReply"
	if !strings.Contains(n.Text, want) {
		t.Fatalf("prompt layout mismatch:\n%s", n.Text)
	}
}
