package channel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
)

// SSEHandler manages Server-Sent Events for channel requests.
type SSEHandler struct {
	mu      sync.RWMutex
	clients map[chan []byte]struct{}
}

// NewSSEHandler creates a new SSE handler.
func NewSSEHandler() *SSEHandler {
	return &SSEHandler{
		clients: make(map[chan []byte]struct{}),
	}
}

// ServeHTTP handles the SSE endpoint.
func (h *SSEHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan []byte, 16)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.clients, ch)
		h.mu.Unlock()
	}()

	// Keep-alive and event forwarding.
	for {
		select {
		case data := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// SendEvent broadcasts a channel request to all SSE clients.
func (h *SSEHandler) SendEvent(req *Request) {
	data, err := json.Marshal(req)
	if err != nil {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.clients {
		select {
		case ch <- data:
		default: // drop if client is slow
		}
	}
}
