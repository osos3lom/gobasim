package database

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"sawt-go/config"
	"sawt-go/internal/monitor"
)

// SeedAdminUser seeds an admin user on first boot only (or updates when ADMIN_PASSWORD is set).
// Credentials come from ADMIN_USERNAME/ADMIN_PASSWORD; if no password is provided a
// random one is generated and printed to stderr.
func SeedAdminUser(ctx context.Context, pool *pgxpool.Pool, queries *Queries, cfg *config.Config) error {
	username := cfg.AdminUsername
	if username == "" {
		username = "admin"
	}

	if cfg.AdminPassword != "" {
		hashedPassword, err := bcrypt.GenerateFromPassword([]byte(cfg.AdminPassword), bcrypt.DefaultCost)
		if err != nil {
			return fmt.Errorf("failed to hash admin password: %w", err)
		}
		_, err = pool.Exec(ctx, `
			INSERT INTO users (username, password_hash)
			VALUES ($1, $2)
			ON CONFLICT (username)
			DO UPDATE SET password_hash = EXCLUDED.password_hash
		`, username, string(hashedPassword))
		if err != nil {
			return fmt.Errorf("failed to upsert admin user: %w", err)
		}
		log.Printf("Database: Admin user '%s' synced from environment configuration.", username)
		return nil
	}

	var userCount int64
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM users").Scan(&userCount); err != nil {
		return fmt.Errorf("failed to count users: %w", err)
	}
	if userCount > 0 {
		return nil
	}

	password := cfg.AdminPassword
	generated := false
	if password == "" {
		buf := make([]byte, 12)
		if _, err := rand.Read(buf); err != nil {
			return fmt.Errorf("failed to generate random password: %w", err)
		}
		password = hex.EncodeToString(buf)
		generated = true
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("failed to hash admin password: %w", err)
	}

	if _, err := queries.CreateUser(ctx, CreateUserParams{
		Username:     username,
		PasswordHash: string(hashedPassword),
	}); err != nil {
		return fmt.Errorf("failed to create admin user: %w", err)
	}

	if generated {
		fmt.Fprintf(os.Stderr, "\nDatabase: Seeded admin user %q with a generated one-time password: %s\n", username, password)
		fmt.Fprintln(os.Stderr, "Database: ^^ This password will NOT be shown again. Log in and change it, or set ADMIN_USERNAME/ADMIN_PASSWORD.")
	} else {
		log.Printf("Database: Seeded admin user %q from ADMIN_USERNAME/ADMIN_PASSWORD env vars.", username)
	}
	return nil
}

// SeedDefaultAgent ensures the default operations agent exists in the database.
func SeedDefaultAgent(ctx context.Context, pool *pgxpool.Pool) error {
	var agentExists bool
	err := pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM agents WHERE id = 'default')").Scan(&agentExists)
	if err != nil {
		return fmt.Errorf("failed to check default agent existence: %w", err)
	}
	if agentExists {
		return nil
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO agents (id, name, status, system_prompt, greeting_message, failure_message)
		VALUES ('default', 'Sawt Operations Agent', 'published', 
		'You are the operations module of Sawt, an ERP assistant for an Arabian horse stable, talking to verified staff over WhatsApp. Use the available tools to answer questions about horses, care plans, and tasks, and to update task status when asked. Always resolve a horse or task by id via get_horse / list_tasks before acting on it — never invent an id. If a name search is ambiguous, ask the user to clarify instead of guessing. Once you have enough information, stop calling tools and answer directly in plain text, in the same language the user used, briefly — the reply may be spoken as a voice note. No markdown.',
		'مرحباً بك في نظام صوت للخيول العربية. كيف يمكنني مساعدتك اليوم؟',
		'عذراً، حدث خطأ أثناء معالجة طلبك. يرجى المحاولة مرة أخرى.')
	`)
	if err != nil {
		return fmt.Errorf("failed to insert default agent: %w", err)
	}
	log.Println("Database: Default operations agent seeded successfully.")
	return nil
}

// RunRetention deletes or redacts PII-bearing rows older than the retention window.
func RunRetention(ctx context.Context, queries *Queries, days int) {
	cutoff := time.Now().AddDate(0, 0, -days)
	log.Printf("Retention: purging/redacting rows older than %s (%d days)...", cutoff.Format("2006-01-02"), days)

	for name, fn := range map[string]func(context.Context, time.Time) error{
		"stt_history":           queries.PurgeSttHistoryBefore,
		"tts_history":           queries.PurgeTtsHistoryBefore,
		"conversation_turns":    queries.PurgeConversationTurnsBefore,
		"wa_activity":           queries.RedactWaActivityBefore,
		"wa_messages":           queries.RedactWaMessagesBefore,
		"wa_voice_notes":        queries.PurgeWaVoiceNotesBefore,
		"processed_messages":   queries.PurgeProcessedMessagesBefore,
		"tool_executions":      queries.PurgeToolExecutionsBefore,
		"pending_confirmations": queries.PurgeExpiredConfirmations,
	} {
		if err := fn(ctx, cutoff); err != nil {
			monitor.ReportError(ctx, "retention:"+name, err)
		}
	}
	log.Println("Retention: pass complete.")
}
