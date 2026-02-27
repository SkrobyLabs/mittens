package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
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
}

// NewCredentialBroker creates a broker that will listen on sockPath.
// seed is the initial credential JSON (may be empty).
func NewCredentialBroker(sockPath, seed string) *CredentialBroker {
	b := &CredentialBroker{
		sockPath: sockPath,
		creds:    seed,
	}
	mux := http.NewServeMux()
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

	return b.srv.Serve(ln)
}

// Close gracefully shuts down the broker.
func (b *CredentialBroker) Close() error {
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
		w.WriteHeader(http.StatusNoContent)
		return
	}
	b.mu.Unlock()
	http.Error(w, "stale credentials", http.StatusConflict)
}
