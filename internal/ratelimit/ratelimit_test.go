package ratelimit

import (
	"testing"
	"time"
)

func TestAllowWithinLimit(t *testing.T) {
	l := New(3, time.Minute)

	for i := 1; i <= 3; i++ {
		allowed, count := l.Allow("key")
		if !allowed {
			t.Fatalf("event %d should be allowed", i)
		}
		if count != i {
			t.Fatalf("expected count %d, got %d", i, count)
		}
	}

	allowed, count := l.Allow("key")
	if allowed {
		t.Fatal("4th event within the window should be rejected")
	}
	if count != 4 {
		t.Fatalf("expected count 4, got %d", count)
	}
}

func TestKeysAreIndependent(t *testing.T) {
	l := New(1, time.Minute)

	if allowed, _ := l.Allow("a"); !allowed {
		t.Fatal("first event for 'a' should be allowed")
	}
	if allowed, _ := l.Allow("b"); !allowed {
		t.Fatal("first event for 'b' should be allowed despite 'a' being at its limit")
	}
	if allowed, _ := l.Allow("a"); allowed {
		t.Fatal("second event for 'a' should be rejected")
	}
}

func TestWindowExpiry(t *testing.T) {
	l := New(1, 10*time.Millisecond)

	if allowed, _ := l.Allow("key"); !allowed {
		t.Fatal("first event should be allowed")
	}
	if allowed, _ := l.Allow("key"); allowed {
		t.Fatal("second immediate event should be rejected")
	}

	time.Sleep(15 * time.Millisecond)

	if allowed, _ := l.Allow("key"); !allowed {
		t.Fatal("event after the window expired should be allowed again")
	}
}
