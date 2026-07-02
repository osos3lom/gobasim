package erp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
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
