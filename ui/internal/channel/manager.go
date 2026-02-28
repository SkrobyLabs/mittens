package channel

import (
	"bufio"
	"encoding/json"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
)

// Manager manages Unix socket servers for container communication.
type Manager struct {
	mu        sync.RWMutex
	baseDir   string
	listeners map[string]net.Listener
	pending   map[string]*Request
	pendingMu sync.RWMutex
	// OnRequest is called when a new channel request arrives.
	OnRequest func(req *Request)
	// responseCh maps request ID to a channel waiting for the response.
	responseCh map[string]chan *Response
	responseChMu sync.Mutex
}

// NewManager creates a channel manager with sockets under baseDir.
func NewManager(baseDir string) *Manager {
	_ = os.MkdirAll(baseDir, 0o755)
	return &Manager{
		baseDir:    baseDir,
		listeners:  make(map[string]net.Listener),
		pending:    make(map[string]*Request),
		responseCh: make(map[string]chan *Response),
	}
}

// StartSocket creates a Unix socket for the given session.
func (m *Manager) StartSocket(sessionID string) (string, error) {
	sockDir := filepath.Join(m.baseDir, sessionID)
	_ = os.MkdirAll(sockDir, 0o755)
	sockPath := filepath.Join(sockDir, "channel.sock")

	// Remove stale socket.
	_ = os.Remove(sockPath)

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return "", err
	}

	m.mu.Lock()
	m.listeners[sessionID] = ln
	m.mu.Unlock()

	go m.acceptLoop(sessionID, ln)
	return sockPath, nil
}

// StopSocket closes the Unix socket for a session.
func (m *Manager) StopSocket(sessionID string) {
	m.mu.Lock()
	ln, ok := m.listeners[sessionID]
	if ok {
		delete(m.listeners, sessionID)
	}
	m.mu.Unlock()

	if ok {
		ln.Close()
	}

	// Clean up socket dir.
	sockDir := filepath.Join(m.baseDir, sessionID)
	_ = os.RemoveAll(sockDir)
}

// Respond sends a response to a pending channel request.
func (m *Manager) Respond(requestID string, resp *Response) error {
	m.responseChMu.Lock()
	ch, ok := m.responseCh[requestID]
	if ok {
		delete(m.responseCh, requestID)
	}
	m.responseChMu.Unlock()

	m.pendingMu.Lock()
	delete(m.pending, requestID)
	m.pendingMu.Unlock()

	if ok {
		ch <- resp
	}
	return nil
}

// Pending returns all pending requests.
func (m *Manager) Pending() []*Request {
	m.pendingMu.RLock()
	defer m.pendingMu.RUnlock()
	var reqs []*Request
	for _, r := range m.pending {
		reqs = append(reqs, r)
	}
	return reqs
}

func (m *Manager) acceptLoop(sessionID string, ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // listener closed
		}
		go m.handleConn(sessionID, conn)
	}
}

func (m *Manager) handleConn(sessionID string, conn net.Conn) {
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		var req Request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			log.Printf("channel: invalid message from %s: %v", sessionID, err)
			continue
		}
		req.SessionID = sessionID

		// Create response channel.
		respCh := make(chan *Response, 1)
		m.responseChMu.Lock()
		m.responseCh[req.ID] = respCh
		m.responseChMu.Unlock()

		m.pendingMu.Lock()
		m.pending[req.ID] = &req
		m.pendingMu.Unlock()

		// Notify UI.
		if m.OnRequest != nil {
			m.OnRequest(&req)
		}

		// Wait for response.
		resp := <-respCh

		// Send response back to container.
		data, _ := json.Marshal(resp)
		data = append(data, '\n')
		_, _ = conn.Write(data)
	}
}

// SocketPath returns the socket path for a session.
func (m *Manager) SocketPath(sessionID string) string {
	return filepath.Join(m.baseDir, sessionID, "channel.sock")
}
