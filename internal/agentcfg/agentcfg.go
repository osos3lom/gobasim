// Package agentcfg is the single source of truth for the shapes stored in the
// agents table's JSONB columns (llm, tts, asr, mcp_servers, skills, sub_agents).
//
// Both the web control panel (validate + default on save) and the workflow
// engine (parse + consume at runtime) depend on these types, so the parsing,
// validation, and clamping rules live in exactly one place. Every Parse* helper
// unmarshals, normalizes in place, applies safe defaults, and rejects invalid
// input — callers can trust the returned value without re-checking it.
package agentcfg

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// Clamp and default constants shared across the web and engine layers. The
// history bounds mirror the engine's memory subsystem (default 8, hard cap 20),
// so the dashboard and the runtime never disagree on the envelope.
const (
	MinHistory     = 1
	MaxHistory     = 20
	DefaultHistory = 8

	MaxSubAgentTokens = 4000

	// SkillsRoot is the embed-relative directory that skill .md files live under
	// inside the workflow package. Skill paths are normalized to a bare filename
	// resolved against this root, defeating path traversal.
	SkillsRoot = "skills"
)

var (
	llmVendors = map[string]bool{"nim": true, "openai": true, "groq": true}
	ttsVendors = map[string]bool{"google": true, "huggingface": true, "local": true}
	genders    = map[string]bool{"MALE": true, "FEMALE": true, "NEUTRAL": true}

	// knownAgents is the set of intent specs a sub-agent delegation may target.
	// It mirrors the agentSpec names in internal/workflow/tools.go.
	knownAgents = map[string]bool{
		"operations": true, "accounting": true, "administration": true,
		"sales": true, "breeding": true, "client": true,
	}

	// envNameRe matches a conventional environment-variable NAME. It deliberately
	// rejects raw secrets (which carry lowercase letters, hyphens, or "sk-"
	// prefixes) so an API key can never be persisted where only its lookup name
	// belongs.
	envNameRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
)

// ---------------------------------------------------------------------------
// LLM (agents.llm)
// ---------------------------------------------------------------------------

type LLM struct {
	Vendor    string `json:"vendor"`      // "nim" | "openai" | "groq"
	URL       string `json:"url"`         // OpenAI-compatible base URL
	APIKeyEnv string `json:"api_key_env"` // env var NAME, never the secret
	Model     string `json:"model"`
}

// DefaultLLM is the seed config for a freshly created agent.
func DefaultLLM() LLM {
	return LLM{Vendor: "openai", URL: "https://api.openai.com/v1", APIKeyEnv: "OPENAI_API_KEY", Model: "gpt-4o-mini"}
}

func ParseLLM(raw []byte) (LLM, error) {
	var c LLM
	if err := unmarshalIfPresent(raw, &c); err != nil {
		return LLM{}, fmt.Errorf("llm: %w", err)
	}
	if err := c.Validate(); err != nil {
		return LLM{}, err
	}
	return c, nil
}

func (c *LLM) Validate() error {
	c.Vendor = strings.ToLower(strings.TrimSpace(c.Vendor))
	if c.Vendor == "" {
		c.Vendor = "openai"
	}
	if !llmVendors[c.Vendor] {
		return fmt.Errorf("agentcfg: unknown llm vendor %q", c.Vendor)
	}
	c.URL = strings.TrimSpace(c.URL)
	if c.URL != "" {
		// LLM base URLs are operator-entered and may legitimately be a local
		// gateway/proxy, so private hosts are allowed; only the scheme is enforced.
		if err := ValidateURL(c.URL, true); err != nil {
			return fmt.Errorf("agentcfg: llm url: %w", err)
		}
	}
	c.APIKeyEnv = strings.TrimSpace(c.APIKeyEnv)
	if c.APIKeyEnv != "" && !envNameRe.MatchString(c.APIKeyEnv) {
		return fmt.Errorf("agentcfg: api_key_env %q is not a valid environment variable name — store the NAME, not the secret", c.APIKeyEnv)
	}
	if len(c.APIKeyEnv) > 64 {
		return fmt.Errorf("agentcfg: api_key_env is too long")
	}
	c.Model = strings.TrimSpace(c.Model)
	return nil
}

func (c LLM) Marshal() []byte { return mustMarshal(c) }

// ---------------------------------------------------------------------------
// TTS (agents.tts)
// ---------------------------------------------------------------------------

type TTS struct {
	Vendor       string  `json:"vendor"`        // "google" | "huggingface" | "local"
	LanguageCode string  `json:"language_code"` // e.g. "ar-XA"
	VoiceName    string  `json:"voice_name"`    // e.g. "ar-XA-Wavenet-B"
	Gender       string  `json:"gender"`        // "MALE" | "FEMALE" | "NEUTRAL"
	Model        string  `json:"model"`         // neural/Wavenet variant
	Speed        float32 `json:"speed"`
}

func DefaultTTS() TTS {
	return TTS{Vendor: "google", LanguageCode: "ar-XA", VoiceName: "ar-XA-Wavenet-B", Gender: "FEMALE", Model: "Wavenet", Speed: 1.0}
}

func ParseTTS(raw []byte) (TTS, error) {
	var c TTS
	if err := unmarshalIfPresent(raw, &c); err != nil {
		return TTS{}, fmt.Errorf("tts: %w", err)
	}
	if err := c.Validate(); err != nil {
		return TTS{}, err
	}
	return c, nil
}

func (c *TTS) Validate() error {
	c.Vendor = strings.ToLower(strings.TrimSpace(c.Vendor))
	if c.Vendor == "" {
		c.Vendor = "google"
	}
	if !ttsVendors[c.Vendor] {
		return fmt.Errorf("agentcfg: unknown tts vendor %q", c.Vendor)
	}
	c.LanguageCode = strings.TrimSpace(c.LanguageCode)
	if c.LanguageCode == "" {
		c.LanguageCode = "ar-XA"
	}
	c.Gender = strings.ToUpper(strings.TrimSpace(c.Gender))
	if c.Gender == "" {
		c.Gender = "FEMALE"
	}
	if !genders[c.Gender] {
		return fmt.Errorf("agentcfg: unknown tts gender %q", c.Gender)
	}
	c.VoiceName = strings.TrimSpace(c.VoiceName)
	c.Model = strings.TrimSpace(c.Model)
	c.Speed = clampSpeed(c.Speed)
	return nil
}

func (c TTS) Marshal() []byte { return mustMarshal(c) }

// voiceCtxKey carries a resolved per-request TTS voice so the speech
// orchestrator and its providers can honor an agent's language/voice/gender/
// model/speed without every Synthesize call site changing its signature (the
// same context-threading pattern the workflow engine uses for LLM providers).
type voiceCtxKey struct{}

// WithVoice returns a context carrying the agent's TTS voice config.
func WithVoice(ctx context.Context, v TTS) context.Context {
	return context.WithValue(ctx, voiceCtxKey{}, v)
}

// VoiceFromContext returns the TTS voice bound to the context, if any.
func VoiceFromContext(ctx context.Context) (TTS, bool) {
	v, ok := ctx.Value(voiceCtxKey{}).(TTS)
	return v, ok
}

func clampSpeed(s float32) float32 {
	if s <= 0 {
		return 1.0
	}
	if s < 0.25 {
		return 0.25
	}
	if s > 4.0 {
		return 4.0
	}
	return s
}

// ---------------------------------------------------------------------------
// ASR (agents.asr)
// ---------------------------------------------------------------------------

type ASR struct {
	Vendor   string `json:"vendor"`
	Model    string `json:"model"`
	Language string `json:"language"`
}

func DefaultASR() ASR {
	return ASR{Vendor: "groq", Model: "whisper-large-v3", Language: "ar"}
}

func ParseASR(raw []byte) (ASR, error) {
	var c ASR
	if err := unmarshalIfPresent(raw, &c); err != nil {
		return ASR{}, fmt.Errorf("asr: %w", err)
	}
	c.Vendor = strings.ToLower(strings.TrimSpace(c.Vendor))
	c.Model = strings.TrimSpace(c.Model)
	c.Language = strings.TrimSpace(c.Language)
	return c, nil
}

func (c ASR) Marshal() []byte { return mustMarshal(c) }

// ---------------------------------------------------------------------------
// MCP servers (agents.mcp_servers) — JSON array
// ---------------------------------------------------------------------------

type MCPServer struct {
	Name    string `json:"name"`
	URL     string `json:"url"` // JSON-RPC endpoint
	Enabled bool   `json:"enabled"`
}

// ParseMCPServers validates a JSON array of MCP server registrations. allowPrivate
// governs whether endpoints on loopback/private ranges are permitted (an SSRF
// guard for the operator-facing registration form; callers pass the deployment's
// policy).
func ParseMCPServers(raw []byte, allowPrivate bool) ([]MCPServer, error) {
	var servers []MCPServer
	if err := unmarshalIfPresent(raw, &servers); err != nil {
		return nil, fmt.Errorf("mcp_servers: %w", err)
	}
	seen := make(map[string]bool, len(servers))
	for i := range servers {
		if err := servers[i].validate(allowPrivate); err != nil {
			return nil, err
		}
		if seen[servers[i].Name] {
			return nil, fmt.Errorf("agentcfg: duplicate mcp server name %q", servers[i].Name)
		}
		seen[servers[i].Name] = true
	}
	if servers == nil {
		servers = []MCPServer{}
	}
	return servers, nil
}

func (m *MCPServer) validate(allowPrivate bool) error {
	m.Name = strings.TrimSpace(m.Name)
	m.URL = strings.TrimSpace(m.URL)
	if m.Name == "" {
		return fmt.Errorf("agentcfg: mcp server name is required")
	}
	if m.URL == "" {
		return fmt.Errorf("agentcfg: mcp server %q has no url", m.Name)
	}
	if err := ValidateURL(m.URL, allowPrivate); err != nil {
		return fmt.Errorf("agentcfg: mcp server %q: %w", m.Name, err)
	}
	return nil
}

// MarshalMCPServers serializes a validated server list.
func MarshalMCPServers(servers []MCPServer) []byte {
	if servers == nil {
		servers = []MCPServer{}
	}
	return mustMarshal(servers)
}

// ---------------------------------------------------------------------------
// Skills (agents.skills) — JSON array
// ---------------------------------------------------------------------------

type Skill struct {
	Name    string `json:"name"`
	Path    string `json:"path"` // normalized to a bare "<file>.md" under SkillsRoot
	Enabled bool   `json:"enabled"`
}

func ParseSkills(raw []byte) ([]Skill, error) {
	var skills []Skill
	if err := unmarshalIfPresent(raw, &skills); err != nil {
		return nil, fmt.Errorf("skills: %w", err)
	}
	for i := range skills {
		if err := skills[i].Validate(); err != nil {
			return nil, err
		}
	}
	if skills == nil {
		skills = []Skill{}
	}
	return skills, nil
}

func (s *Skill) Validate() error {
	s.Name = strings.TrimSpace(s.Name)
	s.Path = strings.TrimSpace(s.Path)
	if s.Path != "" {
		// Collapse any path (including "../" traversals and OS separators) to a
		// bare filename resolved against SkillsRoot; the registry looks this up
		// inside the embedded skills FS.
		base := path.Base(path.Clean("/" + strings.ReplaceAll(s.Path, `\`, "/")))
		if base == "." || base == "/" || base == ".." || !strings.HasSuffix(base, ".md") {
			return fmt.Errorf("agentcfg: skill path %q must reference a .md file", s.Path)
		}
		s.Path = base
	}
	return nil
}

// MarshalSkills serializes a validated skill list.
func MarshalSkills(skills []Skill) []byte {
	if skills == nil {
		skills = []Skill{}
	}
	return mustMarshal(skills)
}

// ---------------------------------------------------------------------------
// Sub-agent delegation (agents.sub_agents) — JSON object
// ---------------------------------------------------------------------------

type SubAgents struct {
	Enabled       bool     `json:"enabled"`
	MaxTokens     int      `json:"max_tokens"`
	AllowedAgents []string `json:"allowed_agents"`
}

func DefaultSubAgents() SubAgents {
	return SubAgents{Enabled: false, MaxTokens: 0, AllowedAgents: []string{}}
}

func ParseSubAgents(raw []byte) (SubAgents, error) {
	var c SubAgents
	if err := unmarshalIfPresent(raw, &c); err != nil {
		return SubAgents{}, fmt.Errorf("sub_agents: %w", err)
	}
	if err := c.Validate(); err != nil {
		return SubAgents{}, err
	}
	return c, nil
}

func (c *SubAgents) Validate() error {
	if c.MaxTokens < 0 {
		c.MaxTokens = 0
	}
	if c.MaxTokens > MaxSubAgentTokens {
		c.MaxTokens = MaxSubAgentTokens
	}
	cleaned := make([]string, 0, len(c.AllowedAgents))
	seen := make(map[string]bool, len(c.AllowedAgents))
	for _, a := range c.AllowedAgents {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" || seen[a] {
			continue
		}
		if !knownAgents[a] {
			return fmt.Errorf("agentcfg: unknown delegate agent %q", a)
		}
		seen[a] = true
		cleaned = append(cleaned, a)
	}
	c.AllowedAgents = cleaned
	return nil
}

// Allows reports whether delegation to the named intent spec is permitted.
func (c SubAgents) Allows(agent string) bool {
	if !c.Enabled {
		return false
	}
	agent = strings.ToLower(strings.TrimSpace(agent))
	for _, a := range c.AllowedAgents {
		if a == agent {
			return true
		}
	}
	return false
}

func (c SubAgents) Marshal() []byte { return mustMarshal(c) }

// ---------------------------------------------------------------------------
// Clarification rules (agents.clarification_rules) — JSON object
//
// Overrides the workflow engine's F-1 fix: before a tool call is parked or
// executed, required args missing from the model's call are either
// auto-derived (e.g. an English horse name transliterated from the Arabic
// one) or asked of the user instead of silently failing. The tool's own
// declared schema is always the source of truth for what's required; this
// config is an override/extension layer on top of it, never a replacement.
// ---------------------------------------------------------------------------

type ClarificationRules struct {
	// Enabled is a pointer so an absent "enabled" key in the JSON (the common
	// case — most agents will never touch this) defaults to true rather than
	// Go's zero value false, without needing a pre-seeded-default trick. Every
	// other config in this file happens to want its zero value as the default;
	// this is the one field that doesn't, so this is the one deliberate
	// departure from the plain zero-value pattern above.
	Enabled       *bool                       `json:"enabled"`
	ToolOverrides []ToolClarificationOverride `json:"tool_overrides"`
	DeriveRules   []DeriveRuleConfig          `json:"derive_rules"`
}

// ToolClarificationOverride disables the "ask if missing" behavior for one
// specific tool id. Mirrors the Skill/MCPServer convention: a plain bool, and
// membership in the list is itself the override signal — a tool absent from
// this list uses the default (ask).
type ToolClarificationOverride struct {
	ToolID       string `json:"tool_id"`
	AskIfMissing bool   `json:"ask_if_missing"`
}

// DeriveRuleConfig lets an operator add or redirect a "derive field X from
// field Y" rule without a Go redeploy. Method is looked up against the
// engine's known derive functions at runtime; an unrecognized Method is
// ignored (the field is treated as not derivable), not an error — so this
// config can never crash the tool loop, only fail to help it.
type DeriveRuleConfig struct {
	ToolID      string `json:"tool_id"`
	Field       string `json:"field"`
	SourceField string `json:"source_field"`
	Method      string `json:"method"`
}

// EffectiveEnabled reports whether the clarification behavior is on for this
// agent: true unless the operator explicitly set "enabled": false.
func (c ClarificationRules) EffectiveEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// DefaultClarificationRules is the seed config for a freshly created agent —
// an empty override layer, which behaves identically to a NULL/absent column.
func DefaultClarificationRules() ClarificationRules {
	return ClarificationRules{ToolOverrides: []ToolClarificationOverride{}, DeriveRules: []DeriveRuleConfig{}}
}

func ParseClarificationRules(raw []byte) (ClarificationRules, error) {
	var c ClarificationRules
	if err := unmarshalIfPresent(raw, &c); err != nil {
		return ClarificationRules{}, fmt.Errorf("clarification_rules: %w", err)
	}
	if err := c.Validate(); err != nil {
		return ClarificationRules{}, err
	}
	return c, nil
}

func (c *ClarificationRules) Validate() error {
	cleanedOverrides := make([]ToolClarificationOverride, 0, len(c.ToolOverrides))
	seenTools := make(map[string]bool, len(c.ToolOverrides))
	for _, o := range c.ToolOverrides {
		o.ToolID = strings.TrimSpace(o.ToolID)
		if o.ToolID == "" || seenTools[o.ToolID] {
			continue
		}
		seenTools[o.ToolID] = true
		cleanedOverrides = append(cleanedOverrides, o)
	}
	c.ToolOverrides = cleanedOverrides

	cleanedRules := make([]DeriveRuleConfig, 0, len(c.DeriveRules))
	for _, r := range c.DeriveRules {
		r.ToolID = strings.TrimSpace(r.ToolID)
		r.Field = strings.TrimSpace(r.Field)
		r.SourceField = strings.TrimSpace(r.SourceField)
		r.Method = strings.TrimSpace(r.Method)
		if r.ToolID == "" || r.Field == "" || r.SourceField == "" {
			continue
		}
		cleanedRules = append(cleanedRules, r)
	}
	c.DeriveRules = cleanedRules
	return nil
}

func (c ClarificationRules) Marshal() []byte { return mustMarshal(c) }

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// ClampHistory bounds a requested max_history into [MinHistory, MaxHistory],
// falling back to DefaultHistory for non-positive input.
func ClampHistory(v int) int {
	if v <= 0 {
		return DefaultHistory
	}
	if v < MinHistory {
		return MinHistory
	}
	if v > MaxHistory {
		return MaxHistory
	}
	return v
}

// ValidateURL enforces an http(s) scheme and, unless allowPrivate is set, rejects
// endpoints whose host is a loopback/private IP literal or a localhost name. This
// is a best-effort SSRF guard: a public hostname that resolves to a private
// address at request time is not caught here and must be defended at dial time.
func ValidateURL(raw string, allowPrivate bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("unparseable url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("url scheme must be http or https, got %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("url has no host")
	}
	if allowPrivate {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if isPrivateIP(ip) {
			return fmt.Errorf("host %q is a private or loopback address", host)
		}
		return nil
	}
	lower := strings.ToLower(host)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return fmt.Errorf("host %q is not allowed", host)
	}
	return nil
}

func isPrivateIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
}

// unmarshalIfPresent decodes raw into v only when raw is non-empty, so a NULL/
// empty column yields the zero value (then normalized by the caller's Validate).
func unmarshalIfPresent(raw []byte, v any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("invalid json: %w", err)
	}
	return nil
}

// mustMarshal serializes a validated config. The input types contain only JSON-
// safe scalars/slices, so encoding cannot fail; a failure would be a programmer
// error worth surfacing loudly.
func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic("agentcfg: marshal failed: " + err.Error())
	}
	return b
}
