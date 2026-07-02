package main

import (
	"context"
	"encoding/base64"
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
	"sawt-go/internal/speech"
	waClient "sawt-go/internal/whatsmeow"
	"sawt-go/internal/workflow"
	"sawt-go/web"
	"strings"
	"syscall"
	"time"

	_ "github.com/lib/pq"
	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	googleProto "google.golang.org/protobuf/proto"
)

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

	queries := database.New(pool)

	// 2. Initialize Speech & Workflow Orchestrators
	sttOrch := speech.NewSTTOrchestrator(cfg)
	ttsOrch := speech.NewTTSOrchestrator(cfg)
	erpClient := erp.NewClient(cfg.MshaliaAPIURL, cfg.AgentGatewaySecret)
	wfEngine := workflow.NewWorkflowEngine(cfg, erpClient)

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
			qrChan, err := waMgr.Client.GetQRChannel(context.Background())
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
					qrterminal.GenerateHalfBlock(qr.Code, qrterminal.ConsoleColors{}, os.Stdout)
				} else {
					log.Printf("QR Channel Event: %s", qr.Event)
				}
			}
		}()
	}

	// Trigger pairing code if phone configuration is set at VM startup
	if waMgr.Client.Store.ID == nil && cfg.PairPhoneNumber != "" {
		go func() {
			time.Sleep(3 * time.Second) // wait for connection to stabilize
			phoneNum := strings.ReplaceAll(cfg.PairPhoneNumber, "+", "")
			code, err := waMgr.RequestPairingCode(phoneNum)
			if err != nil {
				log.Printf("Failed to generate VM startup pairing code: %v", err)
			} else {
				log.Printf("\n============================================\n  STARTUP PHONE PAIRING CODE: %s\n============================================\n", code)
			}
		}()
	}

	// 4. Start Dashboard HTTP Web Server
	webServer := web.NewServer(cfg, queries, waMgr)
	router := webServer.GetRouter()

	go func() {
		log.Printf("Web Dashboard serving at http://localhost:%s\n", cfg.Port)
		if err := http.ResponseWriter(nil); true { // compile placeholder check
			server := &http.Server{
				Addr:    ":" + cfg.Port,
				Handler: router,
			}
			if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("Web server error: %v", err)
			}
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

	log.Printf("Inbound: Received message %s from %s", evt.Info.ID, evt.Info.Chat.String())

	var text string
	var incomingAudio []byte
	var err error

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
		incomingAudio, err = client.Download(evt.Message.AudioMessage)
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
	if !evt.Info.IsGroup {
		phone := strings.Split(evt.Info.Chat.String(), "@")[0]
		identity, err = erpClient.ResolveIdentity(ctx, phone)
		if err != nil {
			log.Printf("Inbound: Identity resolution failed: %v", err)
		}
	}

	// 3. Invoke State Workflow
	var messagesHistory []workflow.Message
	messagesHistory = append(messagesHistory, workflow.Message{
		Role:    "user",
		Content: text,
	})

	state := &State{
		Messages:      messagesHistory,
		ActorIdentity: identity,
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
					Url:           googleProto.String(resp.URL),
					DirectPath:    googleProto.String(resp.DirectPath),
					MediaKey:      resp.MediaKey,
					Mimetype:      googleProto.String("audio/ogg; codecs=opus"),
					Ptt:           googleProto.Bool(true),
					FileLength:    googleProto.Uint64(uint64(len(replyAudio))),
					FileSha256:    resp.FileSHA256,
					FileEncSha256: resp.FileEncSHA256,
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
	var agentID *string
	if identity != nil {
		agentID = &identity.Role
	}
	var sttProvPtr *string
	if sttProvider != "" {
		sttProvPtr = &sttProvider
	}
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
		AgentID:     agentID,
		LlmModel:    sttProvPtr, // mapping for provider logs
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
