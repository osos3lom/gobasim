package agentcfg

import "testing"

func TestParseLLMDefaultsAndValidation(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
		check   func(t *testing.T, c LLM)
	}{
		{
			name: "empty yields openai default vendor",
			raw:  ``,
			check: func(t *testing.T, c LLM) {
				if c.Vendor != "openai" {
					t.Fatalf("vendor = %q, want openai", c.Vendor)
				}
			},
		},
		{
			name: "valid nim config passes",
			raw:  `{"vendor":"nim","url":"https://integrate.api.nvidia.com/v1","api_key_env":"NIM_API_KEY","model":"meta/llama-3.1-70b-instruct"}`,
			check: func(t *testing.T, c LLM) {
				if c.Vendor != "nim" || c.APIKeyEnv != "NIM_API_KEY" {
					t.Fatalf("unexpected parse: %+v", c)
				}
			},
		},
		{name: "unknown vendor rejected", raw: `{"vendor":"anthropic"}`, wantErr: true},
		{name: "raw secret in api_key_env rejected", raw: `{"api_key_env":"sk-abc123def"}`, wantErr: true},
		{name: "bad url scheme rejected", raw: `{"url":"ftp://x/y"}`, wantErr: true},
		{
			name: "local gateway url allowed for llm",
			raw:  `{"url":"http://localhost:1234/v1"}`,
			check: func(t *testing.T, c LLM) {
				if c.URL == "" {
					t.Fatal("expected url preserved")
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := ParseLLM([]byte(tc.raw))
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.check != nil {
				tc.check(t, c)
			}
		})
	}
}

func TestParseTTSDefaults(t *testing.T) {
	c, err := ParseTTS([]byte(``))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.LanguageCode != "ar-XA" || c.Gender != "FEMALE" || c.Speed != 1.0 {
		t.Fatalf("defaults not applied: %+v", c)
	}
	if _, err := ParseTTS([]byte(`{"gender":"ROBOT"}`)); err == nil {
		t.Fatal("expected error for unknown gender")
	}
	clamped, _ := ParseTTS([]byte(`{"speed":99}`))
	if clamped.Speed != 4.0 {
		t.Fatalf("speed = %v, want clamp to 4.0", clamped.Speed)
	}
}

func TestClampHistory(t *testing.T) {
	cases := map[int]int{0: DefaultHistory, -3: DefaultHistory, 1: 1, 8: 8, 20: 20, 21: 20, 999: 20}
	for in, want := range cases {
		if got := ClampHistory(in); got != want {
			t.Errorf("ClampHistory(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestParseSkillsPathTraversalGuard(t *testing.T) {
	skills, err := ParseSkills([]byte(`[{"name":"Accounting","path":"internal/workflow/skills/accounting.md","enabled":true}]`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if skills[0].Path != "accounting.md" {
		t.Fatalf("path = %q, want normalized to accounting.md", skills[0].Path)
	}
	if _, err := ParseSkills([]byte(`[{"name":"evil","path":"../../etc/passwd"}]`)); err == nil {
		t.Fatal("expected error for non-.md traversal path")
	}
	if _, err := ParseSkills([]byte(`[{"name":"evil","path":"../secrets.md"}]`)); err != nil {
		t.Fatalf("traversal to a .md should normalize to base, got err: %v", err)
	}
}

func TestParseMCPServersSSRFGuard(t *testing.T) {
	if _, err := ParseMCPServers([]byte(`[{"name":"a","url":"http://127.0.0.1:9/rpc"}]`), false); err == nil {
		t.Fatal("expected SSRF rejection of loopback when allowPrivate=false")
	}
	if _, err := ParseMCPServers([]byte(`[{"name":"a","url":"http://127.0.0.1:9/rpc"}]`), true); err != nil {
		t.Fatalf("loopback should be allowed when allowPrivate=true: %v", err)
	}
	if _, err := ParseMCPServers([]byte(`[{"name":"a","url":"https://mcp.example.com/rpc"},{"name":"a","url":"https://x.example.com"}]`), false); err == nil {
		t.Fatal("expected duplicate-name rejection")
	}
	ok, err := ParseMCPServers([]byte(`[{"name":"erp","url":"https://mcp.example.com/rpc","enabled":true}]`), false)
	if err != nil || len(ok) != 1 {
		t.Fatalf("valid public server rejected: %v", err)
	}
}

func TestParseSubAgents(t *testing.T) {
	c, err := ParseSubAgents([]byte(`{"enabled":true,"max_tokens":99999,"allowed_agents":["accounting","accounting","sales"]}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.MaxTokens != MaxSubAgentTokens {
		t.Fatalf("max_tokens = %d, want clamp to %d", c.MaxTokens, MaxSubAgentTokens)
	}
	if len(c.AllowedAgents) != 2 {
		t.Fatalf("dedup failed: %+v", c.AllowedAgents)
	}
	if !c.Allows("accounting") || c.Allows("breeding") {
		t.Fatalf("Allows gate wrong: %+v", c)
	}
	if _, err := ParseSubAgents([]byte(`{"allowed_agents":["hacker"]}`)); err == nil {
		t.Fatal("expected rejection of unknown delegate agent")
	}
}
