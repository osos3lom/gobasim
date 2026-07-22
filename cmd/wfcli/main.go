// Command wfcli drives the Sawt reasoning workflow from the terminal. It feeds
// one text message through the exact engine main.go uses for inbound WhatsApp
// messages — identity resolve -> intent classify -> role-filtered tool loop ->
// reply — against a real LLM and a real database, without needing WhatsApp, STT,
// or TTS wired up. It is the fastest way to "check the workflow" locally.
//
// Point it at cmd/mockerp for a working ERP, or pass -role to inject a synthetic
// identity when no ERP is configured (the tool calls then fail, but you still see
// intent classification and which tools the model tried to call).
//
// Examples:
//
//	go run ./cmd/wfcli -role manager "how many tasks are pending?"
//	go run ./cmd/wfcli -role client "ما هو رصيدي؟"
//	echo "tell me about the horse Najm" | go run ./cmd/wfcli -role viewer
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
	"sawt-go/database"
	"sawt-go/internal/erp"
	"sawt-go/internal/workflow"
)

func main() {
	phone := flag.String("phone", "966500000000", "WhatsApp phone number (the identity key)")
	chat := flag.String("chat", "", "chat id (default <phone>@s.whatsapp.net)")
	role := flag.String("role", "", "inject a synthetic identity with this role (client|viewer|manager|admin|super_admin) instead of calling the ERP identity/resolve")
	org := flag.String("org", "org_test", "org id for the injected identity")
	envFile := flag.String("env", ".env", "env file to load (KEY=VALUE lines)")
	flag.Parse()

	_ = config.LoadDotEnv(*envFile)

	msg := strings.TrimSpace(strings.Join(flag.Args(), " "))
	if msg == "" {
		fmt.Fprint(os.Stderr, "message> ")
		sc := bufio.NewScanner(os.Stdin)
		if sc.Scan() {
			msg = strings.TrimSpace(sc.Text())
		}
	}
	if msg == "" {
		log.Fatal("no message provided (pass it as arguments or on stdin)")
	}

	chatID := *chat
	if chatID == "" {
		chatID = *phone + "@s.whatsapp.net"
	}

	cfg := config.LoadConfig()
	if cfg.DatabaseURL == "" {
		log.Fatal("DATABASE_URL is not set (put it in .env or the environment)")
	}

	ctx := context.Background()
	pool, err := database.InitPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer pool.Close()

	// Best-effort schema bootstrap so wfcli works against a fresh DB branch
	// (the main app normally does this at boot). Idempotent CREATE IF NOT EXISTS.
	if data, err := os.ReadFile("schema.sql"); err == nil {
		if _, err := pool.Exec(ctx, string(data)); err != nil {
			log.Printf("schema bootstrap warning: %v", err)
		}
	} else {
		log.Printf("schema.sql not found (run from the repo root); assuming schema already applied")
	}

	queries := database.New(pool)
	erpClient := erp.NewClient(cfg.MshaliaAPIURL, cfg.AgentGatewaySecret)
	engine := workflow.NewWorkflowEngine(cfg, erpClient, queries)

	// Identity: injected (-role) or resolved through the ERP gateway.
	var identity *erp.Identity
	if *role != "" {
		identity = &erp.Identity{UID: "local-user", Phone: *phone, Role: *role, DisplayName: "Local Test User", OrgIDs: []string{*org}}
		log.Printf("identity: injected role=%q org=%q (skipping ERP identity/resolve)", *role, *org)
	} else {
		identity, err = erpClient.ResolveIdentity(ctx, *phone)
		switch {
		case err != nil:
			log.Printf("identity/resolve failed (%v) — continuing UNLINKED; pass -role to inject one", err)
		case identity == nil:
			log.Printf("identity/resolve: phone %s is UNLINKED (no ERP account)", *phone)
		default:
			if erp.ApplyDefaultOrg(identity, cfg.DefaultOrgID) {
				log.Printf("identity: applied DEFAULT_ORG_ID=%q fallback for privileged orgless actor %s", cfg.DefaultOrgID, identity.UID)
			}
			log.Printf("identity: uid=%s role=%s org=%v", identity.UID, identity.Role, identity.OrgIDs)
		}
	}

	summary, history, err := engine.LoadConversation(ctx, chatID)
	if err != nil {
		log.Printf("memory load warning: %v", err)
	}
	messages := append(history, workflow.Message{Role: "user", Content: msg})

	state := &workflow.State{
		Messages:      messages,
		ActorIdentity: identity,
		ChatID:        chatID,
		Summary:       summary,
	}

	fmt.Fprintf(os.Stderr, "\n> %s\n", msg)
	start := time.Now()
	if err := engine.Execute(ctx, state); err != nil {
		log.Fatalf("workflow execute: %v", err)
	}
	engine.SaveTurns(ctx, chatID, msg, state.FinalReply)

	fmt.Println("\n──────── result ────────")
	fmt.Printf("chat:    %s\n", chatID)
	fmt.Printf("intent:  %s\n", nz(state.Intent))
	fmt.Printf("tools:   %d call(s)\n", len(state.ToolResults))
	if len(state.ToolResults) > 0 {
		b, _ := json.MarshalIndent(state.ToolResults, "         ", "  ")
		fmt.Printf("         %s\n", string(b))
	}
	fmt.Printf("elapsed: %s\n", time.Since(start).Round(time.Millisecond))
	fmt.Printf("\nreply:\n%s\n", state.FinalReply)
}

func nz(s string) string {
	if s == "" {
		return "(none / general chat)"
	}
	return s
}
