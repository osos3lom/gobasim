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

// ResolveAndPersistContactIdentity resolves chatID's ERP identity — using
// contact.ErpPhoneOverride when an operator has set one, otherwise the phone
// number derived from the WhatsApp JID the same way the inbound message
// handler and the dashboard's role lookups already do — and persists the
// outcome onto wa_contacts so it doesn't need to be re-resolved on every
// page load.
//
// A transient ERP error is deliberately NOT persisted: it's surfaced to the
// caller via the returned error, leaving any previously known-good link on
// the contact row untouched rather than flapping it to "unresolved".
func ResolveAndPersistContactIdentity(ctx context.Context, client *Client, queries *database.Queries, chatID string, phoneOverride *string) (*LinkResult, error) {
	isLID := strings.HasSuffix(strings.ToLower(chatID), "@lid")
	phone := strings.Split(chatID, "@")[0]
	if phoneOverride != nil && strings.TrimSpace(*phoneOverride) != "" {
		phone = strings.TrimSpace(*phoneOverride)
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
		if isLID && (phoneOverride == nil || strings.TrimSpace(*phoneOverride) == "") {
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
