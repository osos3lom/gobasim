// Command mockerp is a local stand-in for the mshalia ERP Agent Gateway so the
// Sawt workflow can be exercised end-to-end without the real ERP (which doesn't
// exist yet). It implements the same HMAC-signed contract the real gateway must
// implement — see docs/mshalia-side.md and internal/erp/client.go:
//
//	POST /api/agent/v1/identity/resolve   -> { resolved, identity{...} }
//	POST /api/agent/v1/tools/{toolId}     -> { ok, data }
//
// It verifies the x-swa-signature / x-swa-timestamp HMAC exactly as the real
// gateway should, so running it also proves our client's signing is correct.
//
// Run (secret + role are configurable via env):
//
//	AGENT_GATEWAY_SECRET=devsecret MOCK_ROLE=manager go run ./cmd/mockerp
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func main() {
	secret := getenv("AGENT_GATEWAY_SECRET", "devsecret")
	port := getenv("MOCK_ERP_PORT", "3001")
	role := getenv("MOCK_ROLE", "manager")

	mux := http.NewServeMux()

	mux.HandleFunc("/api/agent/v1/identity/resolve", guard(secret, func(w http.ResponseWriter, r *http.Request, body []byte) {
		var req struct {
			Phone string `json:"phone"`
		}
		_ = json.Unmarshal(body, &req)
		log.Printf("[identity] phone=%q -> role=%q org=org_test", req.Phone, role)
		writeJSON(w, http.StatusOK, map[string]any{
			"resolved": true,
			"identity": map[string]any{
				"uid":         "local-user",
				"phone":       req.Phone,
				"role":        role,
				"displayName": "Local Test User",
				"orgIds":      []string{"org_test"},
			},
		})
	}))

	mux.HandleFunc("/api/agent/v1/tools/", guard(secret, func(w http.ResponseWriter, r *http.Request, body []byte) {
		toolID := strings.TrimPrefix(r.URL.Path, "/api/agent/v1/tools/")
		var req struct {
			OrgID         string                 `json:"orgId"`
			ActingUserUID string                 `json:"actingUserUid"`
			Args          map[string]interface{} `json:"args"`
		}
		_ = json.Unmarshal(body, &req)
		log.Printf("[tool]     %-22s args=%v  (org=%s actor=%s)", toolID, req.Args, req.OrgID, req.ActingUserUID)
		writeJSON(w, http.StatusOK, mockTool(toolID, req.Args))
	}))

	addr := ":" + port
	log.Printf("mockerp: listening on http://localhost%s  (AGENT_GATEWAY_SECRET=%q, default role=%q)", addr, secret, role)
	log.Printf("mockerp: point the app at it with MSHALIA_API_URL=http://localhost%s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// guard verifies the HMAC signature and ±5-minute timestamp skew before calling
// the inner handler — the same check the real gateway must perform.
func guard(secret string, next func(http.ResponseWriter, *http.Request, []byte)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, errBody("method not allowed", "METHOD"))
			return
		}
		body, _ := io.ReadAll(r.Body)
		ts := r.Header.Get("x-swa-timestamp")
		sig := r.Header.Get("x-swa-signature")

		expected := sign(secret, ts, string(body))
		if sig == "" || !hmac.Equal([]byte(sig), []byte(expected)) {
			log.Printf("[401]      bad signature on %s (ts=%s)", r.URL.Path, ts)
			writeJSON(w, http.StatusUnauthorized, errBody("signature invalid", "UNAUTHORIZED"))
			return
		}
		if ms, err := strconv.ParseInt(ts, 10, 64); err == nil {
			if d := time.Since(time.UnixMilli(ms)); d > 5*time.Minute || d < -5*time.Minute {
				log.Printf("[401]      timestamp skew on %s (%s)", r.URL.Path, d)
				writeJSON(w, http.StatusUnauthorized, errBody("timestamp skew", "UNAUTHORIZED"))
				return
			}
		}
		next(w, r, body)
	}
}

// mockTool returns plausible canned data per tool id so the LLM has something
// real to reason over. Reads return sample records; writes echo success.
func mockTool(toolID string, args map[string]interface{}) map[string]any {
	switch toolID {
	// --- operations reads ---
	case "get_horse", "get_my_horse":
		return ok(map[string]any{"id": "horse_najm", "nameEn": "Najm", "nameAr": "نجم", "breed": "Arabian", "gender": "stallion", "stall": "A-12", "status": "active", "ownerId": "client_1"})
	case "get_care_plan":
		return ok(map[string]any{"horseId": "horse_najm", "turnoutMinutes": 120, "feeding": "3x daily, no grain", "specialInstructions": "cold-hose left foreleg after turnout"})
	case "list_tasks":
		return ok([]map[string]any{
			{"id": "task_1", "title": "Feed Najm", "status": "pending", "horseId": "horse_najm", "assigneeId": "user_2"},
			{"id": "task_2", "title": "Turnout Sahra", "status": "completed", "horseId": "horse_sahra", "assigneeId": "user_2"},
			{"id": "task_3", "title": "Muck stall A-12", "status": "pending", "horseId": "horse_najm", "assigneeId": "user_3"},
		})
	case "list_horses", "list_available_horses", "list_breeding_stock":
		return ok([]map[string]any{
			{"id": "horse_najm", "nameEn": "Najm", "nameAr": "نجم", "breed": "Arabian", "gender": "stallion", "status": "active"},
			{"id": "horse_sahra", "nameEn": "Sahra", "nameAr": "صحراء", "breed": "Arabian", "gender": "mare", "status": "active"},
		})
	case "list_stalls", "list_available_stalls":
		return ok([]map[string]any{
			{"id": "stall_a12", "code": "A-12", "barnId": "barn_a", "status": "occupied"},
			{"id": "stall_a13", "code": "A-13", "barnId": "barn_a", "status": "empty"},
		})
	case "get_stall_availability":
		return ok(map[string]any{"empty": 4, "occupied": 20, "maintenance": 1})
	case "list_incidents":
		return ok([]map[string]any{{"id": "inc_1", "horseId": "horse_najm", "title": "Minor cut", "severity": "low", "resolved": false}})

	// --- accounting / admin / client reads ---
	case "list_invoices", "list_my_invoices":
		return ok([]map[string]any{
			{"id": "inv_1", "clientId": "client_1", "total": 1500, "currency": "SAR", "status": "overdue"},
			{"id": "inv_2", "clientId": "client_1", "total": 800, "currency": "SAR", "status": "paid"},
		})
	case "get_invoice":
		return ok(map[string]any{"id": "inv_1", "clientId": "client_1", "total": 1500, "currency": "SAR", "status": "overdue", "lineItems": []map[string]any{{"desc": "Boarding — July", "amount": 1500}}})
	case "list_clients":
		return ok([]map[string]any{{"id": "client_1", "name": "Abu Fahd", "phone": "966500000001", "horses": []string{"horse_najm"}}})
	case "get_client":
		return ok(map[string]any{"id": "client_1", "name": "Abu Fahd", "phone": "966500000001", "email": "abufahd@example.com", "horses": []string{"horse_najm"}})
	case "list_contracts", "list_my_contracts":
		return ok([]map[string]any{{"id": "ctr_1", "clientId": "client_1", "status": "active", "monthly": 1500}})
	case "get_contract":
		return ok(map[string]any{"id": "ctr_1", "clientId": "client_1", "status": "active", "monthly": 1500, "startDate": "2026-01-01"})
	case "get_my_balance":
		return ok(map[string]any{"outstanding": 1500, "currency": "SAR"})
	case "get_my_statement":
		return ok([]map[string]any{{"date": "2026-07-01", "type": "invoice", "amount": 1500}, {"date": "2026-07-05", "type": "payment", "amount": -800}})
	case "list_packages":
		return ok([]map[string]any{{"id": "pkg_full", "name": "Full Board", "monthly": 1500}, {"id": "pkg_pasture", "name": "Pasture Board", "monthly": 700}})
	case "list_foals":
		return ok([]map[string]any{{"id": "foal_1", "dam": "horse_sahra", "sire": "horse_najm", "born": "2026-03-14"}})
	case "get_pregnancy_status":
		return ok(map[string]any{"mareId": "horse_sahra", "pregnant": true, "dueDate": "2027-02-20", "lastUltrasound": "2026-06-30"})
	case "recommend_bloodline":
		return ok(map[string]any{"compatible": true, "inbreedingCoefficient": 0.03, "note": "no shared ancestors within 4 generations"})

	// --- writes: echo success ---
	case "update_task_status", "assign_stall", "register_horse", "check_in_horse", "check_out_horse",
		"report_incident", "book_vet_appointment", "record_treatment_plan",
		"record_expense", "record_payment", "book_tour", "submit_inquiry", "book_breeding":
		return ok(map[string]any{"status": "success", "toolId": toolID, "applied": args})

	default:
		return ok(map[string]any{"toolId": toolID, "note": "mock response (add a case in cmd/mockerp for richer data)", "args": args})
	}
}

func sign(secret, ts, body string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(ts + "." + body))
	return hex.EncodeToString(m.Sum(nil))
}

func ok(data any) map[string]any { return map[string]any{"ok": true, "data": data} }

func errBody(msg, code string) map[string]any {
	return map[string]any{"ok": false, "error": msg, "code": code}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
