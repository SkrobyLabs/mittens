package main

import (
	"crypto/subtle"
	"io"
	"net/http"
)

// readBody reads and size-checks the request body. Returns nil, false on error
// after writing the HTTP response.
func (b *HostBroker) readBody(w http.ResponseWriter, r *http.Request, maxSize int) ([]byte, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(maxSize)+1))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return nil, false
	}
	if len(body) > maxSize {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return nil, false
	}
	return body, true
}

// requireMethod validates the request method and writes an error response when
// it does not match.
func (b *HostBroker) requireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method != method {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	return true
}

// withAuth wraps an HTTP handler with the broker's authorization check.
func (b *HostBroker) withAuth(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !b.authorize(w, r) {
			return
		}
		handler(w, r)
	}
}

func (b *HostBroker) authorize(w http.ResponseWriter, r *http.Request) bool {
	if b.AuthToken == "" {
		return true
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Mittens-Token")), []byte(b.AuthToken)) == 1 {
		return true
	}
	http.Error(w, "unauthorized", http.StatusUnauthorized)
	return false
}
