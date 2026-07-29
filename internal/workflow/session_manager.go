package workflow

import (
	"context"
	"sync"
	"time"
)

// Session represents an active interactive workflow session for a client.
type Session struct {
	ID        string
	Workflow  WorkflowExecutor
	CancelFn  context.CancelFunc
	CreatedAt time.Time
}

// SessionManager manages active client workflow sessions concurrently.
type SessionManager struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

// NewSessionManager creates a new SessionManager.
func NewSessionManager() *SessionManager {
	return &SessionManager{
		sessions: make(map[string]*Session),
	}
}

// CreateSession initializes a new session for a given session ID and workflow.
func (sm *SessionManager) CreateSession(sessionID string, wf WorkflowExecutor, cancelFn context.CancelFunc) *Session {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sess := &Session{
		ID:        sessionID,
		Workflow:  wf,
		CancelFn:  cancelFn,
		CreatedAt: time.Now(),
	}
	sm.sessions[sessionID] = sess
	return sess
}

// GetSession retrieves an active session by ID.
func (sm *SessionManager) GetSession(sessionID string) (*Session, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	sess, ok := sm.sessions[sessionID]
	return sess, ok
}

// CloseSession terminates and removes an active session.
func (sm *SessionManager) CloseSession(sessionID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sess, ok := sm.sessions[sessionID]; ok {
		if sess.CancelFn != nil {
			sess.CancelFn()
		}
		delete(sm.sessions, sessionID)
	}
}

// ActiveCount returns the current number of active workflow sessions.
func (sm *SessionManager) ActiveCount() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return len(sm.sessions)
}
