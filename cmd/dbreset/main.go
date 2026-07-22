// Command dbreset wipes the Neon/Postgres database and rebuilds it from the
// current schema.sql, so the DB matches the Go migration exactly.
//
// Usage:
//
//	DATABASE_URL=postgres://... go run ./cmd/dbreset -mode=app
//	DATABASE_URL=postgres://... go run ./cmd/dbreset -mode=full -yes
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// appTables are every table schema.sql owns. whatsmeow's own whatsmeow_* /
// whatsmeow_version tables are intentionally not listed here — "app" mode
// leaves them alone so an existing WhatsApp pairing survives the reset.
var appTables = []string{
	"pending_confirmations",
	"conversation_state",
	"conversation_turns",
	"wa_activity",
	"wa_contacts",
	"wa_messages",
	"wa_voice_notes",
	"processed_messages",
	"tool_executions",
	"users",
	"agents",
	"stt_history",
	"tts_history",
	"settings",
}

func main() {
	mode := flag.String("mode", "app", `"app" drops only Sawt's own tables (keeps WhatsApp pairing intact); "full" drops the entire public schema (also wipes the WhatsApp pairing — you'll need to re-link)`)
	yes := flag.Bool("yes", false, "skip the interactive 'type RESET' confirmation prompt")
	schemaPath := flag.String("schema", "schema.sql", "path to schema.sql to reapply after the reset")
	flag.Parse()

	if *mode != "app" && *mode != "full" {
		log.Fatalf("invalid -mode %q: must be 'app' or 'full'", *mode)
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL is not set")
	}

	schemaSQL, err := os.ReadFile(*schemaPath)
	if err != nil {
		log.Fatalf("failed to read schema file %q (run from the repo root, or pass -schema): %v", *schemaPath, err)
	}

	fmt.Printf("\nThis will PERMANENTLY DELETE data — mode: %q\n", *mode)
	if *mode == "full" {
		fmt.Println("FULL mode drops the entire public schema, including the WhatsApp device pairing.")
		fmt.Println("You will need to re-pair WhatsApp (QR code or PAIR_PHONE_NUMBER) afterwards.")
	} else {
		fmt.Println("APP mode drops only Sawt's own tables (contacts, activity, agents, users,")
		fmt.Println("conversation memory, pending confirmations). WhatsApp pairing is left intact.")
	}
	fmt.Println("This cannot be undone.")

	if !*yes {
		fmt.Print("\nType RESET to continue: ")
		reader := bufio.NewReader(os.Stdin)
		input, _ := reader.ReadString('\n')
		if strings.TrimSpace(input) != "RESET" {
			fmt.Println("Aborted — no changes made.")
			os.Exit(1)
		}
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("failed to connect: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("failed to ping database: %v", err)
	}

	if *mode == "full" {
		log.Println("Dropping and recreating the public schema...")
		if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
			log.Fatalf("failed to reset schema: %v", err)
		}
	} else {
		log.Println("Dropping Sawt application tables...")
		stmt := "DROP TABLE IF EXISTS " + strings.Join(appTables, ", ") + " CASCADE;"
		if _, err := pool.Exec(ctx, stmt); err != nil {
			log.Fatalf("failed to drop application tables: %v", err)
		}
	}

	log.Println("Reapplying schema.sql to rebuild tables matching the current Go migration...")
	if _, err := pool.Exec(ctx, string(schemaSQL)); err != nil {
		log.Fatalf("failed to reapply schema: %v", err)
	}

	log.Println("Done. Database schema now matches the current Go migration.")
	if *mode == "app" {
		log.Println("WhatsApp pairing preserved — no need to re-scan.")
	} else {
		log.Println("Start the app and re-pair WhatsApp (QR code or PAIR_PHONE_NUMBER).")
	}
}
