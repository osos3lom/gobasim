package erp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestComputeSignatureMatchesContract(t *testing.T) {
	// The mshalia contract: HMAC-SHA256(secret, "{timestamp}.{rawBody}") hex.
	secret := "test-secret"
	timestamp := "1700000000000"
	body := `{"phone":"9665xxxxxxx"}`

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "." + body))
	expected := hex.EncodeToString(mac.Sum(nil))

	got := computeSignature(secret, timestamp, body)
	if got != expected {
		t.Fatalf("signature does not follow '{timestamp}.{body}' contract:\ngot  %s\nwant %s", got, expected)
	}
}

func TestComputeSignatureVariesWithInputs(t *testing.T) {
	base := computeSignature("secret", "123", "body")

	if computeSignature("other", "123", "body") == base {
		t.Fatal("changing the secret must change the signature")
	}
	if computeSignature("secret", "124", "body") == base {
		t.Fatal("changing the timestamp must change the signature")
	}
	if computeSignature("secret", "123", "body2") == base {
		t.Fatal("changing the body must change the signature")
	}
}

func TestGetSignedHeadersRequiresSecret(t *testing.T) {
	c := NewClient("http://localhost:3001", "")
	if _, err := c.getSignedHeaders("{}"); err == nil {
		t.Fatal("expected error when ERP secret is not configured")
	}
}

func TestGetSignedHeadersSetsContract(t *testing.T) {
	c := NewClient("http://localhost:3001", "s3cret")
	headers, err := c.getSignedHeaders(`{"a":1}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if headers.Get("x-swa-timestamp") == "" {
		t.Fatal("x-swa-timestamp header missing")
	}
	sig := headers.Get("x-swa-signature")
	if len(sig) != 64 {
		t.Fatalf("expected 64-char hex signature, got %q (len %d)", sig, len(sig))
	}
}

func TestCallToolWithoutSecretReturnsUnconfigured(t *testing.T) {
	c := NewClient("http://localhost:3001", "")
	res, err := c.CallTool(t.Context(), "get_horse", "org1", "uid1", map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res["code"] != "UNCONFIGURED" {
		t.Fatalf("expected UNCONFIGURED result, got %v", res)
	}
}

func TestResolveIdentityCaching(t *testing.T) {
	var callCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if r.URL.Path != "/api/agent/v1/identity/resolve" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"resolved": true,
			"identity": {
				"uid": "user123",
				"phone": "966500000000",
				"role": "manager",
				"displayName": "Test User",
				"orgIds": ["org-demo"]
			}
		}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-secret")

	// First call - should hit the server
	id1, err := client.ResolveIdentity(t.Context(), "966500000000")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id1 == nil || id1.UID != "user123" {
		t.Fatalf("unexpected identity: %+v", id1)
	}
	if callCount != 1 {
		t.Fatalf("expected server to be called once, got %d", callCount)
	}

	// Second call - should hit the cache (callCount remains 1)
	id2, err := client.ResolveIdentity(t.Context(), "966500000000")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id2 == nil || id2.UID != "user123" {
		t.Fatalf("unexpected identity: %+v", id2)
	}
	if callCount != 1 {
		t.Fatalf("expected server call count to remain 1 (hit cache), but got %d", callCount)
	}

	// Third call with expired TTL - should hit the server again
	client.cacheTTL = 1 * time.Millisecond
	time.Sleep(2 * time.Millisecond)
	id3, err := client.ResolveIdentity(t.Context(), "966500000000")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id3 == nil || id3.UID != "user123" {
		t.Fatalf("unexpected identity: %+v", id3)
	}
	if callCount != 2 {
		t.Fatalf("expected server to be called twice after TTL expiration, got %d", callCount)
	}
}
