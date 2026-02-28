package ws

import (
	"encoding/json"
	"sync"

	"github.com/Skroby/mittens/ui/internal/session"
	"github.com/gorilla/websocket"
)

// Hub manages WebSocket connections for a single session.
type Hub struct {
	mu      sync.RWMutex
	clients map[*websocket.Conn]struct{}
}

// NewHub creates a hub for a session.
func NewHub() *Hub {
	return &Hub{
		clients: make(map[*websocket.Conn]struct{}),
	}
}

// Add registers a WebSocket connection.
func (h *Hub) Add(conn *websocket.Conn) {
	h.mu.Lock()
	h.clients[conn] = struct{}{}
	h.mu.Unlock()
}

// Remove unregisters a WebSocket connection.
func (h *Hub) Remove(conn *websocket.Conn) {
	h.mu.Lock()
	delete(h.clients, conn)
	h.mu.Unlock()
}

// Broadcast sends binary data to all connected clients.
func (h *Hub) Broadcast(data []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for conn := range h.clients {
		_ = conn.WriteMessage(websocket.BinaryMessage, data)
	}
}

// StateChanged sends a state change message to all clients.
func (h *Hub) StateChanged(state session.State) {
	msg := Message{Type: MsgState, State: string(state)}
	h.sendJSON(msg)
}

// Exited sends an exit message to all clients.
func (h *Hub) Exited(code int) {
	msg := Message{Type: MsgExit, Code: code}
	h.sendJSON(msg)
}

func (h *Hub) sendJSON(v interface{}) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for conn := range h.clients {
		_ = conn.WriteMessage(websocket.TextMessage, data)
	}
}

// HubManager manages hubs for all sessions.
type HubManager struct {
	mu   sync.RWMutex
	hubs map[string]*Hub
}

// NewHubManager creates a new hub manager.
func NewHubManager() *HubManager {
	return &HubManager{hubs: make(map[string]*Hub)}
}

// GetOrCreate returns the hub for a session, creating one if needed.
func (hm *HubManager) GetOrCreate(sessionID string) *Hub {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	if h, ok := hm.hubs[sessionID]; ok {
		return h
	}
	h := NewHub()
	hm.hubs[sessionID] = h
	return h
}

// Get returns the hub for a session.
func (hm *HubManager) Get(sessionID string) (*Hub, bool) {
	hm.mu.RLock()
	defer hm.mu.RUnlock()
	h, ok := hm.hubs[sessionID]
	return h, ok
}

// Remove deletes the hub for a session.
func (hm *HubManager) Remove(sessionID string) {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	delete(hm.hubs, sessionID)
}
