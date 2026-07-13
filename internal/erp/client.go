package erp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"sawt-go/internal/trace"
)

type Identity struct {
	UID             string   `json:"uid"`
	Phone           string   `json:"phone"`
	Role            string   `json:"role"`
	DisplayName     string   `json:"displayName"`
	OrgIDs          []string `json:"orgIds"`
	PhoneUnverified bool     `json:"phoneUnverified"`
}

type cachedIdentity struct {
	identity  *Identity
	createdAt time.Time
}

type Client struct {
	baseURL string
	secret  string
	http    *http.Client

	// In-memory cache for identity resolution (D-8)
	cacheMu    sync.RWMutex
	cacheTTL   time.Duration
	phoneCache map[string]cachedIdentity
}

func NewClient(baseURL, secret string) *Client {
	return &Client{
		baseURL:    strings.TrimSuffix(baseURL, "/"),
		secret:     secret,
		http:       &http.Client{Timeout: 15 * time.Second},
		cacheTTL:   5 * time.Minute,
		phoneCache: make(map[string]cachedIdentity),
	}
}

func computeSignature(secret, timestamp, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "." + body))
	return hex.EncodeToString(mac.Sum(nil))
}

func (c *Client) getSignedHeaders(body string) (http.Header, error) {
	if c.secret == "" {
		return nil, fmt.Errorf("ERP secret is not configured")
	}

	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	sig := computeSignature(c.secret, timestamp, body)

	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("x-swa-timestamp", timestamp)
	headers.Set("x-swa-signature", sig)
	return headers, nil
}

// retryAttempts bounds how many times a signed POST is tried before giving up.
const retryAttempts = 3

// backoffDelay returns a jittered exponential backoff for the Nth retry
// (attempt=1 is the first retry): ~200ms, ~400ms, ~800ms, each with up to 50%
// added jitter, capped at 3s. Jitter avoids retry storms when the ERP recovers.
func backoffDelay(attempt int) time.Duration {
	base := 200 * time.Millisecond * time.Duration(1<<(attempt-1))
	if base > 3*time.Second {
		base = 3 * time.Second
	}
	return base + time.Duration(rand.Int63n(int64(base/2)+1))
}

// idempotencyKey derives a deterministic key from the exact request body, so
// every retry of one logical request — and any redelivery carrying identical
// content — presents the same key for the gateway to dedup on. This is what
// makes retrying a mutating tool call safe.
func idempotencyKey(bodyBytes []byte) string {
	sum := sha256.Sum256(bodyBytes)
	return hex.EncodeToString(sum[:16])
}

// doSignedPOST performs a signed POST with bounded, jittered backoff. It
// re-signs each attempt with a fresh timestamp, propagates the trace id and a
// content-derived idempotency key, and retries only on transport errors and
// 429/5xx. Because the body (and therefore the idempotency key) is identical on
// every attempt, the gateway can dedup, so this is safe for reads and for
// writes that the gateway treats idempotently by key.
func (c *Client) doSignedPOST(ctx context.Context, url string, bodyBytes []byte, timeout time.Duration) (int, []byte, error) {
	idemKey := idempotencyKey(bodyBytes)

	var lastErr error
	for attempt := 1; attempt <= retryAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-time.After(backoffDelay(attempt - 1)):
			case <-ctx.Done():
				return 0, nil, ctx.Err()
			}
		}

		headers, err := c.getSignedHeaders(string(bodyBytes))
		if err != nil {
			return 0, nil, err // misconfiguration — never retry
		}
		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
		if err != nil {
			return 0, nil, err
		}
		req.Header = headers
		req.Header.Set("x-swa-idempotency-key", idemKey)
		if tid := trace.ID(ctx); tid != "" {
			req.Header.Set("x-swa-trace-id", tid)
		}

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue // transport error — retry
		}
		respBytes, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("failed to read response: %w", readErr)
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("ERP returned HTTP %d: %s", resp.StatusCode, string(respBytes))
			continue // transient server-side condition — retry
		}
		return resp.StatusCode, respBytes, nil
	}
	return 0, nil, lastErr
}

// ResolveIdentity resolves a WhatsApp phone number into an ERP Identity.
func (c *Client) ResolveIdentity(ctx context.Context, phone string) (*Identity, error) {
	if c.secret == "" {
		return nil, fmt.Errorf("AGENT_GATEWAY_SECRET not set — identity resolution disabled")
	}

	c.cacheMu.RLock()
	if cached, ok := c.phoneCache[phone]; ok {
		if time.Since(cached.createdAt) < c.cacheTTL {
			c.cacheMu.RUnlock()
			return cached.identity, nil
		}
	}
	c.cacheMu.RUnlock()

	bodyMap := map[string]string{"phone": phone}
	bodyBytes, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal identity request body: %w", err)
	}

	// Identity resolution is an idempotent read, so it is always safe to retry.
	url := fmt.Sprintf("%s/api/agent/v1/identity/resolve", c.baseURL)
	status, respBytes, err := c.doSignedPOST(ctx, url, bodyBytes, 10*time.Second)
	if err != nil {
		c.cacheMu.Lock()
		delete(c.phoneCache, phone)
		c.cacheMu.Unlock()
		return nil, fmt.Errorf("identity resolve failed: %w", err)
	}
	if status != http.StatusOK {
		c.cacheMu.Lock()
		delete(c.phoneCache, phone)
		c.cacheMu.Unlock()
		return nil, fmt.Errorf("ERP returned HTTP %d: %s", status, string(respBytes))
	}

	var responseStruct struct {
		Resolved        bool      `json:"resolved"`
		Identity        *Identity `json:"identity"`
		PhoneUnverified bool      `json:"phoneUnverified"`
	}

	if err := json.Unmarshal(respBytes, &responseStruct); err != nil {
		return nil, fmt.Errorf("failed to decode response JSON: %w", err)
	}

	if !responseStruct.Resolved {
		c.cacheMu.Lock()
		c.phoneCache[phone] = cachedIdentity{
			identity:  nil,
			createdAt: time.Now(),
		}
		c.cacheMu.Unlock()
		return nil, nil // Unlinked
	}

	if responseStruct.Identity == nil {
		c.cacheMu.Lock()
		c.phoneCache[phone] = cachedIdentity{
			identity:  nil,
			createdAt: time.Now(),
		}
		c.cacheMu.Unlock()
		return nil, nil
	}

	responseStruct.Identity.PhoneUnverified = responseStruct.PhoneUnverified

	c.cacheMu.Lock()
	c.phoneCache[phone] = cachedIdentity{
		identity:  responseStruct.Identity,
		createdAt: time.Now(),
	}
	c.cacheMu.Unlock()

	return responseStruct.Identity, nil
}

// CallTool executes a single tool against the ERP Gateway.
func (c *Client) CallTool(ctx context.Context, toolID, orgID, actingUserUID string, args map[string]interface{}) (map[string]interface{}, error) {
	if c.secret == "" {
		return map[string]interface{}{
			"ok":    false,
			"error": "AGENT_GATEWAY_SECRET not configured",
			"code":  "UNCONFIGURED",
		}, nil
	}

	bodyMap := map[string]interface{}{
		"orgId":         orgID,
		"actingUserUid": actingUserUID,
		"args":          args,
	}

	bodyBytes, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal tool request body: %w", err)
	}

	url := fmt.Sprintf("%s/api/agent/v1/tools/%s", c.baseURL, toolID)
	_, respBytes, err := c.doSignedPOST(ctx, url, bodyBytes, 15*time.Second)
	if err != nil {
		// Preserve the loop-friendly contract: a transport failure or exhausted
		// 5xx retries surfaces as a tool result the LLM/confirmation flow can
		// reason about, not a Go error that aborts the whole turn.
		return map[string]interface{}{
			"ok":    false,
			"error": err.Error(),
			"code":  "NETWORK_ERROR",
		}, nil
	}

	var result map[string]interface{}
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, fmt.Errorf("failed to decode tool response JSON: %w, response: %s", err, string(respBytes))
	}

	return result, nil
}
