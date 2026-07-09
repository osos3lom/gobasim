//go:build ignore

// Standalone WhatsApp QR diagnostics tool. Excluded from the normal package
// build (it's a second `package main` with its own main()) via the ignore
// tag above, so `go build ./...` and CI stay green. Run it directly with:
//
//	go run scratch_connect.go
package main

import (
	"bufio"
	"context"
	"log"
	"os"
	"strings"
	"time"

	waClient "sawt-go/internal/whatsmeow"
)

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
		if i := strings.Index(val, " #"); i >= 0 {
			val = strings.TrimSpace(val[:i])
		}
		if key != "" {
			os.Setenv(key, val)
		}
	}
	return sc.Err()
}

func main() {
	log.Println("Starting WhatsApp diagnostics tool...")
	if err := loadDotEnv(".env.production"); err != nil {
		log.Fatalf("could not read env: %v", err)
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL is empty in .env.production")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	waMgr := waClient.NewWhatsAppManager(dbURL)

	err := waMgr.Initialize(ctx, func(evt interface{}) {
		log.Printf("[EVENT] Received event: %T", evt)
	})
	if err != nil {
		log.Fatalf("Failed to initialize WhatsAppManager: %v", err)
	}

	if waMgr.Client.Store.ID != nil {
		log.Println("WhatsApp device is ALREADY PAIRED in database. QR code will not be generated.")
		return
	}

	log.Println("Registering QR channel listener...")
	qrChan, err := waMgr.Client.GetQRChannel(ctx)
	if err != nil {
		log.Fatalf("Failed to get QR channel: %v", err)
	}

	go func() {
		for {
			select {
			case qr, ok := <-qrChan:
				if !ok {
					log.Println("QR channel closed.")
					return
				}
				log.Printf("[QR EVENT] Event=%q Code=%q", qr.Event, qr.Code)
			case <-ctx.Done():
				return
			}
		}
	}()

	log.Println("Connecting WhatsApp client...")
	err = waMgr.Connect(ctx)
	if err != nil {
		log.Fatalf("Failed to connect WhatsApp: %v", err)
	}

	log.Println("WhatsApp client connected. Waiting 15 seconds for QR events...")
	<-ctx.Done()
	log.Println("Diagnostics finished.")
}
