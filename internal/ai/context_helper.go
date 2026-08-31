package ai

import (
	"context"
	"errors"
)

// isCallerContextError reports whether err is due to the caller's context
// being cancelled or deadline-exceeded (as opposed to a transport-level
// timeout like the http.Client's own Timeout).
//
// Used by provider retry loops (bd-13p): a caller that gave up must not be
// retried — nobody wants the answer. But transport errors (connection reset,
// upstream hang, client timeout) are transient and SHOULD be retried.
func isCallerContextError(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	// DeadlineExceeded: only treat as caller-caused when the caller's ctx
	// is the one that expired. A provider http.Client timeout also wraps
	// as DeadlineExceeded but leaves ctx.Err() nil here.
	return errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil
}
