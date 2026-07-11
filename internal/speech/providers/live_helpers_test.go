package providers

import (
	"os"
	"testing"
)

// skipUnlessLive skips the calling test unless RUN_LIVE_AI_TESTS=1 is set,
// so live-network tests never run as part of the default `go test ./...`.
func skipUnlessLive(t *testing.T) {
	t.Helper()
	if os.Getenv("RUN_LIVE_AI_TESTS") != "1" {
		t.Skip("skipping live test: RUN_LIVE_AI_TESTS=1 not set")
	}
}

// requireEnv skips the calling test if the given environment variable is
// unset or empty, returning its value otherwise. Never logs the value.
func requireEnv(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Skipf("skipping live test: %s not set", key)
	}
	return v
}
