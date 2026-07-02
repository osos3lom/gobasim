package main

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sawt-go/config"
	"sawt-go/database"
	"sawt-go/internal/audio"
	"sawt-go/internal/erp"
	"sawt-go/internal/ratelimit"
	"sawt-go/internal/speech"
	waClient "sawt-go/internal/whatsmeow"
	"sawt-go/internal/workflow"
	"sawt-go/web"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"golang.org/x/crypto/bcrypt"
	googleProto "google.golang.org/protobuf/proto"
)

// inboundLimiter throttles how many messages a single WhatsApp chat can push
// through the STT/LLM/ERP pipeline: 8 messages per rolling minute per chat.
var inboundLimiter = ratelimit.New(8, time.Minute)

//go:embed schema.sql
var schemaSQL string

func main() {
	log.Println("Starting Sawt Unified Daemon...")

	cfg := config.LoadConfig()

	// 1. Initialize Database pgx connection pool
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool, err := database.InitPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Fatal: Database initialization failed: %v", err)
	}
	defer pool.Close()

	// Bootstrap database schema if tables/indexes do not exist
	log.Println("Database: Checking/bootstrapping schema...")
	_, err = pool.Exec(ctx, schemaSQL)
	if err != nil {
		log.Fatalf("Fatal: Database schema bootstrap failed: %v", err)
	}
	log.Println("Database: Schema bootstrap complete.")

	queries := database.New(pool)

	// Seed an admin user on first boot only (empty users table). Credentials
	// come from ADMIN_USERNAME/ADMIN_PASSWORD; if no password is provided a
	// random one is generated and printed exactly once.
	if err := seedAdminUser(ctx, pool, queries, cfg); err != nil {
		log.Printf("Database: Warning: admin user seeding failed: %v", err)
	}

	// Ensure default agent exists in database
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
			log.Printf("Database: Warning: Failed to seed default agent: %v", err)
		} else {
			log.Println("Database: Default operations agent seeded successfully.")
		}
	}

	// 2. Initialize Speech & Workflow Orchestrators
	sttOrch := speech.NewSTTOrchestrator(cfg)
	ttsOrch := speech.NewTTSOrchestrator(cfg)
	erpClient := erp.NewClient(cfg.MshaliaAPIURL, cfg.AgentGatewaySecret)
	wfEngine := workflow.NewWorkflowEngine(cfg, erpClient, queries)

	// 3. Initialize WhatsApp Connection Manager
	waMgr := waClient.NewWhatsAppManager(cfg.DatabaseURL)
	
	err = waMgr.Initialize(ctx, func(evt interface{}) {
		switch v := evt.(type) {
		case *events.Message:
			go handleIncomingMessage(ctx, waMgr.Client, v, queries, sttOrch, ttsOrch, erpClient, wfEngine)
		case *events.Connected:
			waMgr.SetState(waClient.StateConnected, "", "")
			log.Println("WhatsApp connection established.")
		case *events.LoggedOut:
			waMgr.SetState(waClient.StateDisconnected, "", "")
			log.Println("Logged out from WhatsApp. Re-pairing is required.")
		}
	})
	if err != nil {
		log.Fatalf("Fatal: WhatsApp manager initialization failed: %v", err)
	}

	// Connect Whatsmeow client
	err = waMgr.Connect(ctx)
	if err != nil {
		log.Fatalf("Fatal: Failed to connect WhatsApp client: %v", err)
	}

	// Start background QR Code generator if client is not logged in
	if waMgr.Client.Store.ID == nil {
		go func() {
			qrChan, err := waMgr.Client.GetQRChannel(ctx)
			if err != nil {
				log.Printf("Failed to get QR channel: %v", err)
				return
			}
			for qr := range qrChan {
				if qr.Event == "code" {
					// Cache the QR code in the manager for the dashboard
					waMgr.SetState(waClient.StateQRReady, qr.Code, "")
					
					// Print to console terminal
					log.Println("New WhatsApp pairing QR Code generated. Displaying in console:")
					qrterminal.GenerateHalfBlock(qr.Code, qrterminal.L, os.Stdout)
				} else {
					log.Printf("QR Channel Event: %s", qr.Event)
				}
			}
		}()
	}

	// Trigger pairing code if phone configuration is set at VM startup
	if waMgr.Client.Store.ID == nil && cfg.PairPhoneNumber != "" {
		go func() {
			select {
			case <-time.After(3 * time.Second): // wait for connection to stabilize
				phoneNum := strings.ReplaceAll(cfg.PairPhoneNumber, "+", "")
				code, err := waMgr.RequestPairingCode(phoneNum)
				if err != nil {
					log.Printf("Failed to generate VM startup pairing code: %v", err)
				} else {
					log.Printf("\n============================================\n  STARTUP PHONE PAIRING CODE: %s\n============================================\n", code)
				}
			case <-ctx.Done():
				return
			}
		}()
	}

	// 4. Start Dashboard HTTP Web Server
	webServer := web.NewServer(cfg, queries, waMgr)
	router := webServer.GetRouter()

	go func() {
		log.Printf("Web Dashboard serving at http://localhost:%s\n", cfg.Port)
		server := &http.Server{
			Addr:    ":" + cfg.Port,
			Handler: router,
		}
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("Web server error: %v", err)
		}
	}()

	// Keep background listener active
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c

	log.Println("Shutting down daemon gracefully...")
	waMgr.Client.Disconnect()
	log.Println("Shutdown complete.")
}

func seedAdminUser(ctx context.Context, pool *pgxpool.Pool, queries *database.Queries, cfg *config.Config) error {
	var userCount int64
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM users").Scan(&userCount); err != nil {
		return fmt.Errorf("failed to count users: %w", err)
	}
	if userCount > 0 {
		return nil
	}

	username := cfg.AdminUsername
	if username == "" {
		username = "admin"
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

	if _, err := queries.CreateUser(ctx, database.CreateUserParams{
		Username:     username,
		PasswordHash: string(hashedPassword),
	}); err != nil {
		return fmt.Errorf("failed to create admin user: %w", err)
	}

	if generated {
		log.Printf("Database: Seeded admin user %q with a generated one-time password: %s", username, password)
		log.Println("Database: ^^ This password will NOT be shown again. Log in and change it, or set ADMIN_USERNAME/ADMIN_PASSWORD.")
	} else {
		log.Printf("Database: Seeded admin user %q from ADMIN_USERNAME/ADMIN_PASSWORD.", username)
	}
	return nil
}

func handleIncomingMessage(
	ctx context.Context,
	client *whatsmeow.Client,
	evt *events.Message,
	queries *database.Queries,
	sttOrch *speech.STTOrchestrator,
	ttsOrch *speech.TTSOrchestrator,
	erpClient *erp.Client,
	wfEngine *workflow.WorkflowEngine,
) {
	if evt.Info.IsFromMe {
		return
	}
	if evt.Info.Chat.String() == "status@broadcast" {
		return
	}
	if evt.Info.IsGroup {
		log.Printf("Inbound: Skipping group message %s from %s", evt.Info.ID, evt.Info.Chat.String())
		return
	}

	log.Printf("Inbound: Received message %s from %s", evt.Info.ID, evt.Info.Chat.String())

	// Throttle per chat so one number can't hammer the LLM/ERP pipeline.
	if allowed, count := inboundLimiter.Allow(evt.Info.Chat.String()); !allowed {
		// Warn only on the first message over the limit; drop the rest silently.
		if count == 9 {
			sendTextReply(client, evt.Info.Chat, "أرسلت رسائل كثيرة خلال وقت قصير. يرجى الانتظار قليلاً ثم المحاولة مرة أخرى.")
		}
		log.Printf("Inbound: Rate limit exceeded for %s (%d msgs/min), dropping message", evt.Info.Chat.String(), count)
		return
	}

	// Look up contact in wa_contacts to verify if they are enabled
	contact, err := queries.GetWaContact(ctx, evt.Info.Chat.String())
	if err != nil {
		// Contact does not exist yet. Auto-create them as enabled:
		phone := strings.Split(evt.Info.Chat.String(), "@")[0]
		name := evt.Info.PushName
		if name == "" {
			name = phone
		}
		contact, err = queries.CreateOrUpdateWaContact(ctx, database.CreateOrUpdateWaContactParams{
			ChatID:         evt.Info.Chat.String(),
			Name:           name,
			Enabled:        true, // Default to true
			AgentID:        nil,
			PromptOverride: nil,
		})
		if err != nil {
			log.Printf("Inbound: Warning: Failed to auto-create contact %s: %v", evt.Info.Chat.String(), err)
		} else {
			log.Printf("Inbound: Auto-created new contact in database: %s (%s)", contact.Name, contact.ChatID)
		}
	} else if !contact.Enabled {
		// Drop message processing if contact is explicitly disabled
		log.Printf("Inbound: Discarding message from disabled contact %s (%s)", contact.Name, contact.ChatID)
		return
	}

	var text string
	var incomingAudio []byte

	// Extract text content
	if evt.Message.Conversation != nil {
		text = *evt.Message.Conversation
	} else if evt.Message.ExtendedTextMessage != nil && evt.Message.ExtendedTextMessage.Text != nil {
		text = *evt.Message.ExtendedTextMessage.Text
	} else if evt.Message.ImageMessage != nil && evt.Message.ImageMessage.Caption != nil {
		text = *evt.Message.ImageMessage.Caption
	} else if evt.Message.VideoMessage != nil && evt.Message.VideoMessage.Caption != nil {
		text = *evt.Message.VideoMessage.Caption
	}

	isAudio := evt.Message.AudioMessage != nil
	var sttProvider, ttsProvider string
	var sttMs, llmMs, ttsMs int32

	// 1. Handle STT if audio message
	if isAudio {
		log.Println("Inbound: Message contains audio. Downloading...")
		incomingAudio, err = client.Download(ctx, evt.Message.AudioMessage)
		if err != nil {
			log.Printf("Inbound: Failed to download audio: %v", err)
			sendTextReply(client, evt.Info.Chat, "عذراً، لم أتمكن من تحميل الرسالة الصوتية.")
			return
		}

		// Transcode OGG/Opus to WAV
		log.Println("Inbound: Transcoding OGG/Opus to WAV...")
		wavBytes, err := audio.OggToWav(incomingAudio)
		if err != nil {
			log.Printf("Inbound: Audio transcoding failed: %v", err)
			sendTextReply(client, evt.Info.Chat, "عذراً، فشل معالجة الملف الصوتي.")
			return
		}

		// Transcribe
		sttStart := time.Now()
		var lang = "ar" // Target Arabic
		text, sttProvider, err = sttOrch.Transcribe(ctx, wavBytes, lang)
		sttMs = int32(time.Since(sttStart).Milliseconds())
		
		if err != nil {
			log.Printf("Inbound: STT Transcription failed: %v", err)
			sendTextReply(client, evt.Info.Chat, "عذراً، لم أتمكن من تحويل الصوت إلى نص.")
			return
		}

		log.Printf("Inbound: Audio transcribed in %dms via %s: '%s'", sttMs, sttProvider, text)

		// Log to stt_history
		_ = queries.CreateSttHistory(ctx, database.CreateSttHistoryParams{
			ID:         evt.Info.ID,
			Transcript: text,
			Filename:   "voice.ogg",
			DurationMs: sttMs,
			Language:   lang,
		})
	}

	if text == "" {
		return
	}

	// 2. Resolve Actor Identity from Phone JID
	var identity *erp.Identity
	phone := strings.Split(evt.Info.Chat.String(), "@")[0]
	identity, err = erpClient.ResolveIdentity(ctx, phone)
	if err != nil {
		log.Printf("Inbound: Identity resolution failed: %v", err)
	}

	// 3. Load conversation memory, then invoke the workflow with real history
	summary, history, err := wfEngine.LoadConversation(ctx, evt.Info.Chat.String())
	if err != nil {
		log.Printf("Inbound: Warning: failed to load conversation history: %v", err)
	}

	messagesHistory := append(history, workflow.Message{
		Role:    "user",
		Content: text,
	})

	state := &workflow.State{
		Messages:      messagesHistory,
		ActorIdentity: identity,
		ChatID:        evt.Info.Chat.String(),
		Summary:       summary,
	}

	llmStart := time.Now()
	err = wfEngine.Execute(ctx, state)
	llmMs = int32(time.Since(llmStart).Milliseconds())

	if err != nil {
		log.Printf("Inbound: Agent workflow execution failed: %v", err)
		sendTextReply(client, evt.Info.Chat, "حدث خطأ أثناء معالجة طلبك.")
		return
	}

	replyText := state.FinalReply
	log.Printf("Inbound: Agent replied in %dms: '%s'", llmMs, replyText)

	// Persist this exchange so future messages carry conversation context.
	wfEngine.SaveTurns(ctx, evt.Info.Chat.String(), text, replyText)

	// 4. Handle TTS if original message was audio
	var replyAudio []byte
	if isAudio && replyText != "" {
		log.Println("Outbound: Synthesizing reply text to speech...")
		ttsStart := time.Now()
		var rawWav []byte
		rawWav, ttsProvider, err = ttsOrch.Synthesize(ctx, replyText, "ar")
		ttsMs = int32(time.Since(ttsStart).Milliseconds())

		if err != nil {
			log.Printf("Outbound: TTS Synthesis failed: %v", err)
		} else {
			// Transcode WAV to OGG/Opus
			log.Println("Outbound: Transcoding WAV to OGG/Opus...")
			replyAudio, err = audio.WavToOpus(rawWav)
			if err != nil {
				log.Printf("Outbound: Audio transcoding failed: %v", err)
				replyAudio = nil // Send text as fallback
			} else {
				// Log to tts_history
				_ = queries.CreateTtsHistory(ctx, database.CreateTtsHistoryParams{
					ID:         evt.Info.ID + "-reply",
					Text:       replyText,
					Model:      ttsProvider,
					Speed:      1.0,
					DurationMs: ttsMs,
					SizeBytes:  int32(len(replyAudio)),
				})
			}
		}
	}

	totalMs := sttMs + llmMs + ttsMs
	var activityStatus = "ok"
	var activityError *string

	// 5. Send reply
	if len(replyAudio) > 0 {
		log.Println("Outbound: Uploading audio response to WhatsApp...")
		resp, err := client.Upload(context.Background(), replyAudio, whatsmeow.MediaAudio)
		if err != nil {
			log.Printf("Outbound: Failed to upload audio: %v, falling back to text", err)
			sendTextReply(client, evt.Info.Chat, replyText)
			
			errStr := err.Error()
			activityStatus = "failed"
			activityError = &errStr
		} else {
			audioMsg := &proto.Message{
				AudioMessage: &proto.AudioMessage{
					URL:           googleProto.String(resp.URL),
					DirectPath:    googleProto.String(resp.DirectPath),
					MediaKey:      resp.MediaKey,
					Mimetype:      googleProto.String("audio/ogg; codecs=opus"),
					PTT:           googleProto.Bool(true),
					FileLength:    googleProto.Uint64(uint64(len(replyAudio))),
					FileSHA256:    resp.FileSHA256,
					FileEncSHA256: resp.FileEncSHA256,
				},
			}
			_, err = client.SendMessage(context.Background(), evt.Info.Chat, audioMsg)
			if err != nil {
				log.Printf("Outbound: Failed to send audio message: %v", err)
				errStr := err.Error()
				activityStatus = "failed"
				activityError = &errStr
			}
		}
	} else if replyText != "" {
		sendTextReply(client, evt.Info.Chat, replyText)
	}

	// 6. Log Activity Log to Neon DB
	toolCallsBytes, _ := json.Marshal(state.ToolResults)
	
	// Fetch the assigned agent for this contact from database
	var assignedAgentID *string
	contact, err = queries.GetWaContact(ctx, evt.Info.Chat.String())
	if err == nil && contact.AgentID != nil {
		assignedAgentID = contact.AgentID
	}

	cfg := config.LoadConfig()
	llmModelPtr := &cfg.NimModel

	var ttsProvPtr *string
	if ttsProvider != "" {
		ttsProvPtr = &ttsProvider
	}

	_ = queries.CreateWaActivity(ctx, database.CreateWaActivityParams{
		ID:          evt.Info.ID,
		ChatID:      evt.Info.Chat.String(),
		ContactName: evt.Info.PushName,
		Direction:   "in",
		MsgType:     "ptt",
		Transcript:  text,
		Reply:       replyText,
		Language:    "ar",
		AgentID:     assignedAgentID,
		LlmModel:    llmModelPtr,
		TtsModel:    ttsProvPtr,
		ToolCalls:   toolCallsBytes,
		SttMs:       sttMs,
		LlmMs:       llmMs,
		TtsMs:       ttsMs,
		TotalMs:     totalMs,
		Status:      activityStatus,
		Error:       activityError,
	})
}

func sendTextReply(client *whatsmeow.Client, chat types.JID, text string) {
	textMsg := &proto.Message{
		Conversation: googleProto.String(text),
	}
	_, err := client.SendMessage(context.Background(), chat, textMsg)
	if err != nil {
		log.Printf("Outbound: Failed to send text reply: %v", err)
	}
}
