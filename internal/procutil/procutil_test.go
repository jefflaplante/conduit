package procutil

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestCappedBuffer_UnderCapIsExact(t *testing.T) {
	b := NewCappedBuffer(16)
	for _, s := range []string{"hello ", "wor", "ld"} {
		b.Write([]byte(s))
	}
	if got := b.String(); got != "hello world" {
		t.Fatalf("got %q", got)
	}
	if b.Truncated() {
		t.Fatal("should not be truncated")
	}
}

func TestCappedBuffer_ExactlyAtCap(t *testing.T) {
	b := NewCappedBuffer(10)
	b.Write([]byte("0123456789"))
	if got := b.String(); got != "0123456789" || b.Truncated() {
		t.Fatalf("got %q truncated=%v", got, b.Truncated())
	}
}

func TestCappedBuffer_KeepsHeadAndTailAcrossWraps(t *testing.T) {
	var all bytes.Buffer
	b := NewCappedBuffer(20)
	for i := 0; i < 1000; i++ {
		chunk := []byte(strings.Repeat(string(rune('a'+i%26)), i%7+1))
		all.Write(chunk)
		b.Write(chunk)
	}
	src := all.Bytes()
	got := b.String()
	if !strings.HasPrefix(got, string(src[:10])) {
		t.Fatalf("head mismatch: %q", got[:10])
	}
	if !strings.HasSuffix(got, string(src[len(src)-10:])) {
		t.Fatalf("tail mismatch: %q vs %q", got, src[len(src)-10:])
	}
	if b.Dropped() != int64(len(src)-20) || b.Total() != int64(len(src)) {
		t.Fatalf("dropped=%d total=%d len=%d", b.Dropped(), b.Total(), len(src))
	}
	if !strings.Contains(got, "bytes omitted") {
		t.Fatalf("missing marker: %q", got)
	}
}

func TestCappedBuffer_RuneSafeEdges(t *testing.T) {
	b := NewCappedBuffer(9)
	b.Write([]byte(strings.Repeat("é", 50))) // 2 bytes each
	if got := b.String(); !utf8.ValidString(got) {
		t.Fatalf("invalid UTF-8: %q", got)
	}
}

func TestConfigureGroupKill_KillsGrandchildrenOnTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", "sleep 30 & sleep 30")
	ConfigureGroupKill(cmd, 100*time.Millisecond, time.Second)
	buf := NewCappedBuffer(1024)
	cmd.Stdout, cmd.Stderr = buf, buf
	start := time.Now()
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected error from cancelled command")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("Run took %s; grandchild kept the pipe open", d)
	}
}
