// Package main implements a test and run harness to exercise the Sawt Web server
// and templates against the live Neon database without active WhatsApp connections.
package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"sawt-go/config"
	"sawt-go/database"
	"sawt-go/internal/speech"
	waClient "sawt-go/internal/whatsmeow"
	"sawt-go/web"
)

// loadDotEnv reads key=val pairs from a file and sets them in the environment.
func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
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
		// Strip trailing " # inline comment"
		if i := strings.Index(val, " #"); i >= 0 {
			val = strings.TrimSpace(val[:i])
		}
		if key != "" {
			os.Setenv(key, val)
		}
	}
	return sc.Err()
}

// readSchemaFile attempts to locate schema.sql in likely directory paths.
func readSchemaFile() (string, error) {
	paths := []string{"schema.sql", "../schema.sql", "../../schema.sql", "/opt/sawt/schema.sql"}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err == nil {
			log.Printf("Loaded schema from: %s", p)
			return string(data), nil
		}
	}
	return "", fmt.Errorf("schema.sql not found in any expected location")
}

func main() {
	log.Println("Starting Sawt Production readiness harness...")

	// 1. Try to load .env configuration
	if err := loadDotEnv(".env"); err != nil {
		log.Printf("Notice: could not read .env: %v (will rely on environment variables)", err)
	}

	// Override port for the harness if not set, or default to 8091 to avoid collision with standard runs
	if os.Getenv("PORT") == "" {
		os.Setenv("PORT", "8091")
	}

	// Disable secure cookies for local HTTP testing in the harness
	os.Setenv("SECURE_COOKIE", "false")

	cfg := config.LoadConfig()
	if cfg.DatabaseURL == "" {
		log.Fatal("Fatal: DATABASE_URL is not configured.")
	}

	ctx := context.Background()

	// 2. Initialize and ping database connection pool
	log.Printf("Connecting to database: %s...", cfg.DatabaseURL)
	pool, err := database.InitPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Fatal: Database connection/ping failed: %v", err)
	}
	defer pool.Close()
	log.Println("DATABASE CONNECTION OK: Ping succeeded.")

	// 3. Bootstrap database schema
	schemaSQL, err := readSchemaFile()
	if err != nil {
		log.Fatalf("Fatal: %v", err)
	}

	_, err = pool.Exec(ctx, schemaSQL)
	if err != nil {
		log.Fatalf("Fatal: Schema bootstrap failed: %v", err)
	}
	log.Println("Database: Schema bootstrap complete.")

	queries := database.New(pool)

	// Ensure default agent exists
	var agentExists bool
	err = pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM agents WHERE id = 'default')").Scan(&agentExists)
	if err == nil && !agentExists {
		_, err = pool.Exec(ctx, `
			INSERT INTO agents (id, name, status, system_prompt, greeting_message, failure_message)
			VALUES ('default', 'Sawt Operations Agent', 'published', 
			'You are the operations module of Sawt, an ERP assistant for an Arabian horse stable, talking to verified staff over WhatsApp. Use the available tools to answer questions about horses, care plans, and tasks, and to update task status when asked. Always resolve a horse or task by id via get_horse / list_tasks before acting on it — never invent an id. If a name search is ambiguous, ask the user to clarify instead of guessing. Once you have enough information, stop calling tools and answer directly in plain text, in the same language the user used, briefly — the reply may be spoken as a voice note. No markdown.',
			'مرحباً بك في نظام صوت للخيول العربية. كيف يمكنني مساعدتك اليوم؟',
			'عذراً، حدث خطأ أثناء معالجة طلبك. يرجى المحاولة مرة أخرى.')
		`)
		if err != nil {
			log.Printf("Database Warning: Failed to seed default agent: %v", err)
		} else {
			log.Println("Database: Default operations agent seeded successfully.")
		}
	}

	// 4. Construct WhatsApp Manager without active client initialization
	// This satisfies GetStatus() returning "disconnected" when Client == nil
	waMgr := waClient.NewWhatsAppManager(cfg.DatabaseURL)
	ttsOrch := speech.NewTTSOrchestrator(cfg)

	// 5. Initialize Web Server and Router
	webServer := web.NewServer(cfg, queries, waMgr, ttsOrch)
	router := webServer.GetRouter()

	// 6. Setup dev/preview helper login bypass route
	username := cfg.AdminUsername
	if username == "" {
		username = "admin"
	}
	auth := web.NewAuthManager(cfg, queries)

	mux := http.NewServeMux()
	mux.HandleFunc("/preview-login", func(w http.ResponseWriter, r *http.Request) {
		cookie := &http.Cookie{
			Name:     web.SessionCookieName,
			Value:    auth.GenerateCookieValue(username, 3600*1e9), // 1 hour expiration in ns
			Path:     "/",
			HttpOnly: true,
		}
		// Match the secure cookie config
		if cfg.SecureCookie {
			cookie.Secure = true
		}
		http.SetCookie(w, cookie)
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
	})
	mux.Handle("/", router)

	log.Printf("Harness running on http://localhost:%s", cfg.Port)
	log.Printf("To test authenticated routes, visit: http://localhost:%s/preview-login", cfg.Port)
	if err := http.ListenAndServe(":"+cfg.Port, mux); err != nil {
		log.Fatalf("Fatal: HTTP server failed: %v", err)
	}
}
