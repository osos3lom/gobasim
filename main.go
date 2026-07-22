package main

import (
	"context"
	_ "embed"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types/events"

	"sawt-go/config"
	"sawt-go/database"
	"sawt-go/internal/erp"
	"sawt-go/internal/gateway"
	"sawt-go/internal/monitor"
	"sawt-go/internal/speech"
	"sawt-go/internal/voicenotes"
	waClient "sawt-go/internal/whatsmeow"
	"sawt-go/internal/workflow"
	"sawt-go/web"
)

//go:embed schema.sql
var schemaSQL string

func main() {
	log.Println("Starting Sawt Unified Daemon...")

	_ = config.LoadDotEnv(".env")
	cfg := config.LoadConfig()
	monitor.Init(cfg.ErrorWebhookURL)

	if _, err := exec.LookPath("ffmpeg"); err != nil {
		if allow, _ := strconv.ParseBool(os.Getenv("ALLOW_MISSING_FFMPEG")); allow {
			log.Println("WARNING: ffmpeg not found on PATH — voice notes will fail (ALLOW_MISSING_FFMPEG=true).")
		} else {
			log.Fatal("Fatal: ffmpeg not found on PATH. Install it, or set ALLOW_MISSING_FFMPEG=true for a text-only deploy.")
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool, err := database.InitPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Fatal: Database initialization failed: %v", err)
	}
	defer pool.Close()

	log.Println("Database: Checking/bootstrapping schema...")
	_, err = pool.Exec(ctx, schemaSQL)
	if err != nil {
		log.Fatalf("Fatal: Database schema bootstrap failed: %v", err)
	}
	log.Println("Database: Schema bootstrap complete.")

	queries := database.New(pool)

	if err := database.SeedAdminUser(ctx, pool, queries, cfg); err != nil {
		log.Printf("Database: Warning: admin user seeding failed: %v", err)
	}

	if err := database.SeedDefaultAgent(ctx, pool); err != nil {
		log.Printf("Database: Warning: default agent seeding failed: %v", err)
	}

	if cfg.RetentionDays > 0 {
		go func() {
			ticker := time.NewTicker(24 * time.Hour)
			defer ticker.Stop()
			for {
				database.RunRetention(ctx, queries, cfg.RetentionDays)
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

	sttOrch := speech.NewSTTOrchestrator(cfg)
	ttsOrch := speech.NewTTSOrchestrator(cfg)
	erpClient := erp.NewClient(cfg.MshaliaAPIURL, cfg.AgentGatewaySecret)
	wfEngine := workflow.NewWorkflowEngine(cfg, erpClient, queries)
	wfEngine.SetBaseContext(ctx)

	var voiceStore *voicenotes.Store
	if cfg.VoiceStorageBucket != "" {
		up, err := voicenotes.NewGCSUploader(ctx, cfg.VoiceStorageBucket)
		if err != nil {
			log.Fatalf("Fatal: VOICE_STORAGE_BUCKET is set but GCS client init failed: %v", err)
		}
		if voiceStore, err = voicenotes.NewStore(cfg.VoiceStoragePrefix, cfg.VoiceSpoolDir, queries, up); err != nil {
			log.Fatalf("Fatal: VOICE_STORAGE_BUCKET is set but voice-note store init failed: %v", err)
		}
		voiceStore.StartWorker(ctx)
		log.Printf("Voice notes: archiving to gs://%s/%s (spool: %s)", cfg.VoiceStorageBucket, cfg.VoiceStoragePrefix, cfg.VoiceSpoolDir)
	} else {
		log.Println("Voice notes: archival disabled (VOICE_STORAGE_BUCKET not set).")
	}

	waMgr := waClient.NewWhatsAppManager(cfg.DatabaseURL)

	maxInflight := cfg.MaxInflight
	if maxInflight < 1 {
		maxInflight = 32
	}
	var inflightWG sync.WaitGroup
	inflightSem := make(chan struct{}, maxInflight)

	err = waMgr.Initialize(ctx, func(evt interface{}) {
		switch v := evt.(type) {
		case *events.Message:
			inflightWG.Add(1)
			go func(msg *events.Message) {
				defer inflightWG.Done()
				select {
				case inflightSem <- struct{}{}:
					defer func() { <-inflightSem }()
				case <-ctx.Done():
					return
				}
				gateway.HandleIncomingMessage(ctx, cfg, waMgr.Client, msg, queries, sttOrch, ttsOrch, erpClient, wfEngine, voiceStore)
			}(v)
		case *events.Connected:
			waMgr.SetState(waClient.StateConnected, "", "")
			log.Println("WhatsApp connection established.")
		case *events.LoggedOut:
			waMgr.SetState(waClient.StateDisconnected, "", "")
			log.Println("Logged out from WhatsApp. Re-pairing is required. Recreating client locally...")
			if err := waMgr.RecreateClient(); err != nil {
				log.Printf("Warning: failed to clear local WhatsApp device store: %v", err)
			}
		}
	})
	if err != nil {
		log.Fatalf("Fatal: WhatsApp manager initialization failed: %v", err)
	}

	var qrChan <-chan whatsmeow.QRChannelItem
	if waMgr.Client.Store.ID == nil {
		qrChan, err = waMgr.RearmQR(ctx)
		if err != nil {
			log.Printf("Warning: could not open QR channel — dashboard/console QR will be unavailable: %v", err)
		}
	}

	err = waMgr.Connect(ctx)
	if err != nil {
		log.Fatalf("Fatal: Failed to connect WhatsApp client: %v", err)
	}

	if qrChan != nil {
		go waMgr.StreamQRToState(ctx, qrChan, func(code string) {
			log.Println("New WhatsApp pairing QR Code generated. Displaying in console:")
			qrterminal.GenerateHalfBlock(code, qrterminal.L, os.Stdout)
		})
	}

	if waMgr.Client.Store.ID == nil && cfg.PairPhoneNumber != "" {
		go func() {
			select {
			case <-time.After(3 * time.Second):
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

	webServer := web.NewServer(cfg, queries, waMgr, ttsOrch)
	webServer.SetVoiceStore(voiceStore)
	webServer.SetDB(pool)
	webServer.SetERPClient(erpClient)
	router := webServer.GetRouter()

	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		log.Printf("Web Dashboard serving at http://localhost:%s\n", cfg.Port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("Web server error: %v", err)
		}
	}()

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c

	log.Println("Shutting down daemon gracefully...")

	httpCtx, httpCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := server.Shutdown(httpCtx); err != nil {
		log.Printf("HTTP graceful shutdown incomplete (%v) — forcing close.", err)
		_ = server.Close()
	}
	httpCancel()

	waMgr.Client.Disconnect()

	drained := make(chan struct{})
	go func() { inflightWG.Wait(); close(drained) }()
	select {
	case <-drained:
		log.Println("In-flight message handlers drained.")
	case <-time.After(25 * time.Second):
		log.Println("Shutdown drain timeout reached — proceeding with handlers still active.")
	}

	cancel()
	log.Println("Shutdown complete.")
}
