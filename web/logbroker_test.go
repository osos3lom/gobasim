package web

import (
	"context"
	"testing"
	"time"

	"sawt-go/internal/erp"
	"sawt-go/internal/voicenotes"
)

func TestLogBroker_SubscribePublishUnsubscribe(t *testing.T) {
	b := NewLogBroker()
	go b.Start()

	ch := make(chan string, 1)
	b.Subscribe(ch)

	if _, err := b.Write([]byte("hello world")); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}

	select {
	case got := <-ch:
		if got != "hello world" {
			t.Errorf("received %q, want %q", got, "hello world")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for published log line")
	}

	b.Unsubscribe(ch)

	// The channel should be closed by the broker after unsubscribe.
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected channel to be closed after Unsubscribe")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for channel close")
	}
}

func TestServerSetters(t *testing.T) {
	db := &mockDBTX{}
	server, _ := setupTestServer(t, db)

	server.SetVoiceStore(nil)
	server.SetDB(nil)
	server.SetERPClient(nil)

	client := erp.NewClient("http://localhost:3001", "secret")
	server.SetERPClient(client)
	if server.erpClient != client {
		t.Error("expected SetERPClient to store the given client")
	}

	store := &voicenotes.Store{}
	server.SetVoiceStore(store)
	if server.voiceStore != store {
		t.Error("expected SetVoiceStore to store the given store")
	}

	pinger := pingerFunc(func(ctx context.Context) error { return nil })
	server.SetDB(pinger)
	if server.db == nil {
		t.Error("expected SetDB to store the given pinger")
	}
}

type pingerFunc func(ctx context.Context) error

func (f pingerFunc) Ping(ctx context.Context) error { return f(ctx) }
