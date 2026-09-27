package redact

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strings"
	"testing"
)

// fakeToken has the real Telegram token shape but is not a real token.
const fakeToken = "123456789:AAFakeTokenFakeTokenFakeTokenFake_x-" // leak-guard:allow (fake token for redaction test)

func TestTelegramToken(t *testing.T) {
	cases := []struct{ in, want string }{
		{`Post "https://api.telegram.org/bot` + fakeToken + `/getUpdates": context deadline exceeded`,
			`Post "https://api.telegram.org/bot<REDACTED>/getUpdates": context deadline exceeded`},
		{"https://api.telegram.org/file/bot" + fakeToken + "/photos/file_1.jpg",
			"https://api.telegram.org/file/bot<REDACTED>/photos/file_1.jpg"},
		{"token is " + fakeToken + " ok", "token is <REDACTED> ok"},
		{"meeting at 12:30, id 42:abc", "meeting at 12:30, id 42:abc"},
		{"no secrets here", "no secrets here"},
	}
	for _, c := range cases {
		if got := TelegramToken(c.in); got != c.want {
			t.Errorf("TelegramToken(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRegisterSecret(t *testing.T) {
	RegisterSecret("short") // ignored
	RegisterSecret("odd-shaped-secret-value")
	got := String("x odd-shaped-secret-value y short")
	if got != "x <REDACTED> y short" {
		t.Errorf("String = %q", got)
	}
}

func TestError_PreservesChain(t *testing.T) {
	base := fmt.Errorf("error do request for method getUpdates, %w",
		&url.Error{Op: "Post", URL: "https://api.telegram.org/bot" + fakeToken + "/getUpdates", Err: context.Canceled})
	red := Error(base)
	if strings.Contains(red.Error(), fakeToken) {
		t.Fatalf("token leaked: %v", red)
	}
	if !errors.Is(red, context.Canceled) {
		t.Error("errors.Is(context.Canceled) lost after redaction")
	}
	if Error(nil) != nil {
		t.Error("Error(nil) != nil")
	}
	plain := errors.New("plain")
	if Error(plain) != plain {
		t.Error("clean error should be returned unchanged")
	}
}

func TestURLError_KeepsType(t *testing.T) {
	ue := &url.Error{Op: "Get", URL: "https://api.telegram.org/file/bot" + fakeToken + "/voice/f.oga", Err: context.DeadlineExceeded}
	red := URLError(ue)
	var got *url.Error
	if !errors.As(red, &got) {
		t.Fatalf("type lost: %T", red)
	}
	if strings.Contains(red.Error(), fakeToken) {
		t.Fatalf("token leaked: %v", red)
	}
	if !got.Timeout() || !errors.Is(red, context.DeadlineExceeded) {
		t.Error("timeout semantics lost")
	}
}

func TestWriter_StdLogger(t *testing.T) {
	var buf bytes.Buffer
	l := log.New(NewWriter(&buf), "", 0)
	l.Printf("[TGBOT] [ERROR] error get updates, Post %q: net/http: request canceled (Client.Timeout exceeded while awaiting headers)",
		"https://api.telegram.org/bot"+fakeToken+"/getUpdates")
	if strings.Contains(buf.String(), fakeToken) || !strings.Contains(buf.String(), "bot<REDACTED>/getUpdates") {
		t.Errorf("log output not redacted: %s", buf.String())
	}
}
