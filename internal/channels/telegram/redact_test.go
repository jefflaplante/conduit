package telegram

import (
	"bytes"
	"context"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// fakeBotToken has the real token shape; it is not a real token and is
// deliberately NOT registered with redact.RegisterSecret, so these tests
// exercise the pattern-based scrubbing.
const fakeBotToken = "987654321:AAHfakefakefakefakefakefakefake_-X"

// syncBuffer is a goroutine-safe bytes.Buffer for log capture.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLogs redirects both the bot slog hook and the std logger (the
// library's raw log.Printf fallback) into one buffer for the test.
func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prevLog := botLog
	prevOut := log.Writer()
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	botLog = func() *slog.Logger { return logger }
	log.SetOutput(buf)
	t.Cleanup(func() {
		botLog = prevLog
		log.SetOutput(prevOut)
	})
	return buf
}

// A long-poll getUpdates that hits Client.Timeout (the journal leak in
// conduit-31jg.83) must be logged with the token redacted, through our
// handler rather than the library's log.Printf default.
func TestBotGetUpdatesTimeout_TokenRedacted(t *testing.T) {
	buf := captureLogs(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, fakeBotToken) {
			t.Errorf("request path does not carry the token; test is not exercising the leak: %s", r.URL.Path)
		}
		select { // hang past the client timeout
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer func() { srv.CloseClientConnections(); srv.Close() }()

	opts := append(botLoggingOptions(),
		bot.WithSkipGetMe(),
		bot.WithServerURL(srv.URL),
		// Same redacting wrapper as production, with a short timeout.
		bot.WithHTTPClient(2*time.Second, &redactingHTTPClient{inner: &http.Client{Timeout: 50 * time.Millisecond}}),
		bot.WithDefaultHandler(func(context.Context, *bot.Bot, *models.Update) {}),
		bot.WithDebug(),
	)
	b, err := bot.New(fakeBotToken, opts...)
	if err != nil {
		t.Fatalf("bot.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); b.Start(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(buf.String(), "error get updates") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	out := buf.String()
	if !strings.Contains(out, "error get updates") {
		t.Fatalf("expected a getUpdates error to be logged; got:\n%s", out)
	}
	if !strings.Contains(out, "Client.Timeout") || !strings.Contains(out, "bot<REDACTED>/getUpdates") {
		t.Errorf("expected redacted timeout error; got:\n%s", out)
	}
	if strings.Contains(out, fakeBotToken) || strings.Contains(out, "AAHfakefake") {
		t.Fatalf("bot token leaked into logs:\n%s", out)
	}
	if strings.Contains(out, "[TGBOT]") {
		t.Errorf("library default log.Printf handler still in use:\n%s", out)
	}
}

// Request errors returned to callers (SendMessage etc.) are scrubbed at the
// HTTP client, so they are safe to log or surface.
func TestBotRequestError_ReturnedErrorRedacted(t *testing.T) {
	captureLogs(t)
	b, err := bot.New(fakeBotToken, append(botLoggingOptions(),
		bot.WithSkipGetMe(),
		bot.WithServerURL("http://127.0.0.1:1"), // connection refused
	)...)
	if err != nil {
		t.Fatalf("bot.New: %v", err)
	}
	_, err = b.SendMessage(context.Background(), &bot.SendMessageParams{ChatID: 1, Text: "x"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), fakeBotToken) || !strings.Contains(err.Error(), "bot<REDACTED>") {
		t.Fatalf("returned error not redacted: %v", err)
	}
}

// Photo/voice downloads use https://api.telegram.org/file/bot<TOKEN>/...;
// timeouts and connection errors must not carry the token.
func TestFileDownloadErrors_TokenRedacted(t *testing.T) {
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer func() { hang.CloseClientConnections(); hang.Close() }()

	cases := map[string]string{
		"timeout": hang.URL + "/file/bot" + fakeBotToken + "/photos/file_1.jpg",
		"refused": "http://127.0.0.1:1/file/bot" + fakeBotToken + "/voice/file_2.oga",
		"bad url": "http://[::1/file/bot" + fakeBotToken + "/x",
	}
	for name, u := range cases {
		t.Run(name, func(t *testing.T) {
			mb := &voiceMockBot{fileDownloadLink: u}
			a, _ := newTestVoiceAdapter(mb, &mockSTT{text: "hi"}, 1)
			a.fileClient = &http.Client{Timeout: 50 * time.Millisecond} // raw client: scrubbing must not rely on the wrapper

			_, perr := a.downloadPhoto(context.Background(), &models.PhotoSize{FileID: "f"})
			_, verr := a.transcribeVoice(context.Background(), &models.Voice{FileID: "f"})
			for _, err := range []error{perr, verr} {
				if err == nil {
					t.Fatal("expected a download error")
				}
				if strings.Contains(err.Error(), fakeBotToken) {
					t.Fatalf("token leaked in download error: %v", err)
				}
			}
		})
	}
}
