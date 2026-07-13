package workflow

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sawt-go/database"
	"sawt-go/internal/agentcfg"
	"sawt-go/internal/trace"
	"strings"
	"time"
)

// collectRoundsMax bounds the cross-turn slot-filling conversation (distinct
// from executeToolLoop's maxIterations, which bounds in-turn LLM retries).
// After this many rounds without completing the call, the flow gives up and
// asks the user to restate their request from scratch.
const collectRoundsMax = 5

// syntheticRequiredFields are required fields that are machine-internal
// correlation tokens, not something a user would ever supply — record_expense
// and record_payment declare idempotencyKey as required so the model is
// nudged to invent one itself, but if it doesn't, enforceRequiredArgs
// generates a placeholder here rather than asking the user "what idempotency
// key would you like?". This is safe because resolvePendingConfirmation
// (confirmation.go) already overwrites idempotencyKey with a deterministic
// value once the user confirms — only the field's *presence* here matters,
// not its exact value.
var syntheticRequiredFields = map[string]func() string{
	"idempotencyKey": generatePlaceholderToken,
}

func generatePlaceholderToken() string {
	b := make([]byte, 16)
	if _, err := cryptorand.Read(b); err != nil {
		return fmt.Sprintf("pending-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// deriveRule declares how to auto-fill one required field from another,
// rather than asking the user for it. Fn mirrors summarizeIfNeeded's narrow,
// single-purpose completion pattern and degrades gracefully on failure.
type deriveRule struct {
	Field       string
	SourceField string
	Fn          func(e *WorkflowEngine, ctx context.Context, source string) (string, bool)
}

// toolDeriveRules is the Go-side default derive registry, one entry per tool
// id, mirroring the toolRisk/toolMinRole map-of-tool-id pattern in
// confirmation.go/tools.go. Only Arabic->English is auto-derived by default:
// an English-only name is often already the record of truth, and
// transliterating English into Arabic script is a much lossier guess than
// the reverse — an operator can add the opposite direction via a per-agent
// DeriveRuleConfig if ever needed, without a Go redeploy.
var toolDeriveRules = map[string][]deriveRule{
	"register_horse": {
		{Field: "nameEn", SourceField: "nameAr", Fn: (*WorkflowEngine).transliterateArabicName},
	},
}

// deriveMethods maps a DeriveRuleConfig.Method string to a derive function, so
// per-agent config can reference the engine's known derivations by name. An
// unrecognized method is ignored at lookup time, not an error.
var deriveMethods = map[string]func(e *WorkflowEngine, ctx context.Context, source string) (string, bool){
	"transliterate_ar_to_en": (*WorkflowEngine).transliterateArabicName,
}

// transliterateArabicName derives a Latin-script guess for an Arabic horse
// name for register_horse's nameEn field. Mirrors summarizeIfNeeded's narrow
// single-purpose completion pattern (internal/workflow/memory.go): its own
// system prompt, temperature 0, a small token budget, and the engine's normal
// provider cascade via e.complete. It degrades gracefully — any LLM error or
// empty reply returns ok=false, and the caller (enforceRequiredArgs) then
// treats the field as still missing and asks the user, rather than ever
// hard-failing the turn.
func (e *WorkflowEngine) transliterateArabicName(ctx context.Context, arabicName string) (string, bool) {
	systemPrompt := "Transliterate an Arabic horse name into a natural English/Latin spelling " +
		"suitable for an ERP record. Reply with ONLY the transliterated name, nothing else — " +
		"no quotes, no punctuation, no explanation."
	msg, err := e.complete(ctx, []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: arabicName},
	}, nil, 0.0, 20)
	if err != nil || strings.TrimSpace(msg.Content) == "" {
		return "", false
	}
	return strings.TrimSpace(msg.Content), true
}

// isAskDisabled reports whether an agent's clarification_rules turn off the
// "ask if missing" behavior for this tool: an agent-wide Enabled=false, or a
// tool-specific override with AskIfMissing=false. When disabled, the tool
// call proceeds exactly as it did before this feature (no validation at all)
// — a deliberate operator opt-out escape hatch.
func isAskDisabled(toolID string, cfg agentcfg.ClarificationRules) bool {
	if !cfg.EffectiveEnabled() {
		return true
	}
	for _, o := range cfg.ToolOverrides {
		if o.ToolID == toolID {
			return !o.AskIfMissing
		}
	}
	return false
}

// effectiveDeriveRules merges the Go-side default derive registry for a tool
// with any per-agent DeriveRules. An agent-configured rule for the same field
// replaces a built-in default of that field (lets an operator redirect a
// derivation to a different source/method); rules for new fields are
// additive. Config rules whose Method isn't recognized are skipped.
func effectiveDeriveRules(toolID string, cfg agentcfg.ClarificationRules) []deriveRule {
	byField := make(map[string]deriveRule)
	for _, r := range toolDeriveRules[toolID] {
		byField[r.Field] = r
	}
	for _, rc := range cfg.DeriveRules {
		if rc.ToolID != toolID {
			continue
		}
		fn, ok := deriveMethods[rc.Method]
		if !ok {
			continue
		}
		byField[rc.Field] = deriveRule{Field: rc.Field, SourceField: rc.SourceField, Fn: fn}
	}
	rules := make([]deriveRule, 0, len(byField))
	for _, r := range byField {
		rules = append(rules, r)
	}
	return rules
}

// nonEmpty reports whether args[field] is present and "provided": for
// strings (the majority of our tool args — names/ids/enums), non-blank after
// trimming; for any other JSON type (numbers, bools, arrays — e.g. amount,
// medications), simply present and non-nil, since a numeric 0 or false is a
// legitimate provided value, not a missing one.
func nonEmpty(args map[string]interface{}, field string) bool {
	v, ok := args[field]
	if !ok || v == nil {
		return false
	}
	if s, isStr := v.(string); isStr {
		return strings.TrimSpace(s) != ""
	}
	return true
}

// enforceRequiredArgs validates a tool call's args against the tool's own
// declared schema (def.Function.Parameters.Required — the source of truth),
// after auto-deriving any fields the derive registry / per-agent config
// allow (e.g. nameEn from nameAr). It mutates args in place. It returns
// complete=true when args are safe to pass into the existing risk gate
// unchanged. When fields are still missing after derivation, it durably
// parks a "collecting" row for this chat and sets state.FinalReply to a
// clarifying question; the caller must stop the turn immediately (the same
// contract requestConfirmation already follows).
func (e *WorkflowEngine) enforceRequiredArgs(ctx context.Context, state *State, agentName string, def ToolDefinition, args map[string]interface{}) (bool, error) {
	if e.queries == nil {
		return true, nil // no DB configured (unit tests / unconfigured deploy): legacy passthrough
	}

	agentRow, _ := e.resolveAgent(ctx, state.ChatID)
	cfg, err := agentcfg.ParseClarificationRules(agentRow.ClarificationRules)
	if err != nil {
		cfg = agentcfg.DefaultClarificationRules()
	}

	toolID := def.Function.Name
	if isAskDisabled(toolID, cfg) {
		return true, nil
	}

	required := def.Function.Parameters.Required
	if len(required) == 0 {
		return true, nil
	}

	rules := effectiveDeriveRules(toolID, cfg)
	rulesByField := make(map[string]deriveRule, len(rules))
	for _, r := range rules {
		rulesByField[r.Field] = r
	}

	var missing []string
	complete := true
	for _, field := range required {
		if nonEmpty(args, field) {
			continue
		}
		if gen, ok := syntheticRequiredFields[field]; ok {
			args[field] = gen()
			continue
		}
		if rule, ok := rulesByField[field]; ok {
			if !nonEmpty(args, rule.SourceField) {
				// The source isn't supplied yet — never ask about the derived
				// field directly (e.g. don't ask for nameEn before the user has
				// even given nameAr). It resolves automatically once the source
				// itself is answered (the source, being its own required field,
				// is asked for on its own iteration below).
				complete = false
				continue
			}
			if derived, ok := rule.Fn(e, ctx, args[rule.SourceField].(string)); ok {
				args[field] = derived
				continue
			}
			// The source was present but the derivation itself failed (e.g. a
			// transient LLM error) — fall back to asking directly rather than
			// silently stalling with nothing for the user to act on.
		}
		missing = append(missing, field)
		complete = false
	}

	if complete {
		if state.Resume != nil {
			_ = e.queries.DeletePendingConfirmation(ctx, state.ChatID)
		}
		return true, nil
	}
	if len(missing) == 0 {
		// Every unresolved field is silently blocked on a derive source that's
		// itself being asked for — nothing new to ask this round; the parked
		// state elsewhere already reflects it. Treat as incomplete without
		// re-parking a redundant/empty clarification.
		return false, nil
	}

	if err := e.requestClarification(ctx, state, agentName, toolID, args, missing); err != nil {
		return false, err
	}
	return false, nil
}

// commonFieldLabels gives a handful of frequently-recurring fields a short
// bilingual label so the clarifying question reads naturally instead of
// dumping raw JSON keys. Anything unmapped falls back to the field name
// itself — still legible, just less polished.
var commonFieldLabels = map[string]string{
	"breed":       "السلالة",
	"color":       "اللون",
	"gender":      "الجنس",
	"nameAr":      "الاسم بالعربي",
	"nameEn":      "الاسم بالإنجليزي",
	"amount":      "المبلغ",
	"invoiceId":   "رقم الفاتورة",
	"description": "الوصف",
	"status":      "الحالة",
}

// describeMissingFields renders the still-needed fields into a short Arabic
// clarifying question, mirroring describePendingAction's plain, direct style.
func describeMissingFields(missing []string) string {
	labels := make([]string, 0, len(missing))
	for _, f := range missing {
		if l, ok := commonFieldLabels[f]; ok {
			labels = append(labels, l)
		} else {
			labels = append(labels, f)
		}
	}
	return strings.Join(labels, "، ")
}

// requestClarification stores the pending action durably as a "collecting"
// row and turns the reply into a clarifying question, mirroring
// requestConfirmation's shape (single-slot guard, upsert, ToolResults audit
// entry, FinalReply).
func (e *WorkflowEngine) requestClarification(ctx context.Context, state *State, agentName, toolID string, args map[string]interface{}, missing []string) error {
	roundsSoFar := int32(0)
	if state.Resume != nil && state.Resume.ToolID == toolID {
		roundsSoFar = state.Resume.RoundsSoFar
	}

	// Single-slot guard: refuse to clobber a live, unrelated 'pending'
	// confirmation for this chat — mirrors requestConfirmation's own guard.
	if existing, err := e.queries.GetPendingConfirmation(ctx, state.ChatID); err == nil &&
		existing.Status == "pending" && time.Now().Before(existing.ExpiresAt) {
		state.FinalReply = fmt.Sprintf(
			"لديك عملية بانتظار التأكيد: %s. أرسل \"نعم\" لتأكيدها أو أي رد آخر لإلغائها قبل طلب عملية جديدة.",
			existing.Description)
		return nil
	}

	if roundsSoFar+1 > collectRoundsMax {
		_ = e.queries.DeletePendingConfirmation(ctx, state.ChatID)
		state.FinalReply = "لم أتمكن من إكمال طلبك بعد عدة محاولات. من فضلك أعد صياغة طلبك من البداية بكل التفاصيل."
		return nil
	}

	argsBytes, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("failed to marshal collecting args: %w", err)
	}
	missingBytes, err := json.Marshal(missing)
	if err != nil {
		return fmt.Errorf("failed to marshal missing fields: %w", err)
	}

	question := describeMissingFields(missing)

	if err := e.queries.UpsertCollecting(ctx, database.UpsertCollectingParams{
		ChatID:        state.ChatID,
		ToolID:        toolID,
		Args:          argsBytes,
		OrgID:         state.ActorIdentity.OrgIDs[0],
		ActingUserUid: state.ActorIdentity.UID,
		Description:   question,
		ExpiresAt:     time.Now().Add(confirmationTTL),
		MissingFields: missingBytes,
		CollectRounds: roundsSoFar + 1,
		Intent:        agentName,
	}); err != nil {
		return fmt.Errorf("failed to store collecting state: %w", err)
	}

	state.ToolResults = append(state.ToolResults, map[string]interface{}{
		"tool":   toolID,
		"args":   args,
		"output": map[string]interface{}{"status": "collecting", "missing": missing},
	})

	state.FinalReply = fmt.Sprintf("أحتاج أيضاً إلى: %s.", question)
	trace.Logf(ctx, "[workflow] Clarification requested for tool '%s' on chat %s (missing: %v)", toolID, state.ChatID, missing)
	return nil
}

// resumeCollecting checks whether this chat has an active "collecting" row
// (a parked tool call still missing required args) and, if so, resumes the
// same tool-calling loop instead of running normal intent classification. It
// reports resumed=true when the message was consumed by this flow — the tool
// loop it invokes always sets state.FinalReply (or returns an error), so the
// caller returns immediately exactly like resolvePendingConfirmation.
func (e *WorkflowEngine) resumeCollecting(ctx context.Context, state *State) (bool, error) {
	if e.queries == nil || state.ChatID == "" {
		return false, nil
	}

	existing, err := e.queries.GetPendingConfirmation(ctx, state.ChatID)
	if err != nil || existing.Status != "collecting" {
		return false, nil // no row, or it's an ordinary pending confirmation handled above
	}

	if time.Now().After(existing.ExpiresAt) {
		_ = e.queries.DeletePendingConfirmation(ctx, state.ChatID)
		return false, nil // expired: treat the new message as a fresh request
	}
	if existing.CollectRounds >= collectRoundsMax {
		_ = e.queries.DeletePendingConfirmation(ctx, state.ChatID)
		state.FinalReply = "لم أتمكن من إكمال طلبك بعد عدة محاولات. من فضلك أعد صياغة طلبك من البداية بكل التفاصيل."
		return true, nil
	}

	claimed, err := e.queries.ClaimCollecting(ctx, state.ChatID)
	if err != nil {
		return false, nil // lost the claim race to a concurrent goroutine; fall through
	}

	spec, ok := specByName(claimed.Intent)
	if !ok {
		_ = e.queries.DeletePendingConfirmation(ctx, state.ChatID)
		return false, nil // stale/unknown intent — safest to drop and reclassify fresh
	}

	var knownArgs map[string]interface{}
	_ = json.Unmarshal(claimed.Args, &knownArgs)
	var missing []string
	_ = json.Unmarshal(claimed.MissingFields, &missing)

	state.Resume = &ResumeCollecting{
		ToolID:        claimed.ToolID,
		KnownArgs:     knownArgs,
		MissingFields: missing,
		RoundsSoFar:   claimed.CollectRounds,
	}
	return true, e.executeToolLoop(ctx, state, spec)
}
