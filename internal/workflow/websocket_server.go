package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// WorkflowServer manages WebSocket connections and workflow execution sessions.
type WorkflowServer struct {
	sessions *SessionManager
	workflow WorkflowExecutor
	mu       sync.Mutex
}

// NewWorkflowServer creates a new WebSocket workflow server with a default workflow.
func NewWorkflowServer(wf WorkflowExecutor) *WorkflowServer {
	return &WorkflowServer{
		sessions: NewSessionManager(),
		workflow: wf,
	}
}

// ServeHTTP handles incoming HTTP upgrades to WebSocket.
func (ws *WorkflowServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		log.Printf("[WorkflowServer] WebSocket accept error: %v", err)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "connection closed")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	sessionID := fmt.Sprintf("sess_%d", time.Now().UnixNano())
	ws.sessions.CreateSession(sessionID, ws.workflow, cancel)
	defer ws.sessions.CloseSession(sessionID)

	// Send initial dynamic agent configuration event
	agents := ws.workflow.GetAgents()
	initMsg := WSMessage{
		Type:    "agent_config",
		Content: agents,
	}
	if err := ws.writeJSON(ctx, conn, initMsg); err != nil {
		log.Printf("[WorkflowServer] Error sending agent_config: %v", err)
		return
	}

	// Message sender callback for real-time streaming to this connection
	sender := func(msgType string, agent string, content interface{}) error {
		msg := WSMessage{
			Type:    msgType,
			Agent:   agent,
			Content: content,
		}
		return ws.writeJSON(ctx, conn, msg)
	}

	// Read incoming client messages loop
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
				log.Printf("[WorkflowServer] Read connection ended: %v", err)
			}
			break
		}

		var incoming struct {
			Type    string `json:"type"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(data, &incoming); err != nil {
			log.Printf("[WorkflowServer] Invalid message JSON: %v", err)
			continue
		}

		if incoming.Type == "user_input" && strings.TrimSpace(incoming.Content) != "" {
			go func(userInput string) {
				if err := ws.workflow.Execute(ctx, userInput, sender); err != nil {
					log.Printf("[WorkflowServer] Workflow execution error: %v", err)
					_ = sender("error", "", err.Error())
				}
			}(incoming.Content)
		}
	}
}

func (ws *WorkflowServer) writeJSON(ctx context.Context, conn *websocket.Conn, msg WSMessage) error {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	return conn.Write(writeCtx, websocket.MessageText, data)
}
