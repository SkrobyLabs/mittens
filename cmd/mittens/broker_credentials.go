package main

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

const maxCredentialSize = 64 * 1024 // 64KB

// Credentials returns the current credential JSON held by the broker.
func (b *HostBroker) Credentials() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.creds
}

func (b *HostBroker) handle(w http.ResponseWriter, r *http.Request) {
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

func (b *HostBroker) handleGet(w http.ResponseWriter) {
	b.mu.RLock()
	data := b.creds
	b.mu.RUnlock()

	if data == "" {
		b.blog("GET -> 204 (no credentials)")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	b.blog("GET -> 200 (expiresAt: %d, %d bytes)", expiresAt(data), len(data))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, data)
}

func (b *HostBroker) handlePut(w http.ResponseWriter, r *http.Request) {
	body, ok := b.readBody(w, r, maxCredentialSize)
	if !ok {
		return
	}

	incoming := string(body)
	incomingExp := expiresAt(incoming)
	if incomingExp == 0 {
		b.blog("PUT -> 400 (missing/invalid expiresAt, %d bytes, keys: %s)", len(body), jsonKeys(incoming))
		http.Error(w, "invalid credentials: missing or invalid expiresAt", http.StatusBadRequest)
		return
	}

	b.mu.Lock()
	currentExp := expiresAt(b.creds)
	if incomingExp > currentExp {
		b.creds = incoming
		b.mu.Unlock()
		b.blog("PUT -> 204 accepted (incoming: %d, was: %d)", incomingExp, currentExp)
		// Fresh credentials received: release refresh coordination for the next nearing-expiry cycle.
		b.refreshMu.Lock()
		b.refreshInProgress = false
		b.refreshMu.Unlock()
		b.persistToHost(incoming)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	b.mu.Unlock()
	b.blog("PUT -> 409 stale (incoming: %d, current: %d)", incomingExp, currentExp)
	http.Error(w, "stale credentials", http.StatusConflict)
}

// hostSync polls host credential stores every 5 seconds.
func (b *HostBroker) hostSync() {
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
func (b *HostBroker) pullFromHost() {
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
		b.mu.Unlock()
		b.blog("hostSync: pulled fresher creds from host (host: %d, was: %d)", bestExp, currentExp)
		return
	}
	b.mu.Unlock()
}

// persistToHost writes credentials to all host stores.
func (b *HostBroker) persistToHost(jsonData string) {
	for _, s := range b.stores {
		if err := s.Persist(jsonData); err != nil {
			b.blog("persistToHost: FAILED %s: %v", s.Label(), err)
			logWarn("Broker: persist to %s: %v", s.Label(), err)
		} else {
			b.blog("persistToHost: wrote to %s", s.Label())
		}
	}
}

// jsonKeys returns the sorted top-level keys of a JSON object as a bracketed
// list (e.g. `[claudeAiOauth, primaryApiKey]`), or "<invalid JSON>" on failure.
func jsonKeys(s string) string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &obj); err != nil {
		return "<invalid JSON>"
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return "[" + strings.Join(keys, ", ") + "]"
}
