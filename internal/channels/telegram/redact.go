package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"conduit/internal/httpsafe"
	"conduit/internal/redact"

	"github.com/go-telegram/bot"
)

// conduit-31jg.83: the go-telegram/bot library puts the bot token in every
// request URL, and its default errors/debug handlers log.Printf the raw
// error (a *url.Error carrying that URL). These hooks make sure no library
// or adapter path can write the token anywhere:
//
//   - redactingHTTPClient scrubs the URL out of transport errors at the
//     source, so every error the library returns (to its own handlers and to
//     SendMessage/GetFile/... callers) is already clean;
//   - botErrorsHandler / botDebugHandler replace the library's log.Printf
//     defaults with slog output that is redacted again (belt and braces:
//     debug output includes response bodies and request URLs);
//   - the adapter also registers its token with redact.RegisterSecret, and
//     the gateway routes the std logger through redact.NewWriter, which
//     covers the one remaining raw log.Printf in the library (response body
//     close failure).

// telegramPollTimeout mirrors the library's defaultPollTimeout; it must be
// passed explicitly because WithHTTPClient sets both the client and the
// long-poll timeout.
const telegramPollTimeout = time.Minute

// fileDownloadTimeout bounds photo/voice downloads from Telegram's file API.
const fileDownloadTimeout = 30 * time.Second

// redactingHTTPClient wraps a bot.HttpClient and scrubs the bot token out of
// request errors (net/http reports `Post "https://api.telegram.org/bot<TOKEN>/…"`).
type redactingHTTPClient struct {
	inner bot.HttpClient
}

func (c *redactingHTTPClient) Do(req *http.Request) (*http.Response, error) {
	resp, err := c.inner.Do(req)
	if err != nil {
		return resp, redact.URLError(err)
	}
	return resp, nil
}

// newRedactingHTTPClient returns an *http.Client-backed client with the given
// timeout whose errors never contain the request URL's token.
func newRedactingHTTPClient(timeout time.Duration) *redactingHTTPClient {
	return &redactingHTTPClient{inner: &http.Client{Timeout: timeout}}
}

// downloadClient returns the client used for photo/voice file downloads.
func (a *Adapter) downloadClient() bot.HttpClient {
	if a.fileClient != nil {
		return a.fileClient
	}
	return newRedactingHTTPClient(fileDownloadTimeout)
}

// downloadTelegramFile fetches a file from Telegram's file API. The download
// URL is https://api.telegram.org/file/bot<TOKEN>/<path>, so every error
// (request construction, transport, status) is scrubbed before it is
// returned. what names the file in error messages ("photo", "voice").
func downloadTelegramFile(ctx context.Context, client bot.HttpClient, downloadURL, what string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create %s download request: %w", what, redact.Error(err))
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to download %s file: %w", what, redact.URLError(err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status downloading %s file: %d", what, resp.StatusCode)
	}

	data, err := httpsafe.ReadLimited(resp.Body, httpsafe.MediaBodyLimit) // conduit-31jg.7
	if err != nil {
		return nil, fmt.Errorf("failed to read %s file: %w", what, redact.Error(err))
	}
	return data, nil
}

// botLog returns the logger for library errors/debug output (tests swap it).
var botLog = slog.Default

// botErrorsHandler replaces the library's log.Printf("[TGBOT] [ERROR] %v").
// Long-poll timeouts and transient network failures land here; they are
// retried by the library, so they are warnings, not errors.
func botErrorsHandler(err error) {
	if err == nil {
		return
	}
	botLog().Warn("telegram bot library error", "component", "telegram", "error", redact.String(err.Error()))
}

// botDebugHandler replaces the library's log.Printf("[TGBOT] [DEBUG] ...").
// Debug output includes request URLs (with the token) and response payloads.
func botDebugHandler(format string, args ...any) {
	botLog().Debug("telegram bot library debug", "component", "telegram", "msg", redact.String(fmt.Sprintf(format, args...)))
}

// botLoggingOptions returns the bot options that route every library log and
// request error through the redacting hooks above.
func botLoggingOptions() []bot.Option {
	return []bot.Option{
		bot.WithErrorsHandler(botErrorsHandler),
		bot.WithDebugHandler(botDebugHandler),
		bot.WithHTTPClient(telegramPollTimeout, newRedactingHTTPClient(telegramPollTimeout)),
	}
}
