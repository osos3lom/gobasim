package web

import (
	"strings"
	"testing"
	"time"

	"sawt-go/config"
)

func newTestAuthManager() *AuthManager {
	return NewAuthManager(&config.Config{SessionSecret: "test-secret"}, nil)
}

func TestCookieRoundTrip(t *testing.T) {
	a := newTestAuthManager()

	value := a.GenerateCookieValue("admin", time.Hour)
	username, err := a.VerifyCookieValue(value)
	if err != nil {
		t.Fatalf("expected valid cookie to verify, got error: %v", err)
	}
	if username != "admin" {
		t.Fatalf("expected username 'admin', got %q", username)
	}
}

func TestCookieUsernameWithColonRoundTrip(t *testing.T) {
	a := newTestAuthManager()

	value := a.GenerateCookieValue("user:with:colons", time.Hour)
	username, err := a.VerifyCookieValue(value)
	if err != nil {
		t.Fatalf("expected valid cookie to verify, got error: %v", err)
	}
	if username != "user:with:colons" {
		t.Fatalf("expected username preserved, got %q", username)
	}
}

func TestTamperedCookieRejected(t *testing.T) {
	a := newTestAuthManager()

	value := a.GenerateCookieValue("admin", time.Hour)
	tampered := strings.Replace(value, "admin", "hacker", 1)

	if _, err := a.VerifyCookieValue(tampered); err == nil {
		t.Fatal("expected tampered cookie to be rejected")
	}
}

func TestTamperedSignatureRejected(t *testing.T) {
	a := newTestAuthManager()

	value := a.GenerateCookieValue("admin", time.Hour)
	// Flip the last hex char of the signature.
	last := value[len(value)-1]
	var flipped byte = '0'
	if last == '0' {
		flipped = '1'
	}
	tampered := value[:len(value)-1] + string(flipped)

	if _, err := a.VerifyCookieValue(tampered); err == nil {
		t.Fatal("expected cookie with modified signature to be rejected")
	}
}

func TestExpiredCookieRejected(t *testing.T) {
	a := newTestAuthManager()

	value := a.GenerateCookieValue("admin", -time.Minute)
	if _, err := a.VerifyCookieValue(value); err == nil {
		t.Fatal("expected expired cookie to be rejected")
	}
}

func TestMalformedCookieRejected(t *testing.T) {
	a := newTestAuthManager()

	for _, input := range []string{"", "no-colons-here", "one:colon", "a:b:c:d-but-bad-sig"} {
		if _, err := a.VerifyCookieValue(input); err == nil {
			t.Fatalf("expected malformed cookie %q to be rejected", input)
		}
	}
}

func TestDifferentSecretRejected(t *testing.T) {
	a := newTestAuthManager()
	b := NewAuthManager(&config.Config{SessionSecret: "other-secret"}, nil)

	value := a.GenerateCookieValue("admin", time.Hour)
	if _, err := b.VerifyCookieValue(value); err == nil {
		t.Fatal("expected cookie signed with a different secret to be rejected")
	}
}
