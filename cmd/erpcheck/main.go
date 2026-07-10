// Command erpcheck exercises the mshalia ERP Agent Gateway's horse workflows
// directly through our real HMAC-signing client (internal/erp) — no LLM, no
// WhatsApp, no local DB. It resolves an identity, lists horses (the "how many"
// answer), optionally registers a new horse, then lists again to confirm the
// count changed. This is the fastest way to prove the Go↔mshalia contract and
// the horse tools work against a live Firestore.
//
// Requires MSHALIA_API_URL (e.g. http://localhost:3000) and AGENT_GATEWAY_SECRET
// (identical to mshalia's .env.local) in the environment or -env file.
//
//	go run ./cmd/erpcheck -phone 9665XXXXXXXX             # list + count
//	go run ./cmd/erpcheck -phone 9665XXXXXXXX -add        # + register a test horse
//	go run ./cmd/erpcheck -uid <uid> -org <orgId> -add    # skip identity resolve
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"sawt-go/config"
	"sawt-go/internal/erp"
)

func main() {
	envFile := flag.String("env", ".env", "env file with MSHALIA_API_URL + AGENT_GATEWAY_SECRET")
	phone := flag.String("phone", "", "staff phone to resolve (WhatsApp format, e.g. 9665XXXXXXXX)")
	uid := flag.String("uid", "", "acting user uid (use with -org to skip identity/resolve)")
	org := flag.String("org", "", "org id (use with -uid)")
	add := flag.Bool("add", false, "register a test horse and re-count")
	name := flag.String("name", "", "name for the -add horse (default: auto-generated)")
	flag.Parse()

	loadDotEnv(*envFile)
	cfg := config.LoadConfig()
	if cfg.AgentGatewaySecret == "" {
		log.Fatal("AGENT_GATEWAY_SECRET is not set — it must match mshalia's .env.local value")
	}
	fmt.Printf("ERP gateway: %s\n", cfg.MshaliaAPIURL)

	client := erp.NewClient(cfg.MshaliaAPIURL, cfg.AgentGatewaySecret)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// ── resolve the acting identity ──────────────────────────────────────────
	var actingUID, orgID, role string
	switch {
	case *uid != "" && *org != "":
		actingUID, orgID, role = *uid, *org, "(provided)"
		fmt.Printf("identity:    uid=%s org=%s (via flags)\n", actingUID, orgID)
	case *phone != "":
		id, err := client.ResolveIdentity(ctx, *phone)
		if err != nil {
			log.Fatalf("identity/resolve error: %v\n(is mshalia running and AGENT_GATEWAY_SECRET matched?)", err)
		}
		if id == nil {
			log.Fatalf("phone %q did NOT resolve (unknown_phone or no_role).\n"+
				"Seed a Firestore users/{uid} doc with this phone + a role, or pass -uid/-org.", *phone)
		}
		actingUID, role = id.UID, id.Role
		if len(id.OrgIDs) > 0 {
			orgID = id.OrgIDs[0]
		}
		fmt.Printf("identity:    uid=%s role=%s org=%s\n", actingUID, role, orgID)
	default:
		log.Fatal("provide -phone, or both -uid and -org")
	}
	if orgID == "" {
		log.Fatal("resolved identity has no org id — cannot scope horse queries")
	}
	fmt.Println()

	// ── 1. list_horses (how many?) ───────────────────────────────────────────
	before := listHorses(ctx, client, orgID, actingUID)

	// ── 2. register_horse (add) ──────────────────────────────────────────────
	if *add {
		hn := *name
		if hn == "" {
			hn = fmt.Sprintf("Test Horse %d", time.Now().Unix()%100000)
		}
		fmt.Printf("\nregister_horse: adding %q ...\n", hn)
		res, err := client.CallTool(ctx, "register_horse", orgID, actingUID, map[string]interface{}{
			"nameEn": hn, "nameAr": hn, "breed": "Arabian", "color": "Bay", "gender": "stallion",
		})
		if err != nil {
			log.Fatalf("register_horse transport error: %v", err)
		}
		if !okResult(res) {
			fmt.Printf("register_horse FAILED: %v (code %v)\n", res["error"], res["code"])
			fmt.Println("(needs role >= manager and the approve_services scope)")
			return
		}
		fmt.Printf("register_horse OK: %s\n", compact(res["data"]))

		after := listHorses(ctx, client, orgID, actingUID)
		fmt.Printf("\n=> horse count: %d -> %d  (%+d)\n", before, after, after-before)
	}
}

// listHorses calls the list_horses tool and prints a summary; returns the count.
func listHorses(ctx context.Context, client *erp.Client, orgID, uid string) int {
	res, err := client.CallTool(ctx, "list_horses", orgID, uid, map[string]interface{}{})
	if err != nil {
		log.Fatalf("list_horses transport error: %v", err)
	}
	if !okResult(res) {
		fmt.Printf("list_horses FAILED: %v (code %v)\n", res["error"], res["code"])
		return -1
	}
	data, _ := res["data"].(map[string]interface{})
	horses, _ := data["horses"].([]interface{})
	fmt.Printf("list_horses: %d horse(s)\n", len(horses))
	for i, h := range horses {
		if i >= 8 {
			fmt.Printf("   … and %d more\n", len(horses)-8)
			break
		}
		m, _ := h.(map[string]interface{})
		fmt.Printf("   - %v  (%v, %v, %v)\n", str(m["nameEn"]), str(m["breed"]), str(m["gender"]), str(m["status"]))
	}
	return len(horses)
}

func okResult(res map[string]interface{}) bool { ok, _ := res["ok"].(bool); return ok }

func str(v interface{}) string {
	if v == nil || v == "" {
		return "—"
	}
	return fmt.Sprintf("%v", v)
}

func compact(v interface{}) string { b, _ := json.Marshal(v); return string(b) }

func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if i := strings.Index(val, " #"); i >= 0 {
			val = strings.TrimSpace(val[:i])
		}
		if key != "" {
			os.Setenv(key, val)
		}
	}
}
