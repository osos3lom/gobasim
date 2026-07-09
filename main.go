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
	"os/exec"
	"os/signal"
	"sawt-go/config"
	"sawt-go/database"
	"sawt-go/internal/audio"
	"sawt-go/internal/erp"
	"sawt-go/internal/monitor"
	"sawt-go/internal/ratelimit"
	"sawt-go/internal/speech"
	"sawt-go/internal/trace"
	"sawt-go/internal/voicenotes"
	waClient "sawt-go/internal/whatsmeow"
	"sawt-go/internal/workflow"
	"sawt-go/web"
	"strconv"
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
	monitor.Init(cfg.ErrorWebhookURL)

	// Voice notes require ffmpeg; fail fast at boot instead of on the first
	// voice message. Set ALLOW_MISSING_FFMPEG=true for text-only deploys.
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		if allow, _ := strconv.ParseBool(os.Getenv("ALLOW_MISSING_FFMPEG")); allow {
			log.Println("WARNING: ffmpeg not found on PATH — voice notes will fail (ALLOW_MISSING_FFMPEG=true).")
		} else {
			log.Fatal("Fatal: ffmpeg not found on PATH. Install it, or set ALLOW_MISSING_FFMPEG=true for a text-only deploy.")
		}
	}

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

	// Retention: purge/redact PII-bearing rows older than RETENTION_DAYS
	// (0 disables). Runs at boot and then daily.
	if cfg.RetentionDays > 0 {
		go func() {
			ticker := time.NewTicker(24 * time.Hour)
			defer ticker.Stop()
			for {
				runRetention(ctx, queries, cfg.RetentionDays)
				select {
				case <-ticker.C:
				case <-ctx.Done():
					return
				}
			}
		}()
	} else {
		log.Println("Retention: disabled (RETENTION_DAYS=0) — transcripts are kept indefinitely.")
	}

	// 2. Initialize Speech & Workflow Orchestrators
	sttOrch := speech.NewSTTOrchestrator(cfg)
	ttsOrch := speech.NewTTSOrchestrator(cfg)
	erpClient := erp.NewClient(cfg.MshaliaAPIURL, cfg.AgentGatewaySecret)
	wfEngine := workflow.NewWorkflowEngine(cfg, erpClient, queries)

	// Voice-note archival to Firebase Cloud Storage. Optional: a nil store is
	// inert, so the pipeline needs no feature flags. Init failure only warns —
	// archival must never keep the assistant itself from starting.
	var voiceStore *voicenotes.Store
	if cfg.VoiceStorageBucket != "" {
		up, err := voicenotes.NewGCSUploader(ctx, cfg.VoiceStorageBucket)
		if err != nil {
			log.Printf("WARNING: voice-note archival disabled — GCS client init failed: %v", err)
		} else if voiceStore, err = voicenotes.NewStore(cfg.VoiceStoragePrefix, cfg.VoiceSpoolDir, queries, up); err != nil {
			log.Printf("WARNING: voice-note archival disabled — %v", err)
			voiceStore = nil
		} else {
			voiceStore.StartWorker(ctx)
			log.Printf("Voice notes: archiving to gs://%s/%s (spool: %s)", cfg.VoiceStorageBucket, cfg.VoiceStoragePrefix, cfg.VoiceSpoolDir)
		}
	} else {
		log.Println("Voice notes: archival disabled (VOICE_STORAGE_BUCKET not set).")
	}

	// 3. Initialize WhatsApp Connection Manager
	waMgr := waClient.NewWhatsAppManager(cfg.DatabaseURL)

	err = waMgr.Initialize(ctx, func(evt interface{}) {
		switch v := evt.(type) {
		case *events.Message:
			go handleIncomingMessage(ctx, waMgr.Client, v, queries, sttOrch, ttsOrch, erpClient, wfEngine, voiceStore)
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

	// Acquire the QR channel BEFORE connecting, and synchronously. whatsmeow's
	// GetQRChannel returns ErrQRAlreadyConnected once the socket is connected,
	// so it must run before Connect(). Doing it inside a goroutine isn't enough:
	// the goroutine would race Connect() on the main thread and almost always
	// lose, leaving the dashboard stuck on "disconnected" with no QR to scan.
	// RearmQR + StreamQRToState are shared with the dashboard's "Generate new
	// QR" re-pair action (see web/server.go) so this sequence has one
	// implementation, not two.
	var qrChan <-chan whatsmeow.QRChannelItem
	if waMgr.Client.Store.ID == nil {
		qrChan, err = waMgr.RearmQR(ctx)
		if err != nil {
			log.Printf("Warning: could not open QR channel — dashboard/console QR will be unavailable: %v", err)
		}
	}

	// Connect Whatsmeow client
	err = waMgr.Connect(ctx)
	if err != nil {
		log.Fatalf("Fatal: Failed to connect WhatsApp client: %v", err)
	}

	// Stream QR codes (channel acquired above) to the dashboard state + console.
	if qrChan != nil {
		go waMgr.StreamQRToState(ctx, qrChan, func(code string) {
			log.Println("New WhatsApp pairing QR Code generated. Displaying in console:")
			qrterminal.GenerateHalfBlock(code, qrterminal.L, os.Stdout)
		})
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
	webServer := web.NewServer(cfg, queries, waMgr, ttsOrch)
	webServer.SetVoiceStore(voiceStore) // nil-safe: no-ops when archival is disabled
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

// runRetention deletes or redacts PII-bearing rows older than the retention
// window: STT/TTS history and conversation turns are deleted; wa_activity
// and wa_messages rows are redacted in place — wa_activity so the audit
// metadata (timings, status, tool ids) survives, wa_messages so the
// dashboard's Messages tab doesn't show gaps in a conversation thread.
func runRetention(ctx context.Context, queries *database.Queries, days int) {
	cutoff := time.Now().AddDate(0, 0, -days)
	log.Printf("Retention: purging/redacting rows older than %s (%d days)...", cutoff.Format("2006-01-02"), days)

	for name, fn := range map[string]func(context.Context, time.Time) error{
		"stt_history":        queries.PurgeSttHistoryBefore,
		"tts_history":        queries.PurgeTtsHistoryBefore,
		"conversation_turns": queries.PurgeConversationTurnsBefore,
		"wa_activity":        queries.RedactWaActivityBefore,
		"wa_messages":        queries.RedactWaMessagesBefore,
		// Ledger rows only — the GCS objects themselves are deleted by a
		// bucket lifecycle rule matching RETENTION_DAYS (see DEPLOYMENT.md).
		"wa_voice_notes": queries.PurgeWaVoiceNotesBefore,
	} {
		if err := fn(ctx, cutoff); err != nil {
			monitor.ReportError(ctx, "retention:"+name, err)
		}
	}
	log.Println("Retention: pass complete.")
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

// newContactParams builds the row for a first-contact auto-create. Enabled is
// always false: a human operator must explicitly opt each chat in from the
// dashboard before the agent may reply (product rule D1; the schema default
// wa_contacts.enabled DEFAULT FALSE encodes the same intent).
func newContactParams(chatJID, pushName string) database.CreateOrUpdateWaContactParams {
	name := pushName
	if name == "" {
		name = strings.Split(chatJID, "@")[0]
	}
	return database.CreateOrUpdateWaContactParams{
		ChatID:         chatJID,
		Name:           name,
		Enabled:        false,
		AgentID:        nil,
		PromptOverride: nil,
	}
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
	voiceStore *voicenotes.Store,
) {
	if evt.Info.IsFromMe {
		return
	}
	if evt.Info.Chat.String() == "status@broadcast" {
		return
	}
	if evt.Info.IsGroup {
		trace.Logf(ctx, "Inbound: Skipping group message %s from %s", evt.Info.ID, evt.Info.Chat.String())
		return
	}

	// Every log line for this message is grep-able by its WhatsApp message id.
	ctx = trace.With(ctx, evt.Info.ID)
	defer func() {
		if r := recover(); r != nil {
			monitor.ReportPanic("handleIncomingMessage", r)
			sendTextReply(ctx, client, evt.Info.Chat, "حدث خطأ غير متوقع أثناء معالجة طلبك.")
		}
	}()

	trace.Logf(ctx, "Inbound: Received message from %s", evt.Info.Chat.String())

	// Throttle per chat so one number can't hammer the LLM/ERP pipeline.
	if allowed, count := inboundLimiter.Allow(evt.Info.Chat.String()); !allowed {
		// Warn only on the first message over the limit; drop the rest silently.
		if count == 9 {
			sendTextReply(ctx, client, evt.Info.Chat, "أرسلت رسائل كثيرة خلال وقت قصير. يرجى الانتظار قليلاً ثم المحاولة مرة أخرى.")
		}
		trace.Logf(ctx, "Inbound: Rate limit exceeded for %s (%d msgs/min), dropping message", evt.Info.Chat.String(), count)
		return
	}

	// Look up contact in wa_contacts to verify if they are enabled
	contact, err := queries.GetWaContact(ctx, evt.Info.Chat.String())
	if err != nil {
		// Contact does not exist yet. Auto-create it DISABLED so it shows up
		// in the dashboard, then drop the message: the agent must never talk
		// to a new person until an operator explicitly enables the chat
		// (explicit opt-in; matches wa_contacts.enabled DEFAULT FALSE).
		contact, err = queries.CreateOrUpdateWaContact(ctx, newContactParams(evt.Info.Chat.String(), evt.Info.PushName))
		if err != nil {
			trace.Logf(ctx, "Inbound: Warning: Failed to auto-create contact %s: %v", evt.Info.Chat.String(), err)
			return
		}
		trace.Logf(ctx, "Inbound: Auto-created contact %s (%s) as disabled; awaiting operator opt-in", contact.Name, contact.ChatID)
		return
	}
	if !contact.Enabled {
		// Drop message processing if contact is disabled
		trace.Logf(ctx, "Inbound: Discarding message from disabled contact %s (%s)", contact.Name, contact.ChatID)
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
		trace.Logf(ctx, "Inbound: Message contains audio. Downloading...")
		incomingAudio, err = client.Download(ctx, evt.Message.AudioMessage)
		if err != nil {
			trace.Logf(ctx, "Inbound: Failed to download audio: %v", err)
			sendTextReply(ctx, client, evt.Info.Chat, "عذراً، لم أتمكن من تحميل الرسالة الصوتية.")
			return
		}

		// Archive the received voice note (async: spool + ledger row only;
		// the upload worker streams it to Firebase Cloud Storage later).
		voiceStore.Save(ctx, voicenotes.Meta{
			MessageID:       evt.Info.ID,
			ChatID:          evt.Info.Chat.String(),
			Direction:       "in",
			Sender:          strings.Split(evt.Info.Chat.String(), "@")[0],
			Receiver:        "sawt",
			DurationSeconds: int32(evt.Message.AudioMessage.GetSeconds()),
			Timestamp:       evt.Info.Timestamp,
		}, incomingAudio)

		// Transcode OGG/Opus to WAV
		trace.Logf(ctx, "Inbound: Transcoding OGG/Opus to WAV...")
		wavBytes, err := audio.OggToWav(incomingAudio)
		if err != nil {
			trace.Logf(ctx, "Inbound: Audio transcoding failed: %v", err)
			sendTextReply(ctx, client, evt.Info.Chat, "عذراً، فشل معالجة الملف الصوتي.")
			return
		}

		// Transcribe
		sttStart := time.Now()
		var lang = "ar" // Target Arabic
		text, sttProvider, err = sttOrch.Transcribe(ctx, wavBytes, lang)
		sttMs = int32(time.Since(sttStart).Milliseconds())

		if err != nil {
			monitor.ReportError(ctx, "stt", err)
			sendTextReply(ctx, client, evt.Info.Chat, "عذراً، لم أتمكن من تحويل الصوت إلى نص.")
			return
		}

		trace.Logf(ctx, "Inbound: Audio transcribed in %dms via %s: '%s'", sttMs, sttProvider, text)

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

	// Log the inbound message to wa_messages for the dashboard's Messages
	// tab (distinct from wa_activity below, which is a metrics/audit log,
	// not a message browser). Best-effort: a logging failure shouldn't abort
	// the conversation pipeline.
	inMsgType := "text"
	if isAudio {
		inMsgType = "voice"
	}
	_ = queries.CreateWaMessage(ctx, database.CreateWaMessageParams{
		ID:        evt.Info.ID + "-in",
		ChatID:    evt.Info.Chat.String(),
		Direction: "in",
		Sender:    "contact",
		MsgType:   inMsgType,
		Content:   text,
		Status:    "delivered",
	})

	// 2. Resolve Actor Identity from Phone JID
	var identity *erp.Identity
	phone := strings.Split(evt.Info.Chat.String(), "@")[0]
	identity, err = erpClient.ResolveIdentity(ctx, phone)
	if err != nil {
		monitor.ReportError(ctx, "identity", err)
	}

	// 3. Load conversation memory, then invoke the workflow with real history
	summary, history, err := wfEngine.LoadConversation(ctx, evt.Info.Chat.String())
	if err != nil {
		trace.Logf(ctx, "Inbound: Warning: failed to load conversation history: %v", err)
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
		monitor.ReportError(ctx, "workflow", err)
		sendTextReply(ctx, client, evt.Info.Chat, "حدث خطأ أثناء معالجة طلبك.")
		return
	}

	replyText := state.FinalReply
	trace.Logf(ctx, "Inbound: Agent replied in %dms: '%s'", llmMs, replyText)

	// Persist this exchange so future messages carry conversation context.
	wfEngine.SaveTurns(ctx, evt.Info.Chat.String(), text, replyText)

	// 4. Handle TTS if original message was audio
	var replyAudio []byte
	if isAudio && replyText != "" {
		trace.Logf(ctx, "Outbound: Synthesizing reply text to speech...")
		ttsStart := time.Now()
		var rawWav []byte
		rawWav, ttsProvider, err = ttsOrch.Synthesize(ctx, replyText, "ar")
		ttsMs = int32(time.Since(ttsStart).Milliseconds())

		if err != nil {
			trace.Logf(ctx, "Outbound: TTS Synthesis failed: %v", err)
		} else {
			// Transcode WAV to OGG/Opus
			trace.Logf(ctx, "Outbound: Transcoding WAV to OGG/Opus...")
			replyAudio, err = audio.WavToOpus(rawWav)
			if err != nil {
				trace.Logf(ctx, "Outbound: Audio transcoding failed: %v", err)
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

				// Archive the outgoing voice note alongside the inbound one.
				voiceStore.Save(ctx, voicenotes.Meta{
					MessageID: evt.Info.ID + "-reply",
					ChatID:    evt.Info.Chat.String(),
					Direction: "out",
					Sender:    "sawt",
					Receiver:  strings.Split(evt.Info.Chat.String(), "@")[0],
					Timestamp: time.Now(),
				}, replyAudio)
			}
		}
	}

	totalMs := sttMs + llmMs + ttsMs
	var activityStatus = "ok"
	var activityError *string

	// 5. Send reply
	if len(replyAudio) > 0 {
		trace.Logf(ctx, "Outbound: Uploading audio response to WhatsApp...")
		resp, err := client.Upload(context.Background(), replyAudio, whatsmeow.MediaAudio)
		if err != nil {
			trace.Logf(ctx, "Outbound: Failed to upload audio: %v, falling back to text", err)
			sendTextReply(ctx, client, evt.Info.Chat, replyText)

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
				monitor.ReportError(ctx, "whatsapp-send", err)
				errStr := err.Error()
				activityStatus = "failed"
				activityError = &errStr
			}
		}
	} else if replyText != "" {
		sendTextReply(ctx, client, evt.Info.Chat, replyText)
	}

	// Log the outbound reply to wa_messages (see note above on wa_messages
	// vs wa_activity). msg_type/status mirror what was actually attempted.
	if replyText != "" {
		outMsgType := "text"
		if len(replyAudio) > 0 {
			outMsgType = "voice"
		}
		outStatus := "sent"
		if activityStatus != "ok" {
			outStatus = "failed"
		}
		_ = queries.CreateWaMessage(ctx, database.CreateWaMessageParams{
			ID:        evt.Info.ID + "-out",
			ChatID:    evt.Info.Chat.String(),
			Direction: "out",
			Sender:    "bot",
			MsgType:   outMsgType,
			Content:   replyText,
			Status:    outStatus,
		})
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

func sendTextReply(ctx context.Context, client *whatsmeow.Client, chat types.JID, text string) {
	textMsg := &proto.Message{
		Conversation: googleProto.String(text),
	}
	_, err := client.SendMessage(context.Background(), chat, textMsg)
	if err != nil {
		trace.Logf(ctx, "Outbound: Failed to send text reply: %v", err)
	}
}
