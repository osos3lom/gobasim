package trace

import (
	"context"
	"log"
)

type ctxKey struct{}

// With returns a context carrying the given trace id (one per inbound message).
func With(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// ID returns the trace id carried by ctx, or "" when none is set.
func ID(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKey{}).(string); ok {
		return v
	}
	return ""
}

// Logf logs with a [trace=...] prefix so every line for one message's
// processing is grep-able by its trace id.
func Logf(ctx context.Context, format string, args ...interface{}) {
	if id := ID(ctx); id != "" {
		log.Printf("[trace="+id+"] "+format, args...)
		return
	}
	log.Printf(format, args...)
}
