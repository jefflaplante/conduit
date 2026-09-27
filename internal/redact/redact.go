// Package redact scrubs credentials out of strings, errors and log output
// before they leave the process (journal, tool results, chat replies).
//
// conduit-31jg.83: the go-telegram/bot library embeds the bot token in every
// request URL (https://api.telegram.org/bot<TOKEN>/getUpdates and
// .../file/bot<TOKEN>/<path>), and net/http wraps transport failures in a
// *url.Error whose message includes that URL. Unredacted, every long-poll
// timeout wrote the token to the journal.
package redact

import (
	"errors"
	"io"
	"net/url"
	"regexp"
	"strings"
	"sync"
)

// Placeholder replaces a redacted Telegram token.
const Placeholder = "<REDACTED>"

// telegramTokenInURL matches the token segment of a Telegram Bot API URL
// ("bot123456:AA...") wherever it appears (path, error text, log line).
var telegramTokenInURL = regexp.MustCompile(`bot[0-9]+:[A-Za-z0-9_-]+`)

// bareTelegramToken matches a token on its own ("123456:AA...35 chars").
// Telegram tokens are <bot id>:<35-char secret>; requiring a long secret
// keeps ordinary "12:30"-style text untouched.
var bareTelegramToken = regexp.MustCompile(`\b[0-9]{5,}:[A-Za-z0-9_-]{30,}`)

var (
	secretsMu sync.RWMutex
	secrets   []string
)

// RegisterSecret adds a literal secret (e.g. the configured bot token) that
// String, Error and Writer replace with Placeholder wherever it appears,
// whatever its shape. Empty and very short values are ignored.
func RegisterSecret(s string) {
	s = strings.TrimSpace(s)
	if len(s) < 8 {
		return
	}
	secretsMu.Lock()
	defer secretsMu.Unlock()
	for _, existing := range secrets {
		if existing == s {
			return
		}
	}
	secrets = append(secrets, s)
}

// TelegramToken replaces Telegram bot tokens in s: "bot<id>:<secret>" URL
// segments become "bot<REDACTED>", bare "<id>:<secret>" tokens become
// "<REDACTED>".
func TelegramToken(s string) string {
	if !strings.Contains(s, ":") {
		return s
	}
	s = telegramTokenInURL.ReplaceAllString(s, "bot"+Placeholder)
	return bareTelegramToken.ReplaceAllString(s, Placeholder)
}

// String applies TelegramToken and replaces every registered secret.
func String(s string) string {
	s = TelegramToken(s)
	secretsMu.RLock()
	defer secretsMu.RUnlock()
	for _, sec := range secrets {
		if strings.Contains(s, sec) {
			s = strings.ReplaceAll(s, sec, Placeholder)
		}
	}
	return s
}

// redactedError carries a scrubbed message while keeping the original error
// reachable for errors.Is / errors.As (context.Canceled, sentinel errors).
type redactedError struct {
	msg   string
	cause error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.cause }

// Error returns err with a scrubbed message. It returns err unchanged when
// there is nothing to redact, and nil for nil. The original chain stays
// reachable through Unwrap, so errors.Is/As keep working; callers must log
// the returned error, never the unwrapped cause.
func Error(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	clean := String(msg)
	if clean == msg {
		return err
	}
	return &redactedError{msg: clean, cause: err}
}

// URLError scrubs the URL of a *url.Error (and its inner error's message)
// while preserving the *url.Error type so net/http callers relying on
// Timeout()/Temporary() or errors.Is(err, context.Canceled) keep working.
// Other errors are passed to Error.
func URLError(err error) error {
	var ue *url.Error
	if !errors.As(err, &ue) || ue != err {
		return Error(err)
	}
	return &url.Error{Op: ue.Op, URL: String(ue.URL), Err: Error(ue.Err)}
}

// writer scrubs everything written through it.
type writer struct{ w io.Writer }

// NewWriter returns an io.Writer that redacts secrets before forwarding to w.
// It is meant for line-oriented log output (log.SetOutput): the standard
// logger issues one Write per record, so a token is never split across
// calls.
func NewWriter(w io.Writer) io.Writer { return &writer{w: w} }

func (rw *writer) Write(p []byte) (int, error) {
	clean := String(string(p))
	if _, err := io.WriteString(rw.w, clean); err != nil {
		return 0, err
	}
	return len(p), nil
}
