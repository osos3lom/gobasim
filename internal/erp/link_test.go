package erp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sawt-go/database"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// mockDBTX is a minimal database.DBTX so ResolveAndPersistContactIdentity can
// be exercised without a real Postgres connection. Mirrors the pattern in
// web/server_test.go.
type mockDBTX struct {
	queryRowFunc func(ctx context.Context, sql string, args ...interface{}) pgx.Row
}

func (m *mockDBTX) QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row {
	if m.queryRowFunc != nil {
		return m.queryRowFunc(ctx, sql, args...)
	}
	return &mockRow{}
}

func (m *mockDBTX) Query(ctx context.Context, sql string, args ...interface{}) (pgx.Rows, error) {
	return nil, nil
}

func (m *mockDBTX) Exec(ctx context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

type mockRow struct {
	scanFunc func(dest ...interface{}) error
}

func (r *mockRow) Scan(dest ...interface{}) error {
	if r.scanFunc != nil {
		return r.scanFunc(dest...)
	}
	return nil
}

// erpIdentityServer builds an httptest server that plays the mshalia
// identity-resolve endpoint, recording the phone it was last asked to
// resolve so tests can assert on it.
func erpIdentityServer(t *testing.T, respond func(phone string) string) (*httptest.Server, *string) {
	t.Helper()
	var gotPhone string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Phone string `json:"phone"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotPhone = body.Phone
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(respond(body.Phone)))
	}))
	t.Cleanup(server.Close)
	return server, &gotPhone
}

// capturingQueries returns a *database.Queries whose UpdateWaContactErpLink
// calls are appended to captured, decoded back into the params struct so
// tests can assert on exactly what would have been persisted. Any
// UpdateWaContactErpOverride call (issued when a LID phone is
// auto-discovered — see capturingQueriesWithOverrides for tests that need to
// assert on it) is a no-op here.
func capturingQueries(captured *[]database.UpdateWaContactErpLinkParams) *database.Queries {
	return capturingQueriesWithOverrides(captured, nil)
}

// capturingQueriesWithOverrides is capturingQueries plus capturing of
// UpdateWaContactErpOverride calls (the phone string persisted) into
// overrides, when non-nil. The two queries are distinguished by their SQL
// text's INSERT column list ("INSERT INTO wa_contacts (chat_id, erp_uid,
// ..." vs "..., erp_phone_override, ...") — NOT the RETURNING clause, which
// lists every wa_contacts column (including erp_uid AND erp_phone_override)
// on both queries and so can't discriminate between them.
func capturingQueriesWithOverrides(captured *[]database.UpdateWaContactErpLinkParams, overrides *[]string) *database.Queries {
	dbtx := &mockDBTX{
		queryRowFunc: func(ctx context.Context, sql string, args ...interface{}) pgx.Row {
			if strings.Contains(sql, "INSERT INTO wa_contacts (chat_id, erp_uid") {
				return &mockRow{
					scanFunc: func(dest ...interface{}) error {
						// arg order matches UpdateWaContactErpLinkParams field
						// order: ChatID, ErpUid, ErpDisplayName, ErpOrgID, ErpRole, ErpUnresolvedReason.
						params := database.UpdateWaContactErpLinkParams{ChatID: args[0].(string)}
						if v, _ := args[1].(*string); v != nil {
							params.ErpUid = v
						}
						if v, _ := args[2].(*string); v != nil {
							params.ErpDisplayName = v
						}
						if v, _ := args[3].(*string); v != nil {
							params.ErpOrgID = v
						}
						if v, _ := args[4].(*string); v != nil {
							params.ErpRole = v
						}
						if v, _ := args[5].(*string); v != nil {
							params.ErpUnresolvedReason = v
						}
						*captured = append(*captured, params)
						// ResolveAndPersistContactIdentity discards the returned
						// row (only checks the error), so Scan needn't populate dest.
						return nil
					},
				}
			}
			// UpdateWaContactErpOverride: args are (ChatID, ErpPhoneOverride).
			return &mockRow{
				scanFunc: func(dest ...interface{}) error {
					if overrides != nil {
						if v, _ := args[1].(*string); v != nil {
							*overrides = append(*overrides, *v)
						}
					}
					return nil
				},
			}
		},
	}
	return database.New(dbtx)
}

func strVal(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func TestResolveAndPersistContactIdentity_UsesJIDPhoneByDefault(t *testing.T) {
	server, gotPhone := erpIdentityServer(t, func(phone string) string {
		return `{"resolved": true, "identity": {"uid": "u1", "phone": "` + phone + `", "role": "manager", "displayName": "Layla", "orgIds": ["org1"]}}`
	})
	client := NewClient(server.URL, "secret")
	var captured []database.UpdateWaContactErpLinkParams
	queries := capturingQueries(&captured)

	_, err := ResolveAndPersistContactIdentity(context.Background(), client, queries, "971501234567@s.whatsapp.net", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *gotPhone != "971501234567" {
		t.Errorf("expected ERP to be called with JID-derived phone %q, got %q", "971501234567", *gotPhone)
	}
}

func TestResolveAndPersistContactIdentity_OverridePhoneTakesPrecedence(t *testing.T) {
	server, gotPhone := erpIdentityServer(t, func(phone string) string {
		return `{"resolved": true, "identity": {"uid": "u1", "phone": "` + phone + `", "role": "manager", "displayName": "Layla", "orgIds": ["org1"]}}`
	})
	client := NewClient(server.URL, "secret")
	var captured []database.UpdateWaContactErpLinkParams
	queries := capturingQueries(&captured)

	override := "966500000000"
	_, err := ResolveAndPersistContactIdentity(context.Background(), client, queries, "971501234567@s.whatsapp.net", &override, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *gotPhone != override {
		t.Errorf("expected ERP to be called with override phone %q, got %q", override, *gotPhone)
	}

	// A blank/whitespace override must NOT take precedence over the JID phone.
	blank := "   "
	_, err = ResolveAndPersistContactIdentity(context.Background(), client, queries, "971501234567@s.whatsapp.net", &blank, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *gotPhone != "971501234567" {
		t.Errorf("expected blank override to fall back to JID phone, got %q", *gotPhone)
	}
}

func TestResolveAndPersistContactIdentity_NoMatch(t *testing.T) {
	server, _ := erpIdentityServer(t, func(phone string) string {
		return `{"resolved": false}`
	})
	client := NewClient(server.URL, "secret")
	var captured []database.UpdateWaContactErpLinkParams
	queries := capturingQueries(&captured)

	result, err := ResolveAndPersistContactIdentity(context.Background(), client, queries, "971501234567@s.whatsapp.net", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Identity != nil {
		t.Errorf("expected no identity, got %+v", result.Identity)
	}
	if result.Reason != UnresolvedNoMatch {
		t.Errorf("expected reason %q, got %q", UnresolvedNoMatch, result.Reason)
	}
	if len(captured) != 1 {
		t.Fatalf("expected exactly one persisted link, got %d", len(captured))
	}
	if strVal(captured[0].ErpUnresolvedReason) != UnresolvedNoMatch {
		t.Errorf("expected persisted reason %q, got %q", UnresolvedNoMatch, strVal(captured[0].ErpUnresolvedReason))
	}
	if captured[0].ErpUid != nil {
		t.Errorf("expected no UID persisted for an unresolved contact, got %q", strVal(captured[0].ErpUid))
	}
}

func TestResolveAndPersistContactIdentity_PhoneUnverifiedStillPersistsIdentity(t *testing.T) {
	server, _ := erpIdentityServer(t, func(phone string) string {
		return `{"resolved": true, "phoneUnverified": true, "identity": {"uid": "u1", "phone": "` + phone + `", "role": "client", "displayName": "Unverified Layla", "orgIds": ["org1"]}}`
	})
	client := NewClient(server.URL, "secret")
	var captured []database.UpdateWaContactErpLinkParams
	queries := capturingQueries(&captured)

	result, err := ResolveAndPersistContactIdentity(context.Background(), client, queries, "971501234567@s.whatsapp.net", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Reason != UnresolvedPhoneUnverified {
		t.Errorf("expected reason %q, got %q", UnresolvedPhoneUnverified, result.Reason)
	}
	if result.Identity == nil || result.Identity.UID != "u1" {
		t.Fatalf("expected identity to still be returned for a phone-unverified match, got %+v", result.Identity)
	}
	if len(captured) != 1 {
		t.Fatalf("expected exactly one persisted link, got %d", len(captured))
	}
	if strVal(captured[0].ErpUnresolvedReason) != UnresolvedPhoneUnverified {
		t.Errorf("expected persisted reason %q, got %q", UnresolvedPhoneUnverified, strVal(captured[0].ErpUnresolvedReason))
	}
	// Identity details must still be persisted even though the phone is unverified.
	if strVal(captured[0].ErpUid) != "u1" {
		t.Errorf("expected UID to be persisted despite unverified phone, got %q", strVal(captured[0].ErpUid))
	}
	if strVal(captured[0].ErpDisplayName) != "Unverified Layla" {
		t.Errorf("expected display name to be persisted, got %q", strVal(captured[0].ErpDisplayName))
	}
}

func TestResolveAndPersistContactIdentity_CleanMatchPersistsAllFields(t *testing.T) {
	server, _ := erpIdentityServer(t, func(phone string) string {
		return `{"resolved": true, "identity": {"uid": "u42", "phone": "` + phone + `", "role": "owner", "displayName": "Osama", "orgIds": ["org-main", "org-secondary"]}}`
	})
	client := NewClient(server.URL, "secret")
	var captured []database.UpdateWaContactErpLinkParams
	queries := capturingQueries(&captured)

	result, err := ResolveAndPersistContactIdentity(context.Background(), client, queries, "971501234567@s.whatsapp.net", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Reason != "" {
		t.Errorf("expected empty reason for a clean match, got %q", result.Reason)
	}
	if len(captured) != 1 {
		t.Fatalf("expected exactly one persisted link, got %d", len(captured))
	}
	p := captured[0]
	if strVal(p.ErpUid) != "u42" || strVal(p.ErpDisplayName) != "Osama" || strVal(p.ErpRole) != "owner" {
		t.Errorf("unexpected persisted identity fields: %+v", p)
	}
	// Only the first org is persisted (erp_org_id is a single column).
	if strVal(p.ErpOrgID) != "org-main" {
		t.Errorf("expected first org %q to be persisted, got %q", "org-main", strVal(p.ErpOrgID))
	}
	if p.ErpUnresolvedReason != nil {
		t.Errorf("expected no unresolved reason for a clean match, got %q", strVal(p.ErpUnresolvedReason))
	}
}

// fakeLIDResolver is a minimal erp.LIDResolver for tests: it returns a fixed
// (phone, ok) pair regardless of which lidUser it's asked about.
type fakeLIDResolver struct {
	phone string
	ok    bool
}

func (f fakeLIDResolver) ResolvePhoneForLID(ctx context.Context, lidUser string) (string, bool) {
	return f.phone, f.ok
}

// A @lid chat with no manual override must use the phone the LIDResolver
// already knows (whatsmeow's own learned mapping), not the LID's opaque
// digits — and a match found via the resolver is a clean resolution, not
// "lid_unlinked" (this exact scenario broke before the resolver was wired
// in: WhatsApp assigns LIDs as the default chat identifier for many
// contacts, and the app was resolving against the meaningless LID digits
// instead of the phone whatsmeow already had cached).
func TestResolveAndPersistContactIdentity_LIDResolvesViaWhatsmeowStore(t *testing.T) {
	server, gotPhone := erpIdentityServer(t, func(phone string) string {
		return `{"resolved": true, "identity": {"uid": "u1", "phone": "` + phone + `", "role": "manager", "displayName": "Osama", "orgIds": ["org1"]}}`
	})
	client := NewClient(server.URL, "secret")
	var captured []database.UpdateWaContactErpLinkParams
	var overrides []string
	queries := capturingQueriesWithOverrides(&captured, &overrides)

	resolver := fakeLIDResolver{phone: "966546906905", ok: true}
	result, err := ResolveAndPersistContactIdentity(context.Background(), client, queries, "90727124070644@lid", nil, resolver)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *gotPhone != "966546906905" {
		t.Errorf("expected ERP to be called with the resolver's phone %q, got %q", "966546906905", *gotPhone)
	}
	if result.Reason != "" {
		t.Errorf("expected a clean match (empty reason) when the resolver finds a phone, got %q", result.Reason)
	}
	if len(captured) != 1 || strVal(captured[0].ErpUid) != "u1" {
		t.Fatalf("expected identity to be persisted, got %+v", captured)
	}
	// The auto-discovered phone must be persisted to erp_phone_override so
	// the dashboard can display it without a live whatsmeow lookup.
	if len(overrides) != 1 || overrides[0] != "966546906905" {
		t.Errorf("expected the resolved phone to be auto-persisted as erp_phone_override, got %v", overrides)
	}
}

// A manual operator override must still win over the LID resolver — the
// operator is explicitly correcting a phone, which should not be
// second-guessed by whatsmeow's own mapping.
func TestResolveAndPersistContactIdentity_OverrideWinsOverLIDResolver(t *testing.T) {
	server, gotPhone := erpIdentityServer(t, func(phone string) string {
		return `{"resolved": true, "identity": {"uid": "u1", "phone": "` + phone + `", "role": "manager", "displayName": "Osama", "orgIds": ["org1"]}}`
	})
	client := NewClient(server.URL, "secret")
	var captured []database.UpdateWaContactErpLinkParams
	queries := capturingQueries(&captured)

	resolver := fakeLIDResolver{phone: "966546906905", ok: true}
	override := "966500000099"
	_, err := ResolveAndPersistContactIdentity(context.Background(), client, queries, "90727124070644@lid", &override, resolver)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *gotPhone != override {
		t.Errorf("expected the operator override %q to win over the resolver's phone, got %q", override, *gotPhone)
	}
}

// When the resolver has no mapping yet (whatsmeow hasn't learned this LID's
// phone), behavior must fall back to today's lid_unlinked signal rather than
// resolving against the meaningless LID digits.
func TestResolveAndPersistContactIdentity_LIDUnlinkedWhenResolverHasNoMapping(t *testing.T) {
	server, gotPhone := erpIdentityServer(t, func(phone string) string {
		return `{"resolved": false}`
	})
	client := NewClient(server.URL, "secret")
	var captured []database.UpdateWaContactErpLinkParams
	queries := capturingQueries(&captured)

	resolver := fakeLIDResolver{phone: "", ok: false}
	result, err := ResolveAndPersistContactIdentity(context.Background(), client, queries, "90727124070644@lid", nil, resolver)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *gotPhone != "90727124070644" {
		t.Errorf("expected fallback to the LID digits when unresolved, got %q", *gotPhone)
	}
	if result.Reason != UnresolvedLidUnlinked {
		t.Errorf("expected reason %q, got %q", UnresolvedLidUnlinked, result.Reason)
	}
	if len(captured) != 1 || strVal(captured[0].ErpUnresolvedReason) != UnresolvedLidUnlinked {
		t.Fatalf("expected persisted reason %q, got %+v", UnresolvedLidUnlinked, captured)
	}
}

// A nil LIDResolver (e.g. WhatsApp not yet connected) must behave exactly
// like before this resolver existed: an unresolvable @lid falls back to
// lid_unlinked without panicking.
func TestResolveAndPersistContactIdentity_NilLIDResolverIsSafe(t *testing.T) {
	server, _ := erpIdentityServer(t, func(phone string) string {
		return `{"resolved": false}`
	})
	client := NewClient(server.URL, "secret")
	var captured []database.UpdateWaContactErpLinkParams
	queries := capturingQueries(&captured)

	result, err := ResolveAndPersistContactIdentity(context.Background(), client, queries, "90727124070644@lid", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Reason != UnresolvedLidUnlinked {
		t.Errorf("expected reason %q, got %q", UnresolvedLidUnlinked, result.Reason)
	}
}

func TestResolveAndPersistContactIdentity_ErpErrorIsNotPersisted(t *testing.T) {
	server, _ := erpIdentityServer(t, func(phone string) string {
		return `not json`
	})
	client := NewClient(server.URL, "secret")
	var captured []database.UpdateWaContactErpLinkParams
	queries := capturingQueries(&captured)

	_, err := ResolveAndPersistContactIdentity(context.Background(), client, queries, "971501234567@s.whatsapp.net", nil, nil)
	if err == nil {
		t.Fatal("expected an error when the ERP response is malformed")
	}
	if len(captured) != 0 {
		t.Errorf("expected a transient ERP error to leave the contact's link untouched, but %d link(s) were persisted", len(captured))
	}
}
