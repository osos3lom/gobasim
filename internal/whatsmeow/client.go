package whatsmeow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	// Registers the "pgx" database/sql driver used by whatsmeow's sqlstore
	// below. Without this blank import, sqlstore.New fails with
	// `unknown driver "pgx"`. We use pgx (not lib/pq) to stay consistent with
	// the app's pgxpool and because pgx natively parses the Neon connection
	// string (sslmode, channel_binding) the rest of the app relies on.
	_ "github.com/jackc/pgx/v5/stdlib"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
	googleProto "google.golang.org/protobuf/proto"
)

type ConnectionState string

const (
	StateDisconnected    ConnectionState = "disconnected"
	StateAwaitingPairing ConnectionState = "awaiting_pairing"
	StateQRReady         ConnectionState = "qr_ready"
	StatePairingReady    ConnectionState = "pairing_ready"
	StateConnected       ConnectionState = "connected"
)

type WhatsAppManager struct {
	Client            *whatsmeow.Client
	state             ConnectionState
	qrString          string
	pairCode          string
	dbURL             string
	connectedAt       time.Time // zero when not connected
	disconnectedSince time.Time // zero when not disconnected; used to debounce the UI's disconnect alert
	mu                sync.RWMutex
}

func NewWhatsAppManager(dbURL string) *WhatsAppManager {
	return &WhatsAppManager{
		state: StateDisconnected,
		dbURL: dbURL,
	}
}

func (m *WhatsAppManager) GetStatus() (ConnectionState, string, string) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.Client != nil {
		if !m.Client.IsConnected() {
			return StateDisconnected, "", ""
		}
		if m.Client.IsLoggedIn() {
			return StateConnected, "", ""
		}
	}
	return m.state, m.qrString, m.pairCode
}

func (m *WhatsAppManager) SetState(state ConnectionState, qr, pair string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if state == StateConnected {
		if m.state != StateConnected {
			m.connectedAt = time.Now()
		}
		m.disconnectedSince = time.Time{}
	} else {
		m.connectedAt = time.Time{}
		if state == StateDisconnected {
			if m.disconnectedSince.IsZero() {
				m.disconnectedSince = time.Now()
			}
		} else {
			// qr_ready / awaiting_pairing / pairing_ready are normal
			// pre-pairing states, not an alarming drop — don't count them
			// toward the disconnect-alert debounce.
			m.disconnectedSince = time.Time{}
		}
	}

	m.state = state
	m.qrString = qr
	m.pairCode = pair
}

// GetConnectionInfo returns the live connection state (same semantics as
// GetStatus) plus connectedAt (zero if not connected) and how long the
// connection has been continuously disconnected (zero if not disconnected).
// The dashboard uses disconnectedFor to debounce its alert banner — brief
// blips during a reconnect shouldn't immediately alarm the operator.
func (m *WhatsAppManager) GetConnectionInfo() (state ConnectionState, connectedAt time.Time, disconnectedFor time.Duration) {
	state, _, _ = m.GetStatus()

	m.mu.RLock()
	defer m.mu.RUnlock()
	connectedAt = m.connectedAt
	if !m.disconnectedSince.IsZero() {
		disconnectedFor = time.Since(m.disconnectedSince)
	}
	return
}

func (m *WhatsAppManager) Initialize(ctx context.Context, eventHandler func(interface{})) error {
	dbLog := waLog.Stdout("Database", "WARN", true)
	dbContainer, err := sqlstore.New(ctx, "pgx", m.dbURL, dbLog)
	if err != nil {
		return fmt.Errorf("sqlstore init failed: %w", err)
	}

	deviceStore, err := dbContainer.GetFirstDevice(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			deviceStore = dbContainer.NewDevice()
		} else {
			return fmt.Errorf("failed to get device store: %w", err)
		}
	}

	clientLog := waLog.Stdout("WhatsApp", "WARN", true)
	client := whatsmeow.NewClient(deviceStore, clientLog)

	client.AddEventHandler(eventHandler)
	m.Client = client

	return nil
}

func (m *WhatsAppManager) Connect(ctx context.Context) error {
	m.mu.RLock()
	client := m.Client
	m.mu.RUnlock()

	if client == nil {
		return fmt.Errorf("whatsmeow client not initialized")
	}

	if client.Store.ID != nil {
		// Device is already logged in, connect directly
		err := client.Connect()
		if err != nil {
			return err
		}
		m.SetState(StateConnected, "", "")
		return nil
	}

	// Device needs linking. Connect socket.
	err := client.Connect()
	if err != nil {
		return err
	}

	m.SetState(StateAwaitingPairing, "", "")
	return nil
}

func (m *WhatsAppManager) RequestPairingCode(phone string) (string, error) {
	m.mu.RLock()
	client := m.Client
	m.mu.RUnlock()

	if client == nil {
		return "", fmt.Errorf("whatsmeow client not initialized")
	}

	if !client.IsConnected() {
		return "", fmt.Errorf("whatsapp not connected to socket")
	}

	log.Printf("Requesting pairing code for phone number: %s...", phone)
	code, err := client.PairPhone(context.Background(), phone, true, whatsmeow.PairClientChrome, "Sawt Dashboard")
	if err != nil {
		return "", fmt.Errorf("failed to request pairing code: %w", err)
	}

	prettyCode := code
	if len(code) == 8 {
		prettyCode = fmt.Sprintf("%s-%s", code[0:4], code[4:8])
	}

	m.SetState(StatePairingReady, "", prettyCode)
	return prettyCode, nil
}

// Logout unlinks the current device from WhatsApp. whatsmeow's Logout sends
// an IQ to the server, disconnects, and clears the device store (Store.ID
// becomes nil) — the device must go through the full pairing flow again
// (RearmQR + Connect) afterward. Note main.go's *events.LoggedOut handler
// also calls SetState(StateDisconnected, ...) when the server unlinks the
// device remotely (e.g. unlinked from the phone) — both paths converge on
// the same idempotent state update, so there's no conflict between an
// operator-initiated logout and a server-initiated one.
func (m *WhatsAppManager) Logout(ctx context.Context) error {
	m.mu.RLock()
	client := m.Client
	m.mu.RUnlock()

	if client == nil {
		return fmt.Errorf("whatsmeow client not initialized")
	}

	if err := client.Logout(ctx); err != nil {
		return fmt.Errorf("whatsapp logout failed: %w", err)
	}

	m.SetState(StateDisconnected, "", "")
	return nil
}

// RearmQR opens a fresh QR channel for an unpaired device, mirroring the
// boot-time sequence in main.go: whatsmeow's GetQRChannel must be called
// BEFORE Connect (it returns ErrQRAlreadyConnected once the socket is up).
// This makes that sequence re-triggerable after a Logout, or after a QR
// session's ~6 auto-rotated codes all time out and the channel closes.
// Callers must follow this with Connect(ctx) and then StreamQRToState to
// drain the returned channel into dashboard state.
func (m *WhatsAppManager) RearmQR(ctx context.Context) (<-chan whatsmeow.QRChannelItem, error) {
	m.mu.RLock()
	client := m.Client
	m.mu.RUnlock()

	if client == nil {
		return nil, fmt.Errorf("whatsmeow client not initialized")
	}
	if client.Store.ID != nil {
		return nil, fmt.Errorf("device is already paired — log out before re-pairing")
	}
	if client.IsConnected() {
		// GetQRChannel errors once the socket is connected; tear down a
		// stale/still-connected socket first so we can reopen the channel.
		client.Disconnect()
	}

	qrChan, err := client.GetQRChannel(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to open QR channel: %w", err)
	}

	m.SetState(StateAwaitingPairing, "", "")
	return qrChan, nil
}

// StreamQRToState drains a QR channel (from GetQRChannel/RearmQR) into the
// manager's dashboard state. Extracted from main.go's original boot-time
// loop so both the boot sequence and a dashboard-triggered re-pair share one
// implementation — a channel must have exactly one consumer, so this
// replaces (not duplicates) the inline goroutine that used to live in main.go.
// onCode is called with each new QR payload for callers that also want a
// side effect (main.go prints it to the console); pass nil to skip that.
func (m *WhatsAppManager) StreamQRToState(ctx context.Context, qrChan <-chan whatsmeow.QRChannelItem, onCode func(code string)) {
	for qr := range qrChan {
		if qr.Event == whatsmeow.QRChannelEventCode {
			m.SetState(StateQRReady, qr.Code, "")
			if onCode != nil {
				onCode(qr.Code)
			}
		} else {
			log.Printf("QR Channel Event: %s", qr.Event)
		}
	}
}

// SendTextMessage sends a plain-text WhatsApp message. Consolidates what
// main.go's sendTextReply constructs inline (a *proto.Message with just the
// Conversation field set) into the one place every whatsmeow interaction is
// meant to go through.
func (m *WhatsAppManager) SendTextMessage(ctx context.Context, chatJID string, text string) error {
	m.mu.RLock()
	client := m.Client
	m.mu.RUnlock()

	if client == nil {
		return fmt.Errorf("whatsmeow client not initialized")
	}

	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat id %q: %w", chatJID, err)
	}

	msg := &proto.Message{Conversation: googleProto.String(text)}
	if _, err := client.SendMessage(ctx, jid, msg); err != nil {
		return fmt.Errorf("failed to send text message: %w", err)
	}
	return nil
}

// SendVoiceMessage sends already-encoded OGG/Opus audio as a WhatsApp voice
// note (PTT). Mirrors the outbound audio path in main.go's
// handleIncomingMessage (Upload then SendMessage with PTT=true). Callers are
// responsible for any TTS synthesis/transcoding — this method only knows
// about the final encoded bytes, keeping this package free of a dependency
// on the speech/audio packages.
func (m *WhatsAppManager) SendVoiceMessage(ctx context.Context, chatJID string, opusBytes []byte) error {
	m.mu.RLock()
	client := m.Client
	m.mu.RUnlock()

	if client == nil {
		return fmt.Errorf("whatsmeow client not initialized")
	}

	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("invalid chat id %q: %w", chatJID, err)
	}

	resp, err := client.Upload(ctx, opusBytes, whatsmeow.MediaAudio)
	if err != nil {
		return fmt.Errorf("failed to upload voice note: %w", err)
	}

	msg := &proto.Message{
		AudioMessage: &proto.AudioMessage{
			URL:           googleProto.String(resp.URL),
			DirectPath:    googleProto.String(resp.DirectPath),
			MediaKey:      resp.MediaKey,
			Mimetype:      googleProto.String("audio/ogg; codecs=opus"),
			PTT:           googleProto.Bool(true),
			FileLength:    googleProto.Uint64(uint64(len(opusBytes))),
			FileSHA256:    resp.FileSHA256,
			FileEncSHA256: resp.FileEncSHA256,
		},
	}
	if _, err := client.SendMessage(ctx, jid, msg); err != nil {
		return fmt.Errorf("failed to send voice message: %w", err)
	}
	return nil
}
