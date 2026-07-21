package database

import (
	"context"
	"testing"
	"time"
)

func TestInitPool_EmptyURL(t *testing.T) {
	_, err := InitPool(context.Background(), "")
	if err == nil {
		t.Fatal("expected an error for an empty database URL")
	}
}

func TestInitPool_MalformedURL(t *testing.T) {
	_, err := InitPool(context.Background(), "not a valid connection string")
	if err == nil {
		t.Fatal("expected an error for a malformed connection string")
	}
}

func TestInitPool_UnreachableHostFailsPing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Port 1 on loopback is reserved and refuses connections immediately,
	// so this fails fast on Ping rather than hanging for the full timeout.
	_, err := InitPool(ctx, "postgres://user:pass@127.0.0.1:1/dbname?connect_timeout=1")
	if err == nil {
		t.Fatal("expected a ping failure for an unreachable database")
	}
}
