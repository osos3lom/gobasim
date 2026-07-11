package erp

import "strings"

// privilegedRoles are the roles mshalia treats as bypassing org-membership
// (super_admin / admin / owner get all scopes). Only these may receive a
// default-org fallback — a plain viewer or client that resolves with no org
// genuinely has no access and must not be granted one.
var privilegedRoles = map[string]bool{
	"super_admin": true,
	"superadmin":  true,
	"admin":       true,
	"owner":       true,
}

// ApplyDefaultOrg gives a resolved-but-orgless privileged identity a working
// org, mutating identity in place and reporting whether a fallback was
// applied. It is a deliberate no-op when: identity is nil, defaultOrgID is
// empty, the identity already carries an org, or the role is not privileged.
//
// This closes the M9 live gap: super-admin phone numbers (e.g. 0546906905)
// resolved with an empty OrgIDs list, so the workflow tool loop bailed with
// "this number isn't linked" and the ERP interaction never ran. Assigning a
// configured default org mirrors the CLI's explicit -org bypass that staff
// already rely on (see docs/LOCAL-TESTING.md §6).
func ApplyDefaultOrg(identity *Identity, defaultOrgID string) bool {
	if identity == nil || defaultOrgID == "" || len(identity.OrgIDs) > 0 {
		return false
	}
	if !privilegedRoles[strings.ToLower(strings.TrimSpace(identity.Role))] {
		return false
	}
	identity.OrgIDs = []string{defaultOrgID}
	return true
}
