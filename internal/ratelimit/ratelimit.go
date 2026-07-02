package ratelimit

import (
	"sync"
	"time"
)

// Limiter is a simple in-memory sliding-window rate limiter keyed by string.
// It is suitable for a single-instance deployment only.
type Limiter struct {
	mu     sync.Mutex
	window time.Duration
	limit  int
	events map[string][]time.Time
}

func New(limit int, window time.Duration) *Limiter {
	return &Limiter{
		window: window,
		limit:  limit,
		events: make(map[string][]time.Time),
	}
}

// Allow records an event for key and reports whether it is within the limit,
// along with the number of events (including this one) inside the window.
func (l *Limiter) Allow(key string) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-l.window)

	kept := l.events[key][:0]
	for _, t := range l.events[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	kept = append(kept, now)
	l.events[key] = kept

	// Opportunistically drop stale keys so the map does not grow unbounded.
	if len(l.events) > 1024 {
		for k, ts := range l.events {
			if len(ts) == 0 || !ts[len(ts)-1].After(cutoff) {
				delete(l.events, k)
			}
		}
	}

	return len(kept) <= l.limit, len(kept)
}
