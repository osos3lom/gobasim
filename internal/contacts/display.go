// Package contacts holds display and identity helpers for WhatsApp contacts
// that are needed by both the inbound message gateway and the operator
// dashboard. It deliberately depends on nothing else in this module so either
// side can import it without creating a cycle — the dashboard must not become
// a dependency of message processing just to share a formatting rule.
package contacts

import "strings"

// GenericContactName is the placeholder used when a contact has no WhatsApp
// push name and no phone number can be derived from its chat id. It exists so
// callers never fall back to the raw chat id: for a LID chat that id is an
// opaque WhatsApp identifier, and showing it to an operator reads as though it
// were a phone number.
const GenericContactName = "New WhatsApp contact"

// DisplayPhone renders a contact's operator-facing phone number, or "" when
// none can be determined.
//
// Precedence:
//  1. phoneOverride, when set — either an operator-entered correction or a
//     phone auto-discovered from WhatsApp's LID mapping and persisted to
//     wa_contacts.erp_phone_override (see internal/erp.ResolveAndPersistContactIdentity).
//  2. For a normal WhatsApp JID, the user part of the chat id already is the
//     phone number in international digits.
//
// A "@lid" chat id with no override yields "". WhatsApp assigns these opaque
// linked-ids as the primary chat identifier for many contacts, and their
// digits are not a phone number — callers must render their own "unlinked"
// affordance rather than falling back to the raw id.
func DisplayPhone(chatID string, phoneOverride *string) string {
	if phoneOverride != nil && strings.TrimSpace(*phoneOverride) != "" {
		return FormatPhoneDisplay(strings.TrimSpace(*phoneOverride))
	}
	if strings.HasSuffix(strings.ToLower(chatID), "@lid") {
		return ""
	}
	parts := strings.Split(chatID, "@")
	if len(parts) > 0 {
		return FormatPhoneDisplay(parts[0])
	}
	return ""
}

// FormatPhoneDisplay converts a phone number held in international digits
// (the form WhatsApp JIDs and ERP identity resolution both use) into the local
// format operators actually recognize. This deployment is Saudi-centric, so
// +966 is the one country code with a known local-format rule; anything else
// recognizable is shown with a "+" prefix rather than guessed at.
func FormatPhoneDisplay(phone string) string {
	digits := strings.TrimPrefix(strings.TrimSpace(phone), "+")
	if digits == "" {
		return ""
	}
	// Saudi international format: 966546906905 -> 0546906905
	if strings.HasPrefix(digits, "966") && len(digits) == 12 {
		return "0" + digits[3:]
	}
	// Already in Saudi local format: 0546906905
	if strings.HasPrefix(digits, "0") {
		return digits
	}
	// Raw LID digits (typically 14-15 long, e.g. 90727124070644) are not a
	// phone number in any country — refuse rather than emit a plausible-looking
	// "+90727124070644".
	if len(digits) > 13 {
		return ""
	}
	return "+" + digits
}

// BlueprintDefaults is the "system default blueprint" an operator configures
// once and that is then applied to every newly auto-created contact. It is
// persisted as JSON in settings.bot_config.
//
// It lives here rather than in the web package because both sides need it:
// the dashboard edits it, and the inbound message gateway reads it when a
// stranger first messages in. Keeping it in web/ would make the presentation
// layer a dependency of message processing.
type BlueprintDefaults struct {
	DefaultAgentID        string `json:"agent_id"`
	DefaultPromptOverride string `json:"prompt_override"`
	// AutoEnable overrides the safety default that new contacts start
	// disabled until an operator opts them in. Off unless deliberately set.
	AutoEnable bool `json:"auto_enable"`
}
