package erp

import (
	"context"
	"strings"

	"sawt-go/database"
)

// Unresolved reasons persisted to wa_contacts.erp_unresolved_reason.
const (
	UnresolvedNoMatch         = "no_match"
	UnresolvedPhoneUnverified = "phone_unverified"
	UnresolvedLidUnlinked     = "lid_unlinked"
)

// LinkResult is the outcome of resolving a WhatsApp contact's ERP identity.
type LinkResult struct {
	Identity *Identity
	// Reason is empty when Identity was resolved cleanly, otherwise one of
	// the Unresolved* constants.
	Reason string
}

// LIDResolver resolves a WhatsApp LID (linked ID) user — the numeric id
// before "@lid" — to its real phone number. Implemented by
// internal/whatsmeow.WhatsAppManager, using whatsmeow's own device-store
// mapping (learned automatically from contact sync / message processing).
// Kept as a narrow interface here so this package doesn't need to import
// the whatsmeow client.
type LIDResolver interface {
	ResolvePhoneForLID(ctx context.Context, lidUser string) (phone string, ok bool)
}

// ResolveAndPersistContactIdentity resolves chatID's ERP identity and
// persists the outcome onto wa_contacts so it doesn't need to be re-resolved
// on every page load. The phone used for resolution is chosen in order:
//  1. contact.ErpPhoneOverride, when an operator has set one.
//  2. For a WhatsApp LID chat (chatID ending in "@lid" — WhatsApp's opaque
//     addressing mode for contacts that don't expose their phone number
//     directly), the phone lidResolver already knows for that LID, if any.
//  3. Otherwise, the phone number derived directly from the WhatsApp JID
//     (chatID's user part), the same way the inbound message handler and
//     the dashboard's role lookups already do.
//
// lidResolver may be nil (e.g. WhatsApp not yet connected), in which case
// step 2 is skipped and an unresolvable LID falls back to
// UnresolvedLidUnlinked, same as before this resolver existed.
//
// A transient ERP error is deliberately NOT persisted: it's surfaced to the
// caller via the returned error, leaving any previously known-good link on
// the contact row untouched rather than flapping it to "unresolved".
func ResolveAndPersistContactIdentity(ctx context.Context, client *Client, queries *database.Queries, chatID string, phoneOverride *string, lidResolver LIDResolver) (*LinkResult, error) {
	isLID := strings.HasSuffix(strings.ToLower(chatID), "@lid")
	lidUser := strings.Split(chatID, "@")[0]
	phone := lidUser
	hasOverride := phoneOverride != nil && strings.TrimSpace(*phoneOverride) != ""
	autoResolvedFromLID := false
	switch {
	case hasOverride:
		phone = strings.TrimSpace(*phoneOverride)
	case isLID && lidResolver != nil:
		if resolvedPhone, ok := lidResolver.ResolvePhoneForLID(ctx, lidUser); ok && resolvedPhone != "" {
			phone = resolvedPhone
			autoResolvedFromLID = true
		}
	}

	// Persist a LID phone the moment it's discovered, independent of whether
	// ERP recognizes it — so the dashboard can display the contact's real
	// phone number (instead of the opaque LID digits) without needing a live
	// whatsmeow lookup on every page render. Reuses erp_phone_override: once
	// auto-discovered, it IS the phone this contact resolves against, same as
	// an operator-entered override. Best-effort — a failure here only means
	// the display falls back to LID digits until the next successful
	// resolution; it must not fail the identity resolution itself.
	if autoResolvedFromLID {
		_, _ = queries.UpdateWaContactErpOverride(ctx, database.UpdateWaContactErpOverrideParams{
			ChatID:           chatID,
			ErpPhoneOverride: &phone,
		})
	}

	identity, err := client.ResolveIdentity(ctx, phone)
	if err != nil {
		return nil, err
	}

	result := &LinkResult{Identity: identity}
	params := database.UpdateWaContactErpLinkParams{ChatID: chatID}

	switch {
	case identity == nil:
		reason := UnresolvedNoMatch
		if isLID && !hasOverride && !autoResolvedFromLID {
			reason = UnresolvedLidUnlinked
		}
		result.Reason = reason
		params.ErpUnresolvedReason = &reason
	case identity.PhoneUnverified:
		result.Reason = UnresolvedPhoneUnverified
		reason := UnresolvedPhoneUnverified
		params.ErpUnresolvedReason = &reason
		fillIdentityParams(&params, identity)
	default:
		fillIdentityParams(&params, identity)
	}

	if _, err := queries.UpdateWaContactErpLink(ctx, params); err != nil {
		return nil, err
	}
	return result, nil
}

func fillIdentityParams(params *database.UpdateWaContactErpLinkParams, identity *Identity) {
	uid := identity.UID
	params.ErpUid = &uid
	displayName := identity.DisplayName
	params.ErpDisplayName = &displayName
	role := identity.Role
	params.ErpRole = &role
	if len(identity.OrgIDs) > 0 {
		orgID := identity.OrgIDs[0]
		params.ErpOrgID = &orgID
	}
}
