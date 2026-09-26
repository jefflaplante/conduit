// Package procutil holds process-execution helpers shared by the Bash tool,
// the skills executor and the TUI shell (conduit-31jg.20).
//
// Two problems motivate it:
//
//  1. exec.CommandContext only kills the direct child on cancellation. For
//     `sh -c '... &'` the grandchildren survive, keep the stdout/stderr pipe
//     open and block CombinedOutput far past the deadline (then leak as
//     orphans). ConfigureGroupKill puts the child in its own process group,
//     signals the whole group on cancel and bounds the post-exit pipe wait.
//
//  2. CombinedOutput buffers without limit, so `yes` or a large cat can OOM
//     the gateway. CappedBuffer keeps a bounded head and tail and counts
//     what it dropped.
package procutil

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// DefaultKillGrace is how long the process group gets between SIGTERM
	// and SIGKILL after the context is cancelled.
	DefaultKillGrace = 500 * time.Millisecond
	// DefaultWaitDelay bounds how long Wait keeps reading pipes after the
	// child exits (or the context is done) before closing them itself.
	DefaultWaitDelay = 2 * time.Second
)

// ConfigureGroupKill makes cmd run in its own process group and, when its
// context is cancelled, SIGTERMs the whole group and SIGKILLs it after grace.
// WaitDelay is set so a grandchild holding the output pipe cannot block Wait
// indefinitely. Must be called before cmd.Start; cmd must come from
// exec.CommandContext for the cancel path to fire.
func ConfigureGroupKill(cmd *exec.Cmd, grace, waitDelay time.Duration) {
	if grace <= 0 {
		grace = DefaultKillGrace
	}
	if waitDelay <= 0 {
		waitDelay = DefaultWaitDelay
	}
	setProcessGroup(cmd)
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return killGroup(cmd.Process.Pid, grace)
	}
	// WaitDelay must exceed grace so SIGKILL lands before Wait gives up on
	// the pipes; otherwise Wait would return while the group is still alive.
	if waitDelay <= grace {
		waitDelay = grace + time.Second
	}
	cmd.WaitDelay = waitDelay
}

// IsWaitDelayOnly reports whether err is exec.ErrWaitDelay alone — the
// child exited successfully but something it spawned kept the output pipe
// open past WaitDelay (e.g. `server &`). That is a success for callers.
func IsWaitDelayOnly(err error) bool {
	return errors.Is(err, exec.ErrWaitDelay)
}

// CappedBuffer is an io.Writer that retains at most headMax bytes from the
// start and tailMax bytes from the end of what is written, counting the
// bytes dropped in between. Safe for concurrent use.
type CappedBuffer struct {
	mu      sync.Mutex
	headMax int
	tailMax int
	head    []byte
	tail    []byte // ring buffer once full
	tailPos int    // next write index in tail when full
	total   int64
}

// NewCappedBuffer returns a buffer that keeps maxBytes total, split evenly
// between head and tail.
func NewCappedBuffer(maxBytes int) *CappedBuffer {
	if maxBytes < 2 {
		maxBytes = 2
	}
	h := maxBytes / 2
	return &CappedBuffer{headMax: h, tailMax: maxBytes - h}
}

// Write implements io.Writer. It never returns an error: output beyond the
// cap is discarded, not rejected, so the child is not killed by EPIPE.
func (b *CappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	b.total += int64(n)

	if room := b.headMax - len(b.head); room > 0 {
		if room > len(p) {
			room = len(p)
		}
		b.head = append(b.head, p[:room]...)
		p = p[room:]
	}
	if len(p) == 0 {
		return n, nil
	}
	if len(p) >= b.tailMax {
		b.tail = append(b.tail[:0], p[len(p)-b.tailMax:]...)
		b.tailPos = 0
		return n, nil
	}
	if len(b.tail) < b.tailMax {
		room := b.tailMax - len(b.tail)
		if room >= len(p) {
			b.tail = append(b.tail, p...)
			return n, nil
		}
		b.tail = append(b.tail, p[:room]...)
		p = p[room:]
		b.tailPos = 0
	}
	for len(p) > 0 {
		c := copy(b.tail[b.tailPos:], p)
		p = p[c:]
		b.tailPos = (b.tailPos + c) % b.tailMax
	}
	return n, nil
}

// Total returns the number of bytes written, including dropped ones.
func (b *CappedBuffer) Total() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total
}

// Dropped returns the number of bytes discarded between head and tail.
func (b *CappedBuffer) Dropped() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dropped()
}

func (b *CappedBuffer) dropped() int64 {
	return b.total - int64(len(b.head)) - int64(len(b.tail))
}

// Truncated reports whether any output was dropped.
func (b *CappedBuffer) Truncated() bool { return b.Dropped() > 0 }

// Bytes returns the retained output. When bytes were dropped, a marker line
// naming the count separates head and tail, and both cut edges are trimmed
// to UTF-8 rune boundaries.
func (b *CappedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	tail := b.orderedTail()
	d := b.dropped()
	if d == 0 {
		out := make([]byte, 0, len(b.head)+len(tail))
		return append(append(out, b.head...), tail...)
	}
	head := trimInvalidEnd(b.head)
	tail = trimInvalidStart(tail)
	marker := fmt.Sprintf("\n\n[... output truncated: %d bytes omitted ...]\n\n", d)
	out := make([]byte, 0, len(head)+len(marker)+len(tail))
	out = append(out, head...)
	out = append(out, marker...)
	return append(out, tail...)
}

// String is Bytes as a string.
func (b *CappedBuffer) String() string { return string(b.Bytes()) }

func (b *CappedBuffer) orderedTail() []byte {
	if len(b.tail) < b.tailMax || b.tailPos == 0 {
		return append([]byte(nil), b.tail...)
	}
	out := make([]byte, 0, len(b.tail))
	out = append(out, b.tail[b.tailPos:]...)
	return append(out, b.tail[:b.tailPos]...)
}

// trimInvalidEnd drops a partial rune at the end of p (at most 3 bytes).
func trimInvalidEnd(p []byte) []byte {
	for i := 0; i < utf8.UTFMax && len(p) > 0; i++ {
		r, size := utf8.DecodeLastRune(p)
		if r != utf8.RuneError || size != 1 {
			return p
		}
		p = p[:len(p)-1]
	}
	return p
}

// trimInvalidStart drops UTF-8 continuation bytes at the start of p.
func trimInvalidStart(p []byte) []byte {
	for i := 0; i < utf8.UTFMax && len(p) > 0 && !utf8.RuneStart(p[0]); i++ {
		p = p[1:]
	}
	return p
}
