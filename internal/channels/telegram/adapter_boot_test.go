package telegram

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-telegram/bot"

	"conduit/internal/channels"
	"conduit/internal/protocol"
)

// fakeBotAPI is a minimal Telegram Bot API server for boot tests. getMe
// answers with getMeHandler (default: success); getUpdates delivers each
// queued update once, then empty results; every other method succeeds.
type fakeBotAPI struct {
	srv *httptest.Server

	getMeCalls   atomic.Int32
	getMeHandler func(call int32, w http.ResponseWriter)

	mu      sync.Mutex
	updates []string // raw JSON update objects, delivered once
}

func newFakeBotAPI(t *testing.T, getMe func(call int32, w http.ResponseWriter)) *fakeBotAPI {
	t.Helper()
	f := &fakeBotAPI{getMeHandler: getMe}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeBotAPI) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.HasSuffix(r.URL.Path, "/getMe"):
		call := f.getMeCalls.Add(1)
		if f.getMeHandler != nil {
			f.getMeHandler(call, w)
			return
		}
		writeGetMeOK(w)
	case strings.HasSuffix(r.URL.Path, "/getUpdates"):
		f.mu.Lock()
		pending := f.updates
		f.updates = nil
		f.mu.Unlock()
		if len(pending) == 0 {
			// Hold the long poll briefly instead of spinning.
			select {
			case <-r.Context().Done():
			case <-time.After(20 * time.Millisecond):
			}
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":[` + strings.Join(pending, ",") + `]}`))
	default:
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}
}

func (f *fakeBotAPI) queueUpdate(raw string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, raw)
}

func writeGetMeOK(w http.ResponseWriter) {
	_, _ = w.Write([]byte(`{"ok":true,"result":{"id":123456,"is_bot":true,"first_name":"Acme","username":"acme_test_bot"}}`))
}

// newBootTestAdapter returns an adapter (no pairing) pointed at srv.
func newBootTestAdapter(srv *httptest.Server) *Adapter {
	return &Adapter{
		id:          "tg",
		name:        "tg",
		config:      TelegramConfig{BotToken: fakeBotToken},
		status:      channels.StatusInitializing,
		incoming:    make(chan *protocol.IncomingMessage, 10),
		seenUpdates: make(map[int64]time.Time),
		extraBotOptions: []bot.Option{
			bot.WithServerURL(srv.URL),
		},
	}
}

// fastBootRetry shortens the boot backoff for the test.
func fastBootRetry(t *testing.T) {
	t.Helper()
	oldDelay, oldTimeout := bootRetryDelay, getMeTimeout
	bootRetryDelay = func(int) time.Duration { return 5 * time.Millisecond }
	getMeTimeout = time.Second
	t.Cleanup(func() { bootRetryDelay, getMeTimeout = oldDelay, oldTimeout })
}

func waitForStatus(t *testing.T, a *Adapter, want channels.StatusCode, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		a.mutex.RLock()
		got, msg := a.status, a.statusMsg
		a.mutex.RUnlock()
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("status = %s (%q), want %s", got, msg, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestBootRetryDelay_Schedule(t *testing.T) {
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
	for i, w := range want {
		if got := bootRetryDelay(i + 1); got != w {
			t.Errorf("bootRetryDelay(%d) = %v, want %v", i+1, got, w)
		}
	}
	if got := bootRetryDelay(100); got != 30*time.Second {
		t.Errorf("bootRetryDelay(100) = %v, want 30s cap", got)
	}
}

// bd-twb0: a transient getMe failure at boot must not leave the channel
// dead. Start returns nil at once, retries in the background, and the bot
// then receives messages normally.
func TestAdapterStart_RetriesTransientGetMeFailure(t *testing.T) {
	fastBootRetry(t)
	const failures = 3
	fake := newFakeBotAPI(t, func(call int32, w http.ResponseWriter) {
		if call <= failures {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`upstream unavailable`))
			return
		}
		writeGetMeOK(w)
	})
	a := newBootTestAdapter(fake.srv)

	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = a.Stop() }()

	waitForStatus(t, a, channels.StatusOnline, 5*time.Second)
	if n := fake.getMeCalls.Load(); n != failures+1 {
		t.Errorf("getMe calls = %d, want %d", n, failures+1)
	}
	if !a.IsHealthy() {
		t.Error("IsHealthy() = false after reaching Online")
	}

	fake.queueUpdate(`{"update_id":1,"message":{"message_id":7,"date":1,"chat":{"id":42,"type":"private"},"from":{"id":42,"is_bot":false,"first_name":"Rex"},"text":"hello"}}`)
	select {
	case msg := <-a.ReceiveMessages():
		if msg.Text != "hello" || msg.ChannelID != "tg" {
			t.Fatalf("received %+v, want text %q on channel tg", msg, "hello")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no message received after recovery")
	}
}

// While retrying, the status is Reconnecting and the adapter is unhealthy.
func TestAdapterStart_ReconnectingWhileRetrying(t *testing.T) {
	fastBootRetry(t)
	bootRetryDelay = func(int) time.Duration { return time.Hour }
	fake := newFakeBotAPI(t, func(_ int32, w http.ResponseWriter) {
		w.WriteHeader(http.StatusBadGateway)
	})
	a := newBootTestAdapter(fake.srv)
	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = a.Stop() }()

	waitForStatus(t, a, channels.StatusReconnecting, 5*time.Second)
	if a.IsHealthy() {
		t.Error("IsHealthy() = true while reconnecting")
	}
	if st := a.Status(); !strings.Contains(st.Message, "retrying") || strings.Contains(st.Message, fakeBotToken) {
		t.Errorf("status message = %q, want a retry note without the token", st.Message)
	}
}

// A rejected token (401) is terminal: one attempt, no backoff, StatusError.
func TestAdapterStart_UnauthorizedFailsFast(t *testing.T) {
	// A long backoff would make a wrongly-retried 401 time the test out.
	oldDelay := bootRetryDelay
	bootRetryDelay = func(int) time.Duration { return time.Hour }
	t.Cleanup(func() { bootRetryDelay = oldDelay })

	for _, tc := range []struct {
		name string
		body string
	}{
		{"unauthorized", `{"ok":false,"error_code":401,"description":"Unauthorized"}`},
		{"forbidden", `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeBotAPI(t, func(_ int32, w http.ResponseWriter) {
				_, _ = w.Write([]byte(tc.body))
			})
			a := newBootTestAdapter(fake.srv)
			if err := a.Start(context.Background()); err != nil {
				t.Fatalf("Start: %v", err)
			}
			defer func() { _ = a.Stop() }()

			waitForStatus(t, a, channels.StatusError, 2*time.Second)
			a.mutex.RLock()
			runDone := a.runDone
			a.mutex.RUnlock()
			select {
			case <-runDone:
			case <-time.After(2 * time.Second):
				t.Fatal("run goroutine still alive after a rejected token")
			}
			if n := fake.getMeCalls.Load(); n != 1 {
				t.Errorf("getMe calls = %d, want exactly 1", n)
			}
			if a.IsHealthy() {
				t.Error("IsHealthy() = true after a rejected token")
			}
			if st := a.Status(); strings.Contains(st.Message, fakeBotToken) {
				t.Errorf("status message leaks the token: %q", st.Message)
			}
		})
	}
}

// conduit-31jg.26 contract during the retry loop: Stop cancels the backoff
// wait (and an in-flight getMe), returns promptly, the run goroutine exits,
// Stop stays idempotent and a later Start works.
func TestAdapterStop_DuringBootRetry(t *testing.T) {
	fastBootRetry(t)
	bootRetryDelay = func(int) time.Duration { return time.Hour }
	var fail atomic.Bool
	fail.Store(true)
	fake := newFakeBotAPI(t, func(_ int32, w http.ResponseWriter) {
		if fail.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writeGetMeOK(w)
	})
	a := newBootTestAdapter(fake.srv)
	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForStatus(t, a, channels.StatusReconnecting, 5*time.Second)

	a.mutex.RLock()
	runDone := a.runDone
	a.mutex.RUnlock()

	began := time.Now()
	if err := a.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if d := time.Since(began); d >= stopWaitTimeout {
		t.Fatalf("Stop took %v, want well under stopWaitTimeout (%v)", d, stopWaitTimeout)
	}
	select {
	case <-runDone:
	default:
		t.Fatal("run goroutine still running after Stop returned")
	}
	if st := a.Status(); st.Status != channels.StatusOffline {
		t.Errorf("status after Stop = %s, want offline", st.Status)
	}
	if err := a.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}

	// Restart once Telegram is reachable again.
	fail.Store(false)
	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("restart: %v", err)
	}
	waitForStatus(t, a, channels.StatusOnline, 5*time.Second)
	if err := a.Stop(); err != nil {
		t.Fatalf("final Stop: %v", err)
	}
}

// A getMe that hangs is abandoned as soon as Stop cancels the run context,
// not after the per-attempt timeout.
func TestAdapterStop_CancelsHangingGetMe(t *testing.T) {
	fastBootRetry(t)
	getMeTimeout = time.Hour
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	fake := newFakeBotAPI(t, func(_ int32, w http.ResponseWriter) {
		entered <- struct{}{}
		<-release
	})
	defer close(release) // let the server handler return before srv.Close
	a := newBootTestAdapter(fake.srv)
	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("getMe never reached the server")
	}

	a.mutex.RLock()
	runDone := a.runDone
	a.mutex.RUnlock()
	done := make(chan struct{})
	go func() { _ = a.Stop(); close(done) }()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("run goroutine did not abandon the hanging getMe on Stop")
	}
	<-done
}
