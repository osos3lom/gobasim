package erp

import "testing"

func TestApplyDefaultOrg(t *testing.T) {
	tests := []struct {
		name       string
		identity   *Identity
		defaultOrg string
		wantApply  bool
		wantOrgs   []string
	}{
		{
			name:       "super_admin with no org gets the fallback",
			identity:   &Identity{UID: "u1", Role: "super_admin"},
			defaultOrg: "org-demo",
			wantApply:  true,
			wantOrgs:   []string{"org-demo"},
		},
		{
			name:       "role match is case-insensitive and trimmed",
			identity:   &Identity{UID: "u1", Role: "  Super_Admin "},
			defaultOrg: "org-demo",
			wantApply:  true,
			wantOrgs:   []string{"org-demo"},
		},
		{
			name:       "admin and owner are also privileged",
			identity:   &Identity{UID: "u1", Role: "owner"},
			defaultOrg: "org-demo",
			wantApply:  true,
			wantOrgs:   []string{"org-demo"},
		},
		{
			name:       "identity that already has an org is untouched",
			identity:   &Identity{UID: "u1", Role: "super_admin", OrgIDs: []string{"org-real"}},
			defaultOrg: "org-demo",
			wantApply:  false,
			wantOrgs:   []string{"org-real"},
		},
		{
			name:       "non-privileged role does not get a fallback org",
			identity:   &Identity{UID: "u1", Role: "viewer"},
			defaultOrg: "org-demo",
			wantApply:  false,
			wantOrgs:   nil,
		},
		{
			name:       "client role does not get a fallback org",
			identity:   &Identity{UID: "u1", Role: "client"},
			defaultOrg: "org-demo",
			wantApply:  false,
			wantOrgs:   nil,
		},
		{
			name:       "empty default org disables the fallback",
			identity:   &Identity{UID: "u1", Role: "super_admin"},
			defaultOrg: "",
			wantApply:  false,
			wantOrgs:   nil,
		},
		{
			name:       "nil identity is a safe no-op",
			identity:   nil,
			defaultOrg: "org-demo",
			wantApply:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ApplyDefaultOrg(tc.identity, tc.defaultOrg)
			if got != tc.wantApply {
				t.Errorf("ApplyDefaultOrg() = %v, want %v", got, tc.wantApply)
			}
			if tc.identity != nil && !equalStrings(tc.identity.OrgIDs, tc.wantOrgs) {
				t.Errorf("OrgIDs = %v, want %v", tc.identity.OrgIDs, tc.wantOrgs)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
