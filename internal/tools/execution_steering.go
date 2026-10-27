package tools

import "context"

// SteeringSource hands the tool loop human messages that arrived for the
// session while its turn was running (conduit-5uw3). The loop calls it once
// per round boundary, after the round's tool results; every message it
// returns is appended to the turn's history as a user message, before the
// next provider call. It must return each message at most once.
type SteeringSource func(ctx context.Context) []string

type steeringSourceKey struct{}

// WithSteeringSource attaches src to ctx. A nil src detaches any source an
// outer context carried, so a nested turn (sub-agent) never drains its
// parent's messages.
func WithSteeringSource(ctx context.Context, src SteeringSource) context.Context {
	return context.WithValue(ctx, steeringSourceKey{}, src)
}

func steeringSourceFrom(ctx context.Context) SteeringSource {
	src, _ := ctx.Value(steeringSourceKey{}).(SteeringSource)
	return src
}

// takeSteering returns the messages to fold into the next round, nil when
// the turn has no source or was cancelled.
func takeSteering(ctx context.Context) []string {
	src := steeringSourceFrom(ctx)
	if src == nil || ctx.Err() != nil {
		return nil
	}
	return src(ctx)
}
