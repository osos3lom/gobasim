package whatsmeow

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"
)

type ConnectionState string

const (
	StateDisconnected ConnectionState = "disconnected"
	StateQRReady      ConnectionState = "qr_ready"
	StatePairingReady ConnectionState = "pairing_ready"
	StateConnected    ConnectionState = "connected"
)

type WhatsAppManager struct {
	Client     *whatsmeow.Client
	state      ConnectionState
	qrString   string
	pairCode   string
	dbURL      string
	mu         sync.RWMutex
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

	// Double-check active client state
	if m.Client != nil {
		if m.Client.IsConnected() && m.Client.IsLoggedIn() {
			return StateConnected, "", ""
		}
	}
	return m.state, m.qrString, m.pairCode
}

func (m *WhatsAppManager) SetState(state ConnectionState, qr, pair string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state = state
	m.qrString = qr
	m.pairCode = pair
}

func (m *WhatsAppManager) Initialize(ctx context.Context, eventHandler func(interface{})) error {
	dbContainer, err := sqlstore.New("postgres", m.dbURL, nil)
	if err != nil {
		return fmt.Errorf("sqlstore init failed: %w", err)
	}

	deviceStore, err := dbContainer.GetFirstDevice()
	if err != nil {
		return fmt.Errorf("failed to get device store: %w", err)
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

	m.SetState(StateDisconnected, "", "")
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
	code, err := client.PairPhone(phone, true, whatsmeow.PairClientChrome, "Sawt Dashboard")
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
