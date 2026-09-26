// Package httpsafe holds outbound-HTTP safety helpers: bounded body reads and
// an SSRF-guarded dialer/client (conduit-31jg.7).
package httpsafe

import (
	"errors"
	"io"
)

// ErrBodyTooLarge is returned when a response body exceeds its limit.
var ErrBodyTooLarge = errors.New("response body exceeds size limit")

// Per-caller body limits. Pick the smallest that fits the payload.
const (
	// ErrorBodyLimit bounds bodies read only to build an error message.
	ErrorBodyLimit int64 = 64 << 10 // 64 KiB
	// SmallAPIBodyLimit bounds small JSON control-plane responses (OAuth, bot API acks).
	SmallAPIBodyLimit int64 = 1 << 20 // 1 MiB
	// APIBodyLimit bounds ordinary JSON API responses.
	APIBodyLimit int64 = 32 << 20 // 32 MiB
	// WebPageBodyLimit bounds fetched web pages (WebFetch).
	WebPageBodyLimit int64 = 10 << 20 // 10 MiB
	// MediaBodyLimit bounds downloaded media (images, voice notes, snapshots).
	MediaBodyLimit int64 = 50 << 20 // 50 MiB
)

// ReadLimited reads at most max bytes from r. If r holds more than max bytes it
// returns the first max bytes together with ErrBodyTooLarge, so callers that
// can tolerate truncation (e.g. error-message bodies, web pages) can use the
// data while callers that cannot simply treat it as an error.
func ReadLimited(r io.Reader, max int64) ([]byte, error) {
	if max < 0 {
		max = 0
	}
	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return data, err
	}
	if int64(len(data)) > max {
		return data[:max], ErrBodyTooLarge
	}
	return data, nil
}

// LimitReader wraps r so that reading past max bytes fails with
// ErrBodyTooLarge instead of silently truncating (as io.LimitReader does).
// Use it in front of streaming decoders such as json.NewDecoder.
func LimitReader(r io.Reader, max int64) io.Reader {
	return &limitedReader{r: r, remaining: max}
}

type limitedReader struct {
	r         io.Reader
	remaining int64 // bytes still permitted
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.remaining < 0 {
		return 0, ErrBodyTooLarge
	}
	// Allow reading one byte past the limit so we can detect overflow.
	if int64(len(p)) > l.remaining+1 {
		p = p[:l.remaining+1]
	}
	n, err := l.r.Read(p)
	l.remaining -= int64(n)
	if l.remaining < 0 {
		// Drop the overflow byte and report the error.
		return n + int(l.remaining), ErrBodyTooLarge
	}
	return n, err
}
