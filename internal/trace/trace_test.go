package trace

import (
	"context"
	"testing"
)

func TestWithAndID(t *testing.T) {
	ctx := context.Background()
	if got := ID(ctx); got != "" {
		t.Errorf("expected empty ID on bare context, got %q", got)
	}

	ctx = With(ctx, "trace-123")
	if got := ID(ctx); got != "trace-123" {
		t.Errorf("expected ID 'trace-123', got %q", got)
	}
}

func TestLogf(t *testing.T) {
	// Logf writes to the standard logger; just confirm it doesn't panic
	// with and without a trace id set.
	Logf(context.Background(), "plain message %d", 1)
	Logf(With(context.Background(), "abc"), "traced message %d", 2)
}
