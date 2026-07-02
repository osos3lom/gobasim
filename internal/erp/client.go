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
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Identity struct {
	UID         string   `json:"uid"`
	Phone       string   `json:"phone"`
	Role        string   `json:"role"`
	DisplayName string   `json:"displayName"`
	OrgIDs      []string `json:"orgIds"`
}

type Client struct {
	baseURL string
	secret  string
}

func NewClient(baseURL, secret string) *Client {
	return &Client{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		secret:  secret,
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

// ResolveIdentity resolves a WhatsApp phone number into an ERP Identity.
func (c *Client) ResolveIdentity(ctx context.Context, phone string) (*Identity, error) {
	if c.secret == "" {
		return nil, fmt.Errorf("AGENT_GATEWAY_SECRET not set — identity resolution disabled")
	}

	bodyMap := map[string]string{"phone": phone}
	bodyBytes, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal identity request body: %w", err)
	}

	headers, err := c.getSignedHeaders(string(bodyBytes))
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/api/agent/v1/identity/resolve", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create http request: %w", err)
	}
	req.Header = headers

	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP post failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ERP returned HTTP %d: %s", resp.StatusCode, string(respBytes))
	}

	var responseStruct struct {
		Resolved bool      `json:"resolved"`
		Identity *Identity `json:"identity"`
	}

	if err := json.Unmarshal(respBytes, &responseStruct); err != nil {
		return nil, fmt.Errorf("failed to decode response JSON: %w", err)
	}

	if !responseStruct.Resolved || responseStruct.Identity == nil {
		return nil, nil // Unlinked
	}

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

	headers, err := c.getSignedHeaders(string(bodyBytes))
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/api/agent/v1/tools/%s", c.baseURL, toolID)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create http request: %w", err)
	}
	req.Header = headers

	httpClient := &http.Client{Timeout: 15 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return map[string]interface{}{
			"ok":    false,
			"error": err.Error(),
			"code":  "NETWORK_ERROR",
		}, nil
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read tool response: %w", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, fmt.Errorf("failed to decode tool response JSON: %w, response: %s", err, string(respBytes))
	}

	return result, nil
}
