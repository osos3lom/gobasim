package whatsmeow

import (
	"context"
	"testing"
	"time"
)

func TestSetState_TracksConnectedAt(t *testing.T) {
	m := NewWhatsAppManager("postgres://mock")

	m.SetState(StateConnected, "", "")
	state, connectedAt, disconnectedFor := m.GetConnectionInfo()
	if state != StateConnected {
		t.Fatalf("expected state %q, got %q", StateConnected, state)
	}
	if connectedAt.IsZero() {
		t.Fatal("expected connectedAt to be set after transitioning to StateConnected")
	}
	if time.Since(connectedAt) > time.Second {
		t.Fatalf("expected connectedAt to be recent, got %v", connectedAt)
	}
	if disconnectedFor != 0 {
		t.Fatalf("expected disconnectedFor to be 0 while connected, got %v", disconnectedFor)
	}

	m.SetState(StateDisconnected, "", "")
	time.Sleep(time.Millisecond)
	_, connectedAt, disconnectedFor = m.GetConnectionInfo()
	if !connectedAt.IsZero() {
		t.Fatalf("expected connectedAt to reset to zero after disconnecting, got %v", connectedAt)
	}
	if disconnectedFor <= 0 {
		t.Fatalf("expected disconnectedFor to be positive after disconnecting, got %v", disconnectedFor)
	}
}

func TestSetState_QRReadyDoesNotCountAsDisconnectedForDebounce(t *testing.T) {
	m := NewWhatsAppManager("postgres://mock")

	// Never-connected / awaiting-pairing states shouldn't trip the
	// "was connected, now dropped" disconnect-alert debounce.
	m.SetState(StateQRReady, "some-qr-payload", "")
	_, _, disconnectedFor := m.GetConnectionInfo()
	if disconnectedFor != 0 {
		t.Fatalf("expected disconnectedFor to be 0 in qr_ready state, got %v", disconnectedFor)
	}
}

func TestLogout_ErrorsWhenClientNil(t *testing.T) {
	m := NewWhatsAppManager("postgres://mock")

	if err := m.Logout(context.Background()); err == nil {
		t.Fatal("expected an error when Client is nil (Initialize not called)")
	}
}

func TestRearmQR_RejectsWhenClientNil(t *testing.T) {
	m := NewWhatsAppManager("postgres://mock")

	if _, err := m.RearmQR(context.Background()); err == nil {
		t.Fatal("expected an error when Client is nil (Initialize not called)")
	}
}
