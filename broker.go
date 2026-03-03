package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// CredentialBroker is an HTTP server on a Unix socket that acts as the single
// source of truth for OAuth credentials across multiple mittens containers.
//
// Containers push refreshed tokens via PUT and pull the latest via GET.
// The broker accepts a PUT only when the incoming expiresAt exceeds the
// currently stored value, so the freshest token always wins.
type CredentialBroker struct {
	sockPath string
	creds    string // latest credential JSON
	mu       sync.RWMutex
	srv      *http.Server
	ln       net.Listener
	stores   []CredentialStore // host credential stores for bidirectional sync
	done     chan struct{}     // signals hostSync goroutine to stop

	// OnOpen is called when a container requests a URL to be opened on the host.
	OnOpen func(url string)
}

// NewCredentialBroker creates a broker that will listen on sockPath.
// seed is the initial credential JSON (may be empty).
// stores are host credential stores used for bidirectional sync.
func NewCredentialBroker(sockPath, seed string, stores []CredentialStore) *CredentialBroker {
	b := &CredentialBroker{
		sockPath: sockPath,
		creds:    seed,
		stores:   stores,
		done:     make(chan struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/open", b.handleOpen)
	mux.HandleFunc("/", b.handle)
	b.srv = &http.Server{Handler: mux}
	return b
}

// Serve starts listening on the Unix socket. It blocks until the server is
// shut down via Close(). Call this in a goroutine.
func (b *CredentialBroker) Serve() error {
	// Remove stale socket file.
	os.Remove(b.sockPath)

	ln, err := net.Listen("unix", b.sockPath)
	if err != nil {
		return err
	}
	b.ln = ln

	// Allow container's claude user to connect.
	_ = os.Chmod(b.sockPath, 0666)

	// Start bidirectional host sync loop.
	if len(b.stores) > 0 {
		go b.hostSync()
	}

	return b.srv.Serve(ln)
}

// Close gracefully shuts down the broker and stops the host sync loop.
func (b *CredentialBroker) Close() error {
	close(b.done)
	if b.srv != nil {
		return b.srv.Shutdown(context.Background())
	}
	return nil
}

// Credentials returns the current credential JSON held by the broker.
func (b *CredentialBroker) Credentials() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.creds
}

func (b *CredentialBroker) handle(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		b.handleGet(w)
	case http.MethodPut:
		b.handlePut(w, r)
	default:
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (b *CredentialBroker) handleGet(w http.ResponseWriter) {
	b.mu.RLock()
	data := b.creds
	b.mu.RUnlock()

	if data == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, data)
}

const maxCredentialSize = 64 * 1024 // 64KB

func (b *CredentialBroker) handlePut(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxCredentialSize+1))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	if len(body) > maxCredentialSize {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}

	incoming := string(body)
	incomingExp := expiresAt(incoming)
	if incomingExp == 0 {
		http.Error(w, "invalid credentials: missing or invalid expiresAt", http.StatusBadRequest)
		return
	}

	b.mu.Lock()
	currentExp := expiresAt(b.creds)
	if incomingExp > currentExp {
		b.creds = incoming
		b.mu.Unlock()
		// Write-through: persist fresher creds to host stores immediately.
		b.persistToHost(incoming)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	b.mu.Unlock()
	http.Error(w, "stale credentials", http.StatusConflict)
}

// hostSync polls host credential stores every 5 seconds.
// If the host has fresher creds, the broker's in-memory state is updated.
func (b *CredentialBroker) hostSync() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-b.done:
			return
		case <-ticker.C:
			b.pullFromHost()
		}
	}
}

// pullFromHost reads from all host stores, picks the freshest, and updates
// the broker if the host has newer credentials.
func (b *CredentialBroker) pullFromHost() {
	var bestJSON string
	var bestExp int64

	for _, s := range b.stores {
		data, err := s.Extract()
		if err != nil || data == "" {
			continue
		}
		exp := expiresAt(data)
		if exp > bestExp {
			bestJSON = data
			bestExp = exp
		}
	}

	if bestJSON == "" {
		return
	}

	b.mu.Lock()
	currentExp := expiresAt(b.creds)
	if bestExp > currentExp {
		b.creds = bestJSON
	}
	b.mu.Unlock()
}

// persistToHost writes credentials to all host stores (fire-and-forget).
func (b *CredentialBroker) persistToHost(jsonData string) {
	for _, s := range b.stores {
		if err := s.Persist(jsonData); err != nil {
			logWarn("Broker: persist to %s: %v", s.Label(), err)
		}
	}
}

const maxOpenURLSize = 4096

func (b *CredentialBroker) handleOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxOpenURLSize+1))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	if len(body) > maxOpenURLSize {
		http.Error(w, "URL too large", http.StatusRequestEntityTooLarge)
		return
	}

	url := strings.TrimSpace(string(body))
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		http.Error(w, "invalid URL", http.StatusBadRequest)
		return
	}

	if b.OnOpen != nil {
		b.OnOpen(url)
	}
	w.WriteHeader(http.StatusNoContent)
}
